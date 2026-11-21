package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakePublishedStore struct {
	answers []int
	err     error
	floors  []time.Time
	limits  []int
}

func (f *fakePublishedStore) ReconcilePublishedEvents(_ context.Context, floor time.Time, limit int) (int, error) {
	f.floors, f.limits = append(f.floors, floor), append(f.limits, limit)
	if len(f.answers) == 0 {
		return 0, f.err
	}
	n := f.answers[0]
	f.answers = f.answers[1:]
	return n, f.err
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
