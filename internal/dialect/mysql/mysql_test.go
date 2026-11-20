package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	driverpkg "github.com/go-sql-driver/mysql"
	"github.com/mlops-ca/notarx/internal/dialect"
	"github.com/mlops-ca/notarx/internal/testsupport/fakesql"
)

func server(handler func(query string, args []driver.NamedValue) (fakesql.Result, error)) *fakesql.Server {
	return &fakesql.Server{Handler: handler}
}

func TestNewBundlesEveryPrimitive(t *testing.T) {
	set := New(0)
	if set.Name != "mysql" || set.Locker == nil || set.Claimer == nil || set.Notifier == nil || set.Classifier == nil {
		t.Fatalf("incomplete set: %+v", set)
	}
}

func TestBucketIsStableAndWithinTheTable(t *testing.T) {
	first := bucket("a")
	if again := bucket("a"); first != again {
		t.Fatal("the bucket of a key changes")
	}
	seen := map[uint16]bool{}
	for i := range 10_000 {
		b := bucket(fmt.Sprintf("key-%d", i))
		if b >= Buckets {
			t.Fatalf("bucket %d is outside the %d-row table", b, Buckets)
		}
		seen[b] = true
	}
	// 10k keys in 4096 buckets leave almost no bucket empty: the hash spreads.
	if len(seen) < 3500 {
		t.Fatalf("only %d of %d buckets are used", len(seen), Buckets)
	}
}

// TestBucketCollisionRates records what ADR 0022 asks slice 4 to measure on the hash side: how many of k concurrent distinct
// keys share a bucket with another. With 4096 buckets the expected number of shared keys is about k*(k-1)/2/4096 pairs.
func TestBucketCollisionRates(t *testing.T) {
	for _, k := range []int{32, 64, 128, 256, 512} {
		owners := map[uint16]int{}
		for i := range k {
			owners[bucket(fmt.Sprintf("hosted:repo-%d", i))]++
		}
		shared := k - len(owners)
		expected := float64(k) * float64(k-1) / 2 / Buckets
		t.Logf("%d distinct keys: %d share a bucket with another (about %.1f expected)", k, shared, expected)
		// Far from the expectation means the hash does not spread.
		if float64(shared) > 4*expected+4 {
			t.Errorf("%d keys: %d collisions; about %.1f expected", k, shared, expected)
		}
	}
}

func TestBucketsAreSortedAndDeduplicated(t *testing.T) {
	keys := []string{"b", "a", "b", "c", "a"}
	got := buckets(keys)
	if !slices.IsSorted(got) || len(got) > 3 {
		t.Fatalf("buckets = %v", got)
	}
	reversed := buckets([]string{"c", "a", "b"})
	if !slices.Equal(got, reversed) {
		t.Fatalf("the order of the keys changed the buckets: %v %v", got, reversed)
	}
	if len(buckets(nil)) != 0 {
		t.Fatal("no keys must lock nothing")
	}
}

