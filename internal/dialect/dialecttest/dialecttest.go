// Package dialecttest is the shared contract suite of internal/dialect (ADR 0022, ADR 0010): the
// same behaviour is asserted against every engine, so PostgreSQL and MySQL cannot drift apart. A dialect
// package runs it from an integration test with its own Harness.
package dialecttest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/dialect"
)

// Harness is one engine under test. Its tables are created by Run and dropped when the test ends.
type Harness struct {
	Set dialect.Set
	DB  *sql.DB
	// Rebind turns the ? placeholders of the suite into the engine's own ($1 for PostgreSQL).
	Rebind func(string) string
	// Schema creates the scratch tables: dialect_contract_counter(id, n), dialect_contract_queue(id,
	// state, lease_until), dialect_contract_parent(id) and dialect_contract_child(id, parent_id).
	Schema []string
	// ConnectionID returns the server-side id of a connection; Terminate kills that connection.
	ConnectionID func(ctx context.Context, conn *sql.Conn) (int64, error)
	Terminate    func(ctx context.Context, db *sql.DB, id int64) error
	// LockTimeout makes a lock wait on conn fail after about a second (on) or restores the default (off).
	LockTimeout func(ctx context.Context, conn *sql.Conn, on bool) error
	// Notify delivers a notification the way an enqueue would; LatencyBound is how long Wait may take after it.
	Notify       func(ctx context.Context, channel string) error
	LatencyBound time.Duration
	// LockTimeoutRetryable is whether the classifier retries a lock wait timeout (MySQL yes, PostgreSQL no).
	LockTimeoutRetryable bool
}

var tables = []string{"dialect_contract_child", "dialect_contract_parent", "dialect_contract_queue", "dialect_contract_counter"}

// Run runs every contract of the suite as a subtest.
func Run(t *testing.T, h Harness) {
	t.Helper()
	ctx := context.Background()
	drop := func() {
		for _, table := range tables {
			_, _ = h.DB.ExecContext(ctx, "DROP TABLE IF EXISTS "+table)
		}
	}
	drop()
	for _, statement := range h.Schema {
		if _, err := h.DB.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	t.Cleanup(drop)
	for _, contract := range []struct {
		name string
		run  func(*testing.T, Harness)
	}{
		{"MutualExclusion", mutualExclusion},
		{"BucketOrderingWithoutDeadlock", orderingWithoutDeadlock},
		{"TransactionWaiterCancellation", transactionWaiterCancellation},
		{"SessionLocks", sessionLocks},
		{"SessionWaiterCancellation", sessionWaiterCancellation},
		{"LockReleasedOnConnectionKill", releasedOnConnectionKill},
		{"SkipLockedClaimAndRequeue", claimAndRequeue},
		{"LeaseExpiry", leaseExpiry},
		{"ClaimRejectsUnsafeQueries", claimRejectsUnsafeQueries},
		{"NotificationLatency", notificationLatency},
		{"ErrorClassification", errorClassification},
		{"DeadlockIsRetryable", deadlockIsRetryable},
		{"RunTxRetriesADeadlock", runTxRetriesADeadlock},
	} {
		t.Run(contract.name, func(t *testing.T) { contract.run(t, h) })
	}
}

func (h Harness) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := h.DB.ExecContext(context.Background(), h.Rebind(query), args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func (h Harness) reset(t *testing.T) {
	t.Helper()
	for _, table := range tables {
		h.exec(t, "DELETE FROM "+table)
	}
}

func within(t *testing.T, limit time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s did not finish within %s", what, limit)
	}
}

// mutualExclusion: a read-modify-write that is only safe under the lock loses no update.
func mutualExclusion(t *testing.T, h Harness) {
	h.reset(t)
	h.exec(t, "INSERT INTO dialect_contract_counter (id, n) VALUES (1, 0)")
	const workers, rounds = 8, 3
	var wg sync.WaitGroup
	errs := make(chan error, workers*rounds)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				errs <- dialect.RunTx(context.Background(), h.DB, h.Set.Classifier, nil, func(tx *sql.Tx) error {
					if err := h.Set.Locker.LockTx(context.Background(), tx, "contract:counter"); err != nil {
						return err
					}
					var n int
					if err := tx.QueryRow(h.Rebind("SELECT n FROM dialect_contract_counter WHERE id = ?"), 1).Scan(&n); err != nil {
						return err
					}
					time.Sleep(5 * time.Millisecond)
					_, err := tx.Exec(h.Rebind("UPDATE dialect_contract_counter SET n = ? WHERE id = ?"), n+1, 1)
					return err
				})
			}
		}()
	}
	within(t, 60*time.Second, "the workers", wg.Wait)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := h.DB.QueryRow(h.Rebind("SELECT n FROM dialect_contract_counter WHERE id = ?"), 1).Scan(&n); err != nil || n != workers*rounds {
		t.Fatalf("counter = %d, %v; want %d (an update was lost)", n, err, workers*rounds)
	}
}

