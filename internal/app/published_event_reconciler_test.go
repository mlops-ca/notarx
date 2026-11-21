package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/tasklease"
)

type fakePublishedStore struct {
	mu      sync.Mutex
	answers []int
	err     error
	floors  []time.Time
	limits  []int
	ran     chan struct{}
}

func (f *fakePublishedStore) ReconcilePublishedEvents(_ context.Context, floor time.Time, limit int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.floors, f.limits = append(f.floors, floor), append(f.limits, limit)
	if f.ran != nil {
		select {
		case f.ran <- struct{}{}:
		default:
		}
	}
	if len(f.answers) == 0 {
		return 0, f.err
	}
	n := f.answers[0]
	f.answers = f.answers[1:]
	return n, f.err
}

type fakeLeaser struct {
	mu        sync.Mutex
	held      bool
	acquired  []string
	released  []bool
	tasks     []tasklease.Task
	intervals []time.Duration
}

func (f *fakeLeaser) AcquireInternalTaskLease(_ context.Context, task tasklease.Task, owner string, _, minInterval time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquired = append(f.acquired, owner)
	f.tasks = append(f.tasks, task)
	f.intervals = append(f.intervals, minInterval)
	return f.held, nil
}

func (f *fakeLeaser) ReleaseInternalTaskLease(_ context.Context, _ tasklease.Task, _ string, completed bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, completed)
	return nil
}

func TestPublishedEventReconcilerRunsPassesUntilNoneWrites(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	store := &fakePublishedStore{answers: []int{5, 2}}
	reconciler := PublishedEventReconciler{Store: store, Retention: 14 * 24 * time.Hour, Batch: 7, Now: func() time.Time { return now }}
	written, err := reconciler.RunOnce(context.Background())
	if err != nil || written != 7 || len(store.floors) != 3 {
		t.Fatalf("written=%d passes=%d err=%v", written, len(store.floors), err)
	}
	if want := now.Add(-14 * 24 * time.Hour); !store.floors[0].Equal(want) || store.limits[0] != 7 {
		t.Fatalf("floor=%v limit=%d", store.floors[0], store.limits[0])
	}
}

func TestPublishedEventReconcilerNeedsAWindowAndReportsFailures(t *testing.T) {
	store := &fakePublishedStore{answers: []int{1}}
	if _, err := (PublishedEventReconciler{Store: store}).RunOnce(context.Background()); !errors.Is(err, errReconcileWindow) || len(store.floors) != 0 {
		t.Fatalf("an unbounded window ran: %v (%d passes)", err, len(store.floors))
	}
	if _, err := (PublishedEventReconciler{Store: store, Retention: time.Hour}).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.limits[0] != publishedReconcileBatch {
		t.Fatalf("limit=%d", store.limits[0])
	}
	// A pass that wrote events and failed on one publication goes on; the failure of the last pass is reported.
	failing := &fakePublishedStore{answers: []int{3}, err: errors.New("one publication failed")}
	written, err := (PublishedEventReconciler{Store: failing, Retention: time.Hour}).RunOnce(context.Background())
	if err == nil || written != 3 || len(failing.floors) != 2 {
		t.Fatalf("written=%d passes=%d err=%v", written, len(failing.floors), err)
	}
}

func TestPublishedEventReconcilerRunsOnlyUnderTheLease(t *testing.T) {
	store := &fakePublishedStore{answers: []int{1}}
	leases := &fakeLeaser{}
	reconciler := PublishedEventReconciler{Store: store, Retention: time.Hour, Leases: leases, Owner: "node-a"}
	if written, err := reconciler.runLeased(context.Background(), 15*time.Minute); err != nil || written != 0 || len(store.floors) != 0 || len(leases.released) != 0 {
		t.Fatalf("a node without the lease reconciled: written=%d passes=%d released=%v err=%v", written, len(store.floors), leases.released, err)
	}
	leases.held = true
	if written, err := reconciler.runLeased(context.Background(), 15*time.Minute); err != nil || written != 1 || len(leases.released) != 1 || !leases.released[0] {
		t.Fatalf("written=%d released=%v err=%v", written, leases.released, err)
	}
	if leases.intervals[1] != 15*time.Minute || leases.tasks[1].Kind != "event-dispatcher" || leases.acquired[1] != "node-a" {
		t.Fatalf("lease = %+v %v %v", leases.tasks[1], leases.intervals[1], leases.acquired[1])
	}
	// A run cut short by its context hands the lease back uncompleted, so another node may retry at once.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store.answers = []int{1}
	if _, err := reconciler.runLeased(ctx, time.Minute); err != nil || leases.released[len(leases.released)-1] {
		t.Fatalf("an interrupted run released as completed: %v (%v)", leases.released, err)
	}
}

func TestPublishedEventReconcilerStartRunsAndStops(t *testing.T) {
	store := &fakePublishedStore{answers: []int{1}, ran: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	PublishedEventReconciler{Store: store, Retention: time.Hour}.Start(ctx, time.Millisecond)
	timeout := time.After(5 * time.Second)
	for range 3 {
		select {
		case <-store.ran:
		case <-timeout:
			t.Fatal("the reconciler did not run on its interval")
		}
	}
}
