#!/usr/bin/env bash
# The tagged integration suite against one migrated PostgreSQL and RustFS stack.
#
#   scripts/integration-test.sh             every package (both shards, one run)
#   scripts/integration-test.sh --shard N   only shard N of SHARDS below (CI runs each
#                                           shard as its own contract lane, docs/ci.md)
#   scripts/integration-test.sh --list      print "<shard> <package>" for every package
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

# Every integration package belongs to exactly one shard. The packages share one
# database, so a shard runs them one at a time (-p 1); the shards are balanced by
# their measured test time (CI run 44640058817: shard 1 about 3.4 minutes of tests,
# shard 2 about 3.3 minutes). The official Cargo replay below runs with shard 1,
# which holds ./internal/app.
shard_1=(./internal/app ./internal/repository ./internal/aptpublication ./internal/auditwriter
  ./internal/partitions ./internal/retention ./internal/objectstore ./cmd/notarx)
shard_2=(./internal/backupops ./internal/nexusmigration/pgstore ./internal/nexussource/pgstore
  ./internal/secrets/reseal ./internal/repositorypreset/pgstore ./internal/format/nuget
  ./internal/format/helm ./internal/format/rubygems ./internal/rubygemspublication/pgstore
  ./internal/preflight)
SHARDS=2

shard=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --list)
      printf '1 %s\n' "${shard_1[@]}"
      printf '2 %s\n' "${shard_2[@]}"
      exit 0 ;;
    --shard)
      [[ $# -ge 2 ]] || { printf 'integration-test: --shard needs a number\n' >&2; exit 2; }
      shard=$2; shift 2 ;;
    *) printf 'integration-test: unknown argument %s\n' "$1" >&2; exit 2 ;;
  esac
done
case "$shard" in
  '') packages=("${shard_1[@]}" "${shard_2[@]}"); cargo=1 ;;
  1) packages=("${shard_1[@]}"); cargo=1 ;;
  2) packages=("${shard_2[@]}"); cargo=0 ;;
  *) printf 'integration-test: --shard must be 1 to %s, not %s\n' "$SHARDS" "$shard" >&2; exit 2 ;;
esac

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
