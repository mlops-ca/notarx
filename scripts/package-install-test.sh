#!/usr/bin/env bash
# Container install test of the native packages (ADR 0025). Needs Docker and the
# packages built for this host's architecture (scripts/package-check.sh --output DIR
# writes them under DIR/<version>/).
#
#   scripts/package-install-test.sh --packages DIR [--images debian:12@<digest>,rockylinux:9@<digest>]
#
# For every image: install notarx and notarxctl (and notarx-console when present) from the local files,
# check the service account, that the unit is installed but not enabled, the config
# file mode, systemd-analyze verify on the unit, then run notarx-migrate against a
# PostgreSQL 16 container: --check fails before, apply succeeds, --check passes after,
# a second run is a no-op and a modified packaged migration is refused.
set -euo pipefail

packages=
# Pinned by multi-arch index digest like every other external input of the release pipeline;
# bump the digests together with the tags when the base images move.
images=debian:12@sha256:268fb24c39bca92d326dd83ea3abebe4a3f7751c804ffef23bb6d857dd343ff8,rockylinux:9@sha256:03303863808e9cd79f0c58c833db3e5f9cde9f2abe16804a267b05dc54af86e4
postgres_image=${PACKAGE_TEST_POSTGRES_IMAGE:-postgres:16@sha256:3950ad7c3148ca2175ef1b6c36c9efbf78957ae8fbe8b3a5d7c35af50500382f}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --packages) packages=${2:-}; shift 2 ;;
    --images) images=${2:-}; shift 2 ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done
[[ -d "$packages" ]] || { printf 'usage: package-install-test.sh --packages DIR [--images a,b]\n' >&2; exit 2; }
packages=$(cd "$packages" && pwd)
command -v docker >/dev/null 2>&1 || { printf 'package-install-test: docker is required\n' >&2; exit 1; }

case "$(uname -m)" in
  x86_64) deb_arch=amd64 rpm_arch=x86_64 ;;
  aarch64|arm64) deb_arch=arm64 rpm_arch=aarch64 ;;
  *) printf 'package-install-test: unsupported architecture %s\n' "$(uname -m)" >&2; exit 1 ;;
esac

suffix=$$
network=notarx-pkg-test-$suffix
database=notarx-pkg-test-pg-$suffix
password=package-test-password
cleanup() {
  docker rm -f "$database" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker network create "$network" >/dev/null
docker run -d --name "$database" --network "$network" \
  -e POSTGRES_DB=notarx -e POSTGRES_USER=notarx -e POSTGRES_PASSWORD="$password" \
  "$postgres_image" >/dev/null
for _ in $(seq 1 60); do
  # The official image restarts once after initialisation; only the TCP check is conclusive.
  if docker exec "$database" pg_isready -h 127.0.0.1 -U notarx -d notarx >/dev/null 2>&1; then break; fi
  sleep 1
done
docker exec "$database" pg_isready -h 127.0.0.1 -U notarx -d notarx >/dev/null

# Runs inside the container as root. $1 is deb or rpm.
read -r -d '' guest <<'GUEST' || true
set -eu
kind=$1
cd /packages
case "$kind" in
  deb)
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq ./*.deb systemd >/dev/null
    ;;
  rpm)
    dnf install -y -q ./*.rpm systemd >/dev/null
    ;;
esac

id notarx >/dev/null
getent group notarx >/dev/null
test -x /usr/bin/notarx && test -x /usr/bin/notarxctl && test -x /usr/bin/notarx-migrate
test -f /usr/lib/systemd/system/notarx.service
# The console package is built once (amd64 leg), so it is installed only when it was supplied.
if ls /packages/notarx-console* >/dev/null 2>&1; then
  test -f /usr/share/notarx/console/index.html
  test -f /usr/share/doc/notarx-console/examples/nginx.conf
fi
# Installed, not enabled, not started.
if ls /etc/systemd/system/*.wants/notarx.service >/dev/null 2>&1; then echo 'notarx.service is enabled' >&2; exit 1; fi
test "$(stat -c '%a %U:%G' /etc/notarx/notarx.env)" = '640 root:notarx'
command -v psql >/dev/null || { echo 'psql was not installed through the Recommends' >&2; exit 1; }
systemd-analyze verify /usr/lib/systemd/system/notarx.service
notarx version | grep -F 'notarx '
notarxctl version | grep -F 'notarxctl '

printf 'NOTARX_DATABASE_URL=%s\n' "$DATABASE_URL" >> /etc/notarx/notarx.env
baseline_count=$(find /usr/share/notarx/migrations -name '*.sql' | wc -l)

if notarx-migrate --check >/dev/null 2>&1; then echo 'check passed before any migration' >&2; exit 1; fi
notarx-migrate
notarx-migrate --check
applied=$(psql -X -At -d "$DATABASE_URL" -c 'SELECT count(*) FROM notarx_schema_migrations')
test "$applied" = "$baseline_count"
notarx-migrate | grep -c '^Skipping applied migration ' | grep -qx "$baseline_count"

# A changed packaged migration is refused, and the check reports the mismatch.
echo '-- drift' >> /usr/share/notarx/migrations/000001_baseline.sql
if out=$(notarx-migrate 2>&1); then echo 'a checksum mismatch was not refused' >&2; exit 1; fi
printf '%s\n' "$out" | grep -F 'Applied migration 000001_baseline.sql has changed'
if notarx-migrate --check >/dev/null 2>&1; then echo 'check passed on a checksum mismatch' >&2; exit 1; fi
echo guest-ok
GUEST

status=0
IFS=, read -r -a image_list <<<"$images"
for image in "${image_list[@]}"; do
  case "$image" in
    debian*|ubuntu*) kind=deb ;;
    *) kind=rpm ;;
  esac
  staging=$(mktemp -d)
  if [[ "$kind" == deb ]]; then
    cp "$packages"/notarx_*_"$deb_arch".deb "$packages"/notarxctl_*_"$deb_arch".deb "$staging"/
    cp "$packages"/notarx-console_*_all.deb "$staging"/ 2>/dev/null || true
  else
    cp "$packages"/notarx-[0-9]*."$rpm_arch".rpm "$packages"/notarxctl-[0-9]*."$rpm_arch".rpm "$staging"/
    cp "$packages"/notarx-console-[0-9]*.noarch.rpm "$staging"/ 2>/dev/null || true
  fi
  printf 'install test on %s\n' "$image"
  if ! docker run --rm --network "$network" -v "$staging:/packages:ro" \
    -e DATABASE_URL="postgres://notarx:$password@$database:5432/notarx?sslmode=disable" \
    "$image" bash -c "$guest" _ "$kind"; then
    printf 'FAIL: install test on %s\n' "$image" >&2
    status=1
  fi
  rm -rf "$staging"
  # Each image starts from an empty database again.
  docker exec "$database" psql -U notarx -d postgres -v ON_ERROR_STOP=1 -c 'DROP DATABASE notarx WITH (FORCE)' -c 'CREATE DATABASE notarx' >/dev/null
done
exit "$status"
