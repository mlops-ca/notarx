package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

type fakeCatalogStore struct {
	results [][]repository.ArtifactCatalogBackfillProgress
	errs    []error
	calls   int
}

func (f *fakeCatalogStore) BackfillArtifactCatalog(context.Context, int) ([]repository.ArtifactCatalogBackfillProgress, error) {
	call := f.calls
	f.calls++
	return f.results[call], f.errs[call]
}

func TestArtifactCatalogBackfillRetriesUntilEveryFormatCompletes(t *testing.T) {
	store := &fakeCatalogStore{
		results: [][]repository.ArtifactCatalogBackfillProgress{
			nil,
			{{Format: repository.FormatNPM, Refreshed: 5, Completed: true}, {Format: repository.FormatRaw}},
			{{Format: repository.FormatNPM, Completed: true}, {Format: repository.FormatRaw, Refreshed: 3, Completed: true}},
		},
		errs: []error{errors.New("database restarting"), nil, nil},
	}
	done := make(chan struct{})
	go func() {
		runArtifactCatalogBackfill(t.Context(), store, time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the backfill did not finish")
	}
	if store.calls != 3 {
		t.Fatalf("backfill ran %d times, want 3 (error, a busy format, complete)", store.calls)
	}
}

func TestArtifactCatalogBackfillStopsWhenTheNodeShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	store := &fakeCatalogStore{
		results: [][]repository.ArtifactCatalogBackfillProgress{{{Format: repository.FormatNPM}}, {{Format: repository.FormatNPM}}},
		errs:    []error{nil, nil},
	}
	done := make(chan struct{})
	go func() {
		runArtifactCatalogBackfill(ctx, store, time.Hour)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the backfill ignored the shutdown")
	}
}
