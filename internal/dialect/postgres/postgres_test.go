package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mlops-ca/notarx/internal/dialect"
)

// recorder is a minimal database/sql driver that records statements and
// answers every query with one boolean column from the scripted values.
type recorder struct {
	mu      sync.Mutex
	queries []string
	args    [][]driver.NamedValue
	answers []bool
	failOn  string
}

func (r *recorder) Open(string) (driver.Conn, error) { return &recorderConn{r: r}, nil }

type recorderConn struct{ r *recorder }

func (c *recorderConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c *recorderConn) Close() error                        { return nil }
func (c *recorderConn) Begin() (driver.Tx, error)           { return c, nil }
func (c *recorderConn) Commit() error                       { return nil }
func (c *recorderConn) Rollback() error                     { return nil }

func (c *recorderConn) record(query string, args []driver.NamedValue) error {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	c.r.queries = append(c.r.queries, query)
	c.r.args = append(c.r.args, args)
	if c.r.failOn != "" && strings.Contains(query, c.r.failOn) {
		return errors.New("scripted failure")
	}
	return nil
}

func (c *recorderConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), c.record(query, args)
}

func (c *recorderConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.record(query, args); err != nil {
		return nil, err
	}
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	answer := false
	if len(c.r.answers) > 0 {
		answer, c.r.answers = c.r.answers[0], c.r.answers[1:]
	}
	return &boolRows{value: answer}, nil
}

type boolRows struct {
	value bool
	done  bool
}

func (r *boolRows) Columns() []string { return []string{"v"} }
func (r *boolRows) Close() error      { return nil }
func (r *boolRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.value
	return nil
}

var driverCount int
var driverMu sync.Mutex

