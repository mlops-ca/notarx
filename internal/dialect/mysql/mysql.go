// Package mysql implements the internal/dialect primitives for MySQL 8.4 (ADR 0022): transaction
// locks on the notarx_lock_buckets rows, session locks with GET_LOCK, FOR UPDATE SKIP LOCKED
// claims, notification by polling, and the error numbers of InnoDB. PostgreSQL stays the
// reference backend (internal/dialect/postgres); the shared contract suite in
// internal/dialect/dialecttest runs against both.
package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/dialect"
)

// Buckets is the number of rows of notarx_lock_buckets, seeded by the baseline migration
// (internal/mysqlbaseline.LockBuckets). A key hashes to one bucket, so unrelated keys collide one
// time in 4096: a collision only serialises two critical sections, it never lets two run together.
const Buckets = 4096

// New returns the MySQL dialect set. interval is NOTARX_DATABASE_POLL_INTERVAL; it is the cadence of
// the polling Notifier while work flows (DefaultPollInterval when zero).
func New(interval time.Duration) dialect.Set {
	return dialect.Set{Name: database.DriverMySQL, Locker: Locker{}, Claimer: Claimer{}, Notifier: NewNotifier(Config{Interval: interval}), Classifier: Classifier{}}
}

// Locker serialises critical sections with row locks on notarx_lock_buckets (transaction scope)
// and GET_LOCK (session scope).
type Locker struct{}

// bucket maps a key to its lock bucket: FNV-1a 64 modulo 4096.
func bucket(key string) uint16 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return uint16(h.Sum64() % Buckets)
}

// buckets returns the buckets of the keys, sorted ascending and without duplicates: every caller
// locks in the same order, so keys passed together can never deadlock each other.
func buckets(keys []string) []uint16 {
	out := make([]uint16, len(keys))
	for i, key := range keys {
		out[i] = bucket(key)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// LockTx locks one notarx_lock_buckets row per key until the transaction ends. The order is only
// guaranteed inside one call, so a call site that needs several keys passes them together; a
// second LockTx in the same transaction can still deadlock, and RunTx retries that.
func (Locker) LockTx(ctx context.Context, tx *sql.Tx, keys ...string) error {
	for _, b := range buckets(keys) {
		var locked uint16
		err := tx.QueryRowContext(ctx, `SELECT bucket FROM notarx_lock_buckets WHERE bucket = ? FOR UPDATE`, b).Scan(&locked)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("lock bucket %d is missing: notarx_lock_buckets is not seeded by the MySQL baseline", b)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// lockName is nx: plus the first 40 hex characters of SHA-256(database name + key). Lock names are
// server-wide, not per database, so the database name keeps two notarx databases on one server
// apart; the hash keeps the name (43 characters) under the 64 that GET_LOCK allows.
func lockName(database, key string) string {
	sum := sha256.Sum256([]byte(database + key))
	return "nx:" + hex.EncodeToString(sum[:])[:40]
}

func sessionLockName(ctx context.Context, conn *sql.Conn, key string) (string, error) {
	var name sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&name); err != nil {
		return "", err
	}
	if !name.Valid {
		return "", errors.New("the connection names no database")
	}
	return lockName(name.String, key), nil
}

// lockTimeout turns a wait into the seconds GET_LOCK takes: 0 tries once, a negative wait waits
// until the context ends (-1), and any other wait rounds up to whole seconds.
func lockTimeout(wait time.Duration) int64 {
	switch {
	case wait < 0:
		return -1
	case wait == 0:
		return 0
	}
	return int64((wait + time.Second - 1) / time.Second)
}

// LockSession takes a GET_LOCK lock on conn. It reports false when the wait ran out (1 acquired, 0
// timed out, NULL is an error). The lock belongs to the session, so conn must stay out of the pool
// while it is held; a cancelled wait closes the driver connection, which also releases it.
func (Locker) LockSession(ctx context.Context, conn *sql.Conn, key string, wait time.Duration) (bool, error) {
	name, err := sessionLockName(ctx, conn, key)
	if err != nil {
		return false, err
	}
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, ?)`, name, lockTimeout(wait)).Scan(&acquired); err != nil {
		return false, err
	}
	if !acquired.Valid {
		return false, errors.New("GET_LOCK failed")
	}
	return acquired.Int64 == 1, nil
}

// UnlockSession releases a lock taken by LockSession on the same connection.
func (Locker) UnlockSession(ctx context.Context, conn *sql.Conn, key string) error {
	name, err := sessionLockName(ctx, conn, key)
	if err != nil {
		return err
	}
	var released sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT RELEASE_LOCK(?)`, name).Scan(&released); err != nil {
		return err
	}
	if !released.Valid || released.Int64 != 1 {
		return errors.New("session lock was not held")
	}
	return nil
}

// ReleaseAll drops every lock of the session. A connection that held GET_LOCK locks goes through it
// before it returns to the pool, because COMMIT does not release them.
func (Locker) ReleaseAll(ctx context.Context, conn *sql.Conn) error {
	var released sql.NullInt64
	return conn.QueryRowContext(ctx, `SELECT RELEASE_ALL_LOCKS()`).Scan(&released)
}

// Claimer claims rows with FOR UPDATE SKIP LOCKED, supported by InnoDB from 8.0. Keep claims short
// and ordered by an indexed column; the transaction runs in READ COMMITTED (database.Open).
type Claimer struct{}

// ClaimRows runs selectSQL, written with ? placeholders, with LIMIT and FOR UPDATE SKIP LOCKED
// appended. A lease column in the WHERE clause makes the rows of a crashed worker claimable again.
func (Claimer) ClaimRows(ctx context.Context, tx *sql.Tx, selectSQL string, limit int, args ...any) (*sql.Rows, error) {
	if err := dialect.ValidateClaimSelect(selectSQL, limit); err != nil {
		return nil, err
	}
	return tx.QueryContext(ctx, selectSQL+" LIMIT ? FOR UPDATE SKIP LOCKED", append(slices.Clone(args), limit)...)
}

// MySQL error numbers the classifier knows.
const (
	errDupEntry         = 1062
	errRowIsReferenced  = 1451
	errNoReferencedRow  = 1452
	errLockWaitTimeout  = 1205
	errLockDeadlock     = 1213
	errLockNowait       = 3572
	errRowIsReferenced2 = 1217
	errNoReferencedRow2 = 1216
)

// Classifier maps InnoDB error numbers to the dialect sentinels.
type Classifier struct{}

func (Classifier) Classify(err error) error {
	var mysqlError *driver.MySQLError
	if !errors.As(err, &mysqlError) {
		return nil
	}
	switch mysqlError.Number {
	case errDupEntry:
		return dialect.ErrUniqueViolation
	case errRowIsReferenced, errNoReferencedRow, errRowIsReferenced2, errNoReferencedRow2:
		return dialect.ErrForeignKeyViolation
	case errLockDeadlock:
		return dialect.ErrDeadlock
	case errLockWaitTimeout, errLockNowait:
		return dialect.ErrLockTimeout
	}
	return nil
}

// Retryable reports whether the whole transaction may run again: a deadlock (1213, InnoDB rolled the
// transaction back) or a lock wait timeout (1205, only the statement was rolled back, so the caller
// must roll back explicitly first, which dialect.RunTx does).
func (c Classifier) Retryable(err error) bool {
	switch c.Classify(err) {
	case dialect.ErrDeadlock, dialect.ErrLockTimeout:
		return true
	}
	return false
}
