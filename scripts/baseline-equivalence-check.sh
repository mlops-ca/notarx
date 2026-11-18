#!/usr/bin/env bash
# Proves that the baseline migration is equivalent to the source migration chain
# (ADR 0015, "Equivalence proof"). Temporary: run at the cut-over and again at
# the re-squash, then removed with scripts/baseline-generate.sh.
#
# Usage: scripts/baseline-equivalence-check.sh [--source-ref REF] [--source-dir DIR]
#                                              [--baseline FILE] [--pg-image IMAGE]
#
#   --source-ref   git ref holding the source chain (default: HEAD)
#   --source-dir   directory of the chain inside that ref (default: migrations)
#   --baseline     baseline file under test (default:
#                  migrations-next/000001_baseline.sql)
#   --pg-image     PostgreSQL image pinned by sha256 digest (default: the pin in
#                  scripts/baseline-lib.sh, the same one the generator uses)
#
# Database A is the source chain applied by scripts/run-migrations.sh; database
# B is the baseline applied by the same runner. After the ledger and
# schema_version objects are removed from both, the following must be equal:
# the schema-only dump (same flags as the generator, no column-order
# exemption), the data-only dump of every table, and the catalogs pg_dump can
# miss or render differently. No table may be readable by PUBLIC.
#
# One PostgreSQL property needs a documented step. Expressions are stored as
# parse trees and pg_dump prints them as text; re-parsing that text does not
# always rebuild the same tree (a CHECK of nested ANDs is printed
# "((a AND b) AND c)" but re-parsed as the flat "(a AND b AND c)", and a view
# over UNION gains explicit "AS text" aliases). Dumping chain A and dumping a
# restore of that dump therefore differ textually without any drift. Expression
# text (the schema dump and constraint definitions) is consequently compared
# between B and database A2, the restore of A's own raw pg_dump, which isolates
# exactly what the proof exists to validate: the normaliser and the seed
# section. Everything that is not expression text (data, indexes, triggers,
# function bodies, sequences, reloptions, extensions, constraint names and
# types) is compared against A directly.
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/baseline-lib.sh"

source_ref=HEAD
source_dir=migrations
baseline=$baseline_root/migrations-next/000001_baseline.sql

while [[ $# -gt 0 ]]; do
  case "$1" in
    --source-ref) source_ref=${2:?--source-ref needs a value}; shift 2 ;;
    --source-dir) source_dir=${2:?--source-dir needs a value}; shift 2 ;;
    --baseline) baseline=${2:?--baseline needs a value}; shift 2 ;;
    --pg-image) BASELINE_PG_IMAGE=${2:?--pg-image needs a value}; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo/p' "$0" | sed '$d;s/^# \{0,1\}//'; exit 0 ;;
    *) baseline_fail "Unknown argument: $1" ;;
  esac
done

[[ -f "$baseline" ]] || baseline_fail "Baseline file not found: $baseline"
baseline_init
source_dir=${source_dir%/}
mkdir -p "$baseline_work/source" "$baseline_work/candidate"
git -C "$baseline_root" archive "$source_ref" -- "$source_dir" | tar -x -C "$baseline_work/source"
chain=$baseline_work/source/$source_dir
[[ -d "$chain" ]] || baseline_fail "No $source_dir directory at $source_ref."
chain_files=$(find "$chain" -maxdepth 1 -name '*.sql' | wc -l | tr -d ' ')
[[ "$chain_files" -gt 0 ]] || baseline_fail "No *.sql files in $source_dir at $source_ref."
cp "$baseline" "$baseline_work/candidate/000001_baseline.sql"

baseline_start_server chain_a baseline_b chain_a2
baseline_migrate chain_a "$chain"
baseline_migrate baseline_b "$baseline_work/candidate"

failures=0
report() {
  # report NAME FILE_A FILE_B: print the size of the diff, keep the diff on failure.
  local name=$1 a=$2 b=$3 size
  diff -u "$a" "$b" >"$baseline_work/$name.diff" || true
  size=$(grep -c '^[-+][^-+]' "$baseline_work/$name.diff" || true)
  printf '%-26s diff size %s\n' "$name" "$size"
  if [[ "$size" -ne 0 ]]; then
    failures=$((failures + 1))
    head -n 200 "$baseline_work/$name.diff" >&2
  fi
}

# The ledger holds one row per applied file; the baseline has exactly one.
ledger_a=$(baseline_psql chain_a -At -c 'SELECT count(*) FROM notarx_schema_migrations')
ledger_b=$(baseline_psql baseline_b -At -c 'SELECT count(*) FROM notarx_schema_migrations')
printf 'ledger rows                chain %s, baseline %s\n' "$ledger_a" "$ledger_b"
[[ "$ledger_a" -eq "$chain_files" && "$ledger_b" -eq 1 ]] || baseline_fail 'Unexpected ledger row counts.'

# Remove the ledger and the inherited schema_version table from both sides.
for database in chain_a baseline_b; do
  baseline_psql "$database" -q <<'SQL'
