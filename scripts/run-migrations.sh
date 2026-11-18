#!/bin/sh
set -eu

migration_dir=${MIGRATION_DIR:-/migrations}
pg_host=${PGHOST:-postgres}
pg_user=${PGUSER:-gateway}
pg_database=${PGDATABASE:-gateway}

run_psql() {
  psql -X -h "$pg_host" -U "$pg_user" -d "$pg_database" -v ON_ERROR_STOP=1 "$@"
}

file_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
    return
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
    return
  fi
  printf '%s\n' 'A SHA-256 utility (sha256sum or shasum) is required.' >&2
  exit 1
}

# The baseline requires pg_trgm (GIN gin_trgm_ops indexes). Fail before the first
# statement, with a clear message, when the extension is neither installed nor
# creatable by this role. Keep the classification in step with
# internal/preflight/trigram.go, which `make preflight` runs.
trigram_state=$(run_psql -At <<'SQL'
SELECT CASE
  WHEN EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN 'installed'
  WHEN NOT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'pg_trgm') THEN 'unavailable'
  WHEN (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) THEN 'creatable'
  WHEN COALESCE((SELECT trusted FROM pg_available_extension_versions WHERE name = 'pg_trgm' ORDER BY version DESC LIMIT 1), false)
       AND has_database_privilege(current_user, current_database(), 'CREATE')
       AND has_schema_privilege(current_user, 'public', 'CREATE') THEN 'creatable'
  ELSE 'denied'
END;
SQL
)
case "$trigram_state" in
  installed|creatable) ;;
  unavailable)
    printf 'The pg_trgm extension is not available on this PostgreSQL server. Install the contrib package or use a backend that ships it.\n' >&2
    exit 1
    ;;
  *)
    printf 'Role %s may not create the pg_trgm extension. Have a superuser run CREATE EXTENSION pg_trgm in database %s, or grant CREATE on the database and on schema public.\n' "$pg_user" "$pg_database" >&2
    exit 1
    ;;
esac

run_psql <<'SQL'
CREATE TABLE IF NOT EXISTS notarx_schema_migrations (
    filename TEXT PRIMARY KEY,
    checksum TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
SQL

# notarx has no upgrade path from other schemas (ADR 0013): refuse a database that
# holds tables but no notarx ledger rows, instead of failing mid-baseline with a
# confusing SQL error.
foreign_tables=$(run_psql -At <<'SQL'
SELECT count(*)
FROM pg_tables t
WHERE t.schemaname = 'public'
  AND t.tablename <> 'notarx_schema_migrations'
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = (quote_ident(t.schemaname) || '.' || quote_ident(t.tablename))::regclass AND d.deptype = 'e')
  AND NOT EXISTS (SELECT 1 FROM notarx_schema_migrations);
SQL
)
if [ "$foreign_tables" != 0 ]; then
  printf 'Database %s already holds %s table(s) but no notarx migration ledger. Upgrading from another schema is not supported; use an empty database or restore a notarx backup.\n' "$pg_database" "$foreign_tables" >&2
  exit 1
fi

for migration in "$migration_dir"/*.sql; do
  filename=${migration##*/}
  case "$filename" in
    *[!A-Za-z0-9._-]*)
      printf 'Unsupported migration filename: %s\n' "$filename" >&2
      exit 1
      ;;
  esac

  checksum=$(file_sha256 "$migration")
  recorded_checksum=$(run_psql -At -v migration_name="$filename" <<'SQL'
SELECT checksum
FROM notarx_schema_migrations
WHERE filename = :'migration_name';
SQL
  )

  if [ -n "$recorded_checksum" ]; then
    if [ "$recorded_checksum" != "$checksum" ]; then
      printf 'Applied migration %s has changed (recorded %s, current %s).\n' "$filename" "$recorded_checksum" "$checksum" >&2
      exit 1
    fi
    printf 'Skipping applied migration %s.\n' "$filename"
    continue
  fi

  printf 'Applying migration %s.\n' "$filename"
  {
    sed '/-- +goose Down/,$d' "$migration"
    printf "\nINSERT INTO notarx_schema_migrations (filename, checksum) VALUES ('%s', '%s');\n" "$filename" "$checksum"
  } | run_psql --single-transaction
done
