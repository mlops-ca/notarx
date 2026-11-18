package objectstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Identified is implemented by drivers that are bound to one node's storage
// (the filesystem driver). VerifyIdentity enforces the binding.
type Identified interface {
	Identity() (driver, rootID string)
}

// StoredIdentity is the driver and root ID recorded in the database.
type StoredIdentity struct{ Driver, RootID string }

// RootLock is a held process-lifetime lock on a root ID.
type RootLock interface {
	// Alive reports whether the lock is still held.
	Alive(ctx context.Context) error
	Release()
}

// IdentityDatabase is the database side of single-node enforcement.
type IdentityDatabase interface {
	// Claim records identity if none is stored and returns what is stored.
	Claim(ctx context.Context, identity StoredIdentity) (StoredIdentity, error)
	// LockRoot tries to take the session lock for rootID without waiting.
	LockRoot(ctx context.Context, rootID string) (lock RootLock, acquired bool, err error)
}

// ErrIdentityMismatch means the database is bound to a different object store
// root than the one configured: a second node, a restored volume or a wrong mount.
var ErrIdentityMismatch = errors.New("the database is bound to a different object store root")

// ErrRootLocked means another gateway process already holds this root.
var ErrRootLocked = errors.New("another gateway process holds this object store root")

// IdentityGuard keeps the root lock for the life of the process.
type IdentityGuard struct {
	lock RootLock
	once sync.Once
	stop chan struct{}
}

// VerifyIdentity binds a single-node store to the database. For stores that are
// not Identified (S3) it does nothing and returns a nil guard, which is safe to
// Close. Otherwise it records the root ID on first boot, refuses a root ID that
// differs from the recorded one and takes the root's session lock so two
// processes cannot share one root.
func VerifyIdentity(ctx context.Context, store Store, database IdentityDatabase) (*IdentityGuard, error) {
	identified, ok := store.(Identified)
	if !ok {
		return nil, nil
	}
	driver, rootID := identified.Identity()
	stored, err := database.Claim(ctx, StoredIdentity{Driver: driver, RootID: rootID})
	if err != nil {
		return nil, fmt.Errorf("record object store identity: %w", err)
	}
	if stored.Driver != driver || stored.RootID != rootID {
		return nil, fmt.Errorf("%w: the database expects the %s root %s but this node opened root %s; mount the original volume, or restore the database and the object store together", ErrIdentityMismatch, stored.Driver, stored.RootID, rootID)
	}
	lock, acquired, err := database.LockRoot(ctx, rootID)
	if err != nil {
		return nil, fmt.Errorf("lock object store root: %w", err)
	}
	if !acquired {
		return nil, ErrRootLocked
	}
	return &IdentityGuard{lock: lock, stop: make(chan struct{})}, nil
}

// Watch checks the lock every interval until ctx ends or Close is called and
// calls lost once, with the cause, when the lock is gone. A gateway that has
// lost it must stop writing.
func (g *IdentityGuard) Watch(ctx context.Context, interval time.Duration, lost func(error)) {
	if g == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-g.stop:
				return
			case <-ticker.C:
				checkCtx, cancel := context.WithTimeout(ctx, interval)
				err := g.lock.Alive(checkCtx)
				cancel()
				if err != nil && ctx.Err() == nil {
					lost(err)
					return
				}
			}
		}
	}()
}

// Close stops the watcher and releases the lock. It is safe on a nil guard.
func (g *IdentityGuard) Close() {
	if g == nil {
		return
	}
	g.once.Do(func() {
		close(g.stop)
		g.lock.Release()
	})
}
