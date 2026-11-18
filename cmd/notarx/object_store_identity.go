package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

// objectStoreIdentity adapts the repository store to objectstore.IdentityDatabase.
type objectStoreIdentity struct{ store *repository.PostgresStore }

func (i objectStoreIdentity) Claim(ctx context.Context, identity objectstore.StoredIdentity) (objectstore.StoredIdentity, error) {
	stored, err := i.store.ClaimObjectStoreIdentity(ctx, repository.ObjectStoreIdentity{Driver: identity.Driver, RootID: identity.RootID})
	return objectstore.StoredIdentity{Driver: stored.Driver, RootID: stored.RootID}, err
}

func (i objectStoreIdentity) LockRoot(ctx context.Context, rootID string) (objectstore.RootLock, bool, error) {
	lock, acquired, err := i.store.LockObjectStoreRoot(ctx, rootID)
	if err != nil || !acquired {
		return nil, acquired, err
	}
	return rootLock{lock}, true, nil
}

// rootLock reports a definitively lost lock (the session is gone, or no longer
// holds the advisory lock) as objectstore.ErrLockLost; other errors from the
// check are left for the watcher to treat as possibly transient.
type rootLock struct {
	*repository.ObjectStoreRootLock
}

func (l rootLock) Alive(ctx context.Context) error {
	err := l.ObjectStoreRootLock.Alive(ctx)
	if errors.Is(err, repository.ErrObjectStoreRootLockLost) || errors.Is(err, sql.ErrConnDone) || errors.Is(err, driver.ErrBadConn) {
		return fmt.Errorf("%w: %v", objectstore.ErrLockLost, err)
	}
	return err
}
