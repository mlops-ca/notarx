package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLintAcceptsRerunnableMigration(t *testing.T) {
	body := `-- +goose Up
CREATE TABLE IF NOT EXISTS widgets (id CHAR(36) NOT NULL, name VARCHAR(64), PRIMARY KEY (id), KEY (name, id));
ALTER TABLE widgets ADD COLUMN size INT;
CREATE INDEX widgets_size ON widgets (size);
CREATE OR REPLACE VIEW widget_names AS SELECT name FROM widgets;
CREATE TRIGGER IF NOT EXISTS widgets_bi BEFORE INSERT ON widgets FOR EACH ROW SET NEW.size = 1;
INSERT INTO widgets (id, name) VALUES ('a', 'b') ON DUPLICATE KEY UPDATE name = VALUES(name);
UPDATE widgets SET size = 2 WHERE size IS NULL;
DELETE FROM widgets WHERE name = 'x';
DROP TABLE IF EXISTS old_widgets;
DROP VIEW IF EXISTS old_view;
DROP TRIGGER IF EXISTS old_trigger;
DROP INDEX old_idx ON widgets;
ALTER TABLE widgets ADD CONSTRAINT fk FOREIGN KEY (id) REFERENCES other (id) ON DELETE CASCADE;
RENAME TABLE a TO b;
SET @x = 1;
TRUNCATE TABLE scratch;
`
	if findings := LintSQL("000002_ok.sql", body); len(findings) != 0 {
		t.Fatalf("unexpected findings: %v", findings)
	}
}

func TestLintFlagsNonRerunnableStatements(t *testing.T) {
	cases := map[string]string{
		"INSERT INTO t (a) VALUES (1)":        "ON DUPLICATE KEY UPDATE",
		"INSERT IGNORE INTO t (a) VALUES (1)": "INSERT IGNORE",
		"REPLACE INTO t (a) VALUES (1)":       "REPLACE",
		"UPDATE t SET a = 1":                  "UPDATE without WHERE",
		"DELETE FROM t":                       "DELETE without WHERE",
		"CREATE TABLE t (id INT)":             "IF NOT EXISTS",
		"DROP TABLE t":                        "IF EXISTS",
		"DROP VIEW v":                         "IF EXISTS",
		"CREATE VIEW v AS SELECT 1":           "OR REPLACE",
		"CREATE TRIGGER x BEFORE INSERT ON t FOR EACH ROW SET NEW.a = 1": "IF NOT EXISTS",
		"ALTER TABLE t ADD COLUMN a INT, ADD COLUMN b INT":               "one specification",
		"ALTER DATABASE d CHARACTER SET utf8mb4":                         "only ALTER TABLE",
		"GRANT ALL ON *.* TO x":                                          "not allowed",
		"CREATE USER u":                                                  "unsupported CREATE",
		"DELIMITER //":                                                   "DELIMITER",
		"CREATE PROCEDURE p() SELECT 1":                                  "stored routines",
	}
	for statement, want := range cases {
		t.Run(statement, func(t *testing.T) {
			findings := LintSQL("000002_bad.sql", statement+";")
			if len(findings) != 1 || !strings.Contains(findings[0].Message, want) {
				t.Fatalf("findings = %v; want one containing %q", findings, want)
			}
		})
	}
}

func TestFindingString(t *testing.T) {
	if got := (Finding{File: "a.sql", Message: "m"}).String(); got != "a.sql: m" {
		t.Fatal(got)
	}
	if got := (Finding{File: "a.sql", Statement: 3, Message: "m"}).String(); got != "a.sql: statement 3: m" {
		t.Fatal(got)
	}
}

func TestLintDirSkipsTheBaselineAndReportsLaterFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("000001_baseline.sql", "CREATE TABLE t (id INT);\nINSERT INTO t VALUES (1);\n")
	write("000002_fixture.sql", "-- +goose Up\nINSERT INTO t (id) VALUES (2);\n-- +goose Down\nDELETE FROM t;\n")
	findings, err := LintDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].File != "000002_fixture.sql" || findings[0].Statement != 1 {
		t.Fatalf("findings = %v", findings)
	}
	if _, err := LintDir(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing directory was accepted")
	}
}

// TestRepositoryMySQLMigrationsAreRerunnable keeps every post-baseline file of
// migrations/mysql (ADR 0022) re-runnable once that directory exists.
func TestRepositoryMySQLMigrationsAreRerunnable(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations", "mysql")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("no MySQL migrations in this tree yet")
	}
	findings, err := LintDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		t.Error(finding)
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadDir(dir); err == nil {
		t.Fatal("an empty directory was accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "000002_b.sql"), []byte("SELECT 2;"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "000001_a.sql"), []byte("SELECT 1;\n-- +goose Down\nSELECT 0;"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.sql"), 0o700); err != nil {
		t.Fatal(err)
	}
	files, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Name != "000001_a.sql" || files[1].Name != "000002_b.sql" {
		t.Fatalf("files = %+v", files)
	}
	if files[0].Body != "SELECT 1;\n" || files[0].Checksum != checksum([]byte("SELECT 1;\n-- +goose Down\nSELECT 0;")) {
		t.Fatalf("the checksum covers the whole file and the body stops at the Down marker: %+v", files[0])
	}
	if err := os.WriteFile(filepath.Join(dir, "bad name.sql"), []byte("SELECT 1;"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDir(dir); err == nil || !strings.Contains(err.Error(), "unsupported migration filename") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join("..", "..", "migrations", "postgres", "000001_baseline.sql")); err == nil {
		files, err := LoadDir(filepath.Join("..", "..", "migrations", "postgres"))
		if err != nil || files[0].Name != "000001_baseline.sql" {
			t.Fatalf("the repository baseline does not load: %v", err)
		}
	}
}
