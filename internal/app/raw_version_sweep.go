package app

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/mlops-ca/notarx/internal/background"
	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/repository"
)

// rawIdleSealLease is the lease row of the idle auto-seal sweep in scheduled_tasks (ADR 0023 section 2).
var rawIdleSealLease = repository.InternalTaskLease{
	ID:          "00000000-0000-7000-8000-0000000000a3",
	Name:        "internal:raw-idle-seal",
	Kind:        "raw-idle-seal",
	Description: "Internal lease row for the idle auto-seal of versioned Raw repositories",
}

// rawIdleSealLeaseTTL bounds how long a crashed sweeper blocks the others; a sweep renews it once per batch.
const rawIdleSealLeaseTTL = 5 * time.Minute

// rawIdleSealBatch is the number of versions sealed between two renewals of the lease.
const rawIdleSealBatch = 100

// RawVersionSweepStore is what the sweep needs of the store: the lease and the two version queries.
type RawVersionSweepStore interface {
	repository.InternalTaskLeaseStore
	SealIdleRawVersions(ctx context.Context, idleBefore time.Time, limit int) ([]repository.RawVersion, error)
	RawOpenVersionStats(ctx context.Context, staleBefore time.Time) (repository.RawOpenVersionStats, error)
}

// RawVersionSweepMetrics receives the outcome of every run and the open-version gauges.
type RawVersionSweepMetrics interface {
	RecordRawVersionSweepRun(outcome string, sealed int)
	SetRawOpenVersions(stale int64, oldestAgeSeconds float64)
}

// RawVersionSweeper seals the open versions of idle-mode repositories that saw no write for Idle (ADR 0028,
// slice 5), and reports the open versions older than StaleAfter. It never tombstones or deletes a version: an
// abandoned open version is only visible, because tombstoning burns the name@version identifier for good.
// One node sweeps at a time under a lease of scheduled_tasks, and at most once per Interval.
type RawVersionSweeper struct {
	Store      RawVersionSweepStore
	Metrics    RawVersionSweepMetrics
	Owner      string
	Idle       time.Duration
	Interval   time.Duration
	StaleAfter time.Duration
	// Now is the clock of the sweep, injected by tests.
	Now func() time.Time
}

// NewRawVersionSweeper builds the sweeper of a node from its configuration.
func NewRawVersionSweeper(store RawVersionSweepStore, metrics RawVersionSweepMetrics, cfg config.Config) RawVersionSweeper {
	// The owner carries a per-process session id: replicas that share an instance id would otherwise all match the
	// owner of the lease and every one of them would sweep.
	return RawVersionSweeper{Store: store, Metrics: metrics, Owner: cfg.InstanceID + "/" + ids.New(), Idle: cfg.GenericAutosealIdle, Interval: cfg.GenericAutosealInterval, StaleAfter: cfg.GenericStaleOpenAfter}
}

func (s RawVersionSweeper) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Run reports the open-version gauges and, when this node can take the lease, seals the idle versions. It seals
// nothing and returns no error when another node holds the lease or the last sweep is younger than the interval.
func (s RawVersionSweeper) Run(ctx context.Context) (sealed int, err error) {
	sealed, err = s.sweep(ctx)
	return sealed, errors.Join(err, s.reportOpenVersions(ctx))
}

// sweep seals the idle versions when this node can take the lease.
func (s RawVersionSweeper) sweep(ctx context.Context) (sealed int, err error) {
	acquired, err := s.Store.AcquireInternalTaskLease(ctx, rawIdleSealLease, s.Owner, rawIdleSealLeaseTTL, s.Interval)
	if err != nil || !acquired {
		return 0, err
	}
	completed := false
	defer func() {
		// Release even when the run's context ended, so the next node need not wait out the TTL. A completed run
		// starts the minimum interval; an interrupted one releases uncompleted so another node may retry at once.
		finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if releaseErr := s.Store.ReleaseInternalTaskLease(finish, rawIdleSealLease, s.Owner, completed); releaseErr != nil {
			slog.Warn("release raw idle seal lease", "component", "raw_idle_seal", "error", releaseErr)
		}
	}()
	// A version that cannot be sealed is reported and skipped by the store, so it never keeps the younger versions
	// waiting; such a run still completes, which starts the interval, like the meta sweep that reports what it
	// could not sweep. Only an interruption (the context, a lost lease) releases the lease uncompleted.
	var failures error
	for {
		if err = ctx.Err(); err != nil {
			s.run("failed", sealed)
			return sealed, err
		}
		batch, sealErr := s.Store.SealIdleRawVersions(ctx, s.now().Add(-s.Idle), rawIdleSealBatch)
		sealed += len(batch)
		if sealErr != nil {
			if ctx.Err() != nil {
				s.run("failed", sealed)
				return sealed, errors.Join(failures, sealErr)
			}
			failures = errors.Join(failures, sealErr)
		}
		if len(batch) < rawIdleSealBatch {
			break
		}
		// Another node took the lease over (this one stalled past the TTL): it owns the sweep now.
		held, renewErr := s.Store.AcquireInternalTaskLease(ctx, rawIdleSealLease, s.Owner, rawIdleSealLeaseTTL, s.Interval)
		if renewErr != nil || !held {
			s.run("failed", sealed)
			return sealed, errors.Join(renewErr, errors.New("raw idle seal lease lost"))
		}
	}
	completed = true
	if failures != nil {
		s.run("failed", sealed)
		return sealed, failures
	}
	s.run("completed", sealed)
	return sealed, nil
}

