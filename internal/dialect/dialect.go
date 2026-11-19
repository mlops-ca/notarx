// Package dialect holds the four non-SQL primitives that differ between the
// supported databases (ADR 0022): locks, row claims, notifications and error
// classification. The interfaces live here; internal/dialect/postgres and,
// later, internal/dialect/mysql implement them. This package imports no
// database driver and no store, so every layer may depend on it.
package dialect

import (
	"context"
	"database/sql"
	"errors"
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
	// ClaimRows runs selectSQL, a SELECT without a LIMIT or locking clause that
	// uses the dialect's placeholders for args, and locks the rows it returns
	// with skip-locked semantics, so concurrent claimers see disjoint rows.
	ClaimRows(ctx context.Context, tx *sql.Tx, selectSQL string, limit int, args ...any) (*sql.Rows, error)
}

// Subscription delivers wake-ups for the subscribed channels.
type Subscription interface {
	// Wait blocks until a notification arrives or ctx ends; it returns the channel name.
	Wait(ctx context.Context) (string, error)
	Close() error
}

// Notifier lets a worker learn that work was enqueued without polling.
type Notifier interface {
	Subscribe(ctx context.Context, db *sql.DB, channels ...string) (Subscription, error)
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
