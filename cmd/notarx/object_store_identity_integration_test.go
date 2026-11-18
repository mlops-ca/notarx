//go:build integration

package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

func identityTestSetup(t *testing.T) (databaseURL string, reset func()) {
	t.Helper()
	databaseURL = os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	pool, err := database.OpenPostgres(databaseURL, database.DefaultPoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	reset = func() {
		if _, execErr := pool.Exec(`DELETE FROM object_store_identity`); execErr != nil {
			t.Fatal(execErr)
		}
	}
	reset()
	t.Cleanup(reset)
	return databaseURL, reset
}

func newIdentityStore(t *testing.T, databaseURL string) *repository.PostgresStore {
	t.Helper()
	store, err := repository.NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newRoot(t *testing.T) *objectstore.FileStore {
	t.Helper()
	cfg := objectstore.Defaults()
	cfg.Driver = objectstore.DriverFilesystem
	cfg.FSRoot = filepath.Join(t.TempDir(), "objects")
	cfg.FSReserveBytes = 0
	store, err := objectstore.NewFileStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestFilesystemRootIsBoundToTheDatabase(t *testing.T) {
	databaseURL, _ := identityTestSetup(t)
	ctx := context.Background()
	rootA, rootB := newRoot(t), newRoot(t)
	gateway := newIdentityStore(t, databaseURL)

	// First boot records the root and keeps it locked.
	guard, err := objectstore.VerifyIdentity(ctx, rootA, objectStoreIdentity{gateway})
	if err != nil || guard == nil {
		t.Fatalf("first boot: %v", err)
	}

	// A second process on the same root is refused while the first runs.
	second := newIdentityStore(t, databaseURL)
	if _, err = objectstore.VerifyIdentity(ctx, rootA, objectStoreIdentity{second}); !errors.Is(err, objectstore.ErrRootLocked) {
		t.Fatalf("second process on one root: %v, want ErrRootLocked", err)
	}

	// A different root (a second node, a restored volume, a wrong mount) is
	// refused outright, locked or not.
	if _, err = objectstore.VerifyIdentity(ctx, rootB, objectStoreIdentity{second}); !errors.Is(err, objectstore.ErrIdentityMismatch) {
		t.Fatalf("wrong root: %v, want ErrIdentityMismatch", err)
	}

	// After the first process stops, the same root starts again.
	guard.Close()
	restarted, err := objectstore.VerifyIdentity(ctx, rootA, objectStoreIdentity{second})
	if err != nil {
		t.Fatalf("restart on the recorded root: %v", err)
	}
	restarted.Close()
}

func TestStoresWithoutIdentityAreNotBound(t *testing.T) {
	databaseURL, _ := identityTestSetup(t)
	gateway := newIdentityStore(t, databaseURL)
	guard, err := objectstore.VerifyIdentity(context.Background(), objectstore.NewMemoryStore(), objectStoreIdentity{gateway})
	if err != nil || guard != nil {
		t.Fatalf("guard=%v err=%v", guard, err)
	}
	rootA := newRoot(t)
	if guard, err = objectstore.VerifyIdentity(context.Background(), rootA, objectStoreIdentity{gateway}); err != nil {
		t.Fatal(err)
	}
	guard.Close()
}

func TestRootLockLossIsDetected(t *testing.T) {
	databaseURL, _ := identityTestSetup(t)
	ctx := context.Background()
	root := newRoot(t)
	gateway := newIdentityStore(t, databaseURL)
	guard, err := objectstore.VerifyIdentity(ctx, root, objectStoreIdentity{gateway})
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	lost := make(chan error, 1)
	guard.Watch(ctx, 50*time.Millisecond, func(err error) { lost <- err })

	// Kill the session that holds the lock, as a network partition or a database
	// restart would; the lock disappears with it.
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	if _, err = admin.Exec(`SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype = 'advisory' AND objsubid = 1 AND pid <> pg_backend_pid()`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lost:
	case <-time.After(10 * time.Second):
		t.Fatal("the lost root lock was not reported")
	}
}