// reportOpenVersions publishes the open-version gauges. Every node reads them from the database, so each node reports the
// same values and a change of leader leaves no stale gauge behind; alert on the maximum across nodes.
func (s RawVersionSweeper) reportOpenVersions(ctx context.Context) error {
	now := s.now()
	stats, err := s.Store.RawOpenVersionStats(ctx, now.Add(-s.StaleAfter))
	if err != nil {
		return err
	}
	if s.Metrics != nil {
		age := 0.0
		if !stats.OldestCreatedAt.IsZero() {
			age = max(0, now.Sub(stats.OldestCreatedAt).Seconds())
		}
		s.Metrics.SetRawOpenVersions(stats.Stale, age)
	}
	return nil
}

func (s RawVersionSweeper) run(outcome string, sealed int) {
	if s.Metrics != nil {
		s.Metrics.RecordRawVersionSweepRun(outcome, sealed)
	}
}

// Start runs the sweep on a scheduler node. Every node ticks several times per interval and the lease decides which
// one sweeps, and when.
func (s RawVersionSweeper) Start(ctx context.Context) {
	if s.Store == nil {
		return
	}
	tick := max(s.Interval/4, time.Second)
	background.Go(ctx, func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		// The gauges are refreshed once per interval, not once per tick, so a fleet does not run the aggregate on
		// every node every few seconds.
		var reported time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sealed, err := s.sweep(ctx)
				if now := s.now(); now.Sub(reported) >= s.Interval {
					reported = now
					err = errors.Join(err, s.reportOpenVersions(ctx))
				}
				switch {
				case err != nil && ctx.Err() == nil:
					slog.Warn("raw idle seal sweep failed", "component", "raw_idle_seal", "error", err)
				case sealed > 0:
					slog.Info("raw idle seal sweep finished", "component", "raw_idle_seal", "sealed", sealed)
				}
			}
		}
	})
}

// RawVersionSweepCounters is the Prometheus-side state of the sweep, embedded in Metrics.
type RawVersionSweepCounters struct {
	sealed, runsCompleted, runsFailed atomic.Uint64
	staleOpen                         atomic.Int64
	oldestOpenAgeMillis               atomic.Int64
}

// RecordRawVersionSweepRun implements RawVersionSweepMetrics.
func (c *RawVersionSweepCounters) RecordRawVersionSweepRun(outcome string, sealed int) {
	c.sealed.Add(uint64(max(sealed, 0)))
	switch outcome {
	case "completed":
		c.runsCompleted.Add(1)
	case "failed":
		c.runsFailed.Add(1)
	}
}

// SetRawOpenVersions implements RawVersionSweepMetrics.
func (c *RawVersionSweepCounters) SetRawOpenVersions(stale int64, oldestAgeSeconds float64) {
	c.staleOpen.Store(stale)
	c.oldestOpenAgeMillis.Store(int64(oldestAgeSeconds * 1000))
}

func (c *RawVersionSweepCounters) writeRawVersionSweepMetrics(w interface{ Write([]byte) (int, error) }) {
	_, _ = w.Write([]byte("# TYPE notarx_generic_autoseal_versions_total counter\n" +
		"notarx_generic_autoseal_versions_total " + utoa(c.sealed.Load()) + "\n" +
		"# TYPE notarx_generic_autoseal_runs_total counter\n" +
		"notarx_generic_autoseal_runs_total{outcome=\"completed\"} " + utoa(c.runsCompleted.Load()) + "\n" +
		"notarx_generic_autoseal_runs_total{outcome=\"failed\"} " + utoa(c.runsFailed.Load()) + "\n" +
		"# HELP notarx_generic_open_versions_stale Open versions of versioned Raw repositories older than NOTARX_GENERIC_STALE_OPEN_AFTER. Reported only: nothing removes an open version.\n" +
		"# TYPE notarx_generic_open_versions_stale gauge\n" +
		"notarx_generic_open_versions_stale " + strconv.FormatInt(c.staleOpen.Load(), 10) + "\n" +
		"# HELP notarx_generic_open_version_oldest_age_seconds Age of the oldest open version of a versioned Raw repository.\n" +
		"# TYPE notarx_generic_open_version_oldest_age_seconds gauge\n" +
		"notarx_generic_open_version_oldest_age_seconds " + strconv.FormatFloat(float64(c.oldestOpenAgeMillis.Load())/1000, 'f', 3, 64) + "\n"))
}
