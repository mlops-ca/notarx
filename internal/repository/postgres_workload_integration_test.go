//go:build integration

package repository

import (
	"context"
	"os"
	"testing"
)

func TestWorkloadPostgresStoreContract(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	t.Cleanup(func() {
		// The contract names every row it creates with a random suffix; sweep what it left behind.
		ctx := context.Background()
		_, _ = store.db.ExecContext(ctx, "DELETE FROM workload_trust_policies WHERE name ~ '^[a-e]-[0-9a-f]{12}$|^p-[0-9a-f]{12}$'")
		_, _ = store.db.ExecContext(ctx, "DELETE FROM workload_issuers WHERE name ~ '^gh-[0-9a-f]{12}'")
		_, _ = store.db.ExecContext(ctx, "DELETE FROM service_accounts WHERE name ~ '^wl2?-[0-9a-f]{12}$'")
	})
	testWorkloadStoreContract(t, store)
}

func TestWorkloadPostgresStoreRejectsInvalidRows(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// The CHECK constraints are the last line of defence behind the Go validation.
	for name, statement := range map[string]string{
		"empty audiences": "INSERT INTO workload_issuers (id,name,kind,issuer,audiences,jwks_mode) VALUES (gen_random_uuid(),'chk-a','generic','https://chk-a.example.test','{}','discovery')",
		"static no jwks":  "INSERT INTO workload_issuers (id,name,kind,issuer,audiences,jwks_mode) VALUES (gen_random_uuid(),'chk-b','generic','https://chk-b.example.test','{a}','static')",
		"hmac alg":        "INSERT INTO workload_issuers (id,name,kind,issuer,audiences,allowed_algs,jwks_mode) VALUES (gen_random_uuid(),'chk-c','generic','https://chk-c.example.test','{a}','{HS256}','discovery')",
	} {
		if _, err := store.db.ExecContext(t.Context(), statement); err == nil {
			t.Errorf("%s: the database accepted an invalid issuer", name)
			_, _ = store.db.ExecContext(context.Background(), "DELETE FROM workload_issuers WHERE name LIKE 'chk-%'")
		}
	}
}