func TestLockTxLocksEachBucketOnceInOrder(t *testing.T) {
	s := server(func(query string, args []driver.NamedValue) (fakesql.Result, error) {
		return fakesql.One("bucket", args[0].Value), nil
	})
	db := s.Open(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := (Locker{}).LockTx(context.Background(), tx, "z", "a", "z"); err != nil {
		t.Fatal(err)
	}
	logged := s.Logged("FROM notarx_lock_buckets WHERE bucket = ? FOR UPDATE")
	want := buckets([]string{"z", "a"})
	if len(logged) != len(want) {
		t.Fatalf("locks = %v; want %d", logged, len(want))
	}
	for i, b := range want {
		if !strings.HasSuffix(logged[i], fmt.Sprintf("| %d", b)) {
			t.Errorf("lock %d = %q; want bucket %d", i, logged[i], b)
		}
	}
}

func TestLockTxErrors(t *testing.T) {
	boom := errors.New("boom")
	for name, tc := range map[string]struct {
		handler func(string, []driver.NamedValue) (fakesql.Result, error)
		want    string
	}{
		"unseeded table": {func(string, []driver.NamedValue) (fakesql.Result, error) { return fakesql.None(), nil }, "not seeded"},
		"query error":    {func(string, []driver.NamedValue) (fakesql.Result, error) { return fakesql.Result{}, boom }, "boom"},
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := server(tc.handler).Open(t).Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err := (Locker{}).LockTx(context.Background(), tx, "k"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestNotifierIsAWakerThroughTheDialectSet(t *testing.T) {
	set := New(time.Hour)
	waker, ok := set.Notifier.(dialect.Waker)
	if !ok {
		t.Fatal("the notifier of the dialect set cannot wake in-process subscribers")
	}
	subscription, err := set.Notifier.Subscribe(context.Background(), nil, "jobs")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subscription.Close() }()
	if _, ok := subscription.(dialect.Resetter); !ok {
		t.Fatal("the subscription cannot be reset after work")
	}
	waker.Notify("jobs")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if channel, err := subscription.Wait(ctx); err != nil || channel != "jobs" {
		t.Fatalf("Wait = %q, %v", channel, err)
	}
}

func TestLockTimeout(t *testing.T) {
	for in, want := range map[time.Duration]int64{-1: -1, 0: 0, time.Nanosecond: 1, time.Second: 1, time.Second + 1: 2, 90 * time.Second: 90} {
		if got := lockTimeout(in); got != want {
			t.Errorf("lockTimeout(%s) = %d; want %d", in, got, want)
		}
	}
}

func sessionServer(getLock any, release any) *fakesql.Server {
	return server(func(query string, args []driver.NamedValue) (fakesql.Result, error) {
		switch {
		case strings.Contains(query, "GET_LOCK"):
			return fakesql.One("l", getLock), nil
		case strings.Contains(query, "RELEASE_LOCK"):
			return fakesql.One("l", release), nil
		case strings.Contains(query, "RELEASE_ALL_LOCKS"):
			return fakesql.One("l", int64(2)), nil
		}
		return fakesql.None(), nil
	})
}

func TestLockSessionResults(t *testing.T) {
	for name, tc := range map[string]struct {
		answer  any
		want    bool
		wantErr bool
	}{"acquired": {int64(1), true, false}, "timed out": {int64(0), false, false}, "error": {nil, false, true}} {
		t.Run(name, func(t *testing.T) {
			s := sessionServer(tc.answer, int64(1))
			conn, err := s.Open(t).Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			got, err := (Locker{}).LockSession(context.Background(), conn, "k", 3*time.Second)
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("LockSession = %t, %v", got, err)
			}
			logged := s.Logged("GET_LOCK")
			if len(logged) != 1 || !strings.HasSuffix(logged[0], "| k,3") {
				t.Fatalf("GET_LOCK call = %v; want the key and the timeout in seconds", logged)
			}
			// One statement, computed by the server: the name hashes the database, a NUL and the key.
			for _, part := range []string{"CONCAT('nx:', LEFT(SHA2(CONCAT(DATABASE(), CHAR(0), ?), 256), 40))"} {
				if !strings.Contains(logged[0], part) {
					t.Fatalf("GET_LOCK call %q lacks %q", logged[0], part)
				}
			}
		})
	}
}

func TestSessionLockQueryErrors(t *testing.T) {
	boom := errors.New("boom")
	failing := server(func(string, []driver.NamedValue) (fakesql.Result, error) { return fakesql.Result{}, boom })
	conn, err := failing.Open(t).Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Locker{}).LockSession(context.Background(), conn, "k", 0); !errors.Is(err, boom) {
		t.Fatalf("GET_LOCK error = %v", err)
	}
	if err := (Locker{}).UnlockSession(context.Background(), conn, "k"); !errors.Is(err, boom) {
		t.Fatalf("RELEASE_LOCK error = %v", err)
	}
	if err := (Locker{}).ReleaseAll(context.Background(), conn); !errors.Is(err, boom) {
		t.Fatalf("RELEASE_ALL_LOCKS error = %v", err)
	}
}

func TestUnlockSession(t *testing.T) {
	for name, tc := range map[string]struct {
		answer  any
		wantErr bool
	}{"released": {int64(1), false}, "not the owner": {int64(0), true}, "no such lock": {nil, true}} {
		t.Run(name, func(t *testing.T) {
			conn, err := sessionServer(int64(1), tc.answer).Open(t).Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := (Locker{}).UnlockSession(context.Background(), conn, "k"); (err != nil) != tc.wantErr {
				t.Fatalf("UnlockSession = %v", err)
			}
		})
	}
}

func TestReleaseAll(t *testing.T) {
	s := sessionServer(int64(1), int64(1))
	conn, err := s.Open(t).Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := (Locker{}).ReleaseAll(context.Background(), conn); err != nil || len(s.Logged("RELEASE_ALL_LOCKS()")) != 1 {
		t.Fatalf("ReleaseAll = %v, %v", err, s.Log)
	}
}

func TestClaimRowsAppendsLimitAndSkipLocked(t *testing.T) {
	s := server(func(string, []driver.NamedValue) (fakesql.Result, error) { return fakesql.One("id", int64(7)), nil })
	tx, err := s.Open(t).Begin()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := (Claimer{}).ClaimRows(context.Background(), tx, "SELECT id FROM q WHERE state = ? ORDER BY id", 5, "pending")
	if err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if got := s.Logged("LIMIT ? FOR UPDATE SKIP LOCKED"); len(got) != 1 || !strings.HasSuffix(got[0], "| pending,5") {
		t.Fatalf("claim = %v", got)
	}
	for _, bad := range []string{"SELECT id FROM q LIMIT 1", "SELECT id FROM q FOR UPDATE", "SELECT 1; SELECT 2"} {
		if _, err := (Claimer{}).ClaimRows(context.Background(), tx, bad, 1); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := (Claimer{}).ClaimRows(context.Background(), tx, "SELECT id FROM q", 0); err == nil {
		t.Error("a zero limit was accepted")
	}
}

func TestClassifier(t *testing.T) {
	c := Classifier{}
	for number, want := range map[uint16]error{
		1062: dialect.ErrUniqueViolation, 1451: dialect.ErrForeignKeyViolation, 1452: dialect.ErrForeignKeyViolation,
		1216: dialect.ErrForeignKeyViolation, 1217: dialect.ErrForeignKeyViolation,
		1213: dialect.ErrDeadlock, 1205: dialect.ErrLockTimeout, 3572: dialect.ErrLockTimeout, 1146: nil,
	} {
		err := fmt.Errorf("wrapped: %w", &driverpkg.MySQLError{Number: number, Message: "x"})
		if got := c.Classify(err); got != want {
			t.Errorf("Classify(%d) = %v; want %v", number, got, want)
		}
		wantRetry := number == 1213 || number == 1205 || number == 3572
		if c.Retryable(err) != wantRetry {
			t.Errorf("Retryable(%d) = %t", number, !wantRetry)
		}
	}
	if c.Classify(errors.New("plain")) != nil || c.Retryable(errors.New("plain")) || c.Retryable(sql.ErrNoRows) {
		t.Error("a non-MySQL error was classified")
	}
}
