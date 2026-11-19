#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
mkdir "$workdir/bin"
export NOTARX_ISOLATION_LOG="$workdir/docker-calls"
export NOTARX_ISOLATION_MIGRATIONS
NOTARX_ISOLATION_MIGRATIONS=$(find "$root/migrations" -maxdepth 1 -name '*.sql' | wc -l | tr -d ' ')
cat >"$workdir/bin/docker" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == volume ]]; then
  exit 0
fi
[[ "$1" == compose ]] || exit 10
shift
# Every lifecycle call must override both the exported project variable and
# the checkout .env. Validate the public command boundary before simulating it.
[[ "$1" == --env-file && "$2" == /dev/null && "$3" == --project-name && "$4" == notarx-integration && "$5" == -f && "$6" == *compose.integration.yml ]] || exit 11
shift 6
printf '%s\n' "$*" >>"$NOTARX_ISOLATION_LOG"
if [[ "$1" == port ]]; then
  case "$2:$3" in
    postgres:5432) printf '127.0.0.1:15432\n' ;;
    rustfs:9000) printf '127.0.0.1:19000\n' ;;
    *) exit 12 ;;
  esac
  exit 0
fi
if [[ "$*" == *"PGDATABASE=notarx_foreign_schema_test"* && "$*" == *migrate* ]]; then
  printf 'Database notarx_foreign_schema_test already holds 1 table(s) but no notarx migration ledger.\n' >&2
  exit 1
fi
if [[ "$*" == *--volume* ]]; then
  printf 'Applied migration drift probe has changed\n' >&2
  # Match the real runner's migration filename in the diagnostic.
  printf 'Applied migration %s has changed\n' "${NOTARX_ISOLATION_LATEST}" >&2
  exit 1
fi
if [[ "$*" == *migrate* ]]; then
  for ((i=0; i<NOTARX_ISOLATION_MIGRATIONS; i++)); do
    printf 'Skipping applied migration fixture-%s.sql.\n' "$i"
  done
fi
SH
chmod +x "$workdir/bin/docker"
export NOTARX_ISOLATION_REAL_GO
NOTARX_ISOLATION_REAL_GO=$(command -v go)
cat >"$workdir/bin/go" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == test && "$*" == *TestPostgresRustFSCargoProxyOfficialOfflineReplay* ]]; then
  [[ "$TEST_DATABASE_URL" == 'postgres://notarx:integration-password@127.0.0.1:15432/notarx_test?sslmode=disable' ]] || exit 13
  [[ "$TEST_OBJECT_STORE_ENDPOINT" == 'http://127.0.0.1:19000' && "$CARGO_REQUIRED" == 1 ]] || exit 14
  printf 'official Cargo persistent replay invoked\n' >>"$NOTARX_ISOLATION_LOG"
  exit 0
fi
exec "$NOTARX_ISOLATION_REAL_GO" "$@"
SH
chmod +x "$workdir/bin/go"
export PATH="$workdir/bin:$PATH"
export COMPOSE_PROJECT_NAME=existing-local-service-must-not-be-touched
export NOTARX_ISOLATION_LATEST
NOTARX_ISOLATION_LATEST=$(find "$root/migrations" -maxdepth 1 -name '*.sql' | sort | tail -1)
NOTARX_ISOLATION_LATEST=${NOTARX_ISOLATION_LATEST##*/}
"$root/scripts/integration-test.sh" >/dev/null
make --no-print-directory -C "$root" integration-down >/dev/null
for expected in 'down -v --remove-orphans' 'up -d --wait postgres rustfs' 'run --rm --no-deps test' 'port postgres 5432' 'port rustfs 9000' 'official Cargo persistent replay invoked'; do
  grep -Fq -- "$expected" "$NOTARX_ISOLATION_LOG"
done
printf 'Integration Compose isolation passed with an unrelated exported project name.\n'
