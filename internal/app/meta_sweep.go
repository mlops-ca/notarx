package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mlops-ca/notarx/internal/background"
	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/repository"
)

// MetaObjects is the meta namespace of the object store (objectstore.MetaStore).
type MetaObjects interface {
	// List returns the digest of every object of the namespace.
	List(ctx context.Context) ([]string, error)
	// Delete removes the object of a digest.
	Delete(ctx context.Context, digest string) error
}

// MetaSweepMetrics receives the outcome of every object a sweep examined and of every run.
type MetaSweepMetrics interface {
	RecordMetaSweepObject(outcome string)
	RecordMetaSweepRun(outcome string)
}

// MetaSweepResult counts what one run did.
type MetaSweepResult struct {
	Examined, Deleted, Referenced, Young, OrphanedPins int
}

// metaSweepLeaseTTL is how long a node holds the sweep lease without renewing it; a sweep renews it once per batch.
const metaSweepLeaseTTL = 10 * time.Minute

// metaSweepTick is how often a scheduler looks for a sweep to run; the lease and the interval decide whether it does.
const metaSweepTick = time.Minute

// MetaSweeper deletes the objects of the meta namespace that no row references (ADR 0023 section 9).
// Payloads are content-addressed and shared, so a row delete cannot say whether its object may go: the sweep
// lists the namespace and asks the database, object by object, whether anything still references it. The first
// sweep that finds an object unreferenced marks it; it deletes the object only when a later sweep finds it still
// unreferenced a grace period after the mark, and the store makes that decision while it
// holds the object's pin (repository.MetaSweepStore.SweepMetaObject), so a publish that writes the same bytes
// at the same moment never loses them. One node sweeps at a time, under a lease, once per interval.
type MetaSweeper struct {
	Store     repository.MetaSweepStore
	Objects   MetaObjects
	Metrics   MetaSweepMetrics
	Owner     string
	Grace     time.Duration
	BatchSize int
	Interval  time.Duration
}

// NewMetaSweeper builds the sweeper of a node from its configuration.
func NewMetaSweeper(store repository.MetaSweepStore, objects MetaObjects, metrics MetaSweepMetrics, cfg config.Config) MetaSweeper {
	return MetaSweeper{Store: store, Objects: objects, Metrics: metrics, Owner: cfg.InstanceID, Grace: cfg.MetaSweepGrace, BatchSize: cfg.MetaSweepBatchSize, Interval: cfg.MetaSweepInterval}
}

func (s MetaSweeper) batchSize() int {
	if s.BatchSize <= 0 {
		return config.DefaultMetaSweepBatchSize
	}
	return s.BatchSize
}

// Run sweeps once, when this node can take the lease. It returns a zero result and no error when another node
// holds the lease or the last sweep is more recent than the interval.
//
// A sweep that reached the end of the namespace counts as completed, so the fleet waits for the interval, even when
// some objects could not be swept: they are reported in the error, and a persistent failure must not turn into a
// full sweep on every scheduler tick. One that was interrupted (the listing failed, the lease was lost, the context
// ended) releases the lease uncompleted, so another node may retry at once.
func (s MetaSweeper) Run(ctx context.Context) (MetaSweepResult, error) {
	var result MetaSweepResult
	acquired, err := s.Store.AcquireMetaSweepLease(ctx, s.Owner, metaSweepLeaseTTL, s.Interval)
	if err != nil || !acquired {
		return result, err
	}
	completed := false
	defer func() {
		// The lease is released even when the run's context ended, so the next node need not wait out the TTL.
		finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if finishErr := s.Store.FinishMetaSweepLease(finish, s.Owner, completed); finishErr != nil {
			slog.Warn("release meta sweep lease", "component", "meta_sweep", "error", finishErr)
		}
	}()
	failures, interrupted := s.sweep(ctx, &result)
	completed = interrupted == nil
	if failures != nil || interrupted != nil {
		s.run("failed")
		return result, errors.Join(interrupted, failures)
	}
	s.run("completed")
	return result, nil
}

// sweep returns the errors of single objects, which do not stop it, and the error that interrupted it.
func (s MetaSweeper) sweep(ctx context.Context, result *MetaSweepResult) (failures, interrupted error) {
	digests, err := s.Objects.List(ctx)
	if err != nil {
		return nil, err
	}
	live := make(map[string]struct{}, len(digests))
	for _, digest := range digests {
		live[digest] = struct{}{}
	}
	for start := 0; start < len(digests); start += s.batchSize() {
		if err := ctx.Err(); err != nil {
			return failures, err
		}
		if err := s.Store.RenewMetaSweepLease(ctx, s.Owner, metaSweepLeaseTTL); err != nil {
			// Another node took the lease: it owns the sweep now.
			return failures, err
		}
		for _, digest := range digests[start:min(start+s.batchSize(), len(digests))] {
			outcome, err := s.Store.SweepMetaObject(ctx, digest, s.Grace, func(ctx context.Context) error { return s.Objects.Delete(ctx, digest) })
			result.Examined++
			if err != nil {
				if ctx.Err() != nil {
					return failures, ctx.Err()
				}
				// One object that cannot be swept does not stop the rest; the run reports it.
				s.object("failed")
				failures = errors.Join(failures, err)
				continue
			}
			s.object(string(outcome))
			switch outcome {
			case repository.MetaSweepDeleted:
				result.Deleted++
			case repository.MetaSweepReferenced:
				result.Referenced++
			case repository.MetaSweepYoung:
				result.Young++
			}
		}
	}
	if err := s.forgetOrphanedPins(ctx, live, result); err != nil {
		if ctx.Err() != nil {
			return failures, ctx.Err()
		}
		failures = errors.Join(failures, err)
	}
	return failures, nil
}

