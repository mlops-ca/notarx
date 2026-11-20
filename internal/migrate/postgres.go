package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
)

// trigramStateSQL is the pg_trgm classification of scripts/run-migrations.sh and
// internal/preflight/trigram.go; a test keeps the three in step.
const trigramStateSQL = `SELECT CASE
  WHEN EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN 'installed'
  WHEN NOT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'pg_trgm') THEN 'unavailable'
  WHEN (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) THEN 'creatable'
  WHEN COALESCE((SELECT trusted FROM pg_available_extension_versions WHERE name = 'pg_trgm' ORDER BY version DESC LIMIT 1), false)
       AND has_database_privilege(current_user, current_database(), 'CREATE')
       AND has_schema_privilege(current_user, 'public', 'CREATE') THEN 'creatable'
  ELSE 'denied'
END`

const postgresLedgerSQL = `CREATE TABLE IF NOT EXISTS notarx_schema_migrations (
    filename TEXT PRIMARY KEY,
    checksum TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// postgresForeignTablesSQL counts the tables of a database that has no notarx ledger rows.
const postgresForeignTablesSQL = `SELECT count(*)
FROM pg_tables t
WHERE t.schemaname = 'public'
  AND t.tablename <> 'notarx_schema_migrations'
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = (quote_ident(t.schemaname) || '.' || quote_ident(t.tablename))::regclass AND d.deptype = 'e')
  AND NOT EXISTS (SELECT 1 FROM notarx_schema_migrations)`

func runPostgres(ctx context.Context, db *sql.DB, files []File, out io.Writer) error {
	var role, database string
	if err := db.QueryRowContext(ctx, "SELECT current_user, current_database()").Scan(&role, &database); err != nil {
		return fmt.Errorf("read the database session: %w", err)
	}
	if err := checkTrigram(ctx, db, role, database); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, postgresLedgerSQL); err != nil {
		return fmt.Errorf("create the migration ledger: %w", err)
	}
	var foreign int
	if err := db.QueryRowContext(ctx, postgresForeignTablesSQL).Scan(&foreign); err != nil {
		return fmt.Errorf("check for foreign tables: %w", err)
	}
	if foreign != 0 {
		return fmt.Errorf("database %s already holds %d table(s) but no notarx migration ledger; upgrading from another schema is not supported, use an empty database or restore a notarx backup: %w", database, foreign, ErrForeignSchema)
	}
	for _, file := range files {
		var recorded string
		err := db.QueryRowContext(ctx, "SELECT checksum FROM notarx_schema_migrations WHERE filename = $1", file.Name).Scan(&recorded)
		switch {
		case err == nil:
			if recorded != file.Checksum {
				return changedError(file.Name, recorded, file.Checksum)
			}
			_, _ = fmt.Fprintf(out, "Skipping applied migration %s.\n", file.Name)
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("read the ledger row of %s: %w", file.Name, err)
		}
		_, _ = fmt.Fprintf(out, "Applying migration %s.\n", file.Name)
		if err := applyPostgres(ctx, db, file); err != nil {
			return err
		}
	}
	return nil
}

func checkTrigram(ctx context.Context, db *sql.DB, role, database string) error {
	var state string
	if err := db.QueryRowContext(ctx, trigramStateSQL).Scan(&state); err != nil {
		return fmt.Errorf("check the pg_trgm extension: %w", err)
	}
	switch state {
	case "installed", "creatable":
		return nil
	case "unavailable":
		return errors.New("the pg_trgm extension is not available on this PostgreSQL server; install the contrib package or use a backend that ships it")
	default:
		return fmt.Errorf("role %s may not create the pg_trgm extension; have a superuser run CREATE EXTENSION pg_trgm in database %s, or grant CREATE on the database and on schema public", role, database)
	}
}

// applyPostgres runs one file and its ledger row in one transaction, like psql --single-transaction.
func applyPostgres(ctx context.Context, db *sql.DB, file File) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", file.Name, err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	// No arguments: pgx sends the text over the simple protocol, which allows many statements.
	if _, err = tx.ExecContext(ctx, file.Body); err != nil {
		return fmt.Errorf("migration %s failed: %w", file.Name, err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO notarx_schema_migrations (filename, checksum) VALUES ($1, $2)", file.Name, file.Checksum); err != nil {
		return fmt.Errorf("record migration %s: %w", file.Name, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", file.Name, err)
	}
	return nil
}
