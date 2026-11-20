package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/repository"
)

type sweepFixture struct {
	store *repository.MemoryStore
	idle  repository.HostedRepository
	plain repository.HostedRepository
	now   time.Time
}

func newSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	store := repository.NewMemoryStore()
	create := func(name string, mode repository.RawSealMode) repository.HostedRepository {
		repo, err := store.CreateHostedRepository(context.Background(), repository.HostedRepository{ID: uuid.NewString(), Name: name, Format: repository.FormatRaw, RawLayout: repository.RawLayoutVersioned, SealMode: mode})
		if err != nil {
			t.Fatal(err)
		}
		return repo
	}
	return &sweepFixture{store: store, idle: create("idle-feed", repository.RawSealIdle), plain: create("explicit-feed", repository.RawSealExplicit), now: time.Now().UTC()}
}

func (f *sweepFixture) put(t *testing.T, repo repository.HostedRepository, path string) {
	t.Helper()
	sum := sha256.Sum256([]byte(repo.ID + path))
	asset := repository.RawAsset{RepositoryID: repo.ID, Path: path, Digest: fmt.Sprintf("sha256:%x", sum), ObjectKey: fmt.Sprintf("native/raw/sha256/%x", sum), Size: 3, ContentType: "text/plain"}
	if _, err := f.store.PutRawAssetIf(context.Background(), asset, repository.RawWriteCondition{RequireAbsent: true}); err != nil {
		t.Fatal(err)
	}
}

func (f *sweepFixture) state(t *testing.T, repo repository.HostedRepository, name, version string) repository.RawVersionState {
	t.Helper()
	found, err := f.store.GetRawVersion(context.Background(), repo.ID, name, version)
	if err != nil {
		t.Fatal(err)
	}
	return found.State
}

type sweepMetrics struct {
	mu       sync.Mutex
	runs     map[string]int
	sealed   int
	stale    int64
	oldest   float64
	gaugeSet bool
}

func (m *sweepMetrics) RecordRawVersionSweepRun(outcome string, sealed int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runs == nil {
		m.runs = map[string]int{}
	}
	m.runs[outcome]++
	m.sealed += sealed
}

func (m *sweepMetrics) SetRawOpenVersions(stale int64, oldest float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stale, m.oldest, m.gaugeSet = stale, oldest, true
}

func TestRawVersionSweepSealsOnlyIdleVersionsOfIdleRepositories(t *testing.T) {
	f := newSweepFixture(t)
	f.put(t, f.idle, "tool/1.0.0/a")
	f.put(t, f.plain, "tool/1.0.0/a")
	metrics := &sweepMetrics{}
	clock := f.now
	sweeper := RawVersionSweeper{Store: f.store, Metrics: metrics, Owner: "node-a", Idle: 5 * time.Minute, Interval: time.Hour, StaleAfter: 7 * 24 * time.Hour, Now: func() time.Time { return clock }}

	// The clock is at the write: the version is not idle yet.
	if sealed, err := sweeper.Run(context.Background()); err != nil || sealed != 0 {
		t.Fatalf("a version written just now was swept: %d %v", sealed, err)
	}
	if f.state(t, f.idle, "tool", "1.0.0") != repository.RawVersionOpen {
		t.Fatal("the version must still be open")
	}

	// Four minutes later is still inside the idle period; six minutes later is past it. The first completed run set
	// the minimum interval, so a node of a fleet that already swept waits for it.
	clock = f.now.Add(6 * time.Minute)
	if sealed, err := sweeper.Run(context.Background()); err != nil || sealed != 0 {
		t.Fatalf("the minimum interval between two completed sweeps was not enforced: %d %v", sealed, err)
	}
	sweeper.Interval = 0
	sealed, err := sweeper.Run(context.Background())
	if err != nil || sealed != 1 {
		t.Fatalf("idle sweep: %d %v", sealed, err)
	}
	if f.state(t, f.idle, "tool", "1.0.0") != repository.RawVersionSealed {
		t.Fatal("the idle version was not sealed")
	}
	if f.state(t, f.plain, "tool", "1.0.0") != repository.RawVersionOpen {
		t.Fatal("an explicit-mode repository must never be sealed by the sweep")
	}
	sealedVersion, _ := f.store.GetRawVersion(context.Background(), f.idle.ID, "tool", "1.0.0")
	if sealedVersion.ManifestDigest == "" {
		t.Fatal("the sweep must compute the manifest digest")
	}
	if metrics.runs["completed"] != 2 || metrics.sealed != 1 {
		t.Fatalf("run metrics: %#v sealed=%d", metrics.runs, metrics.sealed)
	}
}

