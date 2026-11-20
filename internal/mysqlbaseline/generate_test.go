package mysqlbaseline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/migrate"
	"github.com/mlops-ca/notarx/internal/schemaparity"
)

func root(parts ...string) string {
	return filepath.Join(append([]string{"..", ".."}, parts...)...)
}

// TestCheckedInBaselineIsGenerated is the drift check of ADR 0022 slice 3c: the checked-in
// baseline and decision lists are exactly what the catalogue and the decisions produce.
func TestCheckedInBaselineIsGenerated(t *testing.T) {
	catalogue, err := schemaparity.Load(root("migrations", "catalogue.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := LoadDecisions(root("migrations", "mysql-decisions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := Generate(catalogue, decisions)
	if err != nil {
		t.Fatalf("the catalogue needs decisions:\n%v", err)
	}
	for path, want := range map[string]string{
		root("migrations", "mysql", "000001_baseline.sql"): output.SQL,
		root("docs", "mysql-schema-decisions.md"):          output.Doc,
	} {
		have, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(have) != want {
			t.Errorf("%s is out of date; regenerate it with: go run ./tools/mysqlbaseline", path)
		}
	}
}

// TestBaselineSplitsIntoStatements runs the checked-in file through the migration runner's own loader and
// splitter, so a statement the runner cannot read fails here and not on a MySQL server. The lint rules for later
// migrations (IF NOT EXISTS and so on) do not apply to a baseline, which the runner exempts.
func TestBaselineSplitsIntoStatements(t *testing.T) {
	files, err := migrate.LoadDir(root("migrations", "mysql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "000001_baseline.sql" {
		t.Fatalf("files: %v", files)
	}
	body := files[0].Body
	for _, want := range []string{"CREATE TABLE `notarx_lock_buckets`", "INSERT INTO `notarx_lock_buckets`", "INSERT INTO `settings` (`key`, `value`) VALUES ('deployment_id', UUID());", "COLLATE=utf8mb4_0900_bin"} {
		if !strings.Contains(body, want) {
			t.Errorf("the baseline lacks %q", want)
		}
	}
	if strings.Contains(body, "utf8mb4_0900_ai_ci") {
		t.Error("an ai_ci collation is not allowed outside the parity allow-list")
	}
	if _, err := migrate.SplitStatements(body); err != nil {
		t.Errorf("the migration runner cannot split the baseline: %v", err)
	}
}

func miniCatalogue() schemaparity.Catalogue {
	return schemaparity.Catalogue{Version: 1, Tables: map[string]schemaparity.Table{
		"owners": {
			Columns:    map[string]schemaparity.Column{"id": {Type: "uuid"}, "name": {Type: "text"}, "path": {Type: "text"}, "created_at": {Type: "timestamp"}, "enabled": {Type: "bool"}, "tags": {Type: "array<text>"}, "note": {Type: "text", Nullable: true}, "seq": {Type: "int64"}, "doc": {Type: "json"}, "blob": {Type: "bytes"}, "score": {Type: "decimal", Nullable: true}},
			PrimaryKey: []string{"id"},
			Unique:     [][]string{{"name"}, {"path"}},
			Defaults:   map[string]string{"created_at": "now()", "enabled": "true", "tags": "'[]'", "seq": "identity", "doc": "'{}'", "note": "'it''s\\'", "id": "uuid()", "blob": "'\\x'"},
			Indexes:    []string{"created_at desc,name"},
			Special: map[string]string{
				"owners_live":  "unique USING btree (name) WHERE (enabled)",
				"owners_gin":   "USING gin (note gin_trgm_ops) WITH (fastupdate='on')",
				"owners_brin":  "USING brin (created_at)",
				"owners_nulls": "USING btree (created_at DESC NULLS LAST, name)",
				"owners_flag":  "USING btree (created_at) WHERE (enabled)",
				"owners_lower": "unique USING btree (lower(note))",
			},
			Checks: map[string]string{"owners_seq_check": "(seq >= 0)", "owners_skip_check": "(lower(name) ~ 'x')", "owners_by_hand": "(regexp_replace(name, 'a', 'b') = 'c')"},
		},
		"children": {
			Columns:     map[string]schemaparity.Column{"owner_id": {Type: "uuid"}, "path": {Type: "text"}, "kind": {Type: "text"}},
			PrimaryKey:  []string{"owner_id", "kind"},
			Unique:      [][]string{{"path"}},
			ForeignKeys: []string{"owner_id -> owners(id) on delete cascade", "path -> owners(path)"},
		},
	}, Seeds: map[string][]map[string]string{"owners": {{"name": "x", "enabled": "true", "doc": "{}", "seq": "1", "tags": "[]"}}}}
}

func miniDecisions() Decisions {
	return Decisions{
		Version: 1, DefaultKeyWidth: 64, KeyWidths: map[string]int{"kind": 8},
		HashNames: map[string]string{"path": "paths are long"},
		Arrays:    map[string]string{"owners.tags": "read whole"},
		Indexes: map[string]IndexDecision{
			"owners_flag":  {Action: ActionFlag, Flag: "is_enabled", Reason: "queue claim"},
			"owners_lower": {Action: ActionGenerated, Generated: []GeneratedColumn{{Name: "note_lower", Type: "VARCHAR(64)", Expression: "LOWER(`note`)", Nullable: true}}, Columns: []string{"note_lower"}, Reason: "case-insensitive"},
		},
		Checks:   map[string]CheckDecision{"owners_skip_check": {Skip: true, Reason: "validated in Go"}, "owners_by_hand": {SQL: "(`name` <> 'c')", Reason: "no REGEXP_REPLACE form"}},
		Triggers: map[string]TriggerDecision{}, Views: map[string]ViewDecision{}, Seeds: map[string]map[string]string{"owners": {"id": "UUID()"}},
	}
}

func TestGenerateSmallCatalogue(t *testing.T) {
	output, err := Generate(miniCatalogue(), miniDecisions())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"`id` CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT (UUID())",
		"`name` VARCHAR(64) NOT NULL",
		"`path` TEXT NOT NULL",
		"`path_sha256` BINARY(32) GENERATED ALWAYS AS (UNHEX(SHA2(`path`, 256))) STORED NOT NULL",
		"`created_at` DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)",
		"`enabled` TINYINT(1) NOT NULL DEFAULT 1",
		"`tags` JSON NOT NULL DEFAULT (CAST('[]' AS JSON))",
		"`seq` BIGINT NOT NULL AUTO_INCREMENT",
		"`doc` JSON NOT NULL DEFAULT (CAST('{}' AS JSON))",
		"`note` VARCHAR(64) NULL DEFAULT 'it''s\\\\'",
		"`blob` LONGBLOB NOT NULL DEFAULT (X'')",
		"`score` DECIMAL(20,10) NULL",
		"PRIMARY KEY (`id`)",
		"UNIQUE KEY `uq_owners_name` (`name`)",
		"UNIQUE KEY `uq_owners_path_sha256` (`path_sha256`)",
		"KEY `ix_owners_created_at_desc_name` (`created_at` DESC, `name`)",
		"KEY `ix_owners_nulls` (`created_at` DESC, `name`)",
		"`live_owners_live` TINYINT(1) GENERATED ALWAYS AS (IF((`enabled` <> 0), 1, NULL)) VIRTUAL NULL",
		"UNIQUE KEY `uq_owners_live` (`name`, `live_owners_live`)",
		"`is_enabled` TINYINT(1) GENERATED ALWAYS AS (IF((`enabled` <> 0), 1, 0)) VIRTUAL NOT NULL",
		"KEY `ix_owners_flag` (`is_enabled`, `created_at`)",
		"`note_lower` VARCHAR(64) GENERATED ALWAYS AS (LOWER(`note`)) VIRTUAL NULL",
		"UNIQUE KEY `uq_owners_lower` (`note_lower`)",
		"KEY `ix_owners_gin` (`note`)",
		"CONSTRAINT `owners_seq_check` CHECK ((`seq` >= 0))",
		"CONSTRAINT `owners_by_hand` CHECK ((`name` <> 'c'))",
		"ALTER TABLE `children` ADD CONSTRAINT `fk_children_owner_id` FOREIGN KEY (`owner_id`) REFERENCES `owners` (`id`) ON DELETE CASCADE ON UPDATE NO ACTION;",
		"ALTER TABLE `children` ADD CONSTRAINT `fk_children_path` FOREIGN KEY (`path_sha256`) REFERENCES `owners` (`path_sha256`) ON DELETE NO ACTION ON UPDATE NO ACTION;",
		"INSERT INTO `owners` (`doc`, `enabled`, `id`, `name`, `seq`, `tags`) VALUES (CAST('{}' AS JSON), 1, UUID(), 'x', 1, CAST('[]' AS JSON));",
		"SELECT a.n * 256 + b.n * 16 + c.n",
	} {
		if !strings.Contains(output.SQL, want) {
			t.Errorf("the baseline lacks %q", want)
		}
	}
	if strings.Contains(output.SQL, "owners_skip_check") || strings.Contains(output.SQL, "owners_brin") || strings.Contains(output.SQL, "ix_owners_brin") {
		t.Error("a skipped check or a dropped index was created")
	}
	for _, want := range []string{"# MySQL schema decisions", "| owners | owners_brin | drop |", "| owners | owners_skip_check | validated in Go |", "| owners | tags | JSON | read whole |", "| owners.path | paths are long |", "| owners | owners_lower | generated |"} {
		if !strings.Contains(output.Doc, want) {
			t.Errorf("the decision lists lack %q", want)
		}
	}
	again, err := Generate(miniCatalogue(), miniDecisions())
	if err != nil || again != output {
		t.Fatal("the generator is not deterministic")
	}
}

func TestGenerateReportsEveryMissingDecision(t *testing.T) {
	catalogue := miniCatalogue()
	owners := catalogue.Tables["owners"]
	owners.Special["owners_expr"] = "USING btree (lower(note), id)"
	owners.Special["owners_unique_expr"] = "unique USING btree (id) WHERE (bogus_fn(id))"
	owners.Special["owners_bad"] = "not an index"
	owners.Checks["owners_new_check"] = "(regexp_replace(name, 'a', 'b') = 'c')"
	owners.Defaults["score"] = "(now() + '1 day')"
	catalogue.Tables["owners"] = owners
	catalogue.Triggers = map[string]string{"owners_notify": "owners: AFTER INSERT"}
	catalogue.Views = []string{"owners_view"}
	decisions := miniDecisions()
	decisions.Arrays = nil
	_, err := Generate(catalogue, decisions)
	if err == nil {
		t.Fatal("missing decisions were accepted")
	}
	for _, want := range []string{
		"owners.tags is a PostgreSQL array",
		"index owners_expr of owners is an expression index",
		"owners_unique_expr",
		"unexpected index definition",
		"check owners_new_check of owners",
		"default of owners.score",
		"trigger owners_notify has no decision",
		"view owners_view needs an entry",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the problems lack %q:\n%v", want, err)
		}
	}
}

func TestGenerateRejectsBadDecisions(t *testing.T) {
	for name, mutate := range map[string]func(*Decisions){
		"unknown column":       func(d *Decisions) { d.Columns = map[string]ColumnDecision{"owners.nope": {Width: 4, Reason: "x"}} },
		"unknown default":      func(d *Decisions) { d.Defaults = map[string]string{"nope.x": "1"} },
		"unknown index":        func(d *Decisions) { d.Indexes["nope"] = IndexDecision{Action: ActionDrop, Reason: "x"} },
		"unknown check":        func(d *Decisions) { d.Checks["nope"] = CheckDecision{Skip: true, Reason: "x"} },
		"unknown trigger":      func(d *Decisions) { d.Triggers["nope"] = TriggerDecision{Disposition: DispositionDropped, Reason: "x"} },
		"unknown view":         func(d *Decisions) { d.Views["nope"] = ViewDecision{Disposition: "dropped", Reason: "x"} },
		"unknown seed table":   func(d *Decisions) { d.Seeds = map[string]map[string]string{"nope": {"x": "1"}} },
		"unknown seed column":  func(d *Decisions) { d.Seeds = map[string]map[string]string{"owners": {"nope": "1"}} },
		"index without reason": func(d *Decisions) { d.Indexes["owners_flag"] = IndexDecision{Action: ActionFlag, Flag: "f"} },
		"index bad action":     func(d *Decisions) { d.Indexes["owners_flag"] = IndexDecision{Action: "nope", Reason: "x"} },
		"flag without flag":    func(d *Decisions) { d.Indexes["owners_flag"] = IndexDecision{Action: ActionFlag, Reason: "x"} },
		"unique-flag misuse":   func(d *Decisions) { d.Indexes["owners_flag"] = IndexDecision{Action: ActionUniqueFlag, Reason: "x"} },
		"generated misuse":     func(d *Decisions) { d.Indexes["owners_flag"] = IndexDecision{Action: ActionGenerated, Reason: "x"} },
		"generated clash": func(d *Decisions) {
			d.Indexes["owners_flag"] = IndexDecision{Action: ActionGenerated, Reason: "x", Columns: []string{"name"}, Generated: []GeneratedColumn{{Name: "name", Type: "INT", Expression: "1"}}}
		},
		"check without form": func(d *Decisions) { d.Checks["owners_by_hand"] = CheckDecision{Reason: "x"} },
		"check no reason":    func(d *Decisions) { d.Checks["owners_skip_check"] = CheckDecision{Skip: true} },
		"flag clash": func(d *Decisions) {
			d.Indexes["owners_flag"] = IndexDecision{Action: ActionFlag, Flag: "name", Reason: "x"}
		},
		"hash non-text":  func(d *Decisions) { d.Columns = map[string]ColumnDecision{"owners.seq": {Hash: true, Reason: "x"}} },
		"hash no reason": func(d *Decisions) { d.Columns = map[string]ColumnDecision{"owners.name": {Hash: true}} },
	} {
		t.Run(name, func(t *testing.T) {
			decisions := miniDecisions()
			mutate(&decisions)
			if _, err := Generate(miniCatalogue(), decisions); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestGenerateKeyLimits(t *testing.T) {
	catalogue := schemaparity.Catalogue{Version: 1, Tables: map[string]schemaparity.Table{
		"wide": {
			Columns:    map[string]schemaparity.Column{"a": {Type: "text"}, "b": {Type: "text"}, "c": {Type: "text"}, "d": {Type: "text"}, "j": {Type: "json"}},
			PrimaryKey: []string{"a", "b", "c", "d"},
			Defaults:   map[string]string{"j": "now()"},
		},
		"big": {Columns: map[string]schemaparity.Column{"a": {Type: "uuid"}, "t": {Type: "float64"}, "x": {Type: "weird"}}, PrimaryKey: []string{"a"}, Defaults: map[string]string{"t": "identity"}},
	}}
	decisions := Decisions{Version: 1, DefaultKeyWidth: 255}
	_, err := Generate(catalogue, decisions)
	if err == nil || !strings.Contains(err.Error(), "the primary key needs 4080 key bytes") || !strings.Contains(err.Error(), "has the logical type weird") || !strings.Contains(err.Error(), "identity default needs an integer column") {
		t.Fatalf("err = %v", err)
	}
}

func TestGenerateNamesAndForeignKeys(t *testing.T) {
	long := strings.Repeat("c", 60)
	catalogue := schemaparity.Catalogue{Version: 1, Tables: map[string]schemaparity.Table{
		"p": {Columns: map[string]schemaparity.Column{"id": {Type: "uuid"}, "k": {Type: "text"}, "m": {Type: "text"}}, PrimaryKey: []string{"id"}, Unique: [][]string{{"k", "m"}}},
		"q": {
			Columns:    map[string]schemaparity.Column{"id": {Type: "uuid"}, "pk": {Type: "text"}, "pm": {Type: "text"}, long: {Type: "uuid"}},
			PrimaryKey: []string{"id"},
			ForeignKeys: []string{
				"pm,pk -> p(m,k) on update cascade", long + " -> p(id) on delete set null", "id -> nope(id)", "pk -> p(k)",
			},
		},
	}}
	_, err := Generate(catalogue, Decisions{Version: 1, DefaultKeyWidth: 32})
	if err == nil || !strings.Contains(err.Error(), "references the unknown table nope") || !strings.Contains(err.Error(), "does not match a primary or unique key of p") {
		t.Fatalf("err = %v", err)
	}
	q := catalogue.Tables["q"]
	q.ForeignKeys = []string{"pk,pm -> p(k,m) on update cascade", long + " -> p(id) on delete set null"}
	q.Checks = map[string]string{"shared_check": "(id <> pk)", "blocked_check": "(" + long + " IS NOT NULL)"}
	catalogue.Tables["q"] = q
	p := catalogue.Tables["p"]
	p.Checks = map[string]string{"shared_check": "(id <> k)"}
	catalogue.Tables["p"] = p
	_, err = Generate(catalogue, Decisions{Version: 1, DefaultKeyWidth: 32})
	if err == nil || !strings.Contains(err.Error(), "shared_check is used by both") {
		t.Fatalf("err = %v", err)
	}
	delete(p.Checks, "shared_check")
	catalogue.Tables["p"] = p
	output, err := Generate(catalogue, Decisions{Version: 1, DefaultKeyWidth: 32})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.SQL, "FOREIGN KEY (`pk`, `pm`) REFERENCES `p` (`k`, `m`) ON DELETE NO ACTION ON UPDATE CASCADE") {
		t.Error("the foreign key does not follow the order of the referenced unique key")
	}
	if strings.Contains(output.SQL, "blocked_check") || !strings.Contains(output.Doc, "| q | blocked_check |") {
		t.Error("a check on a foreign key action column must be left out and listed")
	}
	for _, line := range strings.Split(output.SQL, "\n") {
		if idx := strings.Index(line, "CONSTRAINT `fk_"); idx >= 0 {
			name := line[idx+len("CONSTRAINT `") : idx+len("CONSTRAINT `")+strings.Index(line[idx+len("CONSTRAINT `"):], "`")]
			if len(name) > maxIdentifier {
				t.Errorf("%s is longer than %d", name, maxIdentifier)
			}
		}
	}
}

func TestLoadDecisions(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"ok.yaml":      "version: 1\ndefaultKeyWidth: 10\n",
		"version.yaml": "version: 2\ndefaultKeyWidth: 10\n",
		"width.yaml":   "version: 1\n",
		"unknown.yaml": "version: 1\ndefaultKeyWidth: 10\nbogus: 1\n",
		"broken.yaml":  "version: [",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadDecisions(filepath.Join(dir, name))
		if (name == "ok.yaml") != (err == nil) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := LoadDecisions(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestGenerateKeepsIndexNamesUniquePerTable(t *testing.T) {
	catalogue := schemaparity.Catalogue{Version: 1, Tables: map[string]schemaparity.Table{
		"events": {
			Columns:    map[string]schemaparity.Column{"id": {Type: "int64"}, "at": {Type: "timestamp"}},
			PrimaryKey: []string{"at", "id"},
			Defaults:   map[string]string{"id": "identity"},
			Indexes:    []string{"at", "at desc", "id"},
		},
	}}
	output, err := Generate(catalogue, Decisions{Version: 1, DefaultKeyWidth: 32})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, line := range strings.Split(output.SQL, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "KEY ") {
			continue
		}
		name := strings.Fields(strings.TrimSpace(line))[1]
		if names[name] {
			t.Errorf("the index name %s is used twice in one table", name)
		}
		names[name] = true
	}
	if len(names) != 4 { // at, at DESC, id, and the helper key of the AUTO_INCREMENT column
		t.Fatalf("keys = %v", names)
	}
}

func TestGenerateEscapesLiteralDefaultsAndRejectsBadBooleans(t *testing.T) {
	catalogue := schemaparity.Catalogue{Version: 1, Tables: map[string]schemaparity.Table{
		"t": {
			Columns:    map[string]schemaparity.Column{"id": {Type: "uuid"}, "doc": {Type: "json"}, "at": {Type: "timestamp"}, "flag": {Type: "bool"}},
			PrimaryKey: []string{"id"},
			Defaults:   map[string]string{"doc": "'{\"re\":\"\\\\d\"}'", "at": "'2024-01-01 00:00:00+00'", "flag": "'maybe'"},
		},
	}, Seeds: map[string][]map[string]string{"t": {{"flag": "perhaps"}}}}
	t0 := catalogue.Tables["t"]
	t0.Columns["tags"] = schemaparity.Column{Type: "array<text>"}
	t0.Defaults["tags"] = "'{a,b}'"
	catalogue.Tables["t"] = t0
	_, err := Generate(catalogue, Decisions{Version: 1, DefaultKeyWidth: 32, Arrays: map[string]string{"t.tags": "read whole"}})
	if err == nil || !strings.Contains(err.Error(), "which is not JSON") {
		t.Fatalf("a PostgreSQL array literal was accepted as a JSON default: %v", err)
	}
	delete(t0.Defaults, "tags")
	delete(t0.Columns, "tags")
	_, err = Generate(catalogue, Decisions{Version: 1, DefaultKeyWidth: 32})
	if err == nil || !strings.Contains(err.Error(), "boolean text \"maybe\"") || !strings.Contains(err.Error(), "neither true nor false") {
		t.Fatalf("err = %v", err)
	}
	delete(catalogue.Tables["t"].Defaults, "flag")
	catalogue.Seeds = nil
	output, err := Generate(catalogue, Decisions{Version: 1, DefaultKeyWidth: 32})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DEFAULT (CAST('{\"re\":\"\\\\\\\\d\"}' AS JSON))", "DEFAULT '2024-01-01 00:00:00'"} {
		if !strings.Contains(output.SQL, want) {
			t.Errorf("the baseline lacks %q:\n%s", want, output.SQL)
		}
	}
}
