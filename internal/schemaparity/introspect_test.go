package schemaparity

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/testsupport/fakesql"
)

func result(columns string, rows ...[]driver.Value) fakesql.Result {
	return fakesql.Result{Columns: strings.Split(columns, ","), Rows: rows}
}

type row = []driver.Value

func postgresServer(overrides map[string]error) *fakesql.Server {
	answers := map[string]fakesql.Result{
		postgresTablesSQL: result("t,p", row{"widgets", false}, row{"owners", false}, row{"notarx_schema_migrations", false}),
		postgresColumnsSQL: result("t,c,d,u,n,g",
			row{"widgets", "id", "uuid", "uuid", "NO", "NEVER"}, row{"widgets", "name", "text", "text", "NO", "NEVER"},
			row{"widgets", "tags", "ARRAY", "_text", "YES", "NEVER"}, row{"widgets", "slug", "text", "text", "NO", "ALWAYS"},
			row{"widgets", "owner", "uuid", "uuid", "YES", "NEVER"}, row{"owners", "id", "uuid", "uuid", "NO", "NEVER"},
			row{"a_view", "x", "text", "text", "NO", "NEVER"}),
		postgresUniqueSQL: result("t,p,c", row{"widgets", true, "id"}, row{"widgets", false, "name"}, row{"widgets", false, "slug,owner"},
			row{"owners", true, "id"}, row{"a_view", false, "x"}),
		postgresForeignKeysSQL: result("t,r,l,f,d,u", row{"widgets", "owners", "owner", "id", "a", "a"}, row{"a_view", "owners", "x", "id", "a", "a"}),
	}
	return &fakesql.Server{Handler: func(query string, _ []driver.NamedValue) (fakesql.Result, error) {
		for fragment, err := range overrides {
			if strings.Contains(query, fragment) {
				return fakesql.Result{}, err
			}
		}
		if answer, ok := answers[query]; ok {
			return answer, nil
		}
		return fakesql.None(), nil
	}}
}

func withoutSeeds(c Catalogue) Catalogue {
	c.Seeds = nil
	return c
}

