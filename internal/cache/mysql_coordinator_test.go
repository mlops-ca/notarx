package cache

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/testsupport/fakesql"
)

func mysqlLockServer(held bool, release any) *fakesql.Server {
	return &fakesql.Server{Handler: func(query string, args []driver.NamedValue) (fakesql.Result, error) {
		switch {
		case strings.Contains(query, "GET_LOCK"):
			if held {
				return fakesql.One("l", int64(1)), nil
			}
			return fakesql.One("l", int64(0)), nil
		case strings.Contains(query, "RELEASE_LOCK"):
			return fakesql.One("l", release), nil
		case strings.Contains(query, "RELEASE_ALL_LOCKS"):
			return fakesql.One("l", int64(1)), nil
		case strings.Contains(query, "SELECT EXISTS"):
			return fakesql.One("e", int64(1)), nil
		}
		return fakesql.None(), nil
	}}
}

func newMySQLCoordinator(t *testing.T, server *fakesql.Server) *MySQLCoordinator {
	t.Helper()
	c, err := NewMySQLCoordinatorWithPools(server.Open(t), server.Open(t))
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }
	return c
}

func TestMySQLCoordinatorRequiresBothPools(t *testing.T) {
	if _, err := NewMySQLCoordinatorWithPools(nil, nil); err == nil {
		t.Fatal("missing pools were accepted")
	}
}

func TestMySQLCoordinatorLockLifecycle(t *testing.T) {
	server := mysqlLockServer(true, int64(1))
	c := newMySQLCoordinator(t, server)
	owner, acquired, err := c.Acquire(context.Background(), "maven/key", time.Minute)
	if err != nil || !acquired || owner == "" {
		t.Fatalf("Acquire = %q %t %v", owner, acquired, err)
	}
	if got := server.Logged("GET_LOCK"); len(got) != 1 || !strings.HasSuffix(got[0], ",0") {
		t.Fatalf("GET_LOCK = %v; a cache publication try-locks (timeout 0)", got)
	}
	if renewed, err := c.Renew(context.Background(), "maven/key", owner, time.Minute); err != nil || !renewed {
		t.Fatalf("Renew = %t %v", renewed, err)
	}
	if renewed, err := c.Renew(context.Background(), "maven/key", "other", time.Minute); err != nil || renewed {
		t.Fatalf("Renew of an unknown owner = %t %v", renewed, err)
	}
	if err := c.Release(context.Background(), "maven/key", owner); err != nil {
		t.Fatal(err)
	}
	if len(server.Logged("RELEASE_LOCK")) != 1 || len(server.Logged("RELEASE_ALL_LOCKS")) != 1 {
		t.Fatalf("a released lease must unlock and drop every session lock before the connection returns to the pool: %v", server.Log)
	}
	if err := c.Release(context.Background(), "maven/key", owner); err != nil {
		t.Fatalf("a second release must be a no-op: %v", err)
	}
}

func TestMySQLCoordinatorLockHeldElsewhere(t *testing.T) {
	c := newMySQLCoordinator(t, mysqlLockServer(false, int64(1)))
	owner, acquired, err := c.Acquire(context.Background(), "k", time.Minute)
	if err != nil || acquired || owner == "" {
		t.Fatalf("Acquire = %q %t %v", owner, acquired, err)
	}
}

func TestMySQLCoordinatorAcquireErrors(t *testing.T) {
	boom := errors.New("boom")
	failing := &fakesql.Server{Handler: func(string, []driver.NamedValue) (fakesql.Result, error) { return fakesql.Result{}, boom }}
	c := newMySQLCoordinator(t, failing)
	if _, acquired, err := c.Acquire(context.Background(), "k", time.Minute); !errors.Is(err, boom) || acquired {
		t.Fatalf("Acquire = %t %v", acquired, err)
	}
}

func TestMySQLCoordinatorReleaseFailureDiscardsTheConnection(t *testing.T) {
	server := mysqlLockServer(true, int64(0)) // RELEASE_LOCK says the lock was not held
	c := newMySQLCoordinator(t, server)
	owner, _, err := c.Acquire(context.Background(), "k", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Release(context.Background(), "k", owner); err == nil {
		t.Fatal("a lock that was not held was released without an error")
	}
}

func TestMySQLCoordinatorCloseReleasesEveryLease(t *testing.T) {
	server := mysqlLockServer(true, int64(1))
	c := newMySQLCoordinator(t, server)
	for _, key := range []string{"a", "b"} {
		if _, acquired, err := c.Acquire(context.Background(), key, time.Minute); err != nil || !acquired {
			t.Fatal(acquired, err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if len(server.Logged("RELEASE_LOCK")) != 2 {
		t.Fatalf("Close released %d of 2 leases", len(server.Logged("RELEASE_LOCK")))
	}
}

func TestMySQLCoordinatorCircuit(t *testing.T) {
	server := mysqlLockServer(true, int64(1))
	c := newMySQLCoordinator(t, server)
	open, err := c.CircuitOpen(context.Background(), "upstream")
	if err != nil || !open {
		t.Fatalf("CircuitOpen = %t %v", open, err)
	}
	if got := server.Logged("expires_at > ?"); len(got) != 1 || !strings.Contains(got[0], "2026-11-20 12:00:00") {
		t.Fatalf("the breaker is compared with the Go clock in UTC: %v", got)
	}
	if err := c.OpenCircuit(context.Background(), "upstream", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := server.Logged("AS new ON DUPLICATE KEY UPDATE expires_at = new.expires_at"); len(got) != 1 || !strings.Contains(got[0], "2026-11-20 12:00:30") {
		t.Fatalf("OpenCircuit = %v; it must use the row alias, not VALUES()", got)
	}
	if err := c.CloseCircuit(context.Background(), "upstream"); err != nil || len(server.Logged("DELETE FROM cache_circuit_breakers")) != 1 {
		t.Fatalf("CloseCircuit = %v", err)
	}
}
