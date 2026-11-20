package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/repository"
)

type fakeMetaSweepStore struct {
	mu       sync.Mutex
	acquire  bool
	acquireE error
	renewErr error
	renews   int
	finished []bool
	outcomes map[string]repository.MetaSweepOutcome
	failing  map[string]error
	pins     []string
	forgot   []string
	swept    []string
	remove   bool
}

func (f *fakeMetaSweepStore) AcquireMetaSweepLease(context.Context, string, time.Duration, time.Duration) (bool, error) {
	return f.acquire, f.acquireE
}

func (f *fakeMetaSweepStore) RenewMetaSweepLease(context.Context, string, time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renews++
	return f.renewErr
}

func (f *fakeMetaSweepStore) FinishMetaSweepLease(_ context.Context, _ string, completed bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished = append(f.finished, completed)
	return nil
}

func (f *fakeMetaSweepStore) SweepMetaObject(ctx context.Context, digest string, _ time.Duration, remove func(context.Context) error) (repository.MetaSweepOutcome, error) {
	f.mu.Lock()
	f.swept = append(f.swept, digest)
	f.mu.Unlock()
	if err := f.failing[digest]; err != nil {
		return "", err
	}
	outcome := f.outcomes[digest]
	if outcome == repository.MetaSweepDeleted && f.remove {
		if err := remove(ctx); err != nil {
			return "", err
		}
	}
	return outcome, nil
}

func (f *fakeMetaSweepStore) MetaObjectPinsOlderThan(_ context.Context, after string, _ time.Duration, limit int) ([]string, error) {
	var page []string
	for _, pin := range f.pins {
		if pin > after && len(page) < limit {
			page = append(page, pin)
		}
	}
	return page, nil
}

func (f *fakeMetaSweepStore) ForgetMetaObjectPin(_ context.Context, digest string, _ time.Duration) error {
	f.forgot = append(f.forgot, digest)
	return nil
}

type fakeMetaObjects struct {
	digests []string
	listErr error
	deleted []string
}

func (f *fakeMetaObjects) List(context.Context) ([]string, error) { return f.digests, f.listErr }
func (f *fakeMetaObjects) Delete(_ context.Context, digest string) error {
	f.deleted = append(f.deleted, digest)
	return nil
}

type recordingMetaMetrics struct{ objects, runs []string }

func (r *recordingMetaMetrics) RecordMetaSweepObject(outcome string) {
	r.objects = append(r.objects, outcome)
}
func (r *recordingMetaMetrics) RecordMetaSweepRun(outcome string) { r.runs = append(r.runs, outcome) }

func TestMetaSweeperSkipsARunWhenItDoesNotHoldTheLease(t *testing.T) {
	store := &fakeMetaSweepStore{}
	objects := &fakeMetaObjects{digests: []string{"sha256:a"}}
	result, err := MetaSweeper{Store: store, Objects: objects, Owner: "n"}.Run(t.Context())
	if err != nil || result.Examined != 0 || len(store.swept) != 0 || len(store.finished) != 0 {
		t.Fatalf("a node without the lease swept: %+v %v %v", result, err, store.swept)
	}
	store.acquireE = errors.New("database down")
	if _, err = (MetaSweeper{Store: store, Objects: objects}).Run(t.Context()); err == nil {
		t.Fatal("a failed lease acquisition was swallowed")
	}
}

func TestMetaSweeperSweepsEveryObjectInBatchesAndCompletesTheLease(t *testing.T) {
	digests := []string{"sha256:1", "sha256:2", "sha256:3", "sha256:4", "sha256:5"}
	store := &fakeMetaSweepStore{acquire: true, remove: true, outcomes: map[string]repository.MetaSweepOutcome{
		"sha256:1": repository.MetaSweepDeleted, "sha256:2": repository.MetaSweepReferenced, "sha256:3": repository.MetaSweepYoung,
		"sha256:4": repository.MetaSweepDeleted, "sha256:5": repository.MetaSweepReferenced,
	}}
	objects := &fakeMetaObjects{digests: digests}
	metrics := &recordingMetaMetrics{}
	result, err := MetaSweeper{Store: store, Objects: objects, Metrics: metrics, Owner: "n", BatchSize: 2, Grace: time.Hour}.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := (MetaSweepResult{Examined: 5, Deleted: 2, Referenced: 2, Young: 1}); result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
	if store.renews != 3 {
		t.Fatalf("the lease was renewed %d times for 5 objects in batches of 2, want 3", store.renews)
	}
	if !slices.Equal(objects.deleted, []string{"sha256:1", "sha256:4"}) {
		t.Fatalf("deleted %v", objects.deleted)
	}
	if !slices.Equal(store.finished, []bool{true}) || !slices.Equal(metrics.runs, []string{"completed"}) {
		t.Fatalf("lease finish %v, runs %v", store.finished, metrics.runs)
	}
	if !slices.Equal(metrics.objects, []string{"deleted", "referenced", "young", "deleted", "referenced"}) {
		t.Fatalf("object metrics %v", metrics.objects)
	}
}

func TestMetaSweeperKeepsGoingPastOneObjectAndStillCompletesTheLease(t *testing.T) {
	boom := errors.New("boom")
	store := &fakeMetaSweepStore{acquire: true, failing: map[string]error{"sha256:1": boom},
		outcomes: map[string]repository.MetaSweepOutcome{"sha256:2": repository.MetaSweepDeleted}}
	metrics := &recordingMetaMetrics{}
	result, err := MetaSweeper{Store: store, Objects: &fakeMetaObjects{digests: []string{"sha256:1", "sha256:2"}}, Metrics: metrics}.Run(t.Context())
	if !errors.Is(err, boom) || result.Examined != 2 || result.Deleted != 1 {
		t.Fatalf("result %+v err %v", result, err)
	}
	if !slices.Equal(store.finished, []bool{true}) || !slices.Equal(metrics.runs, []string{"failed"}) || !slices.Equal(metrics.objects, []string{"failed", "deleted"}) {
		t.Fatalf("lease finish %v, runs %v, objects %v", store.finished, metrics.runs, metrics.objects)
	}
}