DROP TABLE IF EXISTS public.notarx_schema_migrations, public.artifact_gateway_schema_migrations, public.schema_version;
SQL
done

strip_keys() { grep -v '^\\\(un\)\{0,1\}restrict '; }

# A2: the restore of A's raw dump (see the header). Its statements keep the
# dump's own preamble; only the random \restrict keys are dropped.
baseline_dump chain_a "${BASELINE_DUMP_SCHEMA_FLAGS[@]}" | strip_keys >"$baseline_work/chain_a.raw.sql"
baseline_psql chain_a2 -q <"$baseline_work/chain_a.raw.sql" >/dev/null

# The only seeded timestamp is site_settings.updated_at = now(); pin it so the
# data comparison is about values, not about when each database was created.
for database in chain_a baseline_b; do
  baseline_psql "$database" -q -c "UPDATE public.site_settings SET updated_at = 'epoch'"
done

for database in chain_a chain_a2 baseline_b; do
  baseline_dump "$database" "${BASELINE_DUMP_SCHEMA_FLAGS[@]}" | strip_keys >"$baseline_work/$database.schema.sql"
done
for database in chain_a baseline_b; do
  baseline_dump "$database" --data-only --no-owner --no-privileges | strip_keys >"$baseline_work/$database.data.sql"
done
report schema "$baseline_work/chain_a2.schema.sql" "$baseline_work/baseline_b.schema.sql"
report data "$baseline_work/chain_a.data.sql" "$baseline_work/baseline_b.data.sql"

catalog_extras() {
  baseline_psql "$1" -At -F $'\t' <<'SQL'
\echo == pg_extension
SELECT extname, extversion, extnamespace::regnamespace FROM pg_extension ORDER BY 1;
\echo == pg_indexes
SELECT schemaname, tablename, indexname, indexdef FROM pg_indexes WHERE schemaname = 'public' ORDER BY 1, 2, 3;
\echo == functions
SELECT p.oid::regprocedure::text, pg_get_functiondef(p.oid)
FROM pg_proc p
WHERE p.pronamespace = 'public'::regnamespace
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e')
ORDER BY 1;
\echo == pg_trigger
SELECT tgrelid::regclass::text, tgname, pg_get_triggerdef(oid) FROM pg_trigger WHERE NOT tgisinternal ORDER BY 1, 2;
\echo == pg_constraint names and types
SELECT conrelid::regclass::text, conname, contype
FROM pg_constraint WHERE connamespace = 'public'::regnamespace ORDER BY 1, 2;
\echo == pg_sequences
SELECT * FROM pg_sequences WHERE schemaname = 'public' ORDER BY sequencename;
\echo == reloptions
SELECT oid::regclass::text, reloptions FROM pg_class
WHERE relnamespace = 'public'::regnamespace AND reloptions IS NOT NULL ORDER BY 1;
SQL
}
constraint_definitions() {
  baseline_psql "$1" -At -F $'\t' <<'SQL'
SELECT conrelid::regclass::text, conname, contype, pg_get_constraintdef(oid)
FROM pg_constraint WHERE connamespace = 'public'::regnamespace ORDER BY 1, 2;
SQL
}
catalog_extras chain_a >"$baseline_work/chain_a.catalog.txt"
catalog_extras baseline_b >"$baseline_work/baseline_b.catalog.txt"
report catalogs "$baseline_work/chain_a.catalog.txt" "$baseline_work/baseline_b.catalog.txt"
constraint_definitions chain_a2 >"$baseline_work/chain_a2.constraints.txt"
constraint_definitions baseline_b >"$baseline_work/baseline_b.constraints.txt"
report constraint-definitions "$baseline_work/chain_a2.constraints.txt" "$baseline_work/baseline_b.constraints.txt"

# No object may be granted to PUBLIC (has_table_privilege also covers
# sequences). Members of an extension (the pg_stat_statements views) carry the
# extension's own grants and are not part of the baseline.
for database in chain_a baseline_b; do
  public_tables=$(baseline_psql "$database" -At <<'SQL'
SELECT c.relname FROM pg_class c
WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('r', 'p', 'v', 'm', 'S')
  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = c.oid AND d.deptype = 'e')
  AND has_table_privilege('public', c.oid, 'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
ORDER BY 1;
SQL
  )
  printf '%-26s %s PUBLIC grants\n' "public privileges ($database)" "$(printf '%s' "$public_tables" | grep -c . || true)"
  if [[ -n "$public_tables" ]]; then
    printf 'Tables readable or writable by PUBLIC in %s:\n%s\n' "$database" "$public_tables" >&2
    failures=$((failures + 1))
  fi
done

if [[ "$failures" -ne 0 ]]; then
  baseline_fail "Baseline is NOT equivalent to the source chain ($failures failing comparisons)."
fi
printf 'Baseline %s is equivalent to %s files of %s at %s.\n' "${baseline##*/}" "$chain_files" "$source_dir" "$source_ref"
