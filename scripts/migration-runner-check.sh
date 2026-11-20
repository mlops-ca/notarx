#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
compose=(docker compose --env-file /dev/null --project-name notarx-integration -f "$root/compose.integration.yml")

second_output=$(cd "$root" && "${compose[@]}" run --rm --no-deps migrate 2>&1)
expected_skips=$(find "$root/migrations/postgres" -maxdepth 1 -type f -name '*.sql' | wc -l | tr -d ' ')
actual_skips=$(printf '%s\n' "$second_output" | grep -c '^Skipping applied migration ' || true)
if [[ "$actual_skips" -ne "$expected_skips" ]] || printf '%s\n' "$second_output" | grep -q '^Applying migration '; then
  printf '%s\n' "$second_output" >&2
  printf 'Migration no-op check failed: expected %s skips, observed %s.\n' "$expected_skips" "$actual_skips" >&2
  exit 1
fi
printf 'Migration no-op check passed: %s applied files were skipped.\n' "$actual_skips"

probe_dir=$(mktemp -d)
foreign_database=notarx_foreign_schema_test
cleanup() {
  "${compose[@]}" exec -T postgres dropdb -U notarx --if-exists "$foreign_database" >/dev/null 2>&1 || true
  rm -rf "$probe_dir"
}
trap cleanup EXIT
latest_migration=$(find "$root/migrations/postgres" -maxdepth 1 -type f -name '*.sql' | sort | tail -n 1)
probe_name=${latest_migration##*/}
cp "$latest_migration" "$probe_dir/$probe_name"
printf '\n-- checksum drift probe\n' >> "$probe_dir/$probe_name"

set +e
drift_output=$(cd "$root" && "${compose[@]}" run --rm --no-deps --volume "$probe_dir:/migrations:ro" migrate 2>&1)
drift_status=$?
set -e
if [[ "$drift_status" -eq 0 ]] || ! printf '%s\n' "$drift_output" | grep -Fq "Applied migration $probe_name has changed"; then
  printf '%s\n' "$drift_output" >&2
  printf 'Migration checksum-drift check failed for %s.\n' "$probe_name" >&2
  exit 1
fi
printf 'Migration checksum-drift check passed for %s.\n' "$probe_name"

# A database that holds tables but no notarx ledger (another schema) is refused
# with a clear message instead of failing in the middle of the baseline.
"${compose[@]}" exec -T postgres createdb -U notarx "$foreign_database"
"${compose[@]}" exec -T postgres psql -X -U notarx -d "$foreign_database" -v ON_ERROR_STOP=1 -c 'CREATE TABLE hosted_repositories (id integer)' >/dev/null
set +e
foreign_output=$(cd "$root" && "${compose[@]}" run --rm --no-deps --env "PGDATABASE=$foreign_database" migrate 2>&1)
foreign_status=$?
set -e
if [[ "$foreign_status" -eq 0 ]] || ! printf '%s\n' "$foreign_output" | grep -Fq 'no notarx migration ledger'; then
  printf '%s\n' "$foreign_output" >&2
  printf 'Migration foreign-schema check failed.\n' >&2
  exit 1
fi
printf 'Migration foreign-schema check passed: a database without a notarx ledger is refused.\n'
