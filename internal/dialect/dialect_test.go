package dialect

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/testsupport/fakesql"
)

func TestValidateClaimSelect(t *testing.T) {
	if err := ValidateClaimSelect("SELECT id FROM q WHERE state = ? ORDER BY id", 3); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"SELECT id FROM q LIMIT 1", "select id from q limit 1", "SELECT id FROM q OFFSET 2", "SELECT id FROM q FETCH FIRST 1 ROWS ONLY",
		"SELECT DISTINCT id FROM q", "SELECT id FROM q GROUP  BY id", "SELECT id FROM q UNION SELECT id FROM r",
		"SELECT id FROM q INTERSECT SELECT id FROM r", "SELECT id FROM q EXCEPT SELECT id FROM r",
		"SELECT id FROM q FOR UPDATE", "SELECT id FROM q FOR SHARE", "SELECT id FROM q FOR NO KEY UPDATE", "SELECT id FROM q FOR KEY SHARE",
		"SELECT id FROM q; DROP TABLE q", "SELECT id FROM q -- x", "SELECT id /* x */ FROM q",
	} {
		if err := ValidateClaimSelect(bad, 1); err == nil || !strings.Contains(err.Error(), "plain SELECT") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	for _, limit := range []int{0, -1} {
		if err := ValidateClaimSelect("SELECT id FROM q", limit); err == nil || !strings.Contains(err.Error(), "limit must be positive") {
			t.Errorf("limit %d: err = %v", limit, err)
		}
	}
}

// retryable is a classifier that retries the errors wrapping errRetry.
var errRetry = errors.New("retry me")

type classifier struct{}

func (classifier) Classify(err error) error {
	if errors.Is(err, errRetry) {
		return ErrDeadlock
	}
	return nil
}

func (c classifier) Retryable(err error) bool { return c.Classify(err) == ErrDeadlock }

func txServer() *fakesql.Server {
	return &fakesql.Server{Handler: func(string, []driver.NamedValue) (fakesql.Result, error) { return fakesql.None(), nil }}
}

func TestRunTxCommitsOnSuccess(t *testing.T) {
	s := txServer()
	calls := 0
	err := RunTx(context.Background(), s.Open(t), classifier{}, nil, func(tx *sql.Tx) error {
		calls++
		_, err := tx.Exec("UPDATE t SET x = 1")
		return err
	})
	if err != nil || calls != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}
	if log := strings.Join(s.Log, ";"); !strings.Contains(log, "BEGIN") || !strings.Contains(log, "COMMIT") || strings.Contains(log, "ROLLBACK") {
		t.Fatalf("log = %s", log)
	}
}

func TestRunTxRetriesARetryableFailureWithAnExplicitRollback(t *testing.T) {
	s := txServer()
	var calls atomic.Int32
	err := RunTx(context.Background(), s.Open(t), classifier{}, nil, func(*sql.Tx) error {
		if calls.Add(1) < 3 {
			return errRetry
		}
		return nil
	})
	if err != nil || calls.Load() != 3 {
		t.Fatalf("err = %v, calls = %d", err, calls.Load())
	}
	if rollbacks := len(s.Logged("ROLLBACK")); rollbacks != 2 {
		t.Fatalf("%d rollbacks; a failed attempt must be rolled back before the retry", rollbacks)
	}
}

func TestRunTxGivesUpAfterTheRetries(t *testing.T) {
	s := txServer()
	var calls atomic.Int32
	err := RunTx(context.Background(), s.Open(t), classifier{}, nil, func(*sql.Tx) error {
		calls.Add(1)
		return errRetry
	})
	if !errors.Is(err, errRetry) || int(calls.Load()) != MaxTxRetries+1 {
		t.Fatalf("err = %v after %d attempts; want %d", err, calls.Load(), MaxTxRetries+1)
	}
}

func TestRunTxRollsBackWhenTheFunctionPanics(t *testing.T) {
	s := txServer()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic was swallowed")
			}
		}()
		_ = RunTx(context.Background(), s.Open(t), classifier{}, nil, func(*sql.Tx) error { panic("boom") })
	}()
	if len(s.Logged("ROLLBACK")) != 1 || len(s.Logged("COMMIT")) != 0 {
		t.Fatalf("a panic must roll the transaction back: %v", s.Log)
	}
}

func TestRunTxDoesNotRetryOtherErrors(t *testing.T) {
	plain := errors.New("constraint")
	var calls int
	err := RunTx(context.Background(), txServer().Open(t), classifier{}, nil, func(*sql.Tx) error { calls++; return plain })
	if !errors.Is(err, plain) || calls != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}
}

func TestRunTxStopsRetryingWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	var calls atomic.Int32
	err := RunTx(ctx, txServer().Open(t), classifier{}, nil, func(*sql.Tx) error {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return errRetry
	})
	if !errors.Is(err, errRetry) || !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls.Load())
	}
}

func TestRunTxReportsBeginAndCommitFailures(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunTx(cancelled, txServer().Open(t), classifier{}, nil, func(*sql.Tx) error { return nil }); err == nil {
		t.Fatal("a cancelled context began a transaction")
	}
	// A commit that fails with a retryable error is retried like any other failure.
	db := sql.OpenDB(commitFailer{failures: 1})
	t.Cleanup(func() { _ = db.Close() })
	calls := 0
	if err := RunTx(context.Background(), db, classifier{}, nil, func(*sql.Tx) error { calls++; return nil }); err != nil || calls != 2 {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}
}

type commitFailer struct{ failures int }

func (c commitFailer) Connect(context.Context) (driver.Conn, error) {
	return &failingConn{left: &c.failures}, nil
}
func (commitFailer) Driver() driver.Driver { return nil }

type failingConn struct{ left *int }

func (*failingConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (*failingConn) Close() error                        { return nil }
func (c *failingConn) Begin() (driver.Tx, error)         { return c, nil }
func (c *failingConn) Commit() error {
	if *c.left > 0 {
		*c.left--
		return errRetry
	}
	return nil
}
func (*failingConn) Rollback() error { return nil }
