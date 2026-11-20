package schemaparity

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sample() Catalogue {
	return Catalogue{Version: CatalogueVersion, Tables: map[string]Table{
		"widgets": {
			Columns: map[string]Column{
				"id":    {Type: "uuid"},
				"name":  {Type: "text"},
				"tags":  {Type: "array<text>", Nullable: true},
				"slug":  {Type: "text", Generated: true},
				"owner": {Type: "uuid", Nullable: true},
			},
			PrimaryKey:  []string{"id"},
			Unique:      [][]string{{"name"}, {"slug", "owner"}},
			ForeignKeys: []string{"owner -> owners(id)"},
		},
		"owners": {Columns: map[string]Column{"id": {Type: "uuid"}}, PrimaryKey: []string{"id"}},
	}, Seeds: map[string][]map[string]string{"owners": {{"enabled": "true"}}}}
}

func clone(c Catalogue) Catalogue {
	data, err := Marshal(c)
	if err != nil {
		panic(err)
	}
	out, err := parse(data)
	if err != nil {
		panic(err)
	}
	return out
}

func TestCatalogueRoundTrips(t *testing.T) {
	data, err := Marshal(sample())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"version: 1", "id: uuid", "tags: array<text>?", "slug: text generated", "primaryKey: [id]", "unique: [[name], [slug, owner]]", "- owner -> owners(id)"} {
		if !strings.Contains(text, want) {
			t.Errorf("catalogue lacks %q:\n%s", want, text)
		}
	}
	back, err := parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, sample()) {
		t.Fatalf("round trip changed the catalogue:\n%+v\n%+v", back, sample())
	}
}

func TestLoadRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"version":   "version: 2\ntables: {}\n",
		"unknown":   "version: 1\nbogus: 1\ntables: {}\n",
		"bad type":  "version: 1\ntables:\n  t:\n    columns:\n      a: 'two words'\n",
		"not yaml":  "version: [",
		"empty typ": "version: 1\ntables:\n  t:\n    columns:\n      a: '?'\n",
	} {
		path := filepath.Join(dir, name+".yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Load(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestCompareReportsEveryKindOfDrift(t *testing.T) {
	want := sample()
	if problems := Compare(want, clone(want), DialectPostgres); len(problems) != 0 {
		t.Fatalf("identical catalogues differ: %v", problems)
	}
	drift := clone(want)
	widgets := drift.Tables["widgets"]
	widgets.Columns["name"] = Column{Type: "int32"}           // type
	widgets.Columns["owner"] = Column{Type: "uuid"}           // nullability
	widgets.Columns["slug"] = Column{Type: "text"}            // generated flag
	delete(widgets.Columns, "tags")                           // missing
	widgets.Columns["extra"] = Column{Type: "text"}           // not in the catalogue
	widgets.PrimaryKey = []string{"name"}                     // key
	widgets.Unique = [][]string{{"slug", "owner"}, {"extra"}} // lacks (name), extra set
	widgets.ForeignKeys = nil                                 // foreign key
	drift.Tables["widgets"] = widgets
	delete(drift.Tables, "owners")
	drift.Tables["stray"] = Table{Columns: map[string]Column{}}
	problems := strings.Join(Compare(want, drift, DialectPostgres), "\n")
	for _, expected := range []string{
		"column widgets.name has type int32; the catalogue has text",
		"column widgets.owner is NOT NULL; the catalogue has it nullable",
		"column widgets.slug generated=false; the catalogue has generated=true",
		"column widgets.tags is missing",
		"column widgets.extra is not in the catalogue",
		"table widgets has primary key (name); the catalogue has (id)",
		"table widgets lacks the unique set (name)",
		"table widgets has the unique set (extra), which the catalogue lacks",
		"table widgets has foreign keys []; the catalogue has [owner -> owners(id)]",
		"table owners is missing",
		"table stray is not in the catalogue",
	} {
		if !strings.Contains(problems, expected) {
			t.Errorf("missing %q in\n%s", expected, problems)
		}
	}
}

func TestCompareAllowsWhatMySQLAddsOnPurpose(t *testing.T) {
	want := sample()
	got := clone(want)
	widgets := got.Tables["widgets"]
	widgets.Columns["tags"] = Column{Type: "json", Nullable: true}       // array stored as JSON
	widgets.Columns["live_slug"] = Column{Type: "text", Generated: true} // helper of a partial index
	widgets.Unique = append(widgets.Unique, []string{"live_slug"})
	got.Tables["widgets"] = widgets
	if problems := Compare(want, got, DialectMySQL); len(problems) != 0 {
		t.Fatalf("MySQL's own additions were reported: %v", problems)
	}
	if problems := Compare(want, got, DialectPostgres); len(problems) == 0 {
		t.Fatal("PostgreSQL must match exactly")
	}
	widgets.Columns["plain"] = Column{Type: "text"}
	widgets.Columns["name"] = Column{Type: "int32"}
	widgets.Unique = append(widgets.Unique, []string{"plain"})
	got.Tables["widgets"] = widgets
	problems := strings.Join(Compare(want, got, DialectMySQL), "\n")
	for _, expected := range []string{"widgets.plain is not in the catalogue", "widgets.name has type int32", "unique set (plain)"} {
		if !strings.Contains(problems, expected) {
			t.Errorf("missing %q in\n%s", expected, problems)
		}
	}
}

func TestCompareSeeds(t *testing.T) {
	want := map[string][]map[string]string{"a": {{"k": "1"}}, "b": {{"k": "2"}}}
	if problems := CompareSeeds(want, want); len(problems) != 0 {
		t.Fatal(problems)
	}
	if problems := CompareSeeds(want, map[string][]map[string]string{"a": {{"k": "1"}, {"k": "1"}}, "b": {{"k": "2"}}}); len(problems) != 1 || !strings.Contains(problems[0], "present 2 time(s); the catalogue has it 1") {
		t.Fatalf("a duplicated seed row was not reported: %v", problems)
	}
	got := map[string][]map[string]string{"a": {{"k": "9"}}, "c": {{"k": "3"}}}
	problems := strings.Join(CompareSeeds(want, got), "\n")
	for _, expected := range []string{"seed row {k=1;} of a is present 0 time(s); the catalogue has it 1 time(s)", "seed row {k=9;} of a is not in the catalogue", "seed row {k=2;} of b is present 0 time(s)", "table c holds seed rows"} {
		if !strings.Contains(problems, expected) {
			t.Errorf("missing %q in\n%s", expected, problems)
		}
	}
}

func TestLogicalTypeMapping(t *testing.T) {
	for _, tc := range []struct{ dataType, udt, want string }{
		{"uuid", "uuid", "uuid"}, {"text", "text", "text"}, {"character varying", "varchar", "text"}, {"boolean", "bool", "bool"},
		{"integer", "int4", "int32"}, {"bigint", "int8", "int64"}, {"smallint", "int2", "int16"}, {"numeric", "numeric", "decimal"},
		{"double precision", "float8", "float64"}, {"timestamp with time zone", "timestamptz", "timestamp"}, {"jsonb", "jsonb", "json"},
		{"bytea", "bytea", "bytes"}, {"ARRAY", "_text", "array<text>"}, {"ARRAY", "_uuid", "array<uuid>"}, {"date", "date", "date"},
	} {
		got, err := postgresLogicalType(tc.dataType, tc.udt)
		if err != nil || got != tc.want {
			t.Errorf("postgres %s/%s = %q, %v; want %q", tc.dataType, tc.udt, got, err, tc.want)
		}
	}
	for _, tc := range []struct{ dataType, udt string }{{"tsvector", "tsvector"}, {"ARRAY", "_tsvector"}, {"USER-DEFINED", "citext"}} {
		if _, err := postgresLogicalType(tc.dataType, tc.udt); err == nil {
			t.Errorf("postgres %s was mapped", tc.dataType)
		}
	}
	n := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	c := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	for _, tc := range []struct {
		dataType, columnType string
		length               sql.NullInt64
		collation            sql.NullString
		want                 string
	}{
		{"char", "char(36)", n(36), c("ascii_bin"), "uuid"}, {"char", "char(8)", n(8), c("utf8mb4_0900_bin"), "text"}, {"char", "char(36)", n(36), c("utf8mb4_0900_bin"), "text"},
		{"varchar", "varchar(64)", n(64), c("utf8mb4_0900_bin"), "text"}, {"longtext", "longtext", n(4294967295), c(""), "text"},
		{"tinyint", "tinyint(1)", sql.NullInt64{}, sql.NullString{}, "bool"}, {"smallint", "smallint", sql.NullInt64{}, sql.NullString{}, "int16"},
		{"int", "int unsigned", sql.NullInt64{}, sql.NullString{}, "int32"}, {"bigint", "bigint", sql.NullInt64{}, sql.NullString{}, "int64"},
		{"decimal", "decimal(10,2)", sql.NullInt64{}, sql.NullString{}, "decimal"}, {"double", "double", sql.NullInt64{}, sql.NullString{}, "float64"},
		{"datetime", "datetime(6)", sql.NullInt64{}, sql.NullString{}, "timestamp"}, {"json", "json", sql.NullInt64{}, sql.NullString{}, "json"},
		{"varbinary", "varbinary(32)", n(32), sql.NullString{}, "bytes"}, {"binary", "binary(32)", n(32), sql.NullString{}, "bytes"},
	} {
		got, err := mysqlLogicalType(tc.dataType, tc.columnType, tc.length, tc.collation)
		if err != nil || got != tc.want {
			t.Errorf("mysql %s = %q, %v; want %q", tc.columnType, got, err, tc.want)
		}
	}
	for _, columnType := range []string{"enum('a','b')", "tinyint(4)", "geometry"} {
		dataType := strings.SplitN(columnType, "(", 2)[0]
		if _, err := mysqlLogicalType(dataType, columnType, sql.NullInt64{}, sql.NullString{}); err == nil {
			t.Errorf("mysql %s was mapped", columnType)
		}
	}
	if !compatible("array<text>", "json") || compatible("array<text>", "text") || compatible("json", "text") || !compatible("uuid", "uuid") {
		t.Error("compatible is wrong")
	}
}

func TestSeedValuesAreRenderedAlikeOnBothDrivers(t *testing.T) {
	for _, tc := range []struct {
		logical string
		value   any
		want    string
	}{
		{"bool", true, "true"}, {"bool", int64(0), "false"}, {"bool", []byte("1"), "true"}, {"bool", "t", "true"},
		{"json", `{"b": 1, "a": [2]}`, `{"a":[2],"b":1}`}, {"json", []byte(`{"a":1}`), `{"a":1}`},
		{"array<text>", "{a,b}", `["a","b"]`}, {"array<text>", `{"a,b",c,"d\"e"}`, `["a,b","c","d\"e"]`}, {"array<text>", []byte(`["a","b"]`), `["a","b"]`}, {"array<text>", "{}", `[]`},
		{"bytes", []byte{0xde, 0xad}, "dead"}, {"int64", int64(7), "7"}, {"text", []byte("hello"), "hello"}, {"decimal", []byte("1.50"), "1.50"},
	} {
		got, err := seedValue(tc.logical, tc.value)
		if err != nil || got != tc.want {
			t.Errorf("seedValue(%s, %v) = %q, %v; want %q", tc.logical, tc.value, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		logical string
		value   any
	}{{"json", "not json"}, {"array<text>", "{a"}, {"array<text>", "not an array"}, {"array<text>", `{"a}`}} {
		if got, err := seedValue(tc.logical, tc.value); err == nil {
			t.Errorf("seedValue(%s, %v) = %q; want an error", tc.logical, tc.value, got)
		}
	}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDialectScopeRule(t *testing.T) {
	scope := func(entries string) string { return "version: 1\nmigrations:\n" + entries }
	cases := map[string]struct {
		files map[string]string
		want  []string
	}{
		"both directories":        {map[string]string{"postgres/000002_a.sql": "", "mysql/000002_a.sql": ""}, nil},
		"baselines are exempt":    {map[string]string{"postgres/000001_baseline.sql": ""}, nil},
		"named postgres only":     {map[string]string{"postgres/000002_a.sql": "", ScopeFile: scope("  000002_a.sql: {dialect: postgres, reason: notify trigger}\n")}, nil},
		"named mysql only":        {map[string]string{"mysql/000002_a.sql": "", ScopeFile: scope("  000002_a.sql: {dialect: mysql, reason: lock buckets}\n")}, nil},
		"missing dialect fixture": {map[string]string{"postgres/000002_a.sql": ""}, []string{"000002_a.sql ships only in migrations/postgres"}},
		"wrong dialect":           {map[string]string{"postgres/000002_a.sql": "", ScopeFile: scope("  000002_a.sql: {dialect: mysql, reason: x}\n")}, []string{"declared mysql-only"}},
		"no reason":               {map[string]string{"postgres/000002_a.sql": "", ScopeFile: scope("  000002_a.sql: {dialect: postgres, reason: ' '}\n")}, []string{"needs a reason"}},
		"stale entry":             {map[string]string{"postgres/000002_a.sql": "", "mysql/000002_a.sql": "", ScopeFile: scope("  000002_a.sql: {dialect: postgres, reason: x}\n")}, []string{"must not name it"}},
		"entry without a file":    {map[string]string{"postgres/000002_a.sql": "", "mysql/000002_a.sql": "", ScopeFile: scope("  000009_gone.sql: {dialect: postgres, reason: x}\n")}, []string{"names 000009_gone.sql, which no dialect directory ships"}},
		"non-sql ignored":         {map[string]string{"postgres/README.md": ""}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			problems, err := CheckDialectScope(writeTree(t, tc.files))
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(problems, "\n")
			if len(tc.want) == 0 && len(problems) != 0 {
				t.Fatalf("unexpected problems: %s", joined)
			}
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("missing %q in %q", want, joined)
				}
			}
		})
	}
	if _, err := CheckDialectScope(writeTree(t, map[string]string{ScopeFile: "version: ["})); err == nil {
		t.Error("a broken scope file was accepted")
	}
	if _, err := CheckDialectScope(writeTree(t, map[string]string{ScopeFile: "bogus: 1"})); err == nil {
		t.Error("an unknown scope field was accepted")
	}
}

// TestRepositoryMigrationsShipInEveryDialectOrAreNamed is the rule of ADR 0022: after the
// baseline every migration ships in both dialect directories or is named in
// migrations/dialect-scope.yaml. make test fails when a new file breaks it.
func TestRepositoryMigrationsShipInEveryDialectOrAreNamed(t *testing.T) {
	problems, err := CheckDialectScope(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range problems {
		t.Error(problem)
	}
}

func TestCheckedInCatalogueIsWellFormed(t *testing.T) {
	catalogue, err := Load(filepath.Join("..", "..", "migrations", "catalogue.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(catalogue.Tables) < 100 {
		t.Fatalf("the catalogue lists %d tables", len(catalogue.Tables))
	}
	for name, table := range catalogue.Tables {
		if len(table.Columns) == 0 || len(table.PrimaryKey) == 0 {
			t.Errorf("table %s has no columns or no primary key", name)
		}
		for _, column := range table.PrimaryKey {
			if c, ok := table.Columns[column]; !ok || c.Nullable {
				t.Errorf("table %s: primary key column %s is missing or nullable", name, column)
			}
		}
		for _, set := range table.Unique {
			for _, column := range set {
				if _, ok := table.Columns[column]; !ok {
					t.Errorf("table %s: unique column %s is not a column", name, column)
				}
			}
		}
		for _, fk := range table.ForeignKeys {
			local, remote, ok := strings.Cut(fk, " -> ")
			target, _, _ := strings.Cut(remote, "(")
			if _, known := catalogue.Tables[target]; !ok || !known || local == "" {
				t.Errorf("table %s: foreign key %q does not point at a catalogue table", name, fk)
			}
		}
	}
	// The scan must not have left volatile values behind.
	for name, rows := range catalogue.Seeds {
		for _, row := range rows {
			for column, value := range row {
				if uuidShape.MatchString(value) {
					t.Errorf("seed %s.%s holds the volatile value %s", name, column, value)
				}
			}
		}
	}
}
