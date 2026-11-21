package repository

import (
	"context"
	"sync"
	"time"

	"github.com/mlops-ca/notarx/internal/tasklease"
)

// InternalTaskLease names the lease row of a background job that one node at a time runs. The row lives in
// scheduled_tasks, is never due and is hidden from the management API (ADR 0023 section 2).
type InternalTaskLease = tasklease.Task

// InternalTaskLeaseStore elects the one node that runs an internal job.
type InternalTaskLeaseStore interface {
	// AcquireInternalTaskLease takes or renews the lease for owner. It reports false while another owner holds an
	// unexpired lease, and while the last completed run is younger than minInterval, so a fleet runs the job once
	// per interval however many nodes tick.
	AcquireInternalTaskLease(ctx context.Context, task InternalTaskLease, owner string, ttl, minInterval time.Duration) (bool, error)
	// ReleaseInternalTaskLease hands the lease back. completed records the run as finished now, which starts the
	// minimum interval; an interrupted run releases uncompleted, so another node may retry at once.
	ReleaseInternalTaskLease(ctx context.Context, task InternalTaskLease, owner string, completed bool) error
}

// internalScheduledTaskKinds is the SQL list of the kinds that are lease rows, not operator-defined schedules.
const internalScheduledTaskKinds = "'partition-manager','retention-janitor','raw-idle-seal','event-dispatcher'"

type memoryInternalLease struct {
	owner         string
	until         time.Time
	lastCompleted time.Time
}

type memoryInternalLeases struct {
	mu     sync.Mutex
	leases map[string]*memoryInternalLease
}

func (s *MemoryStore) AcquireInternalTaskLease(_ context.Context, task InternalTaskLease, owner string, ttl, minInterval time.Duration) (bool, error) {
	s.internalLeases.mu.Lock()
	defer s.internalLeases.mu.Unlock()
	if s.internalLeases.leases == nil {
		s.internalLeases.leases = map[string]*memoryInternalLease{}
	}
	now := time.Now()
	lease := s.internalLeases.leases[task.ID]
	if lease == nil {
		lease = &memoryInternalLease{}
		s.internalLeases.leases[task.ID] = lease
	}
	if lease.owner != owner && lease.owner != "" && now.Before(lease.until) {
		return false, nil
	}
	if lease.owner != owner && !lease.lastCompleted.IsZero() && now.Sub(lease.lastCompleted) < minInterval {
		return false, nil
	}
	lease.owner, lease.until = owner, now.Add(ttl)
	return true, nil
}

func (s *MemoryStore) ReleaseInternalTaskLease(_ context.Context, task InternalTaskLease, owner string, completed bool) error {
	s.internalLeases.mu.Lock()
	defer s.internalLeases.mu.Unlock()
	if lease := s.internalLeases.leases[task.ID]; lease != nil && lease.owner == owner {
		lease.owner, lease.until = "", time.Time{}
		if completed {
			lease.lastCompleted = time.Now()
		}
	}
	return nil
}
