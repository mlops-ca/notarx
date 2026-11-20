//go:build integration

package tasklease_test

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/tasklease"
	"github.com/mlops-ca/notarx/internal/testsupport"
)

func TestMain(m *testing.M) {
	if err := testsupport.ValidateIsolatedEnvironment(os.Getenv); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(m.Run())
}

var contractTask = tasklease.Task{ID: "00000000-0000-7000-8000-0000000000b1", Name: "internal:tasklease-contract", Kind: "raw-idle-seal", Description: "tasklease contract test"}

// TestLeaseContract runs the lease contract against the selected engine, so PostgreSQL and MySQL share one
// definition of the behaviour (ADR 0023 section 2, ADR 0022).
func TestLeaseContract(t *testing.T) {
	testsupport.RunPerDriver(t, map[string]func(*testing.T, string){
		testsupport.DriverPostgres: func(t *testing.T, url string) { runContract(t, database.DriverPostgres, url) },
		testsupport.DriverMySQL:    func(t *testing.T, url string) { runContract(t, database.DriverMySQL, url) },
	})
}

func runContract(t *testing.T, driver, url string) {
	db, err := database.Open(driver, url, database.PoolConfig{MaxOpenConns: 40, MaxIdleConns: 10, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	leaser := tasklease.Leaser{DB: db, Dialect: tasklease.Dialect(driver)}
	ctx := t.Context()
	reset := func(t *testing.T) {
		t.Helper()
		if _, err := db.ExecContext(ctx, "DELETE FROM scheduled_tasks WHERE name = "+placeholder(driver, 1), contractTask.Name); err != nil {
			t.Fatal(err)
		}
	}
	reset(t)
	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM scheduled_tasks WHERE name = "+placeholder(driver, 1), contractTask.Name)
	})
	acquire := func(t *testing.T, owner string, ttl, interval time.Duration) bool {
		t.Helper()
		held, err := leaser.Acquire(ctx, contractTask, owner, ttl, interval)
		if err != nil {
			t.Fatal(err)
		}
		return held
	}
	release := func(t *testing.T, owner string, completed bool) {
		t.Helper()
		if err := leaser.Release(ctx, contractTask, owner, completed); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("one_holder_at_a_time", func(t *testing.T) {
		reset(t)
		if !acquire(t, "a", time.Minute, 0) {
			t.Fatal("the first node must get the lease")
		}
		if acquire(t, "b", time.Minute, 0) {
			t.Fatal("a second node got a lease that is held")
		}
		if !acquire(t, "a", time.Minute, time.Hour) {
			t.Fatal("the holder renews its lease whatever the interval")
		}
		release(t, "b", true)
		if acquire(t, "b", time.Minute, 0) {
			t.Fatal("a node that does not hold the lease cannot release it")
		}
		release(t, "a", false)
		if !acquire(t, "b", time.Minute, time.Hour) {
			t.Fatal("an uncompleted run releases at once, so another node retries without waiting for the interval")
		}
		release(t, "b", false)
	})
	t.Run("owners_are_compared_as_binary", func(t *testing.T) {
		reset(t)
		if !acquire(t, "Node-A", time.Minute, 0) {
			t.Fatal("acquire")
		}
		if acquire(t, "node-a", time.Minute, 0) {
			t.Fatal("an owner that differs only in case must not renew another owner's lease")
		}
		release(t, "node-a", false)
		if acquire(t, "node-b", time.Minute, 0) {
			t.Fatal("an owner that differs only in case must not release another owner's lease")
		}
		release(t, "Node-A", false)
		if !acquire(t, "node-b", time.Minute, 0) {
			t.Fatal("the exact owner releases")
		}
		release(t, "node-b", false)
	})
	t.Run("expired_lease_is_taken_over", func(t *testing.T) {
		reset(t)
		if !acquire(t, "a", 200*time.Millisecond, 0) {
			t.Fatal("acquire")
		}
		deadline := time.Now().Add(5 * time.Second)
		for !acquire(t, "b", time.Minute, 0) {
			if time.Now().After(deadline) {
				t.Fatal("a crashed holder must not block the fleet past its TTL")
			}
			time.Sleep(50 * time.Millisecond)
		}
		release(t, "b", false)
	})
	t.Run("interval_between_completed_runs", func(t *testing.T) {
		reset(t)
		if !acquire(t, "a", time.Minute, time.Hour) {
			t.Fatal("acquire")
		}
		release(t, "a", true)
		for _, owner := range []string{"a", "b"} {
			if acquire(t, owner, time.Minute, time.Hour) {
				t.Fatalf("%s ran again inside the minimum interval", owner)
			}
		}
		if !acquire(t, "b", time.Minute, 0) {
			t.Fatal("a zero minimum interval ignores the last completed run")
		}
		release(t, "b", false)
	})
	t.Run("racing_nodes_elect_exactly_one", func(t *testing.T) {
		// Nothing exists yet, so the nodes also race to create the row.
		for round := range 5 {
			reset(t)
			wins := raceFor(t, db, leaser, contractTask, 16)
			if wins != 1 {
				t.Fatalf("round %d: %d nodes hold the lease, want exactly one", round, wins)
			}
			var owner string
			if err := db.QueryRowContext(ctx, "SELECT lease_owner FROM scheduled_tasks WHERE name = "+placeholder(driver, 1), contractTask.Name).Scan(&owner); err != nil || owner == "" {
				t.Fatalf("lease row: %q %v", owner, err)
			}
			// Releasing by the owner lets the next election pick exactly one again.
			release(t, owner, false)
			if wins := raceFor(t, db, leaser, contractTask, 16); wins != 1 {
				t.Fatalf("round %d after release: %d nodes hold the lease, want exactly one", round, wins)
			}
		}
	})
}

// raceFor has n nodes call Acquire at once and returns how many were granted the lease.
func raceFor(t *testing.T, _ *sql.DB, leaser tasklease.Leaser, task tasklease.Task, n int) int {
	t.Helper()
	var wins atomic.Int32
	var group sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			held, err := leaser.Acquire(t.Context(), task, fmt.Sprintf("node-%02d", i), time.Minute, 0)
			if err != nil {
				t.Errorf("node %d: %v", i, err)
				return
			}
			if held {
				wins.Add(1)
			}
		}()
	}
	close(start)
	group.Wait()
	return int(wins.Load())
}

func placeholder(driver string, n int) string {
	if driver == database.DriverMySQL {
		return "?"
	}
	return fmt.Sprintf("$%d", n)
}
