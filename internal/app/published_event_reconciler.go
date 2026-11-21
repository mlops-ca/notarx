package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/mlops-ca/notarx/internal/background"
	"github.com/mlops-ca/notarx/internal/events"
	"github.com/mlops-ca/notarx/internal/repository"
)

const (
	// publishedReconcileBatch is how many publications of one format a pass emits before it asks again.
	publishedReconcileBatch = 200
	// PublishedReconcileConsumer names the reconciler's lease row.
	PublishedReconcileConsumer = "published-reconciler"
	// publishedReconcileLeaseTTL bounds how long a crashed reconciler keeps the other nodes out.
	publishedReconcileLeaseTTL = 10 * time.Minute
)

// PublishedEventReconciler is the catch-up worker and repair tool of ADR 0021 (slice 1b): it emits
// artifact.published for the visible publications that have none, idempotently by dedupe key, so a publication that
// committed before its format emitted natively, or through a path that does not emit, still reaches the event
// consumers. With Leases set only the node holding the lease reconciles; the store checks the dedupe key under an
// advisory lock either way, so a second reconciler (the repair tool next to a node) writes no duplicate.
type PublishedEventReconciler struct {
	Store repository.PublishedEventReconciler
	// Retention is the keep time of the event log (NOTARX_EVENTS_RETENTION): only publications younger than it are
	// reconciled, because an older event is gone by design and history is recovered through a rule's backfill. It
	// must be positive: an unbounded window would announce every publication ever made.
	Retention time.Duration
	// Batch is the publications per format a store call handles; it defaults to publishedReconcileBatch.
	Batch int
	// Leases elects the reconciling node between runs; nil runs on every call.
	Leases events.Leaser
	// Owner names this node in the lease.
	Owner string
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

// runLeased is RunOnce under the lease: a node that does not hold it, or that ran less than interval ago, does nothing.
func (r PublishedEventReconciler) runLeased(ctx context.Context, interval time.Duration) (int, error) {
	if r.Leases == nil {
		return r.RunOnce(ctx)
	}
	task := events.LeaseTask(PublishedReconcileConsumer)
	held, err := r.Leases.AcquireInternalTaskLease(ctx, task, r.Owner, publishedReconcileLeaseTTL, interval)
	if err != nil || !held {
		return 0, err
	}
	written, runErr := r.RunOnce(ctx)
	// Release even when ctx ended, so the next node need not wait out the TTL; a completed run starts the interval.
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	releaseErr := r.Leases.ReleaseInternalTaskLease(release, task, r.Owner, ctx.Err() == nil)
	return written, errors.Join(runErr, releaseErr)
}

// Start runs the reconciler every interval until ctx ends.
func (r PublishedEventReconciler) Start(ctx context.Context, interval time.Duration) {
	background.Go(ctx, func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if written, err := r.runLeased(ctx, interval); err != nil && ctx.Err() == nil {
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