func TestMetaSweeperStopsWhenAnotherNodeTookTheLease(t *testing.T) {
	store := &fakeMetaSweepStore{acquire: true, renewErr: repository.ErrNotFound}
	result, err := MetaSweeper{Store: store, Objects: &fakeMetaObjects{digests: []string{"sha256:1"}}}.Run(t.Context())
	if !errors.Is(err, repository.ErrNotFound) || result.Examined != 0 || len(store.swept) != 0 {
		t.Fatalf("a node that lost the lease went on: %+v %v", result, err)
	}
}

func TestMetaSweeperReportsAListingFailure(t *testing.T) {
	store := &fakeMetaSweepStore{acquire: true}
	_, err := MetaSweeper{Store: store, Objects: &fakeMetaObjects{listErr: errors.New("store down")}}.Run(t.Context())
	if err == nil || !slices.Equal(store.finished, []bool{false}) {
		t.Fatalf("err %v, finish %v", err, store.finished)
	}
}

func TestMetaSweeperReleasesTheLeaseWhenItsContextEnds(t *testing.T) {
	store := &fakeMetaSweepStore{acquire: true, outcomes: map[string]repository.MetaSweepOutcome{}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (MetaSweeper{Store: store, Objects: &fakeMetaObjects{digests: []string{"sha256:1"}}}).Run(ctx); err == nil {
		t.Fatal("a cancelled run reported success")
	}
	if !slices.Equal(store.finished, []bool{false}) {
		t.Fatalf("the lease was not released: %v", store.finished)
	}
}

func TestMetaSweeperForgetsPinsOfObjectsThatDoNotExist(t *testing.T) {
	store := &fakeMetaSweepStore{acquire: true, outcomes: map[string]repository.MetaSweepOutcome{"sha256:b": repository.MetaSweepReferenced},
		pins: []string{"sha256:a", "sha256:b", "sha256:c", "sha256:d"}}
	result, err := MetaSweeper{Store: store, Objects: &fakeMetaObjects{digests: []string{"sha256:b"}}, BatchSize: 2}.Run(t.Context())
	if err != nil || result.OrphanedPins != 3 {
		t.Fatalf("result %+v err %v", result, err)
	}
	if !slices.Equal(store.forgot, []string{"sha256:a", "sha256:c", "sha256:d"}) {
		t.Fatalf("forgot %v", store.forgot)
	}
}

func TestNewMetaSweeperReadsTheConfiguration(t *testing.T) {
	sweeper := NewMetaSweeper(&fakeMetaSweepStore{}, &fakeMetaObjects{}, nil, config.Config{InstanceID: "node-1", MetaSweepGrace: time.Hour, MetaSweepBatchSize: 7, MetaSweepInterval: time.Minute})
	if sweeper.Owner != "node-1" || sweeper.Grace != time.Hour || sweeper.BatchSize != 7 || sweeper.Interval != time.Minute {
		t.Fatalf("%+v", sweeper)
	}
	if got := (MetaSweeper{}).batchSize(); got != config.DefaultMetaSweepBatchSize {
		t.Fatalf("default batch = %d", got)
	}
}

func TestMetaSweepStartRunsAndStopsWithItsContext(t *testing.T) {
	store := &fakeMetaSweepStore{acquire: true, outcomes: map[string]repository.MetaSweepOutcome{"sha256:1": repository.MetaSweepYoung}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	sweeper := MetaSweeper{Store: store, Objects: &fakeMetaObjects{digests: []string{"sha256:1"}}, Interval: 5 * time.Millisecond}
	sweeper.Start(ctx)
	go func() {
		defer close(done)
		deadline := time.After(5 * time.Second)
		for {
			store.mu.Lock()
			n := len(store.swept)
			store.mu.Unlock()
			if n > 0 {
				return
			}
			select {
			case <-deadline:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	<-done
	cancel()
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.swept) == 0 {
		t.Fatal("the scheduler never ran a sweep")
	}
	// A sweeper with nothing to sweep with starts nothing.
	MetaSweeper{}.Start(ctx)
}

func TestMetaSweepCountersRenderTheOutcomes(t *testing.T) {
	var counters MetaSweepCounters
	for _, outcome := range []string{"deleted", "deleted", "referenced", "young", "failed", "other"} {
		counters.RecordMetaSweepObject(outcome)
	}
	counters.RecordMetaSweepRun("completed")
	counters.RecordMetaSweepRun("failed")
	counters.RecordMetaSweepRun("failed")
	recorder := &bodyRecorder{}
	counters.write(recorder)
	for _, want := range []string{
		"notarx_meta_sweep_objects_total{outcome=\"deleted\"} 2",
		"notarx_meta_sweep_objects_total{outcome=\"referenced\"} 1",
		"notarx_meta_sweep_objects_total{outcome=\"young\"} 1",
		"notarx_meta_sweep_objects_total{outcome=\"failed\"} 1",
		"notarx_meta_sweep_runs_total{outcome=\"completed\"} 1",
		"notarx_meta_sweep_runs_total{outcome=\"failed\"} 2",
	} {
		if !strings.Contains(recorder.body, want) {
			t.Errorf("metrics lack %q:\n%s", want, recorder.body)
		}
	}
}

type bodyRecorder struct{ body string }

func (b *bodyRecorder) Write(p []byte) (int, error) { b.body += string(p); return len(p), nil }
