package preflight

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// trigramStateSQL classifies the pg_trgm extension for the connecting role. The
// baseline migration requires it (12 GIN gin_trgm_ops indexes, ADR 0015), and
// scripts/run-migrations.sh runs the same classification before its first
// statement, so keep the two in step.
//
//	installed    the extension exists in this database
//	unavailable  the server does not ship pg_trgm (contrib package missing)
//	creatable    the role may CREATE EXTENSION it (superuser, or a trusted
//	             extension with CREATE on the database and on schema public)
//	denied       the server ships it but the role may not create it
const trigramStateSQL = `SELECT CASE
  WHEN EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN 'installed'
  WHEN NOT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'pg_trgm') THEN 'unavailable'
  WHEN (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) THEN 'creatable'
  WHEN COALESCE((SELECT trusted FROM pg_available_extension_versions WHERE name = 'pg_trgm' ORDER BY version DESC LIMIT 1), false)
       AND has_database_privilege(current_user, current_database(), 'CREATE')
       AND has_schema_privilege(current_user, 'public', 'CREATE') THEN 'creatable'
  ELSE 'denied'
END`

func probeTrigram(ctx context.Context, databaseURL string) (string, error) {
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return "", err
	}
	defer func() { _ = connection.Close(ctx) }()
	var state string
	if err := connection.QueryRow(ctx, trigramStateSQL).Scan(&state); err != nil {
		return "", err
	}
	return state, nil
}

func trigramResult(state string, err error) Result {
	const name = "pg_trgm"
	if err != nil {
		return Result{Name: name, Status: StatusFail, Summary: "pg_trgm check could not run"}
	}
	switch state {
	case "installed":
		return Result{Name: name, Status: StatusPass, Summary: "pg_trgm extension is installed"}
	case "creatable":
		return Result{Name: name, Status: StatusPass, Summary: "pg_trgm extension can be created by the database role"}
	case "unavailable":
		return Result{Name: name, Status: StatusFail, Summary: "pg_trgm extension is not available on this PostgreSQL server; install the contrib package or choose a backend that ships it"}
	default:
		return Result{Name: name, Status: StatusFail, Summary: "the database role may not create pg_trgm; have a superuser run CREATE EXTENSION pg_trgm, or grant CREATE on the database and on schema public"}
	}
}