// forgetOrphanedPins removes the pins of objects that do not exist and have not been written for the grace
// period: a put that failed after pinning leaves one.
func (s MetaSweeper) forgetOrphanedPins(ctx context.Context, live map[string]struct{}, result *MetaSweepResult) error {
	after := ""
	for {
		pins, err := s.Store.MetaObjectPinsOlderThan(ctx, after, s.Grace, s.batchSize())
		if err != nil {
			return err
		}
		for _, digest := range pins {
			after = digest
			if _, exists := live[digest]; exists || !strings.HasPrefix(digest, "sha256:") {
				continue
			}
			if err := s.Store.ForgetMetaObjectPin(ctx, digest, s.Grace); err != nil {
				return err
			}
			result.OrphanedPins++
		}
		if len(pins) < s.batchSize() {
			return nil
		}
	}
}

func (s MetaSweeper) object(outcome string) {
	if s.Metrics != nil {
		s.Metrics.RecordMetaSweepObject(outcome)
	}
}

func (s MetaSweeper) run(outcome string) {
	if s.Metrics != nil {
		s.Metrics.RecordMetaSweepRun(outcome)
	}
}

// Start runs the sweep on a scheduler node. Every node ticks every minute and the lease decides which one sweeps, and
// when: the lease is granted once per interval for the whole fleet.
func (s MetaSweeper) Start(ctx context.Context) {
	if s.Store == nil || s.Objects == nil {
		return
	}
	tick := min(metaSweepTick, s.Interval)
	background.Go(ctx, func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				result, err := s.Run(ctx)
				switch {
				case err != nil && ctx.Err() == nil:
					slog.Warn("meta object sweep failed", "component", "meta_sweep", "error", err)
				case result.Examined > 0 || result.OrphanedPins > 0:
					slog.Info("meta object sweep finished", "component", "meta_sweep", "examined", result.Examined,
						"deleted", result.Deleted, "referenced", result.Referenced, "young", result.Young, "orphaned_pins", result.OrphanedPins)
				}
			}
		}
	})
}

// MetaSweepCounters is the Prometheus-side state of the sweep, embedded in Metrics.
type MetaSweepCounters struct {
	deleted, referenced, young, failed atomic.Uint64
	runsCompleted, runsFailed          atomic.Uint64
}

// RecordMetaSweepObject implements MetaSweepMetrics.
func (c *MetaSweepCounters) RecordMetaSweepObject(outcome string) {
	switch outcome {
	case string(repository.MetaSweepDeleted):
		c.deleted.Add(1)
	case string(repository.MetaSweepReferenced):
		c.referenced.Add(1)
	case string(repository.MetaSweepYoung):
		c.young.Add(1)
	case "failed":
		c.failed.Add(1)
	}
}

// RecordMetaSweepRun implements MetaSweepMetrics.
func (c *MetaSweepCounters) RecordMetaSweepRun(outcome string) {
	switch outcome {
	case "completed":
		c.runsCompleted.Add(1)
	case "failed":
		c.runsFailed.Add(1)
	}
}

func (c *MetaSweepCounters) writeMetaSweepMetrics(w interface{ Write([]byte) (int, error) }) {
	_, _ = w.Write([]byte("# TYPE notarx_meta_sweep_objects_total counter\n" +
		"notarx_meta_sweep_objects_total{outcome=\"deleted\"} " + utoa(c.deleted.Load()) + "\n" +
		"notarx_meta_sweep_objects_total{outcome=\"referenced\"} " + utoa(c.referenced.Load()) + "\n" +
		"notarx_meta_sweep_objects_total{outcome=\"young\"} " + utoa(c.young.Load()) + "\n" +
		"notarx_meta_sweep_objects_total{outcome=\"failed\"} " + utoa(c.failed.Load()) + "\n" +
		"# TYPE notarx_meta_sweep_runs_total counter\n" +
		"notarx_meta_sweep_runs_total{outcome=\"completed\"} " + utoa(c.runsCompleted.Load()) + "\n" +
		"notarx_meta_sweep_runs_total{outcome=\"failed\"} " + utoa(c.runsFailed.Load()) + "\n"))
}
