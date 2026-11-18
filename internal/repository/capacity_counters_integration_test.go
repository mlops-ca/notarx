//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/capacity"
	"github.com/mlops-ca/notarx/internal/repository/explaincheck"
)

func counterTestStore(t *testing.T) *PostgresStore {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func counterTestRepository(t *testing.T, store *PostgresStore, format Format) HostedRepository {
	t.Helper()
	repo, err := store.CreateHostedRepository(context.Background(), HostedRepository{ID: uuid.NewString(), Name: string(format) + "-counter-" + uuid.NewString(), Format: format})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = store.db.Exec(`DELETE FROM hosted_repositories WHERE id=$1`, repo.ID) })
	return repo
}

func counterRow(t *testing.T, db *sql.DB, repositoryID string) (bytes, objects int64, quota sql.NullInt64) {
	t.Helper()
	if err := db.QueryRow(`SELECT bytes,objects,quota_bytes FROM repository_usage_counters WHERE repository_id=$1::uuid`, repositoryID).Scan(&bytes, &objects, &quota); err != nil {
		t.Fatalf("counter row of %s: %v", repositoryID, err)
	}
	return bytes, objects, quota
}

func reserveInTx(ctx context.Context, db *sql.DB, repositoryID string, bytes, objects int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = capacity.Reserve(ctx, tx, repositoryID, bytes, objects); err != nil {
		return err
	}
	return tx.Commit()
}

func TestCapacityReserveCreatesRowEnforcesQuotaAndReleases(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	repo := counterTestRepository(t, store, FormatRaw)
	if err := reserveInTx(ctx, store.db, repo.ID, 40, 1); err != nil {
		t.Fatalf("unlimited reserve creates the row: %v", err)
	}
	if bytes, objects, quota := counterRow(t, store.db, repo.ID); bytes != 40 || objects != 1 || quota.Valid {
		t.Fatalf("counter after first reserve: %d %d %v", bytes, objects, quota)
	}
	if _, err := store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, 100); err != nil {
		t.Fatal(err)
	}
	if _, _, quota := counterRow(t, store.db, repo.ID); quota.Int64 != 100 {
		t.Fatalf("quota API did not write the counter quota: %v", quota)
	}
	if err := reserveInTx(ctx, store.db, repo.ID, 60, 1); err != nil {
		t.Fatalf("reserve up to the quota: %v", err)
	}
	err := reserveInTx(ctx, store.db, repo.ID, 1, 1)
	if !errors.Is(err, ErrQuotaExceeded) || !IsQuotaExceeded(err) {
		t.Fatalf("reserve over quota = %v, want ErrQuotaExceeded", err)
	}
	if bytes, objects, _ := counterRow(t, store.db, repo.ID); bytes != 100 || objects != 2 {
		t.Fatalf("rejected reserve changed the counter: %d %d", bytes, objects)
	}
	// Releases are never rejected and the counter does not go below zero.
	if err = reserveInTx(ctx, store.db, repo.ID, -500, -5); err != nil {
		t.Fatal(err)
	}
	if bytes, objects, _ := counterRow(t, store.db, repo.ID); bytes != 0 || objects != 0 {
		t.Fatalf("counter after release: %d %d", bytes, objects)
	}
	if _, err = store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, quota := counterRow(t, store.db, repo.ID); quota.Valid {
		t.Fatalf("zero quota must clear the counter quota: %v", quota)
	}
	if err = reserveInTx(ctx, store.db, uuid.NewString(), 1, 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("reserve for a missing repository = %v, want sql.ErrNoRows", err)
	}
}

func TestCapacityConcurrentReservesRejectExactlyTheOverflow(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	repo := counterTestRepository(t, store, FormatRaw)
	if _, err := store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, 1000); err != nil {
		t.Fatal(err)
	}
	const workers = 30
	var accepted, rejected atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := reserveInTx(ctx, store.db, repo.ID, 100, 1); {
			case err == nil:
				accepted.Add(1)
			case errors.Is(err, ErrQuotaExceeded):
				rejected.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 10 || rejected.Load() != workers-10 {
		t.Fatalf("accepted %d rejected %d, want 10 and %d", accepted.Load(), rejected.Load(), workers-10)
	}
	if bytes, objects, _ := counterRow(t, store.db, repo.ID); bytes != 1000 || objects != 10 {
		t.Fatalf("counter %d %d, want 1000 10", bytes, objects)
	}
}

