package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const mysqlLedgerSQL = `CREATE TABLE IF NOT EXISTS notarx_schema_migrations (
    filename VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    state VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'applying',
    statements_done INT UNSIGNED NOT NULL DEFAULT 0,
    PRIMARY KEY (filename)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin`

// mysqlTableCountsSQL counts the base tables other than the ledger, and the ledger itself (0 or 1).
const mysqlTableCountsSQL = `SELECT
  COALESCE(SUM(t.table_name <> 'notarx_schema_migrations'), 0),
  COALESCE(SUM(t.table_name = 'notarx_schema_migrations'), 0)
FROM information_schema.tables t
WHERE t.table_schema = DATABASE() AND t.table_type = 'BASE TABLE'`

// mysqlUnsafeModes change how the splitter must read a file: backslash is a literal and double quotes quote identifiers.
var mysqlUnsafeModes = []string{"NO_BACKSLASH_ESCAPES", "ANSI_QUOTES"}

// maxLockName is the longest name GET_LOCK accepts.
const maxLockName = 64

// lockName is notarx-migrate:<database>, shortened with a hash when the
// database name would push it past the 64 characters GET_LOCK allows.
func lockName(database string) string {
	name := "notarx-migrate:" + database
	if len(name) <= maxLockName {
		return name
	}
	sum := sha256.Sum256([]byte(database))
	return "notarx-migrate:" + hex.EncodeToString(sum[:])[:40]
}

func runMySQL(ctx context.Context, db *sql.DB, files []File, opts Options) error {
	out := opts.out()
	// One connection for the lock and every statement: GET_LOCK belongs to the
	// session, and the lock is released if the connection dies.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open a migration connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	var database sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil || !database.Valid {
		return errors.New("the connection names no database; put the database name in the URL")
	}
	var mode string
	if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&mode); err != nil {
		return fmt.Errorf("read the session sql_mode: %w", err)
	}
	for _, unsafe := range mysqlUnsafeModes {
		if strings.Contains(mode, unsafe) {
			return fmt.Errorf("the session sql_mode has %s, which the statement splitter does not support", unsafe)
		}
	}
	release, err := acquireLock(ctx, conn, lockName(database.String), opts)
	if err != nil {
		return err
	}
	defer release()
	if err := refuseForeignSchema(ctx, conn, database.String); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, mysqlLedgerSQL); err != nil {
		return fmt.Errorf("create the migration ledger: %w", err)
	}
	for _, file := range files {
		if err := applyMySQL(ctx, conn, file, opts, out); err != nil {
			return err
		}
	}
	return nil
}

// refuseForeignSchema fails for a database that holds tables but no notarx ledger rows. It runs before the
// ledger is created, so a refused database is left as it was found.
func refuseForeignSchema(ctx context.Context, conn *sql.Conn, database string) error {
	var others, ledgers int
	if err := conn.QueryRowContext(ctx, mysqlTableCountsSQL).Scan(&others, &ledgers); err != nil {
		return fmt.Errorf("check for foreign tables: %w", err)
	}
	if others == 0 {
		return nil
	}
	var rows int
	if ledgers > 0 {
		if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM notarx_schema_migrations").Scan(&rows); err != nil {
			return fmt.Errorf("check for foreign tables: %w", err)
		}
	}
	if rows > 0 {
		return nil
	}
	return fmt.Errorf("database %s already holds %d table(s) but no notarx migration ledger; upgrading from another schema is not supported, use an empty database or restore a notarx backup: %w", database, others, ErrForeignSchema)
}

func acquireLock(ctx context.Context, conn *sql.Conn, name string, opts Options) (func(), error) {
	wait := opts.LockWait
	if wait <= 0 {
		wait = DefaultLockWait
	}
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", name, int(wait.Seconds())).Scan(&acquired); err != nil {
		return nil, fmt.Errorf("take the migration lock: %w", err)
	}
	if !acquired.Valid {
		return nil, errors.New("take the migration lock: the server reported an error")
	}
	if acquired.Int64 != 1 {
		return nil, fmt.Errorf("lock %s was not free after %s: %w", name, wait, ErrLockHeld)
	}
	return func() {
		// The caller's context may be cancelled; closing the connection releases the lock too.
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK(?)", name)
	}, nil
}