// orderingWithoutDeadlock: keys passed together in any order never deadlock, duplicates included.
func orderingWithoutDeadlock(t *testing.T, h Harness) {
	orders := [][]string{{"a", "b", "c"}, {"c", "b", "a"}, {"b", "a", "b"}, {"a", "c"}, {"c", "a", "b", "a"}}
	var wg sync.WaitGroup
	errs := make(chan error, len(orders)*10)
	for range 10 {
		for _, keys := range orders {
			wg.Add(1)
			go func() {
				defer wg.Done()
				tx, err := h.DB.BeginTx(context.Background(), nil)
				if err != nil {
					errs <- err
					return
				}
				defer func() { _ = tx.Rollback() }()
				if err := h.Set.Locker.LockTx(context.Background(), tx, keys...); err != nil {
					errs <- fmt.Errorf("%v: %w", keys, err)
					return
				}
				time.Sleep(2 * time.Millisecond)
				errs <- tx.Commit()
			}()
		}
	}
	within(t, 60*time.Second, "the lockers", wg.Wait)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a transaction failed (a deadlock is a failure here): %v", err)
		}
	}
}

// transactionWaiterCancellation: a waiter for a held transaction lock gives up with its context.
func transactionWaiterCancellation(t *testing.T, h Harness) {
	holder, err := h.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback() }()
	if err := h.Set.Locker.LockTx(context.Background(), holder, "contract:waiter"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	waiter, err := h.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = waiter.Rollback() }()
	started := time.Now()
	var waitErr error
	within(t, 30*time.Second, "the cancelled waiter", func() { waitErr = h.Set.Locker.LockTx(ctx, waiter, "contract:waiter") })
	if waitErr == nil {
		t.Fatal("a waiter got a lock that is held")
	}
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("the waiter took %s to give up", elapsed)
	}
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	next, err := h.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Rollback() }()
	within(t, 30*time.Second, "the lock after the holder committed", func() { err = h.Set.Locker.LockTx(context.Background(), next, "contract:waiter") })
	if err != nil {
		t.Fatalf("the lock was not released at commit: %v", err)
	}
}

