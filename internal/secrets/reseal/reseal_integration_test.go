//go:build integration

package reseal_test

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/mlops-ca/notarx/internal/secrets"
	"github.com/mlops-ca/notarx/internal/secrets/reseal"
	"github.com/mlops-ca/notarx/internal/testsupport"
)

const (
	keyA = "0123456789abcdef0123456789abcdef"
	keyB = "fedcba9876543210fedcba9876543210"
)

// The reseal SQL touches only these columns, so the test schema declares just
// them; TestTargetsCoverSealedColumns ties the names to the baseline.
var schemaDDL = []string{
	"CREATE TABLE email_targets (id uuid PRIMARY KEY, recipient_ciphertext text NOT NULL)",
	"CREATE TABLE email_test_deliveries (id uuid PRIMARY KEY, target_id uuid NOT NULL, recipient_ciphertext text NOT NULL)",
	"CREATE TABLE nexus_migration_sources (id uuid PRIMARY KEY, credential_ciphertext text NOT NULL DEFAULT '')",
	"CREATE TABLE oidc_settings (singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton), client_secret text NOT NULL DEFAULT '')",
	"CREATE TABLE webhook_subscriptions (id uuid PRIMARY KEY, secret_ciphertext text NOT NULL)",
	"CREATE TABLE hosted_repositories (id uuid PRIMARY KEY, upstream_auth jsonb)",
}

func TestResealRotatesEverySealedColumn(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	if err := testsupport.ValidateIsolatedDatabaseURL(raw); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	control, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	schema := "reseal_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := control.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := control.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	parsed, _ := url.Parse(raw)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	for _, ddl := range schemaDDL {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv(secrets.KeyEnv, keyA)
	t.Setenv(secrets.KeyPreviousEnv, "")
	seal := func(purpose, value string) string {
		sealed, err := secrets.Seal(purpose, value)
		if err != nil {
			t.Fatal(err)
		}
		return sealed
	}
	target, email, nexus, hook, repo := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	delivery := uuid.NewString()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("INSERT INTO email_targets VALUES ($1, $2)", email, seal("email-target:"+email, "ops@example.com"))
	exec("INSERT INTO email_test_deliveries VALUES ($1, $2, $3)", delivery, target, seal("email-target:"+target, "ops@example.com"))
	exec("INSERT INTO nexus_migration_sources VALUES ($1, $2)", nexus, seal("nexus-source:"+nexus, "creds"))
	exec("INSERT INTO nexus_migration_sources VALUES ($1, '')", uuid.NewString())
	exec("INSERT INTO oidc_settings (client_secret) VALUES ($1)", seal("oidc.client-secret", "oidc"))
	exec("INSERT INTO webhook_subscriptions VALUES ($1, $2)", hook, seal("webhook-subscription:"+hook, "hook-secret-32-bytes-long-enough!"))
	exec("INSERT INTO hosted_repositories VALUES ($1, jsonb_build_object('scheme','bearer','secret',$2::text))", repo, seal("repository-upstream-auth", "upstream"))
	exec("INSERT INTO hosted_repositories VALUES ($1, NULL)", uuid.NewString())

	// Rotate: B becomes current, A stays readable.
	t.Setenv(secrets.KeyEnv, keyB)
	t.Setenv(secrets.KeyPreviousEnv, keyA)

	dry, err := reseal.Run(ctx, db, true)
	if err != nil {
		t.Fatal(err)
	}
	if total(dry, func(r reseal.Result) int { return r.Resealed }) != 6 {
		t.Fatalf("dry run results: %+v", dry)
	}
	var stored string
	if err := db.QueryRowContext(ctx, "SELECT client_secret FROM oidc_settings").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if need, _ := secrets.NeedsReseal(stored); !need {
		t.Fatal("dry run must not write")
	}

	results, err := reseal.Run(ctx, db, false)
	if err != nil {
		t.Fatal(err)
	}
	if total(results, func(r reseal.Result) int { return r.Resealed }) != 6 {
		t.Fatalf("results: %+v", results)
	}
	again, err := reseal.Run(ctx, db, false)
	if err != nil {
		t.Fatal(err)
	}
	if total(again, func(r reseal.Result) int { return r.Resealed }) != 0 || total(again, func(r reseal.Result) int { return r.Unchanged }) != 6 {
		t.Fatalf("second run must be a no-op: %+v", again)
	}

	// Drop A: everything still opens, and the rows hold the current key.
	t.Setenv(secrets.KeyPreviousEnv, "")
	check := func(purpose, query string, args ...any) {
		t.Helper()
		var sealed string
		if err := db.QueryRowContext(ctx, query, args...).Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		if _, err := secrets.Open(purpose, sealed); err != nil {
			t.Fatalf("%s: %v", purpose, err)
		}
	}
	check("email-target:"+email, "SELECT recipient_ciphertext FROM email_targets WHERE id=$1", email)
	check("email-target:"+target, "SELECT recipient_ciphertext FROM email_test_deliveries WHERE id=$1", delivery)
	check("nexus-source:"+nexus, "SELECT credential_ciphertext FROM nexus_migration_sources WHERE id=$1", nexus)
	check("oidc.client-secret", "SELECT client_secret FROM oidc_settings")
	check("webhook-subscription:"+hook, "SELECT secret_ciphertext FROM webhook_subscriptions WHERE id=$1", hook)
	check("repository-upstream-auth", "SELECT upstream_auth->>'secret' FROM hosted_repositories WHERE id=$1", repo)
	var scheme string
	if err := db.QueryRowContext(ctx, "SELECT upstream_auth->>'scheme' FROM hosted_repositories WHERE id=$1", repo).Scan(&scheme); err != nil || scheme != "bearer" {
		t.Fatalf("other upstream_auth fields must survive: %q %v", scheme, err)
	}
}

func TestResealFailsNamingRowWhenKeyIsMissing(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	if err := testsupport.ValidateIsolatedDatabaseURL(raw); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	control, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	schema := "reseal_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := control.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = control.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	parsed, _ := url.Parse(raw)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	for _, ddl := range schemaDDL {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(secrets.KeyEnv, keyA)
	id := uuid.NewString()
	sealed, _ := secrets.Seal("webhook-subscription:"+id, "hook-secret-32-bytes-long-enough!")
	if _, err := db.ExecContext(ctx, "INSERT INTO webhook_subscriptions VALUES ($1, $2)", id, sealed); err != nil {
		t.Fatal(err)
	}
	t.Setenv(secrets.KeyEnv, keyB)
	_, err = reseal.Run(ctx, db, false)
	if err == nil || !strings.Contains(err.Error(), id) || strings.Contains(err.Error(), sealed) {
		t.Fatalf("err=%v", err)
	}
}

func total(results []reseal.Result, pick func(reseal.Result) int) int {
	sum := 0
	for _, result := range results {
		sum += pick(result)
	}
	return sum
}
