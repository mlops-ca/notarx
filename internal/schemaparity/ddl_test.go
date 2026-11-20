package schemaparity

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/testsupport/fakesql"
)

func TestNormalizeDefault(t *testing.T) {
	for _, tc := range []struct{ expression, logical, want string }{
		{"nextval('public.audit_id_seq'::regclass)", "int64", "identity"},
		{"gen_random_uuid()", "uuid", "uuid()"},
		{"now()", "timestamp", "now()"},
		{"clock_timestamp()", "timestamp", "now()"},
		{"CURRENT_TIMESTAMP", "timestamp", "now()"},
		{"'active'::text", "text", "'active'"},
		{"'x'::character varying", "text", "'x'"},
		{"0", "int32", "0"},
		{"false", "bool", "false"},
		{"'{}'::jsonb", "json", "'{}'"},
		{"'{}'::text[]", "array<text>", "'[]'"},
		{"ARRAY[]::text[]", "array<text>", "'[]'"},
		{"ARRAY['read'::text, 'write'::text]", "array<text>", "'[\"read\",\"write\"]'"},
		{"ARRAY[lower('A'::text)]", "array<text>", "ARRAY[lower('A')]"},
		{"ARRAY['a\"b'::text]", "array<text>", "ARRAY['a\"b']"},
		{"'2024-01-01 00:00:00+00'::timestamp with time zone", "timestamp", "'2024-01-01 00:00:00+00'"},
	} {
		if got := normalizeDefault(tc.expression, tc.logical); got != tc.want {
			t.Errorf("normalizeDefault(%q, %s) = %q; want %q", tc.expression, tc.logical, got, tc.want)
		}
	}
}

func TestNormalizeCheckAndIndexDefinition(t *testing.T) {
	for in, want := range map[string]string{
		"CHECK ((size >= 0))":                     "(size >= 0)",
		"CHECK (singleton)":                       "singleton",
		"CHECK (((a > 0) AND (b > 0))) NOT VALID": "((a > 0) AND (b > 0))",
		"CHECK ((a > 0) OR (b > 0))":              "(a > 0) OR (b > 0)",
		"CHECK ((state = 'a)b'::text))":           "(state = 'a)b'::text)",
		"CHECK (x ~ '(')":                         "x ~ '('",
	} {
		if got := normalizeCheck(in); got != want {
			t.Errorf("normalizeCheck(%q) = %q; want %q", in, got, want)
		}
	}
	if got := normalizeIndexDefinition("CREATE INDEX jobs_idx ON public.jobs USING btree (created_at) WHERE (public.x(state) = 'a'::text)", false); got != "USING btree (created_at) WHERE (x(state) = 'a'::text)" {
		t.Errorf("definition = %q", got)
	}
	if got := normalizeIndexDefinition("CREATE UNIQUE INDEX u ON ONLY public.jobs USING btree (a) WHERE (b)", true); got != "unique USING btree (a) WHERE (b)" {
		t.Errorf("definition = %q", got)
	}
	if !plainIndex(false, false, false, "btree", "CREATE INDEX i ON t USING btree (a)") || plainIndex(false, true, false, "btree", "") || plainIndex(false, false, false, "gin", "") ||
		plainIndex(false, false, false, "btree", "USING btree (a text_pattern_ops)") || plainIndex(false, false, false, "btree", "(a COLLATE \"C\")") || plainIndex(true, false, false, "btree", "") {
		t.Error("plainIndex is wrong")
	}
}

