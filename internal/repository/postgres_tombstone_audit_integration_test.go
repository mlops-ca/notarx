//go:build integration

package repository

import (
	"database/sql"
	"os"
	"testing"

	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/testsupport"
)

// TestPostgresTombstonesRollBackWithTheirAudit makes every audit insert fail and proves each
// tombstone store method a management handler calls rolls its change back (ADR 0023 section 1).
func TestPostgresTombstonesRollBackWithTheirAudit(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var db *sql.DB
	if db, err = database.OpenPostgres(databaseURL, database.DefaultPoolConfig()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var restores []func()
	runTombstoneAuditContract(t, store, func(repositoryName, actor string) {
		restores = append(restores, testsupport.FailSecurityAudits(t, db, repositoryName, actor))
	}, func() {
		for _, restore := range restores {
			restore()
		}
	})
}
