package capacity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

const (
	// DefaultReconcileInterval is NOTARX_QUOTA_RECONCILE_INTERVAL's default: every
	// repository is recomputed once per interval.
	DefaultReconcileInterval = 6 * time.Hour
	// DriftAlertRatio is the drift, relative to the repository's usage, above which
	// the alert fires (0.1 percent).
	DriftAlertRatio = 0.001
	defaultBatch    = 50
	// defaultInitialDelay bounds the jitter before the first pass after start.
	defaultInitialDelay = 30 * time.Second
)

// Stats is the reconciler's observable state.
type Stats struct {
	// DriftBytes is the sum of absolute drift corrected by the last pass.
	DriftBytes int64
	// MaxDriftRatio is the largest per-repository drift / usage of the last pass.
	MaxDriftRatio float64
	// Alerting is true when MaxDriftRatio exceeds DriftAlertRatio.
	Alerting      bool
	Repositories  int
	Corrected     int
	LastPassAt    time.Time
	LastPassError string
}

// Reconciler recomputes every repository's usage with the aggregation query
// and corrects the counters in one UPDATE.
type Reconciler struct {
	db       *sql.DB
	interval time.Duration
	batch    int
	// initialDelay bounds the jitter before the first pass after start.
	initialDelay time.Duration
	logger       *slog.Logger
	// pause is the sleep between batches and between repositories, injectable for tests.
	pause func(context.Context, time.Duration)

	mu    sync.Mutex
	stats Stats
}

// NewReconciler builds a reconciler; a non-positive interval selects the default.
func NewReconciler(db *sql.DB, interval time.Duration, logger *slog.Logger) *Reconciler {
	if interval <= 0 {
		interval = DefaultReconcileInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{db: db, interval: interval, batch: defaultBatch, initialDelay: defaultInitialDelay, logger: logger, pause: sleepContext}
}

func sleepContext(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// WithInitialDelay bounds the random wait before the first pass after Run starts (30s by
// default).
func (r *Reconciler) WithInitialDelay(d time.Duration) *Reconciler {
	r.initialDelay = d
	return r
}

// Stats returns the result of the last pass.
func (r *Reconciler) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}

// Run reconciles every interval, with up to 10 percent jitter so nodes do not
// start in step, until ctx ends. Concurrent runs on several nodes are safe: each
// repository is recomputed under its counter row lock.
func (r *Reconciler) Run(ctx context.Context) {
	// The first pass runs shortly after start, so nodes that restart more often than the
	// interval (rolling deploys) still correct drift and publish the metrics.
	r.pause(ctx, time.Duration(rand.Int64N(int64(min(r.interval/10, r.initialDelay))+1)))
	for ctx.Err() == nil {
		if _, err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
			r.logger.Error("quota reconciliation failed", "error", err)
		}
		jitter := time.Duration(rand.Int64N(int64(r.interval/10) + 1))
		r.pause(ctx, r.interval+jitter)
	}
}

// RunOnce walks every repository in id order, in batches, and returns the
// pass statistics.
func (r *Reconciler) RunOnce(ctx context.Context) (Stats, error) {
	stats := Stats{}
	after := "00000000-0000-0000-0000-000000000000"
	var errs []error
	for {
		rows, err := r.db.QueryContext(ctx, `SELECT id::text,format FROM hosted_repositories WHERE id>$1::uuid ORDER BY id LIMIT $2`, after, r.batch)
		if err != nil {
			errs = append(errs, err)
			break
		}
		type target struct{ id, format string }
		var batch []target
		for rows.Next() {
			var t target
			if err = rows.Scan(&t.id, &t.format); err != nil {
				break
			}
			batch = append(batch, t)
		}
		closeErr := rows.Close()
		if err == nil {
			err = errors.Join(rows.Err(), closeErr)
		}
		if err != nil {
			errs = append(errs, err)
			break
		}
		for _, t := range batch {
			drift, used, err := r.reconcile(ctx, t.id, t.format)
			if err != nil {
				errs = append(errs, fmt.Errorf("repository %s: %w", t.id, err))
				continue
			}
			stats.Repositories++
			if drift != 0 {
				stats.Corrected++
				if drift < 0 {
					drift = -drift
				}
				stats.DriftBytes += drift
				ratio := float64(drift)
				if used > 0 {
					ratio = float64(drift) / float64(used)
				}
				stats.MaxDriftRatio = max(stats.MaxDriftRatio, ratio)
			}
		}
		if len(batch) < r.batch {
			break
		}
		after = batch[len(batch)-1].id
		r.pause(ctx, 100*time.Millisecond)
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
	}
	stats.Alerting = stats.MaxDriftRatio > DriftAlertRatio
	stats.LastPassAt = time.Now().UTC()
	if err := errors.Join(errs...); err != nil {
		stats.LastPassError = err.Error()
	}
	r.mu.Lock()
	r.stats = stats
	r.mu.Unlock()
	return stats, errors.Join(errs...)
}

// reconcile recomputes one repository under the counter row lock, so a publisher
// that already reserved (and holds the row) commits before the sum is taken and a
// publisher that has not yet reserved applies its delta after the correction. It
// returns the corrected drift (counter minus actual) and the actual usage.
func (r *Reconciler) reconcile(ctx context.Context, id, format string) (drift, used int64, err error) {
	query, ok := UsageQuery(format)
	if !ok || !Covered(format) {
		// A pending format's writers do not call Reserve yet, so its counter row is only
		// ever the backfill; recomputing it would report every publish as drift.
		return 0, 0, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, ensureSQL, id); err != nil {
		return 0, 0, err
	}
	var counted int64
	if err = tx.QueryRowContext(ctx, `SELECT bytes FROM repository_usage_counters WHERE repository_id=$1::uuid FOR UPDATE`, id).Scan(&counted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, nil // the repository was deleted meanwhile
		}
		return 0, 0, err
	}
	var objects int64
	if err = tx.QueryRowContext(ctx, query, id).Scan(&used, &objects); err != nil {
		return 0, 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE repository_usage_counters SET bytes=$2::bigint,objects=$3::bigint,reconciled_at=now(),updated_at=CASE WHEN bytes<>$2::bigint OR objects<>$3::bigint THEN now() ELSE updated_at END WHERE repository_id=$1::uuid`, id, used, objects); err != nil {
		return 0, 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	return counted - used, used, nil
}
