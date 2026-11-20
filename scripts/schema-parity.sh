#!/usr/bin/env bash
# Schema parity (ADR 0022 slice 3b): compares the neutral catalogue, migrations/catalogue.yaml,
# with the information_schema of a migrated PostgreSQL and, once migrations/mysql holds a
# baseline, of a migrated MySQL. Fails on any drift: a missing or retyped column, a changed
# nullability, key or foreign key, a generated column, or a seed row.
#
#   scripts/schema-parity.sh        (make schema-parity)
#
# It starts its own PostgreSQL (and MySQL) from compose.integration.yml under a project name
# of its own, so it never touches the integration stack or a development database.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

compose=(docker compose --env-file /dev/null --project-name notarx-schema-parity -f compose.integration.yml)
cleanup() {
  "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker volume create notarx-go-mod >/dev/null
docker volume create notarx-go-build >/dev/null
cleanup

services=(postgres)
mysql=0
if compgen -G 'migrations/mysql/*.sql' >/dev/null; then
  mysql=1
  services+=(mysql)
fi
"${compose[@]}" up -d --wait "${services[@]}"
"${compose[@]}" run --rm --no-deps migrate

args=(go run ./tools/schemaparity check -catalogue migrations/catalogue.yaml)
if [[ "$mysql" == 1 ]]; then
  mysql_url='mysql://root:integration-password@mysql:3306/notarx_test?tls=required'
  "${compose[@]}" run --rm --no-deps --env NOTARX_DATABASE_DRIVER=mysql --env "NOTARX_DATABASE_URL=$mysql_url" test \
    go run ./cmd/notarx migrate --dir migrations/mysql
  args+=(-mysql-url "$mysql_url")
fi
"${compose[@]}" run --rm --no-deps test "${args[@]}"
