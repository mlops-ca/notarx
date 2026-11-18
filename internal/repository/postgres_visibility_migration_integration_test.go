//go:build integration

package repository

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
)

// TestVisibilityMigrationBackfillsAnonymousRead replays migration 000006 over
// the pre-migration shape inside a rolled-back transaction (ADR 0029, data
// migration): anonymous_read=true becomes 'public', false becomes 'private'.
func TestVisibilityMigrationBackfillsAnonymousRead(t *testing.T) {
	connection := os.Getenv("TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	migration, err := os.ReadFile("../../migrations/000006_repository_visibility.sql")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresStore(connection)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	// Return to the pre-migration shape.
	for _, statement := range []string{
		`DROP INDEX hosted_repositories_visibility_name_idx`,
		`DROP INDEX hosted_groups_visibility_name_idx`,
		`ALTER TABLE hosted_repositories DROP COLUMN visibility`,
		`ALTER TABLE hosted_groups DROP COLUMN visibility`,
		`ALTER TABLE hosted_repositories ADD COLUMN anonymous_read boolean NOT NULL DEFAULT false`,
		`ALTER TABLE hosted_groups ADD COLUMN anonymous_read boolean NOT NULL DEFAULT false`,
		`ALTER TABLE public_access_policy RENAME CONSTRAINT public_access_policy_pkey TO anonymous_access_policy_pkey`,
		`ALTER TABLE public_access_policy RENAME CONSTRAINT public_access_policy_singleton_check TO anonymous_access_policy_singleton_check`,
		`ALTER TABLE public_access_policy RENAME TO anonymous_access_policy`,
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	ids := map[string]string{"open": uuid.NewString(), "closed": uuid.NewString()}
	for name, id := range ids {
		if _, err = tx.ExecContext(ctx, `INSERT INTO hosted_repositories (id, name, format, repo_type, anonymous_read) VALUES ($1,$2,'raw','hosted',$3)`, id, "vis-repo-"+name, name == "open"); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO hosted_groups (id, name, format, anonymous_read) VALUES ($1,$2,'raw',$3)`, uuid.NewString(), "vis-group-"+name, name == "open"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"open": "public", "closed": "private"} {
		var repoVisibility, groupVisibility string
		if err = tx.QueryRowContext(ctx, `SELECT visibility FROM hosted_repositories WHERE name=$1`, "vis-repo-"+name).Scan(&repoVisibility); err != nil || repoVisibility != want {
			t.Fatalf("repository %s backfilled to %q (%v), want %q", name, repoVisibility, err, want)
		}
		if err = tx.QueryRowContext(ctx, `SELECT visibility FROM hosted_groups WHERE name=$1`, "vis-group-"+name).Scan(&groupVisibility); err != nil || groupVisibility != want {
			t.Fatalf("group %s backfilled to %q (%v), want %q", name, groupVisibility, err, want)
		}
	}
	var legacyColumns int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE column_name='anonymous_read' AND table_name IN ('hosted_repositories','hosted_groups')`).Scan(&legacyColumns); err != nil || legacyColumns != 0 {
		t.Fatalf("anonymous_read columns remain: %d, %v", legacyColumns, err)
	}
	if _, err = tx.ExecContext(ctx, "SAVEPOINT bad"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hosted_repositories SET visibility='internal' WHERE name='vis-repo-open'`); err == nil {
		t.Fatal("a value outside the enum must be rejected")
	}
}
