package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/mlops-ca/notarx/internal/background"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

// importReaperInterval is how often a node looks for import sessions to reap, and importReaperBatch the sessions one
// store call finishes. The store takes the sessions with SKIP LOCKED, so every node may tick and the passes divide
// the work without a lease.
const (
	importReaperInterval = time.Minute
	importReaperBatch    = 100
)

// ImportSessionReaperStore is what the reaper needs of the store.
type ImportSessionReaperStore interface {
	ReapImportSessions(ctx context.Context, now time.Time, limit int, remove func(ctx context.Context, keys []string) error) (int, error)
}

// ImportSessionReaper removes what abandoned import sessions left behind (ADR 0021, Remote targets): a session past its
// NOTARX_IMPORT_SESSION_TTL expires, the staging objects of every expired, aborted or committed session are deleted, and
// the finished rows are then pruned by the retention janitor (the import_sessions entry of its registry).
type ImportSessionReaper struct {
	Store   ImportSessionReaperStore
	Objects ObjectStore
	// Interval and Batch default to importReaperInterval and importReaperBatch.
	Interval time.Duration
	Batch    int
	// Now is the clock of the reaper, injected by tests.
	Now func() time.Time
}

func (r ImportSessionReaper) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r ImportSessionReaper) batch() int {
	if r.Batch > 0 {
		return r.Batch
	}
	return importReaperBatch
}

// remove deletes the staging objects of one session. An object that is already gone is what the pass wants.
func (r ImportSessionReaper) remove(ctx context.Context, keys []string) error {
	var failures []error
	for _, key := range keys {
		if err := r.Objects.Delete(ctx, key); err != nil && !errors.Is(err, objectstore.ErrNotFound) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Run reaps until a pass finishes fewer sessions than a batch and returns how many it finished.
func (r ImportSessionReaper) Run(ctx context.Context) (int, error) {
	total := 0
	for {
		finished, err := r.Store.ReapImportSessions(ctx, r.now(), r.batch(), r.remove)
		total += finished
		if err != nil || finished < r.batch() || ctx.Err() != nil {
			return total, err
		}
	}
}

// Start reaps on every tick until ctx ends.
func (r ImportSessionReaper) Start(ctx context.Context) {
	if r.Store == nil || r.Objects == nil {
		return
	}
	interval := r.Interval
	if interval <= 0 {
		interval = importReaperInterval
	}
	background.Go(ctx, func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reaped, err := r.Run(ctx)
				switch {
				case err != nil && ctx.Err() == nil:
					slog.Warn("import session reaper failed", "component", "import_reaper", "reaped", reaped, "error", err)
				case reaped > 0:
					slog.Info("import sessions reaped", "component", "import_reaper", "reaped", reaped)
				}
			}
		}
	})
}

var _ ImportSessionReaperStore = (*repository.MemoryStore)(nil)
