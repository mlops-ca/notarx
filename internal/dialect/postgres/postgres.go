// Package postgres implements the internal/dialect primitives for PostgreSQL
// 16, the reference backend. It wraps the behaviour the stores already rely on:
// pg_advisory_xact_lock, pg_try_advisory_lock, FOR UPDATE SKIP LOCKED and
// LISTEN/NOTIFY.
package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/dialect"
)

// New returns the PostgreSQL dialect set.
func New() dialect.Set {
	return dialect.Set{Name: database.DriverPostgres, Locker: Locker{}, Claimer: Claimer{}, Notifier: Notifier{}, Classifier: Classifier{}}
}

const (
	sessionPollInterval = 50 * time.Millisecond
	unlistenTimeout     = 2 * time.Second
)

// claimForbidden matches clauses that conflict with the appended LIMIT and
// FOR UPDATE SKIP LOCKED (PostgreSQL refuses FOR UPDATE with DISTINCT, GROUP BY,
// UNION and aggregates), or that would be applied twice.
var claimForbidden = regexp.MustCompile(`(?i)(;|--|/\*|\b(limit|offset|fetch|distinct|group\s+by|union|intersect|except|for\s+(update|share|no\s+key\s+update|key\s+share))\b)`)

// Locker uses advisory locks keyed by hashtextextended(key, 0).
type Locker struct{}

func (Locker) LockTx(ctx context.Context, tx *sql.Tx, keys ...string) error {
	keys = slices.Clone(keys)
	slices.Sort(keys)
	for _, key := range slices.Compact(keys) {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
			return err
		}
	}
	return nil
}

func (Locker) LockSession(ctx context.Context, conn *sql.Conn, key string, wait time.Duration) (bool, error) {
	if wait < 0 {
		_, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, key)
		return err == nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		var acquired bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, key).Scan(&acquired); err != nil {
			return false, err
		}
		if acquired {
			return true, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		timer := time.NewTimer(min(remaining, sessionPollInterval))
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
}

func (Locker) UnlockSession(ctx context.Context, conn *sql.Conn, key string) error {
	var released bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, key).Scan(&released); err != nil {
		return err
	}
	if !released {
		return errors.New("session lock was not held")
	}
	return nil
}

// Claimer claims rows with FOR UPDATE SKIP LOCKED.
type Claimer struct{}

func (Claimer) ClaimRows(ctx context.Context, tx *sql.Tx, selectSQL string, limit int, args ...any) (*sql.Rows, error) {
	if limit <= 0 {
		return nil, errors.New("claim limit must be positive")
	}
	if claimForbidden.MatchString(selectSQL) {
		return nil, errors.New("claim query must be a plain SELECT without LIMIT, locking, set or grouping clauses")
	}
	query := fmt.Sprintf("%s LIMIT $%d FOR UPDATE SKIP LOCKED", selectSQL, len(args)+1)
	return tx.QueryContext(ctx, query, append(slices.Clone(args), limit)...)
}

// Notifier wraps LISTEN/NOTIFY on a dedicated connection.
type Notifier struct{}

func (Notifier) Subscribe(ctx context.Context, db *sql.DB, channels ...string) (dialect.Subscription, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if err := database.ListenChannels(ctx, conn, channels...); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return subscription{conn: conn}, nil
}

type subscription struct{ conn *sql.Conn }

func (s subscription) Wait(ctx context.Context) (string, error) {
	return database.WaitForNotification(ctx, s.conn)
}

// Close drops the LISTEN registrations before the connection goes back to the
// pool, so a later user of it receives no stray notifications; when that fails
// the connection is discarded instead.
func (s subscription) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), unlistenTimeout)
	defer cancel()
	if _, err := s.conn.ExecContext(ctx, `UNLISTEN *`); err != nil {
		_ = s.conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	if err := s.conn.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
		return err
	}
	return nil
}

// Classifier maps SQLSTATE codes.
type Classifier struct{}

func (Classifier) Classify(err error) error {
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) {
		return nil
	}
	switch pgError.Code {
	case "23505":
		return dialect.ErrUniqueViolation
	case "23503":
		return dialect.ErrForeignKeyViolation
	case "40P01":
		return dialect.ErrDeadlock
	case "40001":
		return dialect.ErrSerializationFailure
	case "55P03":
		return dialect.ErrLockTimeout
	}
	return nil
}

func (c Classifier) Retryable(err error) bool {
	switch c.Classify(err) {
	case dialect.ErrDeadlock, dialect.ErrSerializationFailure:
		return true
	}
	return false
}
