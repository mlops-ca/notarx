//go:build integration

package app

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
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
	seed := func(t *testing.T, store settingsRollbackStore, target repository.EmailTarget) (repository.WebhookSubscription, string, repository.EmailDelivery) {
		t.Helper()
		ctx := context.Background()
		// The subscription is disabled so it never receives other tests' events, and the dead
		// deliveries are inserted directly so no other test's pending delivery is claimed.
		subscription, err := store.CreateWebhookSubscription(ctx, repository.WebhookSubscription{ID: uuid.NewString(), Name: "hook-" + uuid.NewString()[:8], EndpointURL: "https://events.example.test/hooks", SecretCiphertext: "synthetic", EventTypes: []repository.WebhookEventType{repository.WebhookEventArtifactQuarantined}, Enabled: false})
		if err != nil {
			t.Fatal(err)
		}
		eventID, webhookDeliveryID, emailID := uuid.NewString(), uuid.NewString(), uuid.NewString()
		if _, err = db.ExecContext(ctx, `INSERT INTO webhook_events (id, event_type, occurred_at, payload) VALUES ($1::uuid, 'artifact.quarantined', now(), '{}'::jsonb)`, eventID); err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO webhook_deliveries (id, event_id, subscription_id, state, attempts, last_status, last_error) VALUES ($1::uuid, $2::uuid, $3::uuid, 'dead', 8, 503, 'HTTP 503')`, webhookDeliveryID, eventID, subscription.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO email_test_deliveries (id, event_id, request_key, target_id, target_version, scenario, locale, template_version, recipient_ciphertext, from_address, console_origin, state, attempts, error_code)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::bigint, 'warning', 'en-CA', 'v1', 'synthetic', 'gateway@example.test', 'https://console.example.test', 'dead', 8, 'smtp_permanent_rejection')`, emailID, uuid.NewString(), uuid.NewString(), target.ID, target.Version); err != nil {
			t.Fatal(err)
		}
		dead, err := store.GetEmailDelivery(ctx, emailID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = db.Exec(`DELETE FROM webhook_deliveries WHERE id=$1::uuid`, webhookDeliveryID)
			_, _ = db.Exec(`DELETE FROM webhook_events WHERE id=$1::uuid`, eventID)
			_, _ = db.Exec(`DELETE FROM email_test_deliveries WHERE id=$1::uuid`, emailID)
		})
		return subscription, webhookDeliveryID, dead
	}
	runSettingsAuditRollback(t, store, seed, func() {
		// Management audits name no repository; scheduled task audits name "global".
		restores = append(restores, testsupport.FailSecurityAudits(t, db, "", "alice"), testsupport.FailSecurityAudits(t, db, "global", "alice"))
	}, func() {
		for _, restore := range restores {
			restore()
		}
	})
}

// TestPostgresFormatPreviewMutationsRollBackWithTheirAudit makes every insert into
// security_audit_log for the preview repositories and groups fail and drives the Helm and
// NuGet preview handlers: none may leave its change behind (ADR 0023 section 1).
func TestPostgresFormatPreviewMutationsRollBackWithTheirAudit(t *testing.T) {
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
	runFormatPreviewAuditRollback(t, store, func(names ...string) {
		// testAuthenticator's administrator acts as "alice"; preview audits name the repository or group.
		for _, name := range names {
			restores = append(restores, testsupport.FailSecurityAudits(t, db, name, "alice"))
		}
	}, func() {
		for _, restore := range restores {
			restore()
		}
		restores = nil
	})
}
