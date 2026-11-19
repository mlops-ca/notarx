package app

import (
	"context"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

// LifecycleLeaseRecoveryInterval is how often the scheduler returns jobs whose worker lease expired to
// the queue (ADR 0023 section 7). Claims no longer recover leases themselves.
const LifecycleLeaseRecoveryInterval = 30 * time.Second

// LifecycleJobRecovery is the scheduler task that recovers expired leases of lifecycle jobs and, when
// Replications is set, replication plans. It is the only caller of the recovery statements.
type LifecycleJobRecovery struct {
	Store        repository.LifecycleJobStore
	Replications interface {
		RecoverExpiredReplicationPlans(context.Context, time.Time) (int, error)
	}
	Now func() time.Time
}

// Run recovers every expired lease and reports how many rows were returned to the queue.
func (r LifecycleJobRecovery) Run(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	recovered, err := r.Store.RecoverExpiredLifecycleJobs(ctx, now)
	if err != nil || r.Replications == nil {
		return recovered, err
	}
	plans, err := r.Replications.RecoverExpiredReplicationPlans(ctx, now)
	return recovered + plans, err
}

// Start runs one pass immediately and then every interval until ctx ends.
func (r LifecycleJobRecovery) Start(ctx context.Context, interval time.Duration) {
	if r.Store == nil || interval <= 0 {
		return
	}
	go func() {
		_, _ = r.Run(ctx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = r.Run(ctx)
			}
		}
	}()
}
