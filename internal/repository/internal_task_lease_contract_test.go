package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

func TestMemoryInternalTaskLeaseContract(t *testing.T) {
	runInternalTaskLeaseContract(t, func(*testing.T) repository.InternalTaskLeaseStore { return repository.NewMemoryStore() })
}

// runInternalTaskLeaseContract is the lease contract of the background jobs that one node runs at a time. The same
// assertions run against PostgreSQL under the integration build tag.
func runInternalTaskLeaseContract(t *testing.T, factory func(*testing.T) repository.InternalTaskLeaseStore) {
	t.Helper()
	ctx := context.Background()
	task := repository.InternalTaskLease{ID: "00000000-0000-7000-8000-0000000000a3", Name: "internal:raw-idle-seal", Kind: "raw-idle-seal", Description: "contract test"}
	acquire := func(store repository.InternalTaskLeaseStore, owner string, ttl, interval time.Duration) bool {
		t.Helper()
		held, err := store.AcquireInternalTaskLease(ctx, task, owner, ttl, interval)
		if err != nil {
			t.Fatal(err)
		}
		return held
	}
	release := func(store repository.InternalTaskLeaseStore, owner string, completed bool) {
		t.Helper()
		if err := store.ReleaseInternalTaskLease(ctx, task, owner, completed); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("one_holder_at_a_time", func(t *testing.T) {
		store := factory(t)
		if !acquire(store, "a", time.Minute, 0) {
			t.Fatal("the first node must get the lease")
		}
		if acquire(store, "b", time.Minute, 0) {
			t.Fatal("a second node got a lease that is held")
		}
		if !acquire(store, "a", time.Minute, time.Hour) {
			t.Fatal("the holder renews its lease whatever the interval")
		}
		release(store, "b", true)
		if acquire(store, "b", time.Minute, 0) {
			t.Fatal("a node that does not hold the lease cannot release it")
		}
		release(store, "a", false)
		if !acquire(store, "b", time.Minute, time.Hour) {
			t.Fatal("an uncompleted run releases at once, so another node retries without waiting for the interval")
		}
	})
	t.Run("expired_lease_is_taken_over", func(t *testing.T) {
		store := factory(t)
		if !acquire(store, "a", 30*time.Millisecond, 0) {
			t.Fatal("acquire")
		}
		time.Sleep(80 * time.Millisecond)
		if !acquire(store, "b", time.Minute, 0) {
			t.Fatal("a crashed holder must not block the fleet past its TTL")
		}
		release(store, "b", false)
	})
	t.Run("interval_between_completed_runs", func(t *testing.T) {
		store := factory(t)
		if !acquire(store, "a", time.Minute, time.Hour) {
			t.Fatal("acquire")
		}
		release(store, "a", true)
		for _, owner := range []string{"a", "b"} {
			if acquire(store, owner, time.Minute, time.Hour) {
				t.Fatalf("%s ran again inside the minimum interval", owner)
			}
		}
		time.Sleep(60 * time.Millisecond)
		if !acquire(store, "b", time.Minute, 30*time.Millisecond) {
			t.Fatal("the interval has passed")
		}
		release(store, "b", false)
	})
}
