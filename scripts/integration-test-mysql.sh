#!/usr/bin/env bash
# The tagged integration suite against one migrated mysql:8.4 (ADR 0022, slice 5), run by
# scripts/integration-test.sh --driver mysql and the core-integration-mysql lane.
#
#   scripts/integration-test-mysql.sh         run every package of scripts/integration-packages-mysql.txt
#   scripts/integration-test-mysql.sh --list  print the packages
#
# The stack is the MySQL service of compose.integration.yml under the project name of the PostgreSQL
# run. The database is migrated by the Go runner from migrations/mysql, as a deployment would
# migrate it. TEST_DATABASE_DRIVER=mysql selects the engine for the dialect-parametrised suites,
# TEST_DATABASE_URL is left empty so the PostgreSQL-only suites skip themselves, and
# TEST_DATABASE_REQUIRED=1 turns a suite that finds no database into a failure. The run is verbose, so
# the lock contention measurement of internal/dialect/mysql reaches the job log.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

packages=$(grep -Ev '^[[:space:]]*(#|$)' scripts/integration-packages-mysql.txt)
if [[ ${1:-} == --list ]]; then
  printf '%s\n' "$packages"
  exit 0
fi
[[ $# -eq 0 ]] || { printf 'integration-test-mysql: unknown argument %s\n' "$1" >&2; exit 2; }

compose=(docker compose --env-file /dev/null --project-name notarx-integration -f compose.integration.yml)
cleanup() {
  "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker volume create notarx-go-mod >/dev/null
docker volume create notarx-go-build >/dev/null
cleanup
"${compose[@]}" up -d --wait mysql

mysql_url='mysql://root:integration-password@mysql:3306/notarx_test?tls=required'
"${compose[@]}" run --rm --no-deps --env NOTARX_DATABASE_DRIVER=mysql --env "NOTARX_DATABASE_URL=$mysql_url" test \
  go run ./cmd/notarx migrate --dir migrations/mysql

selected=()
while IFS= read -r package; do selected+=("$package"); done <<<"$packages"
"${compose[@]}" run --rm --no-deps \
  --env TEST_DATABASE_DRIVER=mysql --env TEST_DATABASE_URL= --env TEST_DATABASE_REQUIRED=1 test sh -ec '
  go test -count=1 -tags=integration -exec true "$@" >/dev/null
  go test -count=1 -p 1 -v -tags=integration "$@"
' integration-test-mysql "${selected[@]}"
