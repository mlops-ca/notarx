package app

import (
	"context"
	"errors"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

const (
	// publishedReconcileBatch is how many publications of one format a pass emits before it asks again.
	publishedReconcileBatch = 200
)

// PublishedEventReconciler is the repair tool of ADR 0021 (slice 1c): it emits artifact.published for the visible
// publications that have none, idempotently by dedupe key, so a publication whose event was lost (a database restored
// from a backup, an event log trimmed too early) still reaches the event consumers. Every format emits the event in its
// publish transaction, so nothing schedules it: `notarx reconcile-events` runs it once. The store checks the dedupe key
// under an advisory lock, so the tool next to a running node writes no duplicate.
type PublishedEventReconciler struct {
	Store repository.PublishedEventReconciler
	// Retention is the keep time of the event log (NOTARX_EVENTS_RETENTION): only publications younger than it are
	// reconciled, because an older event is gone by design and history is recovered through a rule's backfill. It
	// must be positive: an unbounded window would announce every publication ever made.
	Retention time.Duration
	// Batch is the publications per format a store call handles; it defaults to publishedReconcileBatch.
	Batch int
	// Now is the clock of the retention floor, injected by tests.
	Now func() time.Time
}

var errReconcileWindow = errors.New("published event reconciler needs a positive retention window")

func (r PublishedEventReconciler) floor() time.Time {
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
// A publication it emitted is no longer missing, so the loop ends; a publication that keeps failing is reported by
// the last pass without holding the others back.
func (r PublishedEventReconciler) RunOnce(ctx context.Context) (int, error) {
	if r.Retention <= 0 {
		return 0, errReconcileWindow
	}
	total := 0
	floor := r.floor()
	for {
		written, err := r.Store.ReconcilePublishedEvents(ctx, floor, r.batch())
		total += written
		if written == 0 || ctx.Err() != nil {
			return total, err
		}
	}
}
