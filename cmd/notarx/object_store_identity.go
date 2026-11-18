package main

import (
	"context"

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
	return lock, true, nil
}
