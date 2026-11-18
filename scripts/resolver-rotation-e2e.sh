#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd); cd "$root"
env_file=${NOTARX_ENV_FILE:-.env}; test -f "$env_file"
port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }
project="notarx-rotate-${RANDOM}-${RANDOM}"; isolated=$(mktemp)
notarx_port=$(port); rustfs_api=$(port); rustfs_console=$(port)
awk -F= '$1 != "NOTARX_HTTP_PORT" && $1 != "NOTARX_POSTGRES_PORT" && $1 != "RUSTFS_API_PORT" && $1 != "RUSTFS_CONSOLE_PORT" && $1 != "NOTARX_RESOLVER_TOKEN" && $1 != "NOTARX_REPOSITORY_READERS" {print}' "$env_file" > "$isolated"
printf 'NOTARX_HTTP_PORT=%s\nNOTARX_POSTGRES_PORT=%s\nRUSTFS_API_PORT=%s\nRUSTFS_CONSOLE_PORT=%s\nNOTARX_RESOLVER_TOKEN=resolver-before-rotation\nNOTARX_REPOSITORY_READERS=fixture=rotation-missing\n' "$notarx_port" "$(port)" "$rustfs_api" "$rustfs_console" >> "$isolated"
compose() { COMPOSE_PROJECT_NAME="$project" docker compose --env-file "$isolated" -f compose.yml "$@"; }
trap 'compose down -v --remove-orphans >/dev/null 2>&1 || true; rm -f "$isolated"' EXIT
base="http://127.0.0.1:${notarx_port}"
token() { curl --silent --show-error --fail --user "$1:$2" "$base/auth/token" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p'; }
expect() { local want=$1; shift; local got; got=$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' "$@"); [[ "$got" == "$want" ]] || { printf 'HTTP %s, want %s for %s\n' "$got" "$want" "$*" >&2; exit 1; }; }
compose up -d ${NOTARX_COMPOSE_BUILD_FLAG---build} --wait
old_token=$(token fixture resolver-before-rotation); test -n "$old_token"
expect 404 -H "Authorization: Bearer $old_token" "$base/v2/rotation-missing/manifests/latest"
awk -F= '$1 != "NOTARX_RESOLVER_TOKEN" {print}' "$isolated" > "$isolated.next"
printf 'NOTARX_RESOLVER_TOKEN=resolver-after-rotation\n' >> "$isolated.next"; mv "$isolated.next" "$isolated"
compose up -d ${NOTARX_COMPOSE_BUILD_FLAG---build} --force-recreate --no-deps notarx --wait
expect 401 -H "Authorization: Bearer $old_token" "$base/v2/rotation-missing/manifests/latest"
new_token=$(token fixture resolver-after-rotation); test -n "$new_token"
expect 404 -H "Authorization: Bearer $new_token" "$base/v2/rotation-missing/manifests/latest"
printf '%s\n' 'Resolver rotation E2E passed: old bearer rejected after restart and new bearer accepted.'