func applyMySQL(ctx context.Context, conn *sql.Conn, file File, opts Options, out io.Writer) error {
	var recorded, state string
	var done int
	err := conn.QueryRowContext(ctx, "SELECT checksum, state, statements_done FROM notarx_schema_migrations WHERE filename = ?", file.Name).Scan(&recorded, &state, &done)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read the ledger row of %s: %w", file.Name, err)
	}
	known := err == nil
	changed := known && recorded != file.Checksum
	// A file that finished never changes; a file that stopped half way may be corrected, which --repair accepts.
	if changed && (state == StateApplied || !opts.Repair) {
		return changedError(file.Name, recorded, file.Checksum)
	}
	if known && state == StateApplied {
		_, _ = fmt.Fprintf(out, "Skipping applied migration %s.\n", file.Name)
		return nil
	}
	statements, err := SplitStatements(file.Body)
	if err != nil {
		return fmt.Errorf("migration %s: %w", file.Name, err)
	}
	if !known {
		if _, err := conn.ExecContext(ctx, "INSERT INTO notarx_schema_migrations (filename, checksum, state, statements_done) VALUES (?, ?, 'applying', 0)", file.Name, file.Checksum); err != nil {
			return fmt.Errorf("record migration %s: %w", file.Name, err)
		}
		_, _ = fmt.Fprintf(out, "Applying migration %s.\n", file.Name)
	} else if done == len(statements) && !changed {
		// Every statement is recorded: only the last update to applied was lost, so nothing needs confirming.
		_, _ = fmt.Fprintf(out, "Completing migration %s: all %d statements were recorded.\n", file.Name, done)
	} else {
		if done, err = repair(ctx, conn, file.Name, file.Checksum, changed, statements, done, opts, out); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "Resuming migration %s after statement %d of %d.\n", file.Name, done, len(statements))
	}
	for i := done; i < len(statements); i++ {
		if _, err := conn.ExecContext(ctx, statements[i]); err != nil {
			return fmt.Errorf("migration %s statement %d of %d failed: %w", file.Name, i+1, len(statements), err)
		}
		if _, err := conn.ExecContext(ctx, "UPDATE notarx_schema_migrations SET statements_done = ? WHERE filename = ?", i+1, file.Name); err != nil {
			return fmt.Errorf("record progress of migration %s: %w", file.Name, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "UPDATE notarx_schema_migrations SET state = 'applied', statements_done = ?, applied_at = CURRENT_TIMESTAMP(6) WHERE filename = ?", len(statements), file.Name); err != nil {
		return fmt.Errorf("finish migration %s: %w", file.Name, err)
	}
	return nil
}

// repair decides where a half-applied migration resumes. Without --repair it
// refuses; with --repair and no --completed it shows the offset and refuses;
// with both it validates the operator's count and stores it.
func repair(ctx context.Context, conn *sql.Conn, name, checksum string, changed bool, statements []string, done int, opts Options, out io.Writer) (int, error) {
	if done > len(statements) {
		return 0, fmt.Errorf("migration %s records %d statements done but the file has %d: %w", name, done, len(statements), ErrChecksumChanged)
	}
	if !opts.Repair {
		return 0, fmt.Errorf("migration %s stopped after statement %d of %d: %w", name, done, len(statements), ErrRepairRequired)
	}
	if opts.Completed == nil {
		_, _ = fmt.Fprintf(out, "Migration %s stopped after statement %d of %d.\n", name, done, len(statements))
		if changed {
			_, _ = fmt.Fprintf(out, "The file changed since it started: confirming a count adopts the new file, so check that it keeps the first %d statements.\n", done)
		}
		if done > 0 {
			_, _ = fmt.Fprintf(out, "Last recorded statement (%d): %s\n", done, preview(statements[done-1]))
		}
		if done < len(statements) {
			_, _ = fmt.Fprintf(out, "Next statement (%d): %s\n", done+1, preview(statements[done]))
		}
		_, _ = fmt.Fprintf(out, "Check whether statement %d took effect, then run notarx migrate --repair --completed N with N = %d (it did not) or %d (it did).\n", done+1, done, done+1)
		return 0, fmt.Errorf("migration %s needs the operator to confirm the last statement: %w", name, ErrRepairRequired)
	}
	completed := *opts.Completed
	if completed < done || completed > done+1 || completed > len(statements) {
		return 0, fmt.Errorf("--completed %d is not valid for migration %s: it must be %d or %d (of %d statements)", completed, name, done, done+1, len(statements))
	}
	if _, err := conn.ExecContext(ctx, "UPDATE notarx_schema_migrations SET statements_done = ?, checksum = ? WHERE filename = ?", completed, checksum, name); err != nil {
		return 0, fmt.Errorf("store the confirmed statement count of %s: %w", name, err)
	}
	return completed, nil
}

// maxPreview is the longest statement preview, in characters.
const maxPreview = 160

func preview(statement string) string {
	collapsed := []rune(strings.Join(strings.Fields(statement), " "))
	if len(collapsed) > maxPreview {
		return string(collapsed[:maxPreview]) + "..."
	}
	return string(collapsed)
}
