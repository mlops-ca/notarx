// Package dialect holds the four non-SQL primitives that differ between the
// supported databases (ADR 0022): locks, row claims, notifications and error
// classification. The interfaces live here; internal/dialect/postgres and
// internal/dialect/mysql implement them, and internal/dialect/dialecttest is the
// contract both must pass. This package imports no
// database driver and no store, so every layer may depend on it.
package dialect

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"time"
)

// Sentinels returned by an ErrorClassifier. internal/repository re-exports them
// so store callers keep matching on the repository package.
var (
	ErrUniqueViolation      = errors.New("unique constraint violated")
	ErrForeignKeyViolation  = errors.New("foreign key constraint violated")
	ErrDeadlock             = errors.New("database deadlock")
	ErrSerializationFailure = errors.New("database serialization failure")
	ErrLockTimeout          = errors.New("database lock wait timed out")
)

// LockBuckets is the number of rows of notarx_lock_buckets, the table MySQL's transaction locks lock (ADR 0022).
// internal/dialect/mysql hashes keys modulo it and the MySQL baseline migration seeds that many rows, so both read
// this one constant: changing it changes the hash and the seed together.
const LockBuckets = 4096

// Locker serialises critical sections.
type Locker interface {
	// LockTx takes transaction-scoped locks on every key, released at commit or
	// rollback. Keys are de-duplicated and acquired in a stable order, so keys
	// that must be held together are passed in one call.
	LockTx(ctx context.Context, tx *sql.Tx, keys ...string) error
	// LockSession takes a session-scoped lock on conn, which must stay out of
	// the pool for as long as the lock is held. wait 0 does not wait, a negative
	// wait waits until ctx ends. It reports whether the lock was acquired.
	LockSession(ctx context.Context, conn *sql.Conn, key string, wait time.Duration) (bool, error)
	// UnlockSession releases a lock taken by LockSession on the same conn.
	UnlockSession(ctx context.Context, conn *sql.Conn, key string) error
}

// Claimer builds queue claims: a worker takes up to limit unlocked rows for the
// life of tx, a lease column makes a crashed worker's rows claimable again.
type Claimer interface {
	// ClaimRows runs selectSQL, a plain SELECT (no LIMIT, locking, DISTINCT,
	// GROUP BY or set operation, which an implementation rejects) that
	// uses the dialect's placeholders for args, and locks the rows it returns
	// with skip-locked semantics, so concurrent claimers see disjoint rows.
	ClaimRows(ctx context.Context, tx *sql.Tx, selectSQL string, limit int, args ...any) (*sql.Rows, error)
}

// Subscription delivers wake-ups for the subscribed channels.
type Subscription interface {
	// Wait blocks until a notification arrives or ctx ends; it returns the channel name. On a
	// dialect that polls (MySQL) it also returns when the poll interval passes: a return is a hint to
	// look at the queue, never proof that work exists.
	Wait(ctx context.Context) (string, error)
	Close() error
}

// Notifier lets a worker learn that work was enqueued: PostgreSQL pushes with LISTEN/NOTIFY, MySQL has no
// push and wakes the worker at a poll cadence (ADR 0022).
type Notifier interface {
	Subscribe(ctx context.Context, db *sql.DB, channels ...string) (Subscription, error)
}

// Waker is implemented by a Notifier that cannot push through the database (MySQL): an enqueue in this
// process calls Notify so the subscribers of the channel wake at once instead of at the next poll.
type Waker interface {
	Notify(channel string)
}

// Resetter is implemented by the Subscription of a polling Notifier: a worker calls Reset after it found
// work, which returns the poll interval to its cadence (an idle subscription backs off).
type Resetter interface {
	Reset()
}

// ErrorClassifier maps driver errors to the sentinels above.
type ErrorClassifier interface {
	// Classify returns the matching sentinel, or nil when err is none of them.
	Classify(err error) error
	// Retryable reports whether the whole transaction may be re-run.
	Retryable(err error) bool
}

// Set bundles one dialect's implementations.
type Set struct {
	Name       string
	Locker     Locker
	Claimer    Claimer
	Notifier   Notifier
	Classifier ErrorClassifier
}

// claimForbidden matches clauses that conflict with the LIMIT and FOR UPDATE SKIP LOCKED a
// Claimer appends (both engines refuse FOR UPDATE with DISTINCT, GROUP BY, UNION and
// aggregates), or that would be applied twice.
var claimForbidden = regexp.MustCompile(`(?i)(;|--|/\*|\b(limit|offset|fetch|distinct|group\s+by|union|intersect|except|for\s+(update|share|no\s+key\s+update|key\s+share))\b)`)

// ValidateClaimSelect checks the arguments of Claimer.ClaimRows, so every implementation
// rejects the same queries.
func ValidateClaimSelect(selectSQL string, limit int) error {
	if limit <= 0 {
		return errors.New("claim limit must be positive")
	}
	if claimForbidden.MatchString(selectSQL) {
		return errors.New("claim query must be a plain SELECT without LIMIT, locking, set or grouping clauses")
	}
	return nil
}
