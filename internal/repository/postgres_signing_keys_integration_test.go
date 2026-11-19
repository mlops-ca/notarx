//go:build integration

package repository

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/secrets"
	"github.com/mlops-ca/notarx/internal/tokenissuer"
)

func signingKeyStore(t *testing.T) *PostgresStore {
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
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	t.Setenv(secrets.KeyPreviousEnv, "")
	clear := func() { _, _ = store.db.ExecContext(context.Background(), "DELETE FROM token_signing_keys") }
	clear()
	t.Cleanup(clear)
	return store
}

func TestSigningKeyPartialUniqueIndexes(t *testing.T) {
	store := signingKeyStore(t)
	ctx := context.Background()
	gen := tokenissuer.Generator{RSABits: 2048}
	insert := func(alg string, state tokenissuer.State) error {
		record, err := gen.Generate(alg, state, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.WithRotationLock(ctx, func(ctx context.Context, tx tokenissuer.KeyTx) error { return tx.Insert(ctx, record) })
		return err
	}
	for _, state := range []tokenissuer.State{tokenissuer.StateActive, tokenissuer.StateNext} {
		if err := insert(jose.ES256, state); err != nil {
			t.Fatal(err)
		}
		if err := insert(jose.ES256, state); err == nil {
			t.Fatalf("a second %s ES256 key was accepted", state)
		}
		// Another algorithm has its own pair; retired keys are unlimited.
		if err := insert(jose.RS256, state); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := insert(jose.ES256, tokenissuer.StateRetired); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSigningKeyConcurrentWorkersConverge(t *testing.T) {
	store := signingKeyStore(t)
	manager := func() *tokenissuer.Manager {
		config := tokenissuer.DefaultLifecycleConfig()
		return &tokenissuer.Manager{Store: store, Config: config, Generator: tokenissuer.Generator{RSABits: 2048}}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := manager().Reconcile(context.Background()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// A follow-up pass by any worker finds nothing left to do.
	if _, changed, err := manager().Reconcile(context.Background()); err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	records, err := store.ListKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var active, next int
	for _, record := range records {
		if err := record.VerifyIntegrity(); err != nil {
			t.Fatalf("a row written through PostgreSQL must keep a valid MAC (jsonb round trip): %v", err)
		}
		switch record.State {
		case tokenissuer.StateActive:
			active++
		case tokenissuer.StateNext:
			next++
		}
	}
	if active != 1 || next != 1 || len(records) != 2 {
		t.Fatalf("active=%d next=%d rows=%d", active, next, len(records))
	}
	signer, err := tokenissuer.NewLocalSigner(store, tokenissuer.SignerConfig{Alg: jose.RS256})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := tokenissuer.ClaimBuilder{Issuer: "https://issuer.example.test"}.Build(tokenissuer.ClaimRequest{Subject: "s", Audiences: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := signer.Sign(context.Background(), claims); err != nil {
		t.Fatal(err)
	}
}

func TestSigningKeyLockIsExclusiveAcrossTransactions(t *testing.T) {
	store := signingKeyStore(t)
	ctx := context.Background()
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = store.WithRotationLock(ctx, func(context.Context, tokenissuer.KeyTx) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	acquired, err := store.WithRotationLock(ctx, func(context.Context, tokenissuer.KeyTx) error { return nil })
	close(release)
	<-done
	if err != nil || acquired {
		t.Fatalf("acquired=%v err=%v", acquired, err)
	}
}

func TestDeploymentIDIsStableAndSettingsRoundTrip(t *testing.T) {
	store := signingKeyStore(t)
	ctx := context.Background()
	first, err := store.DeploymentID(ctx)
	if err != nil || first == "" {
		t.Fatalf("id=%q err=%v", first, err)
	}
	if second, _ := store.DeploymentID(ctx); second != first {
		t.Fatalf("deployment id changed: %s %s", first, second)
	}
	if _, err := store.Setting(ctx, "unit-test-missing"); err != ErrSettingNotFound {
		t.Fatalf("err=%v", err)
	}
	if err := store.SetSetting(ctx, "unit-test-key", "a"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSetting(ctx, "unit-test-key", "b"); err != nil {
		t.Fatal(err)
	}
	if value, _ := store.Setting(ctx, "unit-test-key"); value != "b" {
		t.Fatal(value)
	}
	_, _ = store.db.ExecContext(ctx, "DELETE FROM settings WHERE key = 'unit-test-key'")
}