func openConn(t *testing.T, h Harness) *sql.Conn {
	t.Helper()
	conn, err := h.DB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// sessionLocks: try-lock semantics, release, and an unlock of a lock that is not held.
func sessionLocks(t *testing.T, h Harness) {
	ctx := context.Background()
	first, second := openConn(t, h), openConn(t, h)
	acquired, err := h.Set.Locker.LockSession(ctx, first, "contract:session", 0)
	if err != nil || !acquired {
		t.Fatalf("first LockSession = %t, %v", acquired, err)
	}
	if acquired, err = h.Set.Locker.LockSession(ctx, second, "contract:session", 0); err != nil || acquired {
		t.Fatalf("a held session lock was acquired: %t, %v", acquired, err)
	}
	if acquired, err = h.Set.Locker.LockSession(ctx, second, "contract:session", 100*time.Millisecond); err != nil || acquired {
		t.Fatalf("a bounded wait for a held lock acquired it: %t, %v", acquired, err)
	}
	if err := h.Set.Locker.UnlockSession(ctx, first, "contract:session"); err != nil {
		t.Fatal(err)
	}
	if acquired, err = h.Set.Locker.LockSession(ctx, second, "contract:session", 0); err != nil || !acquired {
		t.Fatalf("a released session lock was not acquired: %t, %v", acquired, err)
	}
	if err := h.Set.Locker.UnlockSession(ctx, first, "contract:session"); err == nil {
		t.Fatal("unlocking a lock another session holds succeeded")
	}
	if err := h.Set.Locker.UnlockSession(ctx, second, "contract:session"); err != nil {
		t.Fatal(err)
	}
	if err := h.Set.Locker.UnlockSession(ctx, second, "contract:session"); err == nil {
		t.Fatal("unlocking twice succeeded")
	}
}

// sessionWaiterCancellation: a waiter without a deadline of its own ends with its context.
func sessionWaiterCancellation(t *testing.T, h Harness) {
	holder := openConn(t, h)
	if acquired, err := h.Set.Locker.LockSession(context.Background(), holder, "contract:cancel", 0); err != nil || !acquired {
		t.Fatalf("LockSession = %t, %v", acquired, err)
	}
	waiter, err := h.DB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = waiter.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var acquired bool
	within(t, 30*time.Second, "the cancelled session waiter", func() {
		acquired, err = h.Set.Locker.LockSession(ctx, waiter, "contract:cancel", -1)
	})
	if err == nil || acquired {
		t.Fatalf("a cancelled waiter returned %t, %v", acquired, err)
	}
	if err := h.Set.Locker.UnlockSession(context.Background(), holder, "contract:cancel"); err != nil {
		t.Fatal(err)
	}
	// The cancelled request may still be queued on the server for a moment: it must not leave the lock held
	// once its connection is gone, so close the waiter and give the next taker a few seconds.
	_ = waiter.Raw(func(any) error { return driver.ErrBadConn })
	_ = waiter.Close()
	third := openConn(t, h)
	if acquired, err = h.Set.Locker.LockSession(context.Background(), third, "contract:cancel", 10*time.Second); err != nil || !acquired {
		t.Fatalf("the lock was not free after the waiter was cancelled: %t, %v", acquired, err)
	}
}

// releasedOnConnectionKill: a session lock does not outlive its connection, so a crashed process
// cannot keep a lease.
func releasedOnConnectionKill(t *testing.T, h Harness) {
	ctx := context.Background()
	holder := openConn(t, h)
	if acquired, err := h.Set.Locker.LockSession(ctx, holder, "contract:kill", 0); err != nil || !acquired {
		t.Fatalf("LockSession = %t, %v", acquired, err)
	}
	id, err := h.ConnectionID(ctx, holder)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Terminate(ctx, h.DB, id); err != nil {
		t.Fatal(err)
	}
	// The killed connection must not go back to the pool as if it were usable.
	_ = holder.Raw(func(any) error { return driver.ErrBadConn })
	_ = holder.Close()
	waiter := openConn(t, h)
	var acquired bool
	within(t, 60*time.Second, "the lock after the kill", func() {
		acquired, err = h.Set.Locker.LockSession(ctx, waiter, "contract:kill", 30*time.Second)
	})
	if err != nil || !acquired {
		t.Fatalf("the lock was not released when its connection was killed: %t, %v", acquired, err)
	}
}

func claimIDs(t *testing.T, h Harness, tx *sql.Tx, query string, limit int, args ...any) []int64 {
	t.Helper()
	rows, err := h.Set.Claimer.ClaimRows(context.Background(), tx, h.Rebind(query), limit, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

const pendingQuery = "SELECT id FROM dialect_contract_queue WHERE state = 'pending' ORDER BY id"

func equal(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// claimAndRequeue: concurrent claimers see disjoint rows and a rolled-back claim requeues its rows.
func claimAndRequeue(t *testing.T, h Harness) {
	h.reset(t)
	for id := 1; id <= 10; id++ {
		h.exec(t, "INSERT INTO dialect_contract_queue (id, state) VALUES (?, 'pending')", id)
	}
	begin := func() *sql.Tx {
		tx, err := h.DB.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback() })
		return tx
	}
	first, second, third := begin(), begin(), begin()
	if got := claimIDs(t, h, first, pendingQuery, 4); !equal(got, []int64{1, 2, 3, 4}) {
		t.Fatalf("first claim = %v", got)
	}
	if got := claimIDs(t, h, second, pendingQuery, 4); !equal(got, []int64{5, 6, 7, 8}) {
		t.Fatalf("second claim = %v; it must skip the rows the first holds", got)
	}
	if got := claimIDs(t, h, third, pendingQuery, 4); !equal(got, []int64{9, 10}) {
		t.Fatalf("third claim = %v; it must return what is left", got)
	}
	if err := first.Rollback(); err != nil {
		t.Fatal(err)
	}
	requeued := begin()
	if got := claimIDs(t, h, requeued, pendingQuery, 10); !equal(got, []int64{1, 2, 3, 4}) {
		t.Fatalf("a rolled-back claim was not requeued: %v", got)
	}
}

// leaseExpiry: a claimed row is claimable again once its lease has expired, and not before.
func leaseExpiry(t *testing.T, h Harness) {
	h.reset(t)
	now := time.Now().UTC()
	h.exec(t, "INSERT INTO dialect_contract_queue (id, state, lease_until) VALUES (20, 'claimed', ?)", now.Add(-time.Minute))
	h.exec(t, "INSERT INTO dialect_contract_queue (id, state, lease_until) VALUES (21, 'claimed', ?)", now.Add(time.Hour))
	h.exec(t, "INSERT INTO dialect_contract_queue (id, state) VALUES (22, 'pending')")
	tx, err := h.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	query := "SELECT id FROM dialect_contract_queue WHERE state = 'pending' OR (state = 'claimed' AND lease_until < ?) ORDER BY id"
	if got := claimIDs(t, h, tx, query, 10, now); !equal(got, []int64{20, 22}) {
		t.Fatalf("claim = %v; want the pending row and the expired lease, not the live one", got)
	}
}

// claimRejectsUnsafeQueries: the claim builder owns LIMIT and locking, so it refuses a query that has its own.
func claimRejectsUnsafeQueries(t *testing.T, h Harness) {
	tx, err := h.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, query := range []string{
		"SELECT id FROM dialect_contract_queue LIMIT 1",
		"SELECT id FROM dialect_contract_queue FOR UPDATE",
		"SELECT DISTINCT id FROM dialect_contract_queue",
		"SELECT id FROM dialect_contract_queue GROUP BY id",
		"SELECT id FROM dialect_contract_queue UNION SELECT id FROM dialect_contract_queue",
		"SELECT id FROM dialect_contract_queue; DROP TABLE dialect_contract_queue",
	} {
		if rows, err := h.Set.Claimer.ClaimRows(context.Background(), tx, query, 1); err == nil {
			_ = rows.Close()
			t.Errorf("%q was accepted", query)
		}
	}
	if rows, err := h.Set.Claimer.ClaimRows(context.Background(), tx, pendingQuery, 0); err == nil {
		_ = rows.Close()
		t.Error("a zero limit was accepted")
	}
}

// notificationLatency: a worker learns about an enqueue within the engine's latency bound.
func notificationLatency(t *testing.T, h Harness) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	subscription, err := h.Set.Notifier.Subscribe(ctx, h.DB, "contract_wake")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subscription.Close() }()
	notified := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		notified <- h.Notify(context.Background(), "contract_wake")
	}()
	started := time.Now()
	channel, err := subscription.Wait(ctx)
	if err != nil || channel != "contract_wake" {
		t.Fatalf("Wait = %q, %v", channel, err)
	}
	if err := <-notified; err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > h.LatencyBound {
		t.Fatalf("the notification took %s; the bound is %s", elapsed, h.LatencyBound)
	}
	// A context that ends ends the wait, whatever the engine does between notifications.
	short, stop := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stop()
	within(t, 30*time.Second, "Wait with an ending context", func() { _, _ = subscription.Wait(short) })
}

