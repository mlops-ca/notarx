#!/usr/bin/env bash
# Generates the single baseline migration from a migration chain (ADR 0015,
# "Generation procedure"). Kept until v0.1.0 is tagged, then removed with the
# re-squash.
#
# Usage: scripts/baseline-generate.sh [--source-ref REF] [--source-dir DIR]
#                                     [--output FILE] [--pg-image IMAGE]
#
#   --source-ref   git ref holding the source chain (default: HEAD). At the
#                  cut-over this is the last commit with the inherited chain;
#                  at the re-squash it is the commit whose migrations/ is the
#                  current chain.
#   --source-dir   directory of the chain inside that ref (default: migrations)
#   --output       baseline file to write (default:
#                  migrations-next/000001_baseline.sql)
#   --pg-image     PostgreSQL image pinned by sha256 digest, used for the server
#                  and for pg_dump (default: the digest pinned in
#                  scripts/baseline-lib.sh)
#
# Requires docker (or CONTAINER_ENGINE=podman), tar and go. The chain is applied
# by scripts/run-migrations.sh; the dump is normalised by
# internal/tools/baselinefmt. The output is committed, never generated at build
# time.
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/baseline-lib.sh"

source_ref=HEAD
source_dir=migrations
output=$baseline_root/migrations-next/000001_baseline.sql

while [[ $# -gt 0 ]]; do
  case "$1" in
    --source-ref) source_ref=${2:?--source-ref needs a value}; shift 2 ;;
    --source-dir) source_dir=${2:?--source-dir needs a value}; shift 2 ;;
    --output) output=${2:?--output needs a value}; shift 2 ;;
    --pg-image) BASELINE_PG_IMAGE=${2:?--pg-image needs a value}; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo/p' "$0" | sed '$d;s/^# \{0,1\}//'; exit 0 ;;
    *) baseline_fail "Unknown argument: $1" ;;
  esac
done

baseline_init
source_dir=${source_dir%/}
mkdir -p "$baseline_work/source"
git -C "$baseline_root" archive "$source_ref" -- "$source_dir" | tar -x -C "$baseline_work/source"
chain=$baseline_work/source/$source_dir
[[ -d "$chain" ]] || baseline_fail "No $source_dir directory at $source_ref."
compgen -G "$chain/*.sql" >/dev/null || baseline_fail "No *.sql files in $source_dir at $source_ref."

baseline_start_server source
baseline_migrate source "$chain"
baseline_dump source "${BASELINE_DUMP_SCHEMA_FLAGS[@]}" >"$baseline_work/dump.sql"

mkdir -p "$(dirname "$output")"
(cd "$baseline_root" && go run ./internal/tools/baselinefmt/cmd/baselinefmt) <"$baseline_work/dump.sql" >"$baseline_work/baseline.sql"
mv "$baseline_work/baseline.sql" "$output"
printf 'Wrote %s from %s:%s (%s files).\n' "$output" "$source_ref" "$source_dir" "$(find "$chain" -maxdepth 1 -name '*.sql' | wc -l | tr -d ' ')"
