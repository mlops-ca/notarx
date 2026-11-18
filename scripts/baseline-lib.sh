#!/usr/bin/env bash
# Shared helpers for scripts/baseline-generate.sh and
# scripts/baseline-equivalence-check.sh (ADR 0015 slices 2 and 3).
#
# Every database in the generator and the equivalence proof runs on, and is
# dumped by, the same pinned PostgreSQL image, so both tools see the same
# pg_dump output format. Override BASELINE_PG_IMAGE only deliberately and keep
# the digest suffix.
#
# The container engine is docker unless CONTAINER_ENGINE names a compatible
# one (podman).

baseline_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

# postgres:18-alpine, multi-architecture index digest. PostgreSQL 16 stays in the
# support matrix (docs/postgresql-capabilities.md): the generator output is
# version independent (internal/tools/baselinefmt tests a 16 and an 18 dump) and
# the baseline-equivalence workflow runs the proof on 16 as well. To use 16 by
# hand, pass --pg-image postgres:16-alpine@sha256:81e77a4833487b8132fe4b00bedd39dc3f74b8641e387f19312a48f7de218c2e.
BASELINE_PG_IMAGE=${BASELINE_PG_IMAGE:-postgres:18-alpine@sha256:b6972e1115a4ff8eba5d7f7d19184926d1e16d7d1136ae1e1a7ebf286da0f015}
engine=${CONTAINER_ENGINE:-docker}

# pg_dump flags shared by the generator and the equivalence proof. The proof
# adds nothing to the schema flags except what is listed in its own script.
BASELINE_DUMP_SCHEMA_FLAGS=(--schema-only --no-owner --no-privileges --no-comments --no-tablespaces --no-publications --no-subscriptions)

baseline_server=""
baseline_work=""

baseline_fail() {
  printf '%s\n' "$*" >&2
  exit 1
}

baseline_cleanup() {
  if [[ -n "$baseline_server" ]]; then
    "$engine" rm -f "$baseline_server" >/dev/null 2>&1 || true
  fi
  if [[ -n "$baseline_work" ]]; then
    rm -rf "$baseline_work"
  fi
}

baseline_init() {
  command -v "$engine" >/dev/null 2>&1 || baseline_fail "Container engine '$engine' not found; set CONTAINER_ENGINE."
  case "$BASELINE_PG_IMAGE" in
    *@sha256:????????????????????????????????????????????????????????????????) ;;
    *) baseline_fail "BASELINE_PG_IMAGE must be pinned by sha256 digest: $BASELINE_PG_IMAGE" ;;
  esac
  baseline_work=$(mktemp -d)
  trap baseline_cleanup EXIT
}

# baseline_start_server starts one disposable PostgreSQL server and creates the
# databases named as arguments (role "notarx", trust authentication, reachable
# only from containers that join its network namespace).
baseline_start_server() {
  baseline_server="notarx-baseline-$$-$RANDOM"
  "$engine" run -d --name "$baseline_server" \
    -e POSTGRES_USER=notarx -e POSTGRES_DB=postgres -e POSTGRES_HOST_AUTH_METHOD=trust \
    "$BASELINE_PG_IMAGE" postgres -c shared_preload_libraries=pg_stat_statements >/dev/null
  local attempt
  for attempt in $(seq 1 60); do
    if "$engine" exec "$baseline_server" pg_isready -h 127.0.0.1 -U notarx -d postgres >/dev/null 2>&1; then
      # The image restarts the server once after initialisation; require two
      # consecutive successful probes.
      sleep 1
      if "$engine" exec "$baseline_server" pg_isready -h 127.0.0.1 -U notarx -d postgres >/dev/null 2>&1; then
        break
      fi
    fi
    sleep 1
    if [[ "$attempt" -eq 60 ]]; then
      baseline_fail 'PostgreSQL did not become ready.'
    fi
  done
  local database
  for database in "$@"; do
    "$engine" exec "$baseline_server" psql -X -h 127.0.0.1 -U notarx -d postgres -v ON_ERROR_STOP=1 \
      -c "CREATE DATABASE $database" >/dev/null
  done
}

# baseline_migrate DATABASE DIRECTORY applies every *.sql file of DIRECTORY with
# the repository's own runner.
baseline_migrate() {
  local database=$1 directory=$2
  "$engine" run --rm --network "container:$baseline_server" \
    -e PGHOST=127.0.0.1 -e PGUSER=notarx -e "PGDATABASE=$database" \
    -v "$directory:/migrations:ro" \
    -v "$baseline_root/scripts/run-migrations.sh:/usr/local/bin/run-migrations:ro" \
    --entrypoint /usr/local/bin/run-migrations \
    "$BASELINE_PG_IMAGE" >/dev/null
}

# baseline_psql DATABASE [psql arguments...] reads SQL from stdin or arguments.
baseline_psql() {
  local database=$1
  shift
  "$engine" exec -i "$baseline_server" psql -X -h 127.0.0.1 -U notarx -d "$database" -v ON_ERROR_STOP=1 "$@"
}

# baseline_dump DATABASE [pg_dump arguments...] runs the pinned pg_dump.
baseline_dump() {
  local database=$1
  shift
  "$engine" exec "$baseline_server" pg_dump -h 127.0.0.1 -U notarx -d "$database" "$@"
}
