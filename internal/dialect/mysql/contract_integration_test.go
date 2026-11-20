//go:build integration

package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/dialect"
	"github.com/mlops-ca/notarx/internal/dialect/dialecttest"
	"github.com/mlops-ca/notarx/internal/testsupport"
)

// openMySQL opens the migrated test database, or skips when the run is for another engine.
func openMySQL(t *testing.T) *sql.DB {
	t.Helper()
	driver, url := testsupport.Database(t)
	if driver != testsupport.DriverMySQL {
		t.Skip("the MySQL dialect suites run with TEST_DATABASE_DRIVER=mysql")
	}
	db, err := database.OpenMySQL(url, database.PoolConfig{MaxOpenConns: 40, MaxIdleConns: 10, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestMySQLDialectContract runs the shared dialect contract (internal/dialect/dialecttest), the same
// suite internal/dialect/postgres runs against PostgreSQL.
func TestMySQLDialectContract(t *testing.T) {
	db := openMySQL(t)
	// The poll interval is far above the latency bound, so only the in-process wake-up (Notify) can meet it: a
	// Notify that does nothing fails the contract instead of being covered by the next poll tick.
	notifier := NewNotifier(Config{Interval: 10 * time.Second, MaxInterval: 10 * time.Second})
	dialecttest.Run(t, dialecttest.Harness{
		Set:    dialect.Set{Name: database.DriverMySQL, Locker: Locker{}, Claimer: Claimer{}, Notifier: notifier, Classifier: Classifier{}},
		DB:     db,
		Rebind: func(query string) string { return query },
		Schema: []string{
			"CREATE TABLE dialect_contract_counter (id INT NOT NULL PRIMARY KEY, n INT NOT NULL) ENGINE=InnoDB",
			"CREATE TABLE dialect_contract_queue (id BIGINT NOT NULL PRIMARY KEY, state VARCHAR(16) NOT NULL, lease_until DATETIME(6) NULL, KEY (state, id)) ENGINE=InnoDB",
			"CREATE TABLE dialect_contract_parent (id INT NOT NULL PRIMARY KEY) ENGINE=InnoDB",
			"CREATE TABLE dialect_contract_child (id INT NOT NULL PRIMARY KEY, parent_id INT NOT NULL, FOREIGN KEY (parent_id) REFERENCES dialect_contract_parent (id)) ENGINE=InnoDB",
		},
		ConnectionID: func(ctx context.Context, conn *sql.Conn) (int64, error) {
			var id int64
			err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id)
			return id, err
		},
		Terminate: func(ctx context.Context, db *sql.DB, id int64) error {
			_, err := db.ExecContext(ctx, fmt.Sprintf("KILL CONNECTION %d", id))
			return err
		},
		LockTimeout: func(ctx context.Context, conn *sql.Conn, on bool) error {
			seconds := database.MySQLLockWaitTimeoutSeconds
			if on {
				seconds = 1
			}
			_, err := conn.ExecContext(ctx, fmt.Sprintf("SET SESSION innodb_lock_wait_timeout = %d", seconds))
			return err
		},
		Notify:               func(_ context.Context, channel string) error { notifier.Notify(channel); return nil },
		LatencyBound:         time.Second,
		LockTimeoutRetryable: true,
	})
}

// TestBaselineSeedsTheLockBuckets proves the table LockTx locks is the one the baseline migration seeds.
func TestBaselineSeedsTheLockBuckets(t *testing.T) {
	db := openMySQL(t)
	var count, lowest, highest int
	if err := db.QueryRow("SELECT COUNT(*), MIN(bucket), MAX(bucket) FROM notarx_lock_buckets").Scan(&count, &lowest, &highest); err != nil {
		t.Fatal(err)
	}
	if count != Buckets || lowest != 0 || highest != Buckets-1 {
		t.Fatalf("notarx_lock_buckets holds %d rows from %d to %d; want %d from 0 to %d", count, lowest, highest, Buckets, Buckets-1)
	}
}

// TestReleaseAllDropsEveryLockOfTheSession is the step a dedicated connection takes before it returns to the pool.
func TestReleaseAllDropsEveryLockOfTheSession(t *testing.T) {
	db := openMySQL(t)
	ctx := context.Background()
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	locker := Locker{}
	for _, key := range []string{"release-all:a", "release-all:b"} {
		if acquired, err := locker.LockSession(ctx, holder, key, 0); err != nil || !acquired {
			t.Fatalf("LockSession(%s) = %t, %v", key, acquired, err)
		}
	}
	if err := locker.ReleaseAll(ctx, holder); err != nil {
		t.Fatal(err)
	}
	other, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	for _, key := range []string{"release-all:a", "release-all:b"} {
		if acquired, err := locker.LockSession(ctx, other, key, 0); err != nil || !acquired {
			t.Fatalf("%s was not released: %t, %v", key, acquired, err)
		}
	}
}

// TestLockContentionWithFourThousandBuckets records what ADR 0022 asks slice 4 to measure: how often
// unrelated keys share a bucket and what that costs. 32 workers each lock a distinct key for 5 ms,
// so with no collision the run takes about the time of one critical section; every collision adds
// one more. The result is logged for the pull request.
func TestLockContentionWithFourThousandBuckets(t *testing.T) {
	db := openMySQL(t)
	locker := Locker{}
	const workers, hold = 32, 5 * time.Millisecond
	keys := make([]string, workers)
	owners := map[uint16]int{}
	for i := range keys {
		keys[i] = fmt.Sprintf("contention:%d", i)
		owners[bucket(keys[i])]++
	}
	shared := 0
	for _, n := range owners {
		shared += n - 1
	}
	var wg sync.WaitGroup
	started := time.Now()
	errs := make(chan error, workers)
	for _, key := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- dialect.RunTx(context.Background(), db, Classifier{}, nil, func(tx *sql.Tx) error {
				if err := locker.LockTx(context.Background(), tx, key); err != nil {
					return err
				}
				time.Sleep(hold)
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(started)
	t.Logf("%d distinct keys in %d buckets: %d keys share a bucket with another; %d workers held %s each and finished in %s (no collision: about %s)",
		workers, Buckets, shared, workers, hold, elapsed.Round(time.Millisecond), hold)
	if elapsed > 30*time.Second {
		t.Fatalf("32 distinct keys took %s: the buckets serialise unrelated work", elapsed)
	}
}
