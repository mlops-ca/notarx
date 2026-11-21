package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
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
	if f.err != nil {
		return 0, f.err
	}
	if len(f.answers) == 0 {
		return 0, nil
	}
	n := f.answers[0]
	f.answers = f.answers[1:]
	return n, nil
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

func TestPublishedEventReconcilerKeepsForeverWithoutRetentionAndReportsFailures(t *testing.T) {
	store := &fakePublishedStore{answers: []int{1}}
	if _, err := (PublishedEventReconciler{Store: store}).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !store.floors[0].IsZero() || store.limits[0] != publishedReconcileBatch {
		t.Fatalf("floor=%v limit=%d", store.floors[0], store.limits[0])
	}
	failing := &fakePublishedStore{err: errors.New("down")}
	if _, err := (PublishedEventReconciler{Store: failing}).RunOnce(context.Background()); err == nil {
		t.Fatal("a store failure was swallowed")
	}
}

func TestPublishedEventReconcilerStartRunsAndStops(t *testing.T) {
	store := &fakePublishedStore{answers: []int{1}, ran: make(chan struct{}, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	PublishedEventReconciler{Store: store}.Start(ctx, time.Millisecond)
	timeout := time.After(5 * time.Second)
	for range 3 {
		select {
		case <-store.ran:
		case <-timeout:
			t.Fatal("the reconciler did not run on its interval")
		}
	}
}
