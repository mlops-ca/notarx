//go:build integration

package migrate

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
)

// The tests need TEST_DATABASE_URL (PostgreSQL, already migrated by
// scripts/run-migrations.sh) and TEST_MYSQL_URL (a MySQL 8.4 server with an
// account that may create databases). Each test works in a database of its own.

var integrationPool = database.PoolConfig{MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute}

func requireURL(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		if os.Getenv("NOTARX_MIGRATE_INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("%s is required", name)
		}
		t.Skipf("%s is not set", name)
	}
	return value
}

var databaseCounter struct {
	sync.Mutex
	n int
}

func uniqueName(t *testing.T) string {
	t.Helper()
	databaseCounter.Lock()
	defer databaseCounter.Unlock()
	databaseCounter.n++
	return fmt.Sprintf("notarx_migrate_%d_%d", os.Getpid(), databaseCounter.n)
}

// withDatabase returns the URL of url's server with the path replaced.
func withDatabase(t *testing.T, raw, name string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	return parsed.String()
}

func newDatabase(t *testing.T, driver, adminURL, create, drop string) (name, databaseURL string) {
	t.Helper()
	admin, err := database.Open(driver, adminURL, integrationPool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name = uniqueName(t)
	if _, err := admin.Exec(fmt.Sprintf(create, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(fmt.Sprintf(drop, name)) })
	return name, withDatabase(t, adminURL, name)
}

func openDB(t *testing.T, driver, databaseURL string) *sql.DB {
	t.Helper()
	db, err := database.Open(driver, databaseURL, integrationPool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func postgresDatabase(t *testing.T) (string, *sql.DB) {
	t.Helper()
	_, databaseURL := newDatabase(t, database.DriverPostgres, requireURL(t, "TEST_DATABASE_URL"), "CREATE DATABASE %s", "DROP DATABASE IF EXISTS %s WITH (FORCE)")
	return databaseURL, openDB(t, database.DriverPostgres, databaseURL)
}

func mysqlDatabase(t *testing.T) (string, string, *sql.DB) {
	t.Helper()
	name, databaseURL := newDatabase(t, database.DriverMySQL, requireURL(t, "TEST_MYSQL_URL"), "CREATE DATABASE %s CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin", "DROP DATABASE IF EXISTS %s")
	return name, databaseURL, openDB(t, database.DriverMySQL, databaseURL)
}

func fixtureDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func load(t *testing.T, dir string) []File {
	t.Helper()
	files, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func rowsOf(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		values := make([]sql.NullString, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(values))
		for i, v := range values {
			parts[i] = v.String
		}
		out = append(out, strings.Join(parts, " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestPostgresLedgerIsByteIdenticalToTheScript applies the repository baseline
// and later files with the Go runner to a fresh database and compares the
// ledger and the column catalogue with the database that
// scripts/run-migrations.sh migrated (ADR 0022 slice 2).
func TestPostgresLedgerIsByteIdenticalToTheScript(t *testing.T) {
	scriptURL := requireURL(t, "TEST_DATABASE_URL")
	scriptDB := openDB(t, database.DriverPostgres, scriptURL)
	_, goDB := postgresDatabase(t)
	files := load(t, filepath.Join("..", "..", "migrations", "postgres"))
	var out bytes.Buffer
	if err := Run(context.Background(), goDB, DialectPostgres, files, Options{Out: &out}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "Applying migration ") != len(files) {
		t.Fatalf("not every file was applied: %s", out.String())
	}
	const ledger = `SELECT filename || ' ' || checksum FROM notarx_schema_migrations ORDER BY filename COLLATE "C"`
	want, got := rowsOf(t, scriptDB, ledger), rowsOf(t, goDB, ledger)
	if len(want) != len(files) || strings.Join(want, "\n") != strings.Join(got, "\n") {
		t.Fatalf("ledger differs\nscript:\n%s\ngo:\n%s", strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
	const catalogue = `SELECT table_name || '.' || column_name || ' ' || data_type || ' ' || is_nullable FROM information_schema.columns WHERE table_schema = 'public' ORDER BY table_name COLLATE "C", column_name COLLATE "C"`
	if strings.Join(rowsOf(t, scriptDB, catalogue), "\n") != strings.Join(rowsOf(t, goDB, catalogue), "\n") {
		t.Fatal("the column catalogue differs between the script and the Go runner")
	}
	out.Reset()
	if err := Run(context.Background(), goDB, DialectPostgres, files, Options{Out: &out}); err != nil || strings.Count(out.String(), "Skipping applied migration ") != len(files) {
		t.Fatalf("second run: %v %s", err, out.String())
	}
}

func TestPostgresRunnerRefusalsAndRollback(t *testing.T) {
	_, db := postgresDatabase(t)
	dir := fixtureDir(t, map[string]string{"000001_a.sql": "CREATE TABLE a (id int);\n", "000002_b.sql": "CREATE TABLE b (id int);\nSELECT 1/0;\n"})
	err := Run(context.Background(), db, DialectPostgres, load(t, dir), Options{})
	if err == nil || !strings.Contains(err.Error(), "000002_b.sql failed") {
		t.Fatalf("err = %v", err)
	}
	if got := rowsOf(t, db, "SELECT filename FROM notarx_schema_migrations ORDER BY filename"); len(got) != 1 || got[0] != "000001_a.sql" {
		t.Fatalf("ledger = %v", got)
	}
	if got := rowsOf(t, db, "SELECT to_regclass('b')::text"); got[0] != "" {
		t.Fatalf("the failed file left table b behind: %v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "000001_a.sql"), []byte("CREATE TABLE a (id int);\n-- drift\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), db, DialectPostgres, load(t, dir), Options{}); !errors.Is(err, ErrChecksumChanged) {
		t.Fatalf("err = %v", err)
	}

	_, foreign := postgresDatabase(t)
	if _, err := foreign.Exec("CREATE TABLE hosted_repositories (id integer)"); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), foreign, DialectPostgres, load(t, dir), Options{}); !errors.Is(err, ErrForeignSchema) {
		t.Fatalf("err = %v", err)
	}
}

func TestMySQLAppliesAndSkips(t *testing.T) {
	_, _, db := mysqlDatabase(t)
	dir := fixtureDir(t, map[string]string{
		"000001_baseline.sql": "CREATE TABLE widgets (id CHAR(36) NOT NULL, name VARCHAR(64), PRIMARY KEY (id));\nCREATE TABLE gadgets (id INT PRIMARY KEY);\n",
		"000002_more.sql":     "ALTER TABLE widgets ADD COLUMN size INT;\nINSERT INTO gadgets (id) VALUES (1) ON DUPLICATE KEY UPDATE id = id;\n",
	})
	files := load(t, dir)
	var out bytes.Buffer
	if err := Run(context.Background(), db, DialectMySQL, files, Options{Out: &out}); err != nil {
		t.Fatal(err)
	}
	got := rowsOf(t, db, "SELECT filename, state, statements_done FROM notarx_schema_migrations ORDER BY filename")
	if len(got) != 2 || got[0] != "000001_baseline.sql applied 2" || got[1] != "000002_more.sql applied 2" {
		t.Fatalf("ledger = %v", got)
	}
	if rowsOf(t, db, "SELECT COUNT(*) FROM gadgets")[0] != "1" {
		t.Fatal("the backfill did not run")
	}
	out.Reset()
	if err := Run(context.Background(), db, DialectMySQL, files, Options{Out: &out}); err != nil || strings.Count(out.String(), "Skipping applied migration ") != 2 {
		t.Fatalf("second run: %v %s", err, out.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "000002_more.sql"), []byte("SELECT 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), db, DialectMySQL, load(t, dir), Options{}); !errors.Is(err, ErrChecksumChanged) {
		t.Fatalf("err = %v", err)
	}
}

func TestMySQLCrashLeavesApplyingAndRepairResumes(t *testing.T) {
	_, _, db := mysqlDatabase(t)
	dir := fixtureDir(t, map[string]string{
		"000001_baseline.sql": "CREATE TABLE widgets (id INT PRIMARY KEY);\n",
		"000002_more.sql":     "CREATE TABLE IF NOT EXISTS first_done (id INT);\nALTER TABLE not_yet ADD COLUMN x INT;\nCREATE TABLE IF NOT EXISTS last_one (id INT);\n",
	})
	files := load(t, dir)
	err := Run(context.Background(), db, DialectMySQL, files, Options{})
	if err == nil || !strings.Contains(err.Error(), "statement 2 of 3 failed") {
		t.Fatalf("err = %v", err)
	}
	if got := rowsOf(t, db, "SELECT state, statements_done FROM notarx_schema_migrations WHERE filename = '000002_more.sql'"); len(got) != 1 || got[0] != "applying 1" {
		t.Fatalf("ledger = %v", got)
	}
	if err := Run(context.Background(), db, DialectMySQL, files, Options{}); !errors.Is(err, ErrRepairRequired) {
		t.Fatalf("a half-applied migration ran without --repair: %v", err)
	}
	var out bytes.Buffer
	if err := Run(context.Background(), db, DialectMySQL, files, Options{Repair: true, Out: &out}); !errors.Is(err, ErrRepairRequired) || !strings.Contains(out.String(), "Next statement (2): ALTER TABLE not_yet") {
		t.Fatalf("repair without a count: %v %q", err, out.String())
	}
	// The operator fixes the cause and confirms that statement 2 did not take effect.
	if _, err := db.Exec("CREATE TABLE not_yet (id INT)"); err != nil {
		t.Fatal(err)
	}
	one := 1
	if err := Run(context.Background(), db, DialectMySQL, files, Options{Repair: true, Completed: &one, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if got := rowsOf(t, db, "SELECT state, statements_done FROM notarx_schema_migrations WHERE filename = '000002_more.sql'"); got[0] != "applied 3" {
		t.Fatalf("ledger = %v", got)
	}
	if rowsOf(t, db, "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'not_yet' AND column_name = 'x'")[0] != "1" || rowsOf(t, db, "SELECT COUNT(*) FROM last_one")[0] != "0" {
		t.Fatal("the resumed statements did not run")
	}
}

func TestMySQLRunnersExcludeEachOther(t *testing.T) {
	name, databaseURL, first := mysqlDatabase(t)
	second := openDB(t, database.DriverMySQL, databaseURL)
	dir := fixtureDir(t, map[string]string{"000001_baseline.sql": "CREATE TABLE widgets (id INT PRIMARY KEY);\nDO SLEEP(2);\nCREATE TABLE gadgets (id INT PRIMARY KEY);\n"})
	files := load(t, dir)

	var outputs [2]bytes.Buffer
	var errs [2]error
	var wg sync.WaitGroup
	for i, db := range []*sql.DB{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = Run(context.Background(), db, DialectMySQL, files, Options{Out: &outputs[i], LockWait: time.Minute})
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	applying := strings.Count(outputs[0].String()+outputs[1].String(), "Applying migration ")
	skipping := strings.Count(outputs[0].String()+outputs[1].String(), "Skipping applied migration ")
	if applying != 1 || skipping != 1 {
		t.Fatalf("two runners did not take turns: applying %d skipping %d\n%s\n%s", applying, skipping, outputs[0].String(), outputs[1].String())
	}

	// A lock held elsewhere makes a runner wait for LockWait and then give up.
	holder, err := first.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	var acquired int
	if err := holder.QueryRowContext(context.Background(), "SELECT GET_LOCK(?, 0)", lockName(name)).Scan(&acquired); err != nil || acquired != 1 {
		t.Fatalf("holder: %d %v", acquired, err)
	}
	if err := Run(context.Background(), second, DialectMySQL, files, Options{LockWait: time.Second}); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("err = %v", err)
	}
	if _, err := holder.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", lockName(name)); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), second, DialectMySQL, files, Options{LockWait: time.Second}); err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
}

func TestMySQLRunnerRefusesAForeignSchema(t *testing.T) {
	_, _, db := mysqlDatabase(t)
	if _, err := db.Exec("CREATE TABLE hosted_repositories (id INT)"); err != nil {
		t.Fatal(err)
	}
	dir := fixtureDir(t, map[string]string{"000001_baseline.sql": "CREATE TABLE widgets (id INT);\n"})
	if err := Run(context.Background(), db, DialectMySQL, load(t, dir), Options{}); !errors.Is(err, ErrForeignSchema) {
		t.Fatalf("err = %v", err)
	}
}