func ddlPostgresServer(overrides map[string]error) *fakesql.Server {
	server := postgresServer(overrides)
	inner := server.Handler
	server.Handler = func(query string, args []driver.NamedValue) (fakesql.Result, error) {
		for fragment, err := range overrides {
			if strings.Contains(query, fragment) {
				return fakesql.Result{}, err
			}
		}
		switch query {
		case postgresDefaultsSQL:
			return result("t,c,e", row{"widgets", "tags", "'{}'::text[]"}, row{"widgets", "name", "''::text"}, row{"ghost", "x", "0"}), nil
		case postgresIndexesSQL:
			return result("t,i,u,p,x,am,d,c",
				row{"widgets", "widgets_name_idx", false, false, false, "btree", "CREATE INDEX widgets_name_idx ON public.widgets USING btree (name)", "name,id desc"},
				row{"widgets", "widgets_name_key", true, false, false, "btree", "CREATE UNIQUE INDEX widgets_name_key ON public.widgets USING btree (name)", "name"},
				row{"widgets", "widgets_live", true, true, false, "btree", "CREATE UNIQUE INDEX widgets_live ON public.widgets USING btree (name) WHERE (tags IS NOT NULL)", "name"},
				row{"widgets", "widgets_trgm", false, false, false, "gin", "CREATE INDEX widgets_trgm ON public.widgets USING gin (name public.gin_trgm_ops)", "name"},
				row{"ghost", "ghost_idx", false, false, false, "btree", "CREATE INDEX ghost_idx ON public.ghost USING btree (x)", "x"}), nil
		case postgresChecksSQL:
			return result("t,n,d", row{"widgets", "widgets_name_check", "CHECK ((length(name) <= 64))"}, row{"ghost", "g", "CHECK ((x > 0))"}), nil
		}
		return inner(query, args)
	}
	return server
}

