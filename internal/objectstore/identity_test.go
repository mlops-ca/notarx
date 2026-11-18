package objectstore

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRootLock struct {
	alive    atomic.Bool
	released atomic.Int32
}

func (l *fakeRootLock) Alive(context.Context) error {
	if !l.alive.Load() {
		return errors.New("lock lost")
	}
	return nil
}
func (l *fakeRootLock) Release() { l.released.Add(1) }

type fakeIdentityDB struct {
	stored     *StoredIdentity
	claimErr   error
	lockErr    error
	lockTaken  bool
	lock       *fakeRootLock
	lockedRoot string
}

func (d *fakeIdentityDB) Claim(_ context.Context, identity StoredIdentity) (StoredIdentity, error) {
	if d.claimErr != nil {
		return StoredIdentity{}, d.claimErr
	}
	if d.stored == nil {
		d.stored = &identity
	}
	return *d.stored, nil
}

func (d *fakeIdentityDB) LockRoot(_ context.Context, rootID string) (RootLock, bool, error) {
	if d.lockErr != nil {
		return nil, false, d.lockErr
	}
	if d.lockTaken {
		return nil, false, nil
	}
	d.lockedRoot = rootID
	d.lock = &fakeRootLock{}
	d.lock.alive.Store(true)
	return d.lock, true, nil
}

func newIdentifiedStore(t *testing.T) *FileStore {
	t.Helper()
	return openTestFileStore(t, testFileConfig(t))
}

func TestVerifyIdentityRecordsFirstBootAndAcceptsTheSameRoot(t *testing.T) {
	store := newIdentifiedStore(t)
	db := &fakeIdentityDB{}
	guard, err := VerifyIdentity(context.Background(), store, db)
	if err != nil || guard == nil {
		t.Fatalf("first boot: guard=%v err=%v", guard, err)
	}
	if db.stored == nil || db.stored.Driver != DriverFilesystem || db.stored.RootID != store.RootID() || db.lockedRoot != store.RootID() {
		t.Fatalf("stored=%+v lockedRoot=%q", db.stored, db.lockedRoot)
	}
	guard.Close()
	guard.Close() // idempotent
	if db.lock.released.Load() != 1 {
		t.Fatalf("lock released %d times, want once", db.lock.released.Load())
	}
	// The same root again, as after a restart.
	db.lockTaken = false
	if guard, err = VerifyIdentity(context.Background(), store, db); err != nil {
		t.Fatalf("restart: %v", err)
	}
	guard.Close()
}

func TestVerifyIdentityRefusesAWrongRootOrDriver(t *testing.T) {
	store := newIdentifiedStore(t)
	for name, stored := range map[string]StoredIdentity{
		"different root ID": {Driver: DriverFilesystem, RootID: "00000000-0000-4000-8000-000000000000"},
		"different driver":  {Driver: "other", RootID: store.RootID()},
	} {
		db := &fakeIdentityDB{stored: &stored}
		guard, err := VerifyIdentity(context.Background(), store, db)
		if !errors.Is(err, ErrIdentityMismatch) || guard != nil {
			t.Fatalf("%s: guard=%v err=%v, want ErrIdentityMismatch", name, guard, err)
		}
		if db.lock != nil {
			t.Fatalf("%s: the root was locked despite the mismatch", name)
		}
		if !strings.Contains(err.Error(), "restore the database and the object store together") {
			t.Fatalf("%s: the error does not say what to do: %v", name, err)
		}
	}
}

func TestVerifyIdentityRefusesALockedRootAndReportsErrors(t *testing.T) {
	store := newIdentifiedStore(t)
	if _, err := VerifyIdentity(context.Background(), store, &fakeIdentityDB{lockTaken: true}); !errors.Is(err, ErrRootLocked) {
		t.Fatalf("locked root: %v, want ErrRootLocked", err)
	}
	boom := errors.New("database down")
	if _, err := VerifyIdentity(context.Background(), store, &fakeIdentityDB{claimErr: boom}); !errors.Is(err, boom) {
		t.Fatalf("claim error: %v", err)
	}
	if _, err := VerifyIdentity(context.Background(), store, &fakeIdentityDB{lockErr: boom}); !errors.Is(err, boom) {
		t.Fatalf("lock error: %v", err)
	}
}

func TestVerifyIdentityIgnoresStoresThatAreNotSingleNode(t *testing.T) {
	db := &fakeIdentityDB{}
	for name, store := range map[string]Store{"memory": NewMemoryStore(), "s3": &S3Store{}} {
		guard, err := VerifyIdentity(context.Background(), store, db)
		if err != nil || guard != nil {
			t.Fatalf("%s: guard=%v err=%v, want nothing to enforce", name, guard, err)
		}
	}
	if db.stored != nil {
		t.Fatal("a store without identity wrote a row")
	}
	var guard *IdentityGuard
	guard.Close() // a nil guard is safe
	guard.Watch(context.Background(), time.Millisecond, func(error) { t.Fatal("nil guard watched") })
}

func TestIdentityGuardWatchReportsALostLock(t *testing.T) {
	store := newIdentifiedStore(t)
	db := &fakeIdentityDB{}
	guard, err := VerifyIdentity(context.Background(), store, db)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	lost := make(chan error, 2)
	guard.Watch(context.Background(), 5*time.Millisecond, func(err error) { lost <- err })
	select {
	case <-lost:
		t.Fatal("reported a lost lock while it was held")
	case <-time.After(30 * time.Millisecond):
	}
	db.lock.alive.Store(false)
	select {
	case err := <-lost:
		if err == nil {
			t.Fatal("lost callback got a nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a lost lock was not reported")
	}
}

func TestIdentityGuardWatchStopsOnCloseAndContext(t *testing.T) {
	store := newIdentifiedStore(t)
	db := &fakeIdentityDB{}
	guard, err := VerifyIdentity(context.Background(), store, db)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	guard.Watch(ctx, 5*time.Millisecond, func(error) { calls.Add(1) })
	cancel()
	db.lock.alive.Store(false)
	time.Sleep(40 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("watcher reported after its context ended: %d", calls.Load())
	}
	guard.Close()
}