func classify(h Harness, err error) error {
	if err == nil {
		return nil
	}
	return h.Set.Classifier.Classify(err)
}

// errorClassification: the engine's own errors map to the shared sentinels.
func errorClassification(t *testing.T, h Harness) {
	h.reset(t)
	ctx := context.Background()
	h.exec(t, "INSERT INTO dialect_contract_counter (id, n) VALUES (1, 0)")
	_, err := h.DB.ExecContext(ctx, h.Rebind("INSERT INTO dialect_contract_counter (id, n) VALUES (1, 0)"))
	if !errors.Is(classify(h, err), dialect.ErrUniqueViolation) || h.Set.Classifier.Retryable(err) {
		t.Errorf("a duplicate key = %v: classified %v", err, classify(h, err))
	}
	_, err = h.DB.ExecContext(ctx, h.Rebind("INSERT INTO dialect_contract_child (id, parent_id) VALUES (1, 99)"))
	if !errors.Is(classify(h, err), dialect.ErrForeignKeyViolation) || h.Set.Classifier.Retryable(err) {
		t.Errorf("a missing parent = %v: classified %v", err, classify(h, err))
	}
	if classify(h, errors.New("not a database error")) != nil || h.Set.Classifier.Retryable(errors.New("x")) {
		t.Error("a plain error was classified")
	}

	// A lock wait timeout: the first connection holds the row, the second gives up.
	holder, waiter := openConn(t, h), openConn(t, h)
	holderTx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holderTx.Rollback() }()
	if _, err := holderTx.ExecContext(ctx, h.Rebind("UPDATE dialect_contract_counter SET n = 1 WHERE id = 1")); err != nil {
		t.Fatal(err)
	}
	if err := h.LockTimeout(ctx, waiter, true); err != nil {
		t.Fatal(err)
	}
	waiterTx, err := waiter.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = waiterTx.ExecContext(ctx, h.Rebind("UPDATE dialect_contract_counter SET n = 2 WHERE id = 1"))
	_ = waiterTx.Rollback()
	_ = h.LockTimeout(ctx, waiter, false)
	if !errors.Is(classify(h, err), dialect.ErrLockTimeout) || h.Set.Classifier.Retryable(err) != h.LockTimeoutRetryable {
		t.Errorf("a lock wait timeout = %v: classified %v, retryable %t", err, classify(h, err), h.Set.Classifier.Retryable(err))
	}
}

