//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/background"
)

func openClaimStore(t *testing.T) *PostgresStore {
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

// claimFixture creates repositories and queues their jobs, and removes both when the test ends so a
// leftover pending job cannot be claimed by an unrelated test sharing the database.
type claimFixture struct {
	t     *testing.T
	store *PostgresStore
	repos []string
}

func newClaimFixture(t *testing.T, store *PostgresStore) *claimFixture {
	fixture := &claimFixture{t: t, store: store}
	t.Cleanup(func() {
		for _, id := range fixture.repos {
			_, _ = store.db.Exec(`DELETE FROM lifecycle_jobs WHERE repository_id=$1::uuid`, id)
		}
	})
	return fixture
}

func (f *claimFixture) repository(name string) string {
	f.t.Helper()
	repo, err := f.store.CreateHostedRepository(context.Background(), HostedRepository{ID: uuid.NewString(), Name: name + "-" + uuid.NewString(), Format: FormatRaw})
	if err != nil {
		f.t.Fatal(err)
	}
	f.repos = append(f.repos, repo.ID)
	return repo.ID
}

func (f *claimFixture) enqueue(repositoryID string, kind LifecycleJobKind, n int) {
	f.t.Helper()
	for i := 0; i < n; i++ {
		key := uuid.NewString()
		if _, _, err := f.store.EnqueueLifecycleJob(context.Background(), LifecycleJob{ID: uuid.NewString(), RepositoryID: repositoryID, Kind: kind, IdempotencyKey: key, Payload: []byte(`{"format":"raw"}`)}); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *claimFixture) running(repositoryID string) int {
	f.t.Helper()
	var count int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM lifecycle_jobs WHERE repository_id=$1::uuid AND state='running'`, repositoryID).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	return count
}

func TestPostgresLifecycleClaimCandidateQueriesDoNotSortThePendingSet(t *testing.T) {
	store := openClaimStore(t)
	for _, tc := range []struct {
		name  string
		query string
		args  []any
		index string
	}{
		{"one kind", claimLifecycleCandidatesByKindQuery, []any{string(LifecycleJobReclaim), []string{}, []string{}, 10}, "lifecycle_jobs_kind_actionable_idx"},
		{"all kinds", claimLifecycleCandidatesAnyKindQuery, []any{[]string{}, []string{}, 10}, "lifecycle_jobs_actionable_idx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := store.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			// Discourage the alternatives to an ordered index scan so the plan shows what the indexes can serve,
			// independent of how many rows other tests left behind.
			for _, setting := range []string{"enable_seqscan", "enable_bitmapscan", "enable_sort", "enable_incremental_sort"} {
				if _, err = tx.Exec(`SET LOCAL ` + setting + ` = off`); err != nil {
					t.Fatal(err)
				}
			}
			var raw []byte
			if err = tx.QueryRow(`EXPLAIN (FORMAT JSON) `+tc.query, tc.args...).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var plans []struct {
				Plan explainNode `json:"Plan"`
			}
			if err = json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
				t.Fatalf("explain output: %s %v", raw, err)
			}
			nodes := map[string]bool{}
			plans[0].Plan.walk(func(n explainNode) { nodes[n.NodeType] = true })
			for _, forbidden := range []string{"Sort", "Incremental Sort", "Unique", "WindowAgg", "HashAggregate", "GroupAggregate"} {
				if nodes[forbidden] {
					t.Fatalf("claim candidate plan contains %s: %s", forbidden, raw)
				}
			}
			if !strings.Contains(string(raw), tc.index) {
				t.Fatalf("claim candidate plan does not use %s: %s", tc.index, raw)
			}
		})
	}
}

func TestPostgresLifecycleClaimHonoursRepoConcurrencyUnderConcurrentClaimers(t *testing.T) {
	store := openClaimStore(t)
	fixture := newClaimFixture(t, store)
	hot := fixture.repository("hot")
	fixture.enqueue(hot, LifecycleJobReclaim, 20)
	scan := fixture.repository("scan")
	fixture.enqueue(scan, LifecycleJobScan, 10)
	var others []string
	for i := 0; i < 5; i++ {
		id := fixture.repository("other")
		fixture.enqueue(id, LifecycleJobReclaim, 1)
		others = append(others, id)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.ClaimLifecycleJobs(context.Background(), 10)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	// A claimer that loses the repository lock skips the row for another claimer, so drain what is left.
	for i := 0; i < 3; i++ {
		if _, err := store.ClaimLifecycleJobs(context.Background(), 10); err != nil {
			t.Fatal(err)
		}
	}
	if got := fixture.running(hot); got != 1 {
		t.Fatalf("hot repository running=%d, want the default RepoConcurrency of 1", got)
	}
	if got := fixture.running(scan); got != 4 {
		t.Fatalf("scan repository running=%d, want the scan RepoConcurrency of 4", got)
	}
	for _, id := range others {
		if got := fixture.running(id); got != 1 {
			t.Fatalf("repository %s running=%d: a hot repository starved it", id, got)
		}
	}
}

func TestPostgresLifecycleClaimScansPastAHotRepositoryBacklog(t *testing.T) {
	store := openClaimStore(t)
	fixture := newClaimFixture(t, store)
	hot := fixture.repository("hot")
	fixture.enqueue(hot, LifecycleJobReclaim, 30)
	var others []string
	for i := 0; i < 3; i++ {
		id := fixture.repository("cold")
		fixture.enqueue(id, LifecycleJobReclaim, 1)
		others = append(others, id)
	}
	var scanned int
	store.SetQueueClaimObserver(func(_ LifecycleJobKind, rows int) { scanned = rows })
	jobs, err := store.ClaimLifecycleJobsByKind(context.Background(), LifecycleJobReclaim, 4)
	if err != nil {
		t.Fatal(err)
	}
	claimed := map[string]int{}
	for _, job := range jobs {
		claimed[job.RepositoryID]++
	}
	if claimed[hot] != 1 {
		t.Fatalf("hot repository claimed %d jobs, want 1: %v", claimed[hot], claimed)
	}
	for _, id := range others {
		if claimed[id] != 1 {
			t.Fatalf("repository %s was starved by the hot backlog: %v", id, claimed)
		}
	}
	if scanned < len(jobs) || scanned > QueueClaimScanCap {
		t.Fatalf("scanned rows=%d for %d claimed jobs", scanned, len(jobs))
	}
}

func TestPostgresLifecycleClaimAcrossKindsRoundRobinsWhenTheScanCapIsHit(t *testing.T) {
	store := openClaimStore(t)
	fixture := newClaimFixture(t, store)
	hot := fixture.repository("hot")
	// One repository's reclaim backlog fills the whole scan budget of the all-kinds index; only one of its jobs can run.
	fixture.enqueue(hot, LifecycleJobReclaim, QueueClaimScanCap+10)
	cold := fixture.repository("cold")
	fixture.enqueue(cold, LifecycleJobPromotion, 1)
	jobs, err := store.ClaimLifecycleJobs(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	claimed := map[string]int{}
	for _, job := range jobs {
		claimed[job.RepositoryID]++
	}
	if claimed[hot] != 1 || claimed[cold] != 1 {
		t.Fatalf("the promotion job behind the reclaim backlog was starved: %v", claimed)
	}
}

func TestPostgresLifecycleClaimByKindAndFormatsGivesEachFormatItsShare(t *testing.T) {
	store := openClaimStore(t)
	fixture := newClaimFixture(t, store)
	// A deep, older backlog of one format must not starve a newer job of another format in the same claim.
	for i := 0; i < 20; i++ {
		repo := fixture.repository("deep")
		if _, _, err := store.EnqueueLifecycleJob(context.Background(), LifecycleJob{ID: uuid.NewString(), RepositoryID: repo, Kind: LifecycleJobReclaim, IdempotencyKey: uuid.NewString(), Payload: []byte(`{"format":"maven"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	other := fixture.repository("other")
	if _, _, err := store.EnqueueLifecycleJob(context.Background(), LifecycleJob{ID: uuid.NewString(), RepositoryID: other, Kind: LifecycleJobReclaim, IdempotencyKey: uuid.NewString(), Payload: []byte(`{"format":"npm"}`)}); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.ClaimLifecycleJobsByKindAndFormats(context.Background(), LifecycleJobReclaim, []Format{FormatMaven, FormatNPM}, 4)
	if err != nil {
		t.Fatal(err)
	}
	var npm bool
	for _, job := range jobs {
		npm = npm || job.RepositoryID == other
	}
	if len(jobs) != 4 || !npm {
		t.Fatalf("claimed %d jobs, npm served: %v; the older maven backlog starved npm", len(jobs), npm)
	}
}

func TestPostgresLifecycleClaimByKindAndFormatsClaimsEveryListedFormatOnce(t *testing.T) {
	store := openClaimStore(t)
	fixture := newClaimFixture(t, store)
	repoFormat := map[string]Format{}
	for _, format := range []Format{FormatRaw, FormatNPM, FormatMaven} {
		repo := fixture.repository("formats")
		repoFormat[repo] = format
		if _, _, err := store.EnqueueLifecycleJob(context.Background(), LifecycleJob{ID: uuid.NewString(), RepositoryID: repo, Kind: LifecycleJobReclaim, IdempotencyKey: uuid.NewString(), Payload: []byte(`{"format":"` + string(format) + `"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := store.ClaimLifecycleJobsByKindAndFormats(context.Background(), LifecycleJobReclaim, []Format{FormatRaw, FormatNPM}, 10)
	if err != nil {
		t.Fatal(err)
	}
	claimed := map[Format]bool{}
	for _, job := range jobs {
		if format, ok := repoFormat[job.RepositoryID]; ok {
			claimed[format] = true
		}
	}
	if !claimed[FormatRaw] || !claimed[FormatNPM] || claimed[FormatMaven] {
		t.Fatalf("claimed formats %v, want raw and npm only", claimed)
	}
}

func TestPostgresLifecycleClaimDoesNotRecoverExpiredLeases(t *testing.T) {
	store := openClaimStore(t)
	fixture := newClaimFixture(t, store)
	repo := fixture.repository("lease")
	fixture.enqueue(repo, LifecycleJobReclaim, 1)
	jobs, err := store.ClaimLifecycleJobsByKind(context.Background(), LifecycleJobReclaim, 1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim=%v err=%v", jobs, err)
	}
	if _, err = store.db.Exec(`UPDATE lifecycle_jobs SET lease_expires_at=now()-interval '1 minute' WHERE id=$1::uuid`, jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	again, err := store.ClaimLifecycleJobsByKind(context.Background(), LifecycleJobReclaim, 1)
	if err != nil || len(again) != 0 {
		t.Fatalf("a claim recovered an expired lease: %v err=%v", again, err)
	}
	if recovered, err := store.RecoverExpiredLifecycleJobs(context.Background(), jobs[0].LeaseExpiresAt.AddDate(0, 0, 1)); err != nil || recovered < 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
}

// The final write of a claimed job outlives the cancellation of its worker's context: a job finished as the
// process stops is completed, and one interrupted by the stop goes back to the queue with its attempt
// returned, instead of staying leased until the lease expires or consuming an attempt for a deploy.
func TestPostgresLifecycleTerminalWritesSurviveACancelledContext(t *testing.T) {
	store := openClaimStore(t)
	fixture := newClaimFixture(t, store)
	// One repository per job: a repository runs one job of a kind at a time.
	repos := []string{fixture.repository("terminal-a"), fixture.repository("terminal-b"), fixture.repository("terminal-c"), fixture.repository("terminal-d")}
	for _, repo := range repos {
		fixture.enqueue(repo, LifecycleJobReclaim, 1)
	}
	claimed := map[string]LifecycleJob{}
	mine := map[string]bool{}
	for _, repo := range repos {
		mine[repo] = true
	}
	for range 10 {
		jobs, err := store.ClaimLifecycleJobsByKind(context.Background(), LifecycleJobReclaim, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range jobs {
			if mine[job.RepositoryID] {
				claimed[job.RepositoryID] = job
				continue
			}
			// A job another test left behind is handed straight back, so this test leaves nothing leased.
			if err := store.ReleaseLifecycleJobLease(context.Background(), job.ID, job.LeaseToken); err != nil {
				t.Fatal(err)
			}
		}
		if len(claimed) >= len(repos) {
			break
		}
	}
	for _, repo := range repos {
		if _, ok := claimed[repo]; !ok {
			t.Fatalf("no job of repository %s was claimed", repo)
		}
	}
	stopped, stop := context.WithCancelCause(context.Background())
	stop(background.ErrShutdown)
	finished, interrupted, failed, lostLease := claimed[repos[0]], claimed[repos[1]], claimed[repos[2]], claimed[repos[3]]

	if err := store.CompleteLifecycleJob(stopped, finished.ID, finished.LeaseToken); err != nil {
		t.Fatalf("complete on a cancelled context: %v", err)
	}
	got, err := store.GetLifecycleJob(context.Background(), repos[0], finished.ID)
	if err != nil || got.State != LifecycleJobCompleted || got.LeaseToken != "" {
		t.Fatalf("completed job = %+v, %v", got, err)
	}
	if err := store.FailLifecycleJob(stopped, interrupted.ID, interrupted.LeaseToken, "context canceled"); err != nil {
		t.Fatalf("fail on a cancelled context: %v", err)
	}
	got, err = store.GetLifecycleJob(context.Background(), repos[1], interrupted.ID)
	if err != nil || got.State != LifecycleJobPending || got.Attempts != interrupted.Attempts-1 || got.LastError != "context canceled" {
		t.Fatalf("interrupted job = %+v (claimed with %d attempts), %v; want pending with the attempt returned", got, interrupted.Attempts, err)
	}
	// A context cancelled for another reason (a lost lease cancels its job's context) is a failure that counts its attempt.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.FailLifecycleJob(cancelled, lostLease.ID, lostLease.LeaseToken, "lease lost"); err != nil {
		t.Fatalf("fail on a cancelled job context: %v", err)
	}
	got, err = store.GetLifecycleJob(context.Background(), repos[3], lostLease.ID)
	if err != nil || got.State != LifecycleJobRetrying || got.Attempts != lostLease.Attempts {
		t.Fatalf("job whose lease was lost = %+v, %v; want retrying with its attempt counted, not requeued as interrupted", got, err)
	}
	// A failure on a live context still counts its attempt.
	if err := store.FailLifecycleJob(context.Background(), failed.ID, failed.LeaseToken, "boom"); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetLifecycleJob(context.Background(), repos[2], failed.ID)
	if err != nil || got.State != LifecycleJobRetrying || got.Attempts != failed.Attempts {
		t.Fatalf("failed job = %+v, %v; want retrying with its attempt counted", got, err)
	}
}