func TestIntrospectPostgresDDL(t *testing.T) {
	got, err := Introspect(context.Background(), ddlPostgresServer(nil).Open(t), DialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	widgets := got.Tables["widgets"]
	if !reflect.DeepEqual(widgets.Defaults, map[string]string{"tags": "'[]'", "name": "''"}) {
		t.Errorf("defaults: %v", widgets.Defaults)
	}
	if !reflect.DeepEqual(widgets.Indexes, []string{"name,id desc"}) {
		t.Errorf("indexes: %v", widgets.Indexes)
	}
	if want := map[string]string{
		"widgets_live": "unique USING btree (name) WHERE (tags IS NOT NULL)",
		"widgets_trgm": "USING gin (name gin_trgm_ops)",
	}; !reflect.DeepEqual(widgets.Special, want) {
		t.Errorf("special: %v", widgets.Special)
	}
	if !reflect.DeepEqual(widgets.Checks, map[string]string{"widgets_name_check": "(length(name) <= 64)"}) {
		t.Errorf("checks: %v", widgets.Checks)
	}
	if _, ok := got.Tables["ghost"]; ok {
		t.Error("a table outside the catalogue was read")
	}
}

func TestIntrospectPostgresDDLErrors(t *testing.T) {
	boom := errors.New("boom")
	for name, fragment := range map[string]string{"defaults": "pg_attrdef", "indexes": "pg_get_indexdef", "checks": "pg_get_constraintdef"} {
		t.Run(name, func(t *testing.T) {
			if _, err := Introspect(context.Background(), ddlPostgresServer(map[string]error{fragment: boom}).Open(t), DialectPostgres); !errors.Is(err, boom) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestIntrospectMySQLIndexes(t *testing.T) {
	server := mysqlServer(nil)
	inner := server.Handler
	server.Handler = func(query string, args []driver.NamedValue) (fakesql.Result, error) {
		if query == mysqlPlainIndexesSQL {
			return result("t,i,c,o",
				row{"widgets", "by_name", "name", "A"}, row{"widgets", "by_name", "owner", "D"},
				row{"widgets", "functional", nil, "A"}, row{"ghost", "g", "x", "A"}), nil
		}
		return inner(query, args)
	}
	got, err := Introspect(context.Background(), server.Open(t), DialectMySQL)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"name,owner desc"}; !reflect.DeepEqual(got.Tables["widgets"].Indexes, want) {
		t.Errorf("indexes: %v", got.Tables["widgets"].Indexes)
	}
	failing := mysqlServer(map[string]error{"index_type = 'BTREE'": errors.New("boom")})
	if _, err := Introspect(context.Background(), failing.Open(t), DialectMySQL); err == nil {
		t.Error("a failing index query was ignored")
	}
}

func TestBaseColumnMapsHashColumns(t *testing.T) {
	table := Table{Columns: map[string]Column{"path": {Type: "text"}, "path_sha256": {Type: "bytes", Generated: true}, "x_sha256": {Type: "bytes", Generated: true}, "plain_sha256": {Type: "bytes"}, "plain": {Type: "text"}}}
	for in, want := range map[string]string{"path_sha256": "path", "x_sha256": "x_sha256", "plain_sha256": "plain_sha256", "path": "path"} {
		if got := baseColumn(table, in); got != want {
			t.Errorf("baseColumn(%s) = %s; want %s", in, got, want)
		}
	}
}

func TestCompareDDL(t *testing.T) {
	want := Catalogue{Tables: map[string]Table{"t": {
		Columns: map[string]Column{"a": {Type: "text"}}, PrimaryKey: []string{"a"},
		Defaults: map[string]string{"a": "''"}, Indexes: []string{"a"}, Special: map[string]string{"s": "USING gin (a)"}, Checks: map[string]string{"c": "a <> ''"},
	}}}
	if problems := Compare(want, clone(want), DialectPostgres); len(problems) != 0 {
		t.Fatalf("equal: %v", problems)
	}
	drift := clone(want)
	table := drift.Tables["t"]
	table.Defaults = map[string]string{"a": "'x'", "extra": "1"}
	table.Indexes = []string{"a desc"}
	table.Special = nil
	table.Checks = map[string]string{"c": "a <> 'x'"}
	drift.Tables["t"] = table
	problems := strings.Join(Compare(want, drift, DialectPostgres), "\n")
	for _, expected := range []string{
		"table t lacks the index (a)", "table t has the index (a desc), which the catalogue lacks",
		"table t has the default of column a as \"'x'\"; the catalogue has \"''\"", "table t has the default of column extra, which the catalogue lacks",
		"table t lacks the special index s", "table t has the check constraint c as \"a <> 'x'\"",
	} {
		if !strings.Contains(problems, expected) {
			t.Errorf("missing %q in\n%s", expected, problems)
		}
	}
	// MySQL carries every plain index and may carry more; it is not introspected for the rest.
	mysql := clone(want)
	mt := mysql.Tables["t"]
	mt.Defaults, mt.Special, mt.Checks = nil, nil, nil
	mt.Indexes = []string{"a", "a,b"}
	mysql.Tables["t"] = mt
	if problems := Compare(want, mysql, DialectMySQL); len(problems) != 0 {
		t.Fatalf("mysql: %v", problems)
	}
	mt.Indexes = nil
	mysql.Tables["t"] = mt
	if problems := Compare(want, mysql, DialectMySQL); len(problems) != 1 || !strings.Contains(problems[0], "lacks the index (a)") {
		t.Fatalf("mysql missing index: %v", problems)
	}
}

func TestForeignKeyActions(t *testing.T) {
	if got := foreignKey("p", []string{"id"}, []string{"p_id"}, "cascade", "set null"); got != "p_id -> p(id) on delete cascade on update set null" {
		t.Errorf("got %q", got)
	}
	for in, want := range map[string]string{"a": "", "r": "restrict", "c": "cascade", "n": "set null", "d": "set default"} {
		if got := postgresAction(in); got != want {
			t.Errorf("postgresAction(%s) = %q", in, got)
		}
	}
	for in, want := range map[string]string{"NO ACTION": "", "RESTRICT": "restrict", "CASCADE": "cascade", "SET NULL": "set null", "SET DEFAULT": "set default"} {
		if got := mysqlAction(in); got != want {
			t.Errorf("mysqlAction(%s) = %q", in, got)
		}
	}
}
