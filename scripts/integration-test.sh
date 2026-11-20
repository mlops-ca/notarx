#!/usr/bin/env bash
# The tagged integration suite against one migrated PostgreSQL and RustFS stack.
#
#   scripts/integration-test.sh             every package (every shard, one run)
#   scripts/integration-test.sh --shard N   only shard N of scripts/integration-packages.txt
#                                           (CI runs each shard as its own contract lane,
#                                           docs/ci.md)
#   scripts/integration-test.sh --list      print "<shard> <package>" for every package
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

# The package list and its shards live in scripts/integration-packages.txt.
assignments=$(grep -Ev '^[[:space:]]*(#|$)' scripts/integration-packages.txt)

shard=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --list)
      printf '%s\n' "$assignments"
      exit 0 ;;
    --shard)
      [[ $# -ge 2 ]] || { printf 'integration-test: --shard needs a number\n' >&2; exit 2; }
      shard=$2; shift 2 ;;
    *) printf 'integration-test: unknown argument %s\n' "$1" >&2; exit 2 ;;
  esac
done
if [[ -n "$shard" ]] && ! awk -v shard="$shard" '$1 == shard { found = 1 } END { exit !found }' <<<"$assignments"; then
  printf 'integration-test: scripts/integration-packages.txt has no shard %s\n' "$shard" >&2
  exit 2
fi
packages=()
while read -r package_shard package; do
  if [[ -z "$shard" || "$package_shard" == "$shard" ]]; then packages+=("$package"); fi
done <<<"$assignments"
# The official Cargo replay belongs to the shard that holds ./internal/app.
app_shard=$(awk '$2 == "./internal/app" { print $1 }' <<<"$assignments")
cargo=0
if [[ -z "$shard" || "$shard" == "$app_shard" ]]; then cargo=1; fi

compose=(docker compose --env-file /dev/null --project-name notarx-integration -f compose.integration.yml)
cleanup() {
  "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker volume create notarx-go-mod >/dev/null
docker volume create notarx-go-build >/dev/null
cleanup
"${compose[@]}" up -d --wait postgres rustfs
"${compose[@]}" run --rm --no-deps rustfs-ready
"${compose[@]}" run --rm --no-deps migrate
./scripts/migration-runner-check.sh
# -p 1 also serializes compilation, so the test binaries are built first with full
# parallelism (-exec true builds and links them but runs nothing). The second go test
# finds every compiled package in the build cache, so it only relinks or reuses each
# test binary (seconds: ./internal/app took 44 s cold and 4 s warm) before running the
# packages one at a time.
"${compose[@]}" run --rm --no-deps test sh -ec '
  go test -count=1 -tags=integration -exec true "$@" >/dev/null
  go test -count=1 -p 1 -tags=integration "$@"
' integration-test "${packages[@]}"

[[ "$cargo" == 1 ]] || exit 0
# The Go test container has no Cargo binary. Run the pinned official client on
# the host against the same migrated PostgreSQL and RustFS services.
postgres_address=$("${compose[@]}" port postgres 5432)
rustfs_address=$("${compose[@]}" port rustfs 9000)
TEST_DATABASE_URL="postgres://notarx:integration-password@${postgres_address}/notarx_test?sslmode=disable" \
TEST_OBJECT_STORE_ENDPOINT="http://${rustfs_address}" \
TEST_OBJECT_STORE_ACCESS_KEY=integration-rustfs \
TEST_OBJECT_STORE_SECRET_KEY=integration-password \
CARGO_REQUIRED=1 go test -count=1 -tags=integration ./internal/app -run '^TestPostgresRustFSCargoProxyOfficialOfflineReplay$'
