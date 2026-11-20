//go:build integration

package schemaparity

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/migrate"
)

// TEST_DATABASE_URL is the PostgreSQL that scripts/run-migrations.sh migrated;
// TEST_MYSQL_URL is a MySQL 8.4 server with an account that may create databases.

var pool = database.PoolConfig{MaxOpenConns: 2, MaxIdleConns: 1, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute}

func requireURL(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Skipf("%s is not set", name)
	}
	return value
}

func openDB(t *testing.T, driver, databaseURL string) *sql.DB {
	t.Helper()
	db, err := database.Open(driver, databaseURL, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// scratchDatabase creates a database of its own on the server of adminURL.
func scratchDatabase(t *testing.T, driver, adminURL, create, drop string) *sql.DB {
	t.Helper()
	admin := openDB(t, driver, adminURL)
	name := fmt.Sprintf("notarx_parity_%d_%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	if _, err := admin.Exec(fmt.Sprintf(create, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(fmt.Sprintf(drop, name)) })
	parsed, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	return openDB(t, driver, parsed.String())
}

// TestCheckedInCatalogueMatchesMigratedPostgres is the parity check on the reference backend. It migrates a
// database of its own: the shared test database holds the rows other packages wrote.
func TestCheckedInCatalogueMatchesMigratedPostgres(t *testing.T) {
	db := scratchDatabase(t, database.DriverPostgres, requireURL(t, "TEST_DATABASE_URL"), "CREATE DATABASE %s", "DROP DATABASE IF EXISTS %s WITH (FORCE)")
	files, err := migrate.LoadDir(filepath.Join("..", "..", "migrations", "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Run(context.Background(), db, migrate.DialectPostgres, files, migrate.Options{}); err != nil {
		t.Fatal(err)
	}
	want, err := Load(filepath.Join("..", "..", "migrations", "catalogue.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Introspect(context.Background(), db, DialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range Compare(want, got, DialectPostgres) {
		t.Error(problem)
	}
	seeds, err := IntrospectSeeds(context.Background(), db, DialectPostgres, want)
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range CompareSeeds(want.Seeds, seeds) {
		t.Error(problem)
	}
}

const fixturePostgres = `
CREATE TABLE owners (id uuid PRIMARY KEY, enabled boolean NOT NULL DEFAULT true);
INSERT INTO owners (id, enabled) VALUES (gen_random_uuid(), true);
CREATE TABLE widgets (
  id uuid PRIMARY KEY,
  name text NOT NULL,
  tags text[],
  size integer,
  slug text GENERATED ALWAYS AS (lower(name)) STORED,
  owner_id uuid REFERENCES owners (id),
  doc jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (name, owner_id)
);
CREATE UNIQUE INDEX widgets_slug_live ON widgets (slug) WHERE size IS NOT NULL;`

const fixtureMySQL = `
CREATE TABLE owners (id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY, enabled TINYINT(1) NOT NULL DEFAULT 1);
INSERT INTO owners (id, enabled) VALUES (UUID(), 1);
CREATE TABLE widgets (
  id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
  name VARCHAR(64) NOT NULL,
  tags JSON NULL,
  size INT NULL,
  slug VARCHAR(64) AS (LOWER(name)) STORED,
  owner_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
  doc JSON NOT NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  live_slug VARCHAR(64) AS (IF(size IS NOT NULL, slug, NULL)) VIRTUAL,
  UNIQUE KEY widgets_name_owner (name, owner_id),
  UNIQUE KEY widgets_slug_live (live_slug),
  CONSTRAINT widgets_owner_fk FOREIGN KEY (owner_id) REFERENCES owners (id)
) DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin`

func execAll(t *testing.T, db *sql.DB, script string) {
	t.Helper()
	for _, statement := range strings.Split(script, ";\n") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func fixtureCatalogue() Catalogue {
	return Catalogue{Version: CatalogueVersion, Tables: map[string]Table{
		"owners": {Columns: map[string]Column{"id": {Type: "uuid"}, "enabled": {Type: "bool"}}, PrimaryKey: []string{"id"}, Defaults: map[string]string{"enabled": "true"}},
		"widgets": {
			Columns: map[string]Column{
				"id": {Type: "uuid"}, "name": {Type: "text"}, "tags": {Type: "array<text>", Nullable: true}, "size": {Type: "int32", Nullable: true},
				"slug": {Type: "text", Nullable: true, Generated: true}, "owner_id": {Type: "uuid", Nullable: true}, "doc": {Type: "json"}, "created_at": {Type: "timestamp"},
			},
			PrimaryKey:  []string{"id"},
			Unique:      [][]string{{"name", "owner_id"}},
			ForeignKeys: []string{"owner_id -> owners(id)"},
			Defaults:    map[string]string{"doc": "'{}'", "created_at": "now()"},
			Special:     map[string]string{"widgets_slug_live": "unique USING btree (slug) WHERE (size IS NOT NULL)"},
		},
	}, Seeds: map[string][]map[string]string{"owners": {{"enabled": "true"}}}}
}

func checkFixture(t *testing.T, db *sql.DB, dialect string) []string {
	t.Helper()
	// A schema change must not meet a cached statement plan of an idle connection.
	db.SetMaxIdleConns(0)
	got, err := Introspect(context.Background(), db, dialect)
	if err != nil {
		t.Fatal(err)
	}
	want := fixtureCatalogue()
	problems := Compare(want, got, dialect)
	seeds, err := IntrospectSeeds(context.Background(), db, dialect, want)
	if err != nil {
		t.Fatal(err)
	}
	return append(problems, CompareSeeds(want.Seeds, seeds)...)
}

func TestPostgresFixtureMatchesAndDriftFails(t *testing.T) {
	db := scratchDatabase(t, database.DriverPostgres, requireURL(t, "TEST_DATABASE_URL"), "CREATE DATABASE %s", "DROP DATABASE IF EXISTS %s WITH (FORCE)")
	execAll(t, db, fixturePostgres)
	if problems := checkFixture(t, db, DialectPostgres); len(problems) != 0 {
		t.Fatalf("a matching schema drifted: %v", problems)
	}
	execAll(t, db, "ALTER TABLE widgets ALTER COLUMN size TYPE bigint")
	problems := strings.Join(checkFixture(t, db, DialectPostgres), "\n")
	if !strings.Contains(problems, "column widgets.size has type int64; the catalogue has int32") {
		t.Fatalf("a deliberately drifted column was not reported: %q", problems)
	}
}

func TestMySQLFixtureMatchesAndDriftFails(t *testing.T) {
	db := scratchDatabase(t, database.DriverMySQL, requireURL(t, "TEST_MYSQL_URL"), "CREATE DATABASE %s CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin", "DROP DATABASE IF EXISTS %s")
	execAll(t, db, fixtureMySQL)
	if problems := checkFixture(t, db, DialectMySQL); len(problems) != 0 {
		t.Fatalf("a matching schema drifted: %v", problems)
	}
	execAll(t, db, "ALTER TABLE widgets MODIFY name VARCHAR(64) NULL")
	problems := strings.Join(checkFixture(t, db, DialectMySQL), "\n")
	if !strings.Contains(problems, "column widgets.name is nullable; the catalogue has it NOT NULL") {
		t.Fatalf("a deliberately drifted column was not reported: %q", problems)
	}
}