func TestRawVersionSweepReportsStaleOpenVersionsAndNeverRemovesThem(t *testing.T) {
	f := newSweepFixture(t)
	f.put(t, f.plain, "old/1.0.0/a")
	f.put(t, f.plain, "old/2.0.0/a")
	metrics := &sweepMetrics{}
	clock := f.now.Add(10 * 24 * time.Hour)
	sweeper := RawVersionSweeper{Store: f.store, Metrics: metrics, Owner: "node-a", Idle: 5 * time.Minute, Interval: time.Hour, StaleAfter: 7 * 24 * time.Hour, Now: func() time.Time { return clock }}
	if _, err := sweeper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !metrics.gaugeSet || metrics.stale != 2 || metrics.oldest < (10*24*time.Hour-time.Minute).Seconds() {
		t.Fatalf("gauges: stale=%d oldest=%v", metrics.stale, metrics.oldest)
	}
	// The repository is in explicit mode, so an open version ten days old is reported and nothing else.
	for _, version := range []string{"1.0.0", "2.0.0"} {
		if f.state(t, f.plain, "old", version) != repository.RawVersionOpen {
			t.Fatalf("%s was changed by the sweep", version)
		}
		if files, err := f.store.ListRawVersionFiles(context.Background(), f.plain.ID, "old", version); err != nil || len(files) != 1 {
			t.Fatalf("%s lost its files: %v %v", version, files, err)
		}
	}
	// In idle mode the stale version is sealed, never tombstoned or deleted.
	f.put(t, f.idle, "old/1.0.0/a")
	clock = clock.Add(10 * 24 * time.Hour)
	sweeper.Interval = 0
	if _, err := sweeper.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.state(t, f.idle, "old", "1.0.0"); got != repository.RawVersionSealed {
		t.Fatalf("idle repository version is %s", got)
	}
}

func TestRawVersionSweepRunsOnOneNodeAtATime(t *testing.T) {
	f := newSweepFixture(t)
	f.put(t, f.idle, "tool/1.0.0/a")
	clock := f.now.Add(time.Hour)
	node := func(owner string) RawVersionSweeper {
		return RawVersionSweeper{Store: f.store, Owner: owner, Idle: 5 * time.Minute, Interval: time.Minute, StaleAfter: time.Hour, Now: func() time.Time { return clock }}
	}
	// Node A holds the lease, as it does in the middle of a sweep.
	if held, err := f.store.AcquireInternalTaskLease(context.Background(), rawIdleSealLease, "node-a", time.Minute, time.Minute); err != nil || !held {
		t.Fatalf("acquire: %v %v", held, err)
	}
	if sealed, err := node("node-b").Run(context.Background()); err != nil || sealed != 0 {
		t.Fatalf("node B swept while node A held the lease: %d %v", sealed, err)
	}
	if f.state(t, f.idle, "tool", "1.0.0") != repository.RawVersionOpen {
		t.Fatal("node B sealed a version")
	}
	if err := f.store.ReleaseInternalTaskLease(context.Background(), rawIdleSealLease, "node-a", false); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]int, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sealed, err := node(fmt.Sprint("node-", i)).Run(context.Background())
			if err != nil {
				t.Error(err)
			}
			results[i] = sealed
		}()
	}
	wg.Wait()
	total := 0
	for _, sealed := range results {
		total += sealed
	}
	if total != 1 {
		t.Fatalf("one version, %d seals across the fleet: %v", total, results)
	}
}

// poisonedSweepStore reports a failure for one version of every call while the others are sealed, as the stores do.
type poisonedSweepStore struct {
	*repository.MemoryStore
	err error
}

func (s poisonedSweepStore) SealIdleRawVersions(ctx context.Context, idleBefore time.Time, limit int) ([]repository.RawVersion, error) {
	sealed, err := s.MemoryStore.SealIdleRawVersions(ctx, idleBefore, limit)
	return sealed, errors.Join(err, s.err)
}

func TestRawVersionSweepReportsAFailedSealButStillCompletes(t *testing.T) {
	f := newSweepFixture(t)
	f.put(t, f.idle, "tool/1.0.0/a")
	metrics := &sweepMetrics{}
	failure := errors.New("seal tool@0.0.1 of repository idle-feed: audit unavailable")
	sweeper := RawVersionSweeper{Store: poisonedSweepStore{f.store, failure}, Metrics: metrics, Owner: "node-a", Idle: 5 * time.Minute, Interval: time.Hour, StaleAfter: time.Hour, Now: func() time.Time { return f.now.Add(time.Hour) }}
	sealed, err := sweeper.Run(context.Background())
	if sealed != 1 || !errors.Is(err, failure) {
		t.Fatalf("the healthy version is sealed and the failure reported: %d %v", sealed, err)
	}
	if f.state(t, f.idle, "tool", "1.0.0") != repository.RawVersionSealed {
		t.Fatal("a failing version must not keep the others from being sealed")
	}
	if metrics.runs["failed"] != 1 || metrics.runs["completed"] != 0 {
		t.Fatalf("run metrics: %#v", metrics.runs)
	}
	// The run counts as completed for the lease, so the fleet waits out the interval instead of retrying every tick.
	if held, _ := f.store.AcquireInternalTaskLease(context.Background(), rawIdleSealLease, "node-b", time.Minute, time.Hour); held {
		t.Fatal("a run with a reported failure must still start the minimum interval")
	}
}

func TestRawVersionSweepMetricsAreExposed(t *testing.T) {
	var counters RawVersionSweepCounters
	counters.RecordRawVersionSweepRun("completed", 3)
	counters.RecordRawVersionSweepRun("failed", 0)
	counters.SetRawOpenVersions(4, 90.5)
	var out strings.Builder
	counters.writeRawVersionSweepMetrics(&out)
	for _, want := range []string{
		"notarx_generic_autoseal_versions_total 3",
		`notarx_generic_autoseal_runs_total{outcome="completed"} 1`,
		`notarx_generic_autoseal_runs_total{outcome="failed"} 1`,
		"notarx_generic_open_versions_stale 4",
		"notarx_generic_open_version_oldest_age_seconds 90.500",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
}