func TestIntrospectPostgres(t *testing.T) {
	got, err := Introspect(context.Background(), postgresServer(nil).Open(t), DialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	if want := withoutSeeds(sample()); !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestIntrospectPostgresErrors(t *testing.T) {
	boom := errors.New("boom")
	for name, fragment := range map[string]string{"tables": "FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace\nWHERE", "columns": "information_schema.columns", "keys": "FROM pg_index", "foreign keys": "FROM pg_constraint"} {
		t.Run(name, func(t *testing.T) {
			if _, err := Introspect(context.Background(), postgresServer(map[string]error{fragment: boom}).Open(t), DialectPostgres); !errors.Is(err, boom) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	server := postgresServer(nil)
	inner := server.Handler
	server.Handler = func(query string, args []driver.NamedValue) (fakesql.Result, error) {
		if query == postgresColumnsSQL {
			return result("t,c,d,u,n,g", row{"widgets", "x", "tsvector", "tsvector", "NO", "NEVER"}), nil
		}
		return inner(query, args)
	}
	if _, err := Introspect(context.Background(), server.Open(t), DialectPostgres); err == nil || !strings.Contains(err.Error(), "widgets.x") {
		t.Fatalf("an unmapped type was accepted: %v", err)
	}
	if _, err := Introspect(context.Background(), server.Open(t), "oracle"); err == nil {
		t.Fatal("an unknown dialect was accepted")
	}
}

func mysqlServer(overrides map[string]error) *fakesql.Server {
	text := func(table, column, columnType, nullable, extra string) row {
		return row{table, column, "varchar", columnType, int64(64), "utf8mb4_0900_bin", nullable, extra}
	}
	answers := map[string]fakesql.Result{
		mysqlTablesSQL: result("t", row{"widgets"}, row{"owners"}, row{"notarx_lock_buckets"}),
		mysqlColumnsSQL: result("t,c,d,ct,l,co,n,e",
			row{"widgets", "id", "char", "char(36)", int64(36), "ascii_bin", "NO", ""}, text("widgets", "name", "varchar(64)", "NO", ""),
			row{"widgets", "tags", "json", "json", nil, nil, "YES", ""}, text("widgets", "slug", "varchar(64)", "NO", "VIRTUAL GENERATED"),
			row{"widgets", "owner", "char", "char(36)", int64(36), "ascii_bin", "YES", "DEFAULT_GENERATED"},
			row{"owners", "id", "char", "char(36)", int64(36), "ascii_bin", "NO", ""}, text("ghost", "x", "varchar(8)", "NO", "")),
		mysqlIndexesSQL: result("t,i,s,c",
			row{"widgets", "PRIMARY", int64(1), "id"}, row{"widgets", "name", int64(1), "name"}, row{"widgets", "owner_slug", int64(1), "slug"},
			row{"widgets", "owner_slug", int64(2), "owner"}, row{"widgets", "lower_name", int64(1), nil}, row{"owners", "PRIMARY", int64(1), "id"}, row{"ghost", "PRIMARY", int64(1), "x"}),
		mysqlForeignKeysSQL: result("t,n,c,rt,rc,d,u", row{"widgets", "fk_owner", "owner", "owners", "id", "NO ACTION", "NO ACTION"}, row{"ghost", "fk", "x", "owners", "id", "NO ACTION", "NO ACTION"}),
	}
	return &fakesql.Server{Handler: func(query string, _ []driver.NamedValue) (fakesql.Result, error) {
		for fragment, err := range overrides {
			if strings.Contains(query, fragment) {
				return fakesql.Result{}, err
			}
		}
		if answer, ok := answers[query]; ok {
			return answer, nil
		}
		return fakesql.None(), nil
	}}
}

func TestIntrospectMySQL(t *testing.T) {
	got, err := Introspect(context.Background(), mysqlServer(nil).Open(t), DialectMySQL)
	if err != nil {
		t.Fatal(err)
	}
	widgets := got.Tables["widgets"]
	if widgets.Columns["slug"] != (Column{Type: "text", Generated: true}) || widgets.Columns["owner"] != (Column{Type: "uuid", Nullable: true}) || widgets.Columns["tags"].Type != "json" {
		t.Fatalf("columns: %+v", widgets.Columns)
	}
	if !reflect.DeepEqual(widgets.PrimaryKey, []string{"id"}) || !reflect.DeepEqual(widgets.Unique, [][]string{{"name"}, {"owner", "slug"}}) || !reflect.DeepEqual(widgets.ForeignKeys, []string{"owner -> owners(id)"}) {
		t.Fatalf("keys: %+v", widgets)
	}
	if _, ok := got.Tables["notarx_lock_buckets"]; ok || len(got.Tables) != 2 {
		t.Fatalf("tables: %v", got.TableNames())
	}
	// The MySQL form of the sample catalogue: arrays as JSON, a functional index and an unlisted table ignored.
	if problems := Compare(withoutSeeds(sample()), got, DialectMySQL); len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
}

func TestIntrospectMySQLErrors(t *testing.T) {
	boom := errors.New("boom")
	for name, fragment := range map[string]string{"tables": "information_schema.tables", "columns": "information_schema.columns", "keys": "information_schema.statistics", "foreign keys": "key_column_usage"} {
		t.Run(name, func(t *testing.T) {
			if _, err := Introspect(context.Background(), mysqlServer(map[string]error{fragment: boom}).Open(t), DialectMySQL); !errors.Is(err, boom) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	server := mysqlServer(nil)
	inner := server.Handler
	server.Handler = func(query string, args []driver.NamedValue) (fakesql.Result, error) {
		if query == mysqlColumnsSQL {
			return result("t,c,d,ct,l,co,n,e", row{"widgets", "x", "enum", "enum('a')", nil, nil, "NO", ""}), nil
		}
		return inner(query, args)
	}
	if _, err := Introspect(context.Background(), server.Open(t), DialectMySQL); err == nil || !strings.Contains(err.Error(), "widgets.x") {
		t.Fatalf("an unmapped type was accepted: %v", err)
	}
}

func TestIntrospectSeeds(t *testing.T) {
	catalogue := Catalogue{Tables: map[string]Table{
		"owners": {Columns: map[string]Column{
			"id": {Type: "uuid"}, "enabled": {Type: "bool"}, "created_at": {Type: "timestamp"}, "label": {Type: "text"},
			"note": {Type: "text", Nullable: true}, "deployment": {Type: "text"}, "computed": {Type: "int32", Generated: true},
		}},
		"empty": {Columns: map[string]Column{"id": {Type: "uuid"}}},
	}}
	server := &fakesql.Server{Handler: func(query string, _ []driver.NamedValue) (fakesql.Result, error) {
		if strings.Contains(query, "owners") {
			return result("id,enabled,created_at,label,note,deployment,computed,unknown",
				row{"0194f0c2-6a53-7000-8000-000000000001", int64(1), time.Now(), "main", nil, "0194f0c2-6a53-7000-8000-000000000002", int64(5), "x"}), nil
		}
		return fakesql.None(), nil
	}}
	got, err := IntrospectSeeds(context.Background(), server.Open(t), DialectMySQL, catalogue)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]map[string]string{"owners": {{"enabled": "true", "label": "main"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v; want %v", got, want)
	}
	if len(server.Logged("SELECT * FROM `owners` LIMIT 21")) != 1 {
		t.Fatalf("MySQL identifiers are not quoted with backticks: %v", server.Log)
	}
	pg := &fakesql.Server{Handler: server.Handler}
	if _, err := IntrospectSeeds(context.Background(), pg.Open(t), DialectPostgres, catalogue); err != nil || len(pg.Logged("SELECT * FROM \"owners\" LIMIT 21")) != 1 {
		t.Fatalf("PostgreSQL identifiers are not quoted with double quotes: %v %v", err, pg.Log)
	}

	var many []row
	for i := 0; i <= maxSeedRows; i++ {
		many = append(many, row{int64(i)})
	}
	big := &fakesql.Server{Handler: func(string, []driver.NamedValue) (fakesql.Result, error) {
		return result("n", many...), nil
	}}
	numbers := Catalogue{Tables: map[string]Table{"counters": {Columns: map[string]Column{"n": {Type: "int64"}}}}}
	if _, err := IntrospectSeeds(context.Background(), big.Open(t), DialectPostgres, numbers); err == nil || !strings.Contains(err.Error(), "loads data") {
		t.Fatalf("err = %v", err)
	}
	failing := &fakesql.Server{Handler: func(string, []driver.NamedValue) (fakesql.Result, error) { return fakesql.Result{}, errors.New("boom") }}
	if _, err := IntrospectSeeds(context.Background(), failing.Open(t), DialectPostgres, numbers); err == nil {
		t.Fatal("a failing query was ignored")
	}
	badJSON := &fakesql.Server{Handler: func(string, []driver.NamedValue) (fakesql.Result, error) { return result("doc", row{"not json"}), nil }}
	docs := Catalogue{Tables: map[string]Table{"docs": {Columns: map[string]Column{"doc": {Type: "json"}}}}}
	if _, err := IntrospectSeeds(context.Background(), badJSON.Open(t), DialectPostgres, docs); err == nil || !strings.Contains(err.Error(), "docs.doc") {
		t.Fatalf("err = %v", err)
	}
}
