package repository

import (
	"context"
	"time"

	"github.com/mlops-ca/notarx/internal/tasklease"
)

var _ InternalTaskLeaseStore = (*PostgresStore)(nil)

// AcquireInternalTaskLease takes or renews the lease row of an internal job; the statements live in internal/tasklease,
// which the partition manager and the retention janitor share.
func (s *PostgresStore) AcquireInternalTaskLease(ctx context.Context, task InternalTaskLease, owner string, ttl, minInterval time.Duration) (bool, error) {
	return tasklease.Leaser{DB: s.db, Dialect: tasklease.Postgres}.Acquire(ctx, task, owner, ttl, minInterval)
}

// ReleaseInternalTaskLease hands the lease back; see tasklease.Leaser.Release.
func (s *PostgresStore) ReleaseInternalTaskLease(ctx context.Context, task InternalTaskLease, owner string, completed bool) error {
	return tasklease.Leaser{DB: s.db, Dialect: tasklease.Postgres}.Release(ctx, task, owner, completed)
}