// crossingUpdates runs two transactions that update the rows 1 and 2 in opposite order, so exactly one of
// them is chosen as the deadlock victim; it returns their errors.
func crossingUpdates(t *testing.T, h Harness, a, b func(tx *sql.Tx) error) (errA, errB error) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errA = a(nil) }()
	go func() { defer wg.Done(); errB = b(nil) }()
	within(t, 120*time.Second, "the crossing transactions", wg.Wait)
	return errA, errB
}

func deadlockIsRetryable(t *testing.T, h Harness) {
	h.reset(t)
	h.exec(t, "INSERT INTO dialect_contract_counter (id, n) VALUES (1, 0), (2, 0)")
	ctx := context.Background()
	var ready sync.WaitGroup
	ready.Add(2)
	crossing := func(first, second int) func(*sql.Tx) error {
		return func(*sql.Tx) error {
			tx, err := h.DB.BeginTx(ctx, nil)
			if err != nil {
				ready.Done()
				return err
			}
			defer func() { _ = tx.Rollback() }()
			_, err = tx.ExecContext(ctx, h.Rebind("UPDATE dialect_contract_counter SET n = n + 1 WHERE id = ?"), first)
			ready.Done()
			ready.Wait()
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, h.Rebind("UPDATE dialect_contract_counter SET n = n + 1 WHERE id = ?"), second); err != nil {
				return err
			}
			return tx.Commit()
		}
	}
	errA, errB := crossingUpdates(t, h, crossing(1, 2), crossing(2, 1))
	victims := 0
	for _, err := range []error{errA, errB} {
		if err == nil {
			continue
		}
		if !errors.Is(classify(h, err), dialect.ErrDeadlock) || !h.Set.Classifier.Retryable(err) {
			t.Fatalf("the deadlock victim error %v was classified %v, retryable %t", err, classify(h, err), h.Set.Classifier.Retryable(err))
		}
		victims++
	}
	if victims != 1 {
		t.Fatalf("%d deadlock victims; want exactly one (errors %v, %v)", victims, errA, errB)
	}
}

// runTxRetriesADeadlock: RunTx re-runs the victim, so both transactions end up committed.
func runTxRetriesADeadlock(t *testing.T, h Harness) {
	h.reset(t)
	h.exec(t, "INSERT INTO dialect_contract_counter (id, n) VALUES (1, 0), (2, 0)")
	var arrived atomic.Int32
	barrier := make(chan struct{})
	crossing := func(first, second int) func(*sql.Tx) error {
		return func(tx *sql.Tx) error {
			if _, err := tx.Exec(h.Rebind("UPDATE dialect_contract_counter SET n = n + 1 WHERE id = ?"), first); err != nil {
				return err
			}
			if arrived.Add(1) == 2 {
				close(barrier) // the first attempts of both transactions hold their first row
			}
			select {
			case <-barrier:
			case <-time.After(10 * time.Second):
			}
			_, err := tx.Exec(h.Rebind("UPDATE dialect_contract_counter SET n = n + 1 WHERE id = ?"), second)
			return err
		}
	}
	run := func(first, second int) func(*sql.Tx) error {
		return func(*sql.Tx) error {
			return dialect.RunTx(context.Background(), h.DB, h.Set.Classifier, nil, crossing(first, second))
		}
	}
	errA, errB := crossingUpdates(t, h, run(1, 2), run(2, 1))
	if errA != nil || errB != nil {
		t.Fatalf("RunTx did not recover the deadlock: %v, %v", errA, errB)
	}
	var total int
	if err := h.DB.QueryRow("SELECT SUM(n) FROM dialect_contract_counter").Scan(&total); err != nil || total != 4 {
		t.Fatalf("total = %d, %v; want 4 (each transaction applies two updates exactly once)", total, err)
	}
}

// PostgresRebind turns ? placeholders into $1, $2, ...
func PostgresRebind(query string) string {
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
