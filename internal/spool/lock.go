package spool

import (
	"context"
	"errors"
	"sync"
	"time"

	cache "github.com/mlops-ca/notarx/internal/cache"
)

// DefaultLockLease is the distributed single-flight lease; it is renewed at a
// third of its length while the lock is held.
const DefaultLockLease = 35 * time.Second

// Locker serializes work per key across Gateway instances through a
// cache.Coordinator. A nil Coordinator makes Acquire a no-op.
type Locker struct {
	Coordinator cache.Coordinator
	Lease       time.Duration
	RenewEvery  time.Duration
	// Prefix namespaces keys, for example "cache-request:".
	Prefix string
}

// ErrLocked reports that another holder has the lock.
var ErrLocked = errors.New("lock is held by another node")

// Acquire takes the lock for key, waiting until it is available. The returned
// context is cancelled if lease renewal fails; release reports that failure.
func (l Locker) Acquire(ctx context.Context, key string) (context.Context, func() error, error) {
	for {
		workCtx, release, err := l.TryAcquire(ctx, key)
		if !errors.Is(err, ErrLocked) {
			return workCtx, release, err
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TryAcquire takes the lock for key without waiting and returns ErrLocked when another holder has
// it. The returned context is cancelled if lease renewal fails; release reports that failure.
func (l Locker) TryAcquire(ctx context.Context, key string) (context.Context, func() error, error) {
	if l.Coordinator == nil {
		return ctx, func() error { return nil }, nil
	}
	lockKey := l.Prefix + key
	owner, acquired, err := l.Coordinator.Acquire(ctx, lockKey, l.Lease)
	if err != nil {
		return nil, nil, err
	}
	if !acquired {
		return nil, nil, ErrLocked
	}
	workCtx, cancel := context.WithCancel(ctx)
	renewalFailed := make(chan struct{})
	stopped := make(chan struct{})
	go l.renew(workCtx, lockKey, owner, renewalFailed, stopped, cancel)
	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			cancel()
			<-stopped
			select {
			case <-renewalFailed:
				releaseErr = errors.New("distributed request lock renewal failed")
			default:
			}
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			releaseErr = errors.Join(releaseErr, l.Coordinator.Release(releaseCtx, lockKey, owner))
		})
		return releaseErr
	}
	return workCtx, release, nil
}

func (l Locker) renew(ctx context.Context, key, owner string, failed chan<- struct{}, stopped chan<- struct{}, cancel context.CancelFunc) {
	defer close(stopped)
	ticker := time.NewTicker(l.RenewEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ok, err := l.Coordinator.Renew(ctx, key, owner, l.Lease)
			if err != nil || !ok {
				close(failed)
				cancel()
				return
			}
		}
	}
}