func open(t *testing.T, r *recorder) *sql.DB {
	t.Helper()
	driverMu.Lock()
	driverCount++
	name := "recorder-" + strings.Repeat("x", driverCount)
	driverMu.Unlock()
	sql.Register(name, r)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestLockTxSortsAndDeduplicatesKeys(t *testing.T) {
	r := &recorder{}
	db := open(t, r)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := (Locker{}).LockTx(context.Background(), tx, "b", "a", "b", "c"); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, args := range r.args {
		got = append(got, args[0].Value.(string))
	}
	if strings.Join(got, ",") != "a,b,c" {
		t.Fatalf("lock order = %v", got)
	}
	if !strings.Contains(r.queries[0], "pg_advisory_xact_lock(hashtextextended($1, 0))") {
		t.Fatalf("query = %q", r.queries[0])
	}
	r.failOn = "pg_advisory_xact_lock"
	if err := (Locker{}).LockTx(context.Background(), tx, "x"); err == nil {
		t.Fatal("lock failure swallowed")
	}
	_ = tx.Rollback()
}

func TestLockSessionModes(t *testing.T) {
	ctx := context.Background()
	r := &recorder{answers: []bool{true}}
	conn, err := open(t, r).Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := (Locker{}).LockSession(ctx, conn, "k", 0)
	if err != nil || !ok || !strings.Contains(r.queries[0], "pg_try_advisory_lock") {
		t.Fatalf("try lock: %v %v %v", ok, err, r.queries)
	}

	ok, err = (Locker{}).LockSession(ctx, conn, "k", -1)
	if err != nil || !ok || !strings.Contains(r.queries[1], "pg_advisory_lock(") {
		t.Fatalf("blocking lock: %v %v %v", ok, err, r.queries)
	}

	// busy for two polls, then free
	r.answers = []bool{false, false, true}
	ok, err = (Locker{}).LockSession(ctx, conn, "k", time.Second)
	if err != nil || !ok {
		t.Fatalf("polling lock: %v %v", ok, err)
	}

	// never free: times out without an error
	r.mu.Lock()
	r.answers = nil
	r.mu.Unlock()
	ok, err = (Locker{}).LockSession(ctx, conn, "k", 60*time.Millisecond)
	if err != nil || ok {
		t.Fatalf("timeout: %v %v", ok, err)
	}

	// a cancelled waiter returns the context error
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (Locker{}).LockSession(cancelled, conn, "k", time.Second); err == nil {
		t.Fatal("cancelled waiter did not fail")
	}
	r.failOn = "pg_try_advisory_lock"
	if _, err := (Locker{}).LockSession(ctx, conn, "k", 0); err == nil {
		t.Fatal("query failure swallowed")
	}
	r.failOn = "pg_advisory_lock("
	if ok, err := (Locker{}).LockSession(ctx, conn, "k", -1); err == nil || ok {
		t.Fatal("blocking failure swallowed")
	}
}

func TestUnlockSession(t *testing.T) {
	ctx := context.Background()
	r := &recorder{answers: []bool{true, false}}
	conn, err := open(t, r).Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := (Locker{}).UnlockSession(ctx, conn, "k"); err != nil {
		t.Fatal(err)
	}
	if err := (Locker{}).UnlockSession(ctx, conn, "k"); err == nil {
		t.Fatal("unlock of a lock that was not held succeeded")
	}
	r.failOn = "pg_advisory_unlock"
	if err := (Locker{}).UnlockSession(ctx, conn, "k"); err == nil {
		t.Fatal("unlock failure swallowed")
	}
}

func TestClaimRowsAppendsSkipLockedAndLimit(t *testing.T) {
	ctx := context.Background()
	r := &recorder{}
	db := open(t, r)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := (Claimer{}).ClaimRows(ctx, tx, "SELECT id FROM jobs WHERE kind = $1 AND due < $2 ORDER BY due", 5, "email", 7)
	if err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	want := "SELECT id FROM jobs WHERE kind = $1 AND due < $2 ORDER BY due LIMIT $3 FOR UPDATE SKIP LOCKED"
	if r.queries[0] != want || len(r.args[0]) != 3 || r.args[0][2].Value != int64(5) {
		t.Fatalf("query %q args %v", r.queries[0], r.args[0])
	}
	for _, bad := range []string{"SELECT 1;", "SELECT id FROM t LIMIT 3", "SELECT id FROM t FOR UPDATE", "select distinct id from t", "SELECT id FROM t GROUP BY id", "SELECT 1 UNION SELECT 2", "SELECT 1 -- x"} {
		if _, err := (Claimer{}).ClaimRows(ctx, tx, bad, 1); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := (Claimer{}).ClaimRows(ctx, tx, "SELECT 1", 0); err == nil {
		t.Fatal("non-positive limit accepted")
	}
	_ = tx.Rollback()
}

func TestClassifier(t *testing.T) {
	c := Classifier{}
	cases := map[string]struct {
		want      error
		retryable bool
	}{
		"23505": {dialect.ErrUniqueViolation, false},
		"23503": {dialect.ErrForeignKeyViolation, false},
		"40P01": {dialect.ErrDeadlock, true},
		"40001": {dialect.ErrSerializationFailure, true},
		"55P03": {dialect.ErrLockTimeout, false},
		"42P01": {nil, false},
	}
	for code, tc := range cases {
		wrapped := errors.Join(errors.New("context"), &pgconn.PgError{Code: code})
		if got := c.Classify(wrapped); got != tc.want {
			t.Errorf("%s: classified %v, want %v", code, got, tc.want)
		}
		if c.Retryable(wrapped) != tc.retryable {
			t.Errorf("%s: retryable = %v", code, !tc.retryable)
		}
	}
	if c.Classify(errors.New("plain")) != nil || c.Classify(nil) != nil || c.Retryable(nil) {
		t.Fatal("non-database errors must not be classified")
	}
}

func TestNewBundlesEveryPrimitive(t *testing.T) {
	set := New()
	if set.Name != "postgres" || set.Locker == nil || set.Claimer == nil || set.Notifier == nil || set.Classifier == nil {
		t.Fatalf("incomplete dialect set: %#v", set)
	}
}

func TestSubscribeClosesTheConnectionWhenListenFails(t *testing.T) {
	ctx := context.Background()
	r := &recorder{failOn: "LISTEN"}
	if _, err := (Notifier{}).Subscribe(ctx, open(t, r), "queue"); err == nil {
		t.Fatal("LISTEN failure swallowed")
	}
	r2 := &recorder{}
	db := open(t, r2)
	if _, err := (Notifier{}).Subscribe(ctx, db, ""); err == nil {
		t.Fatal("empty channel accepted")
	}
	sub, err := (Notifier{}).Subscribe(ctx, db, "queue")
	if err != nil {
		t.Fatal(err)
	}
	if r2.queries[len(r2.queries)-1] != `LISTEN "queue"` {
		t.Fatalf("queries = %v", r2.queries)
	}
	if _, err := sub.Wait(ctx); err == nil {
		t.Fatal("the recorder driver exposes no pgx connection, so Wait must fail")
	}
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
	if r2.queries[len(r2.queries)-1] != "UNLISTEN *" {
		t.Fatalf("Close did not UNLISTEN: %v", r2.queries)
	}

	failing := &recorder{}
	db3 := open(t, failing)
	sub, err = (Notifier{}).Subscribe(ctx, db3, "queue")
	if err != nil {
		t.Fatal(err)
	}
	failing.mu.Lock()
	failing.failOn = "UNLISTEN"
	failing.mu.Unlock()
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
	if got := db3.Stats().OpenConnections; got != 0 {
		t.Fatalf("a connection whose UNLISTEN failed went back to the pool (%d open)", got)
	}
}
