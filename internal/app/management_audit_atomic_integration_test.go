//go:build integration

package app

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/testsupport"
)

// TestPostgresManagementMutationsRollBackWithTheirAudit makes every insert into
// security_audit_log by the management actor fail and drives each repository policy,
// access, lifecycle job, quarantine, scan, intelligence and tombstone handler: none of them
// may leave its change behind (ADR 0023 section 1).
func TestPostgresManagementMutationsRollBackWithTheirAudit(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := repository.NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var db *sql.DB
	if db, err = database.OpenPostgres(databaseURL, database.DefaultPoolConfig()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The tombstoned raw object stays unreferenced; other tests list unreferenced raw objects
	// across the whole database, so remove what this test leaves behind.
	t.Cleanup(func() {
		scope := `(SELECT id FROM hosted_repositories WHERE name LIKE 'audit-raw-%')`
		tx, err := db.Begin()
		if err != nil {
			t.Errorf("cleanup: %v", err)
			return
		}
		defer func() { _ = tx.Rollback() }()
		for _, statement := range []string{
			`CREATE TEMP TABLE audit_raw_digests ON COMMIT DROP AS SELECT digest FROM native_raw_assets WHERE repository_id IN ` + scope + ` UNION SELECT digest FROM native_raw_asset_tombstones WHERE repository_id IN ` + scope,
			`DELETE FROM native_raw_asset_tombstones WHERE repository_id IN ` + scope,
			`DELETE FROM native_raw_assets WHERE repository_id IN ` + scope,
			`DELETE FROM native_raw_objects WHERE digest IN (SELECT digest FROM audit_raw_digests)`,
		} {
			if _, err := tx.Exec(statement); err != nil {
				t.Errorf("cleanup %q: %v", statement, err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	started := time.Now().UTC().Add(-time.Second)
	var restores []func()
	runManagementAuditRollback(t, store, func(repositoryNames ...string) {
		// testAuthenticator's administrator acts as "alice".
		for _, name := range repositoryNames {
			restores = append(restores, testsupport.FailSecurityAudits(t, db, name, "alice"))
		}
	}, func() {
		for _, restore := range restores {
			restore()
		}
	}, func() []repository.AuditRecord {
		records, err := store.ListAudits(context.Background(), repository.AuditQuery{Actor: "alice", From: started, Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		return records
	})
}
