package dialect

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"time"
)

// MaxTxRetries is how many times RunTx re-runs a transaction that failed with a retryable error:
// a deadlock (1213, 40P01), a serialization failure (40001) or, on MySQL, a lock wait timeout (1205).
const MaxTxRetries = 3

// retryBase is the first back-off; each retry doubles it and adds as much jitter again.
const retryBase = 10 * time.Millisecond

// RunTx runs fn in a transaction and commits it, re-running the whole transaction up to
// MaxTxRetries times when classifier says the failure is retryable. Every failed attempt is rolled
// back explicitly first: InnoDB rolls back only the failing statement on a lock wait timeout
// (innodb_rollback_on_timeout is OFF), so the transaction would otherwise stay open and hold its
// locks. fn must be safe to run again: it may not keep state from an earlier attempt, and it
// may not do anything that cannot be repeated (ADR 0022). A statement that waits out a lock
// (innodb_lock_wait_timeout) and fails is retried too, so a blocked operation can take up to
// MaxTxRetries+1 lock waits plus the back-off; ctx bounds it.
func RunTx(ctx context.Context, db *sql.DB, classifier ErrorClassifier, opts *sql.TxOptions, fn func(tx *sql.Tx) error) error {
	for attempt := 0; ; attempt++ {
		err := runOnce(ctx, db, opts, fn)
		if err == nil || attempt >= MaxTxRetries || !classifier.Retryable(err) {
			return err
		}
		backoff := retryBase << attempt
		timer := time.NewTimer(backoff + rand.N(backoff))
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
}

func runOnce(ctx context.Context, db *sql.DB, opts *sql.TxOptions, fn func(tx *sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		// A failure, and a panic in fn, release the transaction's locks at once.
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}
