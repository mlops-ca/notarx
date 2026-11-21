package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/mlops-ca/notarx/internal/background"
	"github.com/mlops-ca/notarx/internal/repository"
)

// publishedReconcileBatch is how many publications of one format a pass emits before it asks again.
const publishedReconcileBatch = 200

// PublishedEventReconciler is the catch-up worker and repair tool of ADR 0021 (slice 1b): it emits
// artifact.published for the visible publications that have none, idempotently by dedupe key, so a publication that
// committed before its format emitted natively, or through a path that does not emit, still reaches the event
// consumers. Several nodes may run it at once; the store checks the dedupe key under an advisory lock.
type PublishedEventReconciler struct {
	Store repository.PublishedEventReconciler
	// Retention is the keep time of the event log (NOTARX_EVENTS_RETENTION): only publications younger than it are
	// reconciled, because an older event is gone by design and history is recovered through a rule's backfill.
	// Zero or less means the log is kept for ever and every publication is checked.
	Retention time.Duration
	// Batch is the publications per format a store call handles; it defaults to publishedReconcileBatch.
	Batch int
	// Now is the clock of the retention floor, injected by tests.
	Now func() time.Time
}

func (r PublishedEventReconciler) floor() time.Time {
	if r.Retention <= 0 {
		return time.Time{}
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	return now().Add(-r.Retention)
}

func (r PublishedEventReconciler) batch() int {
	if r.Batch > 0 {
		return r.Batch
	}
	return publishedReconcileBatch
}

// RunOnce emits every missing event, pass after pass until a pass writes none, and returns how many it wrote.
// A publication it emitted is no longer missing, so the loop ends.
func (r PublishedEventReconciler) RunOnce(ctx context.Context) (int, error) {
	total := 0
	floor := r.floor()
	for {
		written, err := r.Store.ReconcilePublishedEvents(ctx, floor, r.batch())
		total += written
		if err != nil || written == 0 {
			return total, err
		}
	}
}

// Start runs the reconciler every interval until ctx ends.
func (r PublishedEventReconciler) Start(ctx context.Context, interval time.Duration) {
	background.Go(ctx, func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if written, err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("published event reconciliation failed", "error", err)
			} else if written > 0 {
				slog.Info("published events reconciled", "events", written)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}