// TestCapacityReserveIsASinglePrimaryKeyUpdate asserts the plan of the guarded
// UPDATE touches only the repository's counter row through its primary key, so the cost
// does not depend on how many asset rows the repository holds.
func TestCapacityReserveIsASinglePrimaryKeyUpdate(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	repo := counterTestRepository(t, store, FormatRaw)
	if err := reserveInTx(ctx, store.db, repo.ID, 1, 1); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	plan, err := explaincheck.Plan(ctx, tx, capacity.ReserveStatement, repo.ID, int64(10), int64(1))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.UsesIndex("repository_usage_counters_pkey") {
		t.Fatalf("reserve plan does not use the primary key: %v", plan.Indexes())
	}
	touched := map[string]bool{}
	plan.Walk(func(node explaincheck.Node) {
		if node.Relation != "" {
			touched[node.Relation] = true
		}
	})
	if len(touched) != 1 || !touched["repository_usage_counters"] {
		t.Fatalf("reserve plan reads relations other than the counter row: %v", touched)
	}
}

func TestCapacityReconcilerCorrectsDriftAndReportsIt(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	repo := counterTestRepository(t, store, FormatRaw)
	digest := "sha256:" + strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
	if _, err := store.db.Exec(`INSERT INTO native_raw_objects(digest,repository_id,object_key,size) VALUES ($1,$2,$3,100)`, digest, repo.ID, "raw/"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO native_raw_assets(repository_id,path,digest,content_type) VALUES ($1,'a.bin',$2,'application/octet-stream')`, repo.ID, digest); err != nil {
		t.Fatal(err)
	}
	// No counter row yet and one that disagrees: both converge to the sum.
	if err := reserveInTx(ctx, store.db, repo.ID, 30, 3); err != nil {
		t.Fatal(err)
	}
	reconciler := capacity.NewReconciler(store.db, 0, nil)
	stats, err := reconciler.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bytes, objects, _ := counterRow(t, store.db, repo.ID); bytes != 100 || objects != 1 {
		t.Fatalf("reconciled counter %d %d, want 100 1", bytes, objects)
	}
	if stats.Corrected < 1 || stats.DriftBytes < 70 || !stats.Alerting || stats.MaxDriftRatio < 0.5 {
		t.Fatalf("reconciler did not report the drift: %+v", stats)
	}
	var reconciledAt sql.NullTime
	if err = store.db.QueryRow(`SELECT reconciled_at FROM repository_usage_counters WHERE repository_id=$1::uuid`, repo.ID).Scan(&reconciledAt); err != nil || !reconciledAt.Valid {
		t.Fatalf("reconciled_at not set: %v %v", reconciledAt, err)
	}
	again, err := reconciler.RunOnce(ctx)
	if err != nil || again.DriftBytes != 0 {
		t.Fatalf("second pass must find no drift: %+v %v", again, err)
	}
	if reconciler.Stats().DriftBytes != 0 {
		t.Fatalf("Stats must report the last pass: %+v", reconciler.Stats())
	}
}

func TestCapacityOverviewListsRepositoryWithoutCounterRow(t *testing.T) {
	store := counterTestStore(t)
	ctx := context.Background()
	repo := counterTestRepository(t, store, FormatRaw)
	if _, err := store.db.Exec(`DELETE FROM repository_usage_counters WHERE repository_id=$1::uuid`, repo.ID); err != nil {
		t.Fatal(err)
	}
	// A repository without a counter row (created after the backfill, never written) is
	// still listed by the overview.
	records, err := store.ListRepositoryCapacityRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range records {
		if record.Repository.ID == repo.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("overview misses a repository without a counter row")
	}
}
