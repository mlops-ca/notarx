#!/usr/bin/env bash
set -euo pipefail

environment_file=${NOTARX_ENV_FILE:-.env}
backup_dir=${1:-.artifacts/backup-drill/$(date -u +%Y%m%dT%H%M%SZ)}
# The object store the Gateway runs on (ADR 0017): s3 (the Compose RustFS) or
# filesystem, whose root directory on this host is NOTARX_OBJECT_STORE_FS_ROOT.
object_driver=${NOTARX_OBJECT_STORE_DRIVER:-s3}
compose_file=${NOTARX_COMPOSE_FILE:-compose.yml}
compose=(docker compose --env-file "$environment_file" -f "$compose_file")
mkdir -p "$backup_dir"

case "$object_driver" in
  s3) ;;
  filesystem)
    fs_root=${NOTARX_OBJECT_STORE_FS_ROOT:?set NOTARX_OBJECT_STORE_FS_ROOT to the filesystem object root}
    test -f "$fs_root/.notarx-root" || { printf '%s\n' "$fs_root is not an initialised filesystem object root." >&2; exit 1; }
    # The root is owned by the Gateway's user with mode 0700: run this as that
    # user (or root) so the archive can read it.
    [[ -r "$fs_root" && -x "$fs_root" ]] || { printf '%s\n' "$fs_root is not readable by this user." >&2; exit 1; }
    # The scripts only back up what the Gateway really uses: its Compose service
    # must be configured for the filesystem driver (compose.yml itself runs on S3;
    # set NOTARX_COMPOSE_FILE to a Compose file whose gateway service uses it).
    if ! "${compose[@]}" config 2>/dev/null | grep -Eq 'NOTARX_OBJECT_STORE_DRIVER: "?filesystem"?'; then
      printf '%s\n' "$compose_file does not configure the gateway service with NOTARX_OBJECT_STORE_DRIVER=filesystem." >&2
      exit 1
    fi
    ;;
  *) printf 'Unsupported NOTARX_OBJECT_STORE_DRIVER %s (use s3 or filesystem).\n' "$object_driver" >&2; exit 1 ;;
esac

gateway_container=$("${compose[@]}" ps -aq gateway)
test -n "$gateway_container"
if [[ "$object_driver" == s3 ]]; then
  rustfs_container=$("${compose[@]}" ps -aq rustfs)
  test -n "$rustfs_container"
fi
services_stopped=0
resume_services() {
  if [[ "$object_driver" == s3 ]]; then
    docker start "$rustfs_container" >/dev/null
    "${compose[@]}" run --rm --no-deps rustfs-ready
  fi
  docker start "$gateway_container" >/dev/null
}
cleanup() {
  local status=$?
  if [[ "$services_stopped" == 1 ]]; then
    resume_services || true
  fi
  exit "$status"
}
trap cleanup EXIT

"${compose[@]}" stop gateway
services_stopped=1
"${compose[@]}" exec -T postgres \
  pg_dump -U gateway -d gateway --format=custom >"$backup_dir/gateway.dump"

if [[ "$object_driver" == s3 ]]; then
  # Stop the RustFS writer before streaming its volume. This archive is supported
  # only for the pinned RustFS baseline used by the running stack.
  "${compose[@]}" stop rustfs
  docker cp "$rustfs_container:/data/." - >"$backup_dir/rustfs-data.tar"
  artifacts=(gateway.dump rustfs-data.tar)
else
  # The Gateway is stopped, so the root is quiescent. The archive keeps the
  # .notarx-root marker, which the restored database's recorded object store
  # identity must match; tmp/ holds only abandoned in-flight writes.
  tar -C "$fs_root" --exclude=./tmp -cf "$backup_dir/objects-fs.tar" .
  artifacts=(gateway.dump objects-fs.tar)
fi
resume_services
services_stopped=0
trap - EXIT

shasum -a 256 "${artifacts[@]/#/$backup_dir/}" >"$backup_dir/SHA256SUMS"
printf 'Backup created: %s\n' "$backup_dir"
