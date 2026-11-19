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

// TestPostgresIdentityMutationsRollBackWithTheirAudit makes every insert into
// security_audit_log by the management actor fail and drives each user and service
// account handler: none of them may leave its change behind (ADR 0023 section 1).
func TestPostgresIdentityMutationsRollBackWithTheirAudit(t *testing.T) {
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
	started := time.Now().UTC().Add(-time.Second)
	var restore func()
	runIdentityAuditRollback(t, store, func() {
		// testAuthenticator's administrator acts as "alice"; management audits name no repository.
		restore = testsupport.FailSecurityAudits(t, db, "", "alice")
	}, func() { restore() }, func() []repository.AuditRecord {
		records, err := store.ListAudits(context.Background(), repository.AuditQuery{Actor: "alice", Format: "management", From: started, Limit: 1000})
		if err != nil {
			t.Fatal(err)
		}
		return records
	})
}

// TestPostgresLocalLoginRollsBackWithItsAudit proves the login counter and the
// user.login audit commit together.
func TestPostgresLocalLoginRollsBackWithItsAudit(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := repository.NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	db, err := database.OpenPostgres(databaseURL, database.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runLoginAuditRollback(t, store, func(actor string) { testsupport.FailSecurityAudits(t, db, "", actor) })
}

// TestPostgresSettingsMutationsRollBackWithTheirAudit makes every insert into
// security_audit_log by the management actor fail and drives the settings, theme, email,
// webhook and scheduled task handlers: none may leave its change behind (ADR 0023 section 1).
func TestPostgresSettingsMutationsRollBackWithTheirAudit(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := repository.NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	db, err := database.OpenPostgres(databaseURL, database.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var restores []func()
	runSettingsAuditRollback(t, store, func() {
		// Management audits name no repository; scheduled task audits name "global".
		restores = append(restores, testsupport.FailSecurityAudits(t, db, "", "alice"), testsupport.FailSecurityAudits(t, db, "global", "alice"))
	}, func() {
		for _, restore := range restores {
			restore()
		}
	})
}
