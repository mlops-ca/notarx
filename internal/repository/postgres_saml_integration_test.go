//go:build integration

package repository

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/repository/explaincheck"
)

func samlStore(t *testing.T) *PostgresStore {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestSAMLPostgresStateContract(t *testing.T) {
	store := samlStore(t)
	testSAMLStateContract(t, store)
}

// TestSAMLConcurrentConsumeIsSingleUse races several consumers of one request state; exactly one wins.
func TestSAMLPostgresConcurrentConsumeIsSingleUse(t *testing.T) {
	store := samlStore(t)
	ctx := context.Background()
	provider := ids.New()
	id, err := store.CreateSAMLRequestState(ctx, provider, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan bool, 8)
	for range cap(results) {
		go func() {
			ok, _ := store.ConsumeSAMLRequestState(ctx, provider, id, time.Now())
			results <- ok
		}()
	}
	wins := 0
	for range cap(results) {
		if <-results {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d consumers won, want 1", wins)
	}
}

// TestSAMLExpiryDeletesUseTheExpiresAtIndex asserts the retention janitor's expiry scan is index-served
// on both tables (ADR 0023 section 5).
func TestSAMLExpiryDeletesUseTheExpiresAtIndex(t *testing.T) {
	store := samlStore(t)
	for table, index := range map[string]string{
		"saml_request_state": "saml_request_state_expires_at_idx",
		"saml_assertion_ids": "saml_assertion_ids_expires_at_idx",
	} {
		t.Run(table, func(t *testing.T) {
			tx, err := store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			plan, err := explaincheck.Plan(t.Context(), tx, "SELECT ctid FROM "+table+" WHERE expires_at < $1 ORDER BY expires_at LIMIT $2", time.Now(), 10)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.UsesIndex(index) {
				t.Fatalf("plan scans %v, want %s", plan.Indexes(), index)
			}
		})
	}
}

func TestSAMLPostgresIdentityContract(t *testing.T) {
	store := samlStore(t)
	ctx := context.Background()
	suffix := strings.ToLower(ids.New()[:12])
	provider, err := store.CreateIdentityProvider(ctx, identity.Provider{
		ID: ids.New(), Slug: "saml-" + suffix, Kind: identity.KindSAML, DisplayName: "SAML provider",
		Settings: []byte(`{"entityId":"https://idp.example.test/m"}`), ProvisioningMode: identity.ProvisioningJIT, JITDefaultRole: identity.JITRoleNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(ctx, "DELETE FROM user_sessions WHERE user_id IN (SELECT user_id FROM user_identities WHERE provider_id=$1::uuid)", provider.ID)
		_, _ = store.db.ExecContext(ctx, "DELETE FROM user_identities WHERE provider_id=$1::uuid", provider.ID)
		_, _ = store.db.ExecContext(ctx, "DELETE FROM users WHERE name IN ('ada-'||$1, 'saml-'||$1) OR name LIKE '%-'||$1", suffix)
		_, _ = store.db.ExecContext(ctx, "DELETE FROM identity_providers WHERE id=$1::uuid", provider.ID)
	})
	testSAMLIdentityContract(t, store, provider.ID, suffix)
	testSAMLEmailLinkContract(t, store, provider.ID, suffix)

	// The consistency check of migration 000019 refuses a verified account without a verifier or a time.
	user, err := store.CreateUser(ctx, User{ID: ids.New(), Name: "check-" + suffix, Email: "check-" + suffix + "@example.test", Role: "member", State: UserActive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE users SET email_verified_by_admin=true WHERE id=$1::uuid", user.ID); err == nil {
		t.Fatal("a verified account without verifier and time must violate users_email_verified_audit_check")
	}
}
