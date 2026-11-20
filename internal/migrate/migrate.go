// Package migrate applies the SQL migrations of a database dialect and keeps
// the notarx_schema_migrations ledger (ADR 0022, ADR 0015).
//
// PostgreSQL follows scripts/run-migrations.sh statement for statement: one
// transaction per file, a (filename, checksum, applied_at) ledger that is
// byte-identical to the script's. That runner stays the deployed one; this path
// runs beside it as a parity check until the Go runner is trusted.
//
// MySQL has no transactional DDL, so a crash can leave a file half applied. Its
// ledger adds a state (applying or applied) and the number of statements done,
// a session lock (GET_LOCK) keeps two replicas from racing, and "--repair"
// resumes a half-applied file from the recorded statement.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"
)

// LedgerTable is the migration ledger of both dialects.
const LedgerTable = "notarx_schema_migrations"

// Ledger states of the MySQL ledger.
const (
	StateApplying = "applying"
	StateApplied  = "applied"
)

// Dialect names, equal to the NOTARX_DATABASE_DRIVER values.
const (
	DialectPostgres = "postgres"
	DialectMySQL    = "mysql"
)

// DefaultLockWait is how long a MySQL runner waits for another runner of the
// same database to finish.
const DefaultLockWait = 5 * time.Minute

var (
	// ErrChecksumChanged reports an applied migration whose file changed.
	ErrChecksumChanged = errors.New("an applied migration has changed")
	// ErrForeignSchema reports a database that holds tables but no notarx ledger.
	ErrForeignSchema = errors.New("the database holds tables but no notarx migration ledger")
	// ErrRepairRequired reports a MySQL migration that a crash left half applied.
	ErrRepairRequired = errors.New("a migration is half applied; run notarx migrate --repair")
	// ErrLockHeld reports that another runner kept the MySQL migration lock.
	ErrLockHeld = errors.New("another migration run holds the lock")
)

// Options tune one run.
type Options struct {
	// Repair resumes a half-applied MySQL migration. Without Completed it only
	// reports where the migration stopped and fails.
	Repair bool
	// Completed is the number of statements of the half-applied migration the
	// operator confirms took effect. It must be the recorded count or one more
	// (a crash can fall between a statement and its counter). Nil: unset.
	Completed *int
	// LockWait bounds the wait for the MySQL migration lock; zero is DefaultLockWait.
	LockWait time.Duration
	// Out receives progress lines; nil discards them.
	Out io.Writer
}

func (o Options) out() io.Writer {
	if o.Out == nil {
		return io.Discard
	}
	return o.Out
}

// Run applies the migrations of files, in the given order, to db.
func Run(ctx context.Context, db *sql.DB, dialect string, files []File, opts Options) error {
	switch dialect {
	case DialectPostgres:
		if opts.Repair {
			return errors.New("--repair applies to MySQL only: PostgreSQL applies each file in one transaction")
		}
		return runPostgres(ctx, db, files, opts.out())
	case DialectMySQL:
		return runMySQL(ctx, db, files, opts)
	default:
		return fmt.Errorf("unsupported dialect %q", dialect)
	}
}

func changedError(name, recorded, current string) error {
	return fmt.Errorf("applied migration %s has changed (recorded %s, current %s): %w", name, recorded, current, ErrChecksumChanged)
}
