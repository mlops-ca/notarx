//go:build integration

package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/capacity"
	"github.com/mlops-ca/notarx/internal/repository/explaincheck"
)

// TestCapacityConcurrentFirstReservesCreateTheCounterRowOnce covers the lazy row: several
// transactions find no counter row and all try to create it; every reserve must succeed.
func TestCapacityConcurrentFirstReservesCreateTheCounterRowOnce(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	repo := counterTestRepository(t, store, FormatRaw)
	if _, err := store.db.Exec(`DELETE FROM repository_usage_counters WHERE repository_id=$1::uuid`, repo.ID); err != nil {
		t.Fatal(err)
	}
	const workers = 16
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := reserveInTx(ctx, store.db, repo.ID, 1, 1); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if bytes, objects, _ := counterRow(t, store.db, repo.ID); bytes != workers || objects != workers {
		t.Fatalf("counter %d %d, want %d %d", bytes, objects, workers, workers)
	}
}

// TestCapacityReconcilerLeavesPendingFormatsAlone: a pending format's writers do not call
// Reserve, so recomputing its counter would report every publish as drift.
func TestCapacityReconcilerLeavesPendingFormatsAlone(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	if capacity.Covered(string(FormatMaven)) {
		t.Skip("maven is covered")
	}
	repo := counterTestRepository(t, store, FormatMaven)
	if err := reserveInTx(ctx, store.db, repo.ID, 123, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := capacity.NewReconciler(store.db, 0, nil).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if bytes, objects, _ := counterRow(t, store.db, repo.ID); bytes != 123 || objects != 4 {
		t.Fatalf("a pending format's counter was recomputed: %d %d", bytes, objects)
	}
}

// TestCapacityReconcilerRunsShortlyAfterStart: nodes restarting more often than the interval
// must still reconcile.
func TestCapacityReconcilerRunsShortlyAfterStart(t *testing.T) {
	store := counterTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reconciler := capacity.NewReconciler(store.db, 6*time.Hour, nil).WithInitialDelay(100 * time.Millisecond)
	done := make(chan struct{})
	go func() { reconciler.Run(ctx); close(done) }()
	deadline := time.After(10 * time.Second)
	for reconciler.Stats().LastPassAt.IsZero() {
		select {
		case <-deadline:
			t.Fatal("no reconciliation pass within 10s of start with a 6h interval and a 100ms initial delay")
		case <-time.After(50 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

// TestAuditedMutationLookupsUseTheTransaction: with a single pooled connection, a mutation
// that reports not found after its statement must not wait for a second connection.
func TestAuditedMutationLookupsUseTheTransaction(t *testing.T) {
	store := counterTestStore(t)
	store.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	audit := AuditRecord{Actor: "tester", Outcome: AuditResolved, Format: "management", Operation: "test", Status: 200}
	missing := uuid.NewString()
	if _, err := store.UpdateUserPassword(ctx, missing, "hash", "1", false, audit); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateUserPassword = %v, want ErrNotFound", err)
	}
	if _, err := store.RevokeUserSessions(ctx, missing, "1", audit); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RevokeUserSessions = %v, want ErrNotFound", err)
	}
	if _, err := store.UpdateServiceAccount(ctx, ServiceAccountUpdate{ID: missing}, "1", audit); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateServiceAccount = %v, want ErrNotFound", err)
	}
	if _, err := store.CreateServiceAccountCredential(ctx, APIKey{ID: uuid.NewString(), ServiceAccountID: missing, Name: "k", SecretHash: uuid.NewString(), Roles: []string{}}, audit); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateServiceAccountCredential = %v, want ErrNotFound", err)
	}
	if _, err := store.CreateUserIdentity(ctx, UserIdentity{UserID: missing, Kind: UserIdentityOIDC, Issuer: "https://issuer.example", Subject: "x"}, audit); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateUserIdentity = %v, want ErrNotFound", err)
	}
}

// TestAPTGeneratedUsageIsIndexed: the measurement taken on every snapshot publication
// is served by an index of native_apt_snapshot_assets, not a scan of every pool asset of the visible snapshots.
func TestAPTGeneratedUsageUsesTheDistsIndex(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	plan, err := explaincheck.Plan(ctx, tx, `SELECT COALESCE(SUM(size),0)::bigint,COUNT(*)::bigint FROM (
		SELECT MAX(a.size)::bigint AS size
		FROM native_apt_snapshot_assets a JOIN native_apt_repository_snapshots s ON s.id=a.snapshot_id
		WHERE a.repository_id=$1::uuid AND s.state='visible' AND a.path LIKE 'dists/%'
		GROUP BY a.object_key) generated`, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	for _, relation := range plan.SequentialScans() {
		if relation == "native_apt_snapshot_assets" {
			t.Fatalf("the generated-object measurement scans every snapshot asset: %v", plan.Indexes())
		}
	}
	if len(plan.Indexes()) == 0 {
		t.Fatal("the generated-object measurement uses no index")
	}
}
