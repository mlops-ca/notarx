#!/usr/bin/env bash
set -euo pipefail

environment_file=${NOTARX_ENV_FILE:-.env}
backup_dir=${1:?usage: scripts/restore-drill.sh <backup-directory>}
object_driver=${NOTARX_OBJECT_STORE_DRIVER:-s3}
compose_file=${NOTARX_COMPOSE_FILE:-compose.yml}
compose=(docker compose --env-file "$environment_file" -f "$compose_file")

case "$object_driver" in
  s3) ;;
  filesystem)
    fs_root=${NOTARX_OBJECT_STORE_FS_ROOT:?set NOTARX_OBJECT_STORE_FS_ROOT to the filesystem object root}
    [[ -r "$fs_root" && -w "$fs_root" && -x "$fs_root" ]] || { printf '%s\n' "$fs_root is not readable and writable by this user; run the restore as the Gateway's user or root." >&2; exit 1; }
    if ! "${compose[@]}" config 2>/dev/null | grep -Eq 'NOTARX_OBJECT_STORE_DRIVER: "?filesystem"?'; then
      printf '%s\n' "$compose_file does not configure the notarx service with NOTARX_OBJECT_STORE_DRIVER=filesystem." >&2
      exit 1
    fi
    test -f "$backup_dir/objects-fs.tar" || { printf '%s\n' "$backup_dir has no objects-fs.tar; was it made with NOTARX_OBJECT_STORE_DRIVER=filesystem?" >&2; exit 1; }
    ;;
  *) printf 'Unsupported NOTARX_OBJECT_STORE_DRIVER %s (use s3 or filesystem).\n' "$object_driver" >&2; exit 1 ;;
esac
shasum -a 256 --check "$backup_dir/SHA256SUMS"

if [[ "$object_driver" == s3 ]]; then
  "${compose[@]}" stop notarx rustfs
else
  "${compose[@]}" stop notarx
fi
# pg_restore --clean drops each partition's inherited primary key constraint and
# fails on partitioned tables (the audit tables, ADR 0023), so reset the schema
# explicitly and restore into the empty database.
"${compose[@]}" exec -T postgres \
  psql -U notarx -d notarx -v ON_ERROR_STOP=1 -c 'DROP SCHEMA public CASCADE; CREATE SCHEMA public;'
"${compose[@]}" exec -T postgres \
  pg_restore -U notarx -d notarx --exit-on-error <"$backup_dir/notarx.dump"
if [[ "$object_driver" == s3 ]]; then
  rustfs_container=$("${compose[@]}" ps -aq rustfs)
  test -n "$rustfs_container"
  "${compose[@]}" run --rm --no-deps --entrypoint sh rustfs \
    -ec 'find /data -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +'
  docker cp - "$rustfs_container:/data" <"$backup_dir/rustfs-data.tar"
  docker start "$rustfs_container" >/dev/null
  if ! "${compose[@]}" run --rm --no-deps rustfs-ready; then
    printf 'Restored RustFS did not become healthy before the readiness deadline.\n' >&2
    "${compose[@]}" ps rustfs >&2 || true
    "${compose[@]}" logs --tail=50 rustfs >&2 || true
    exit 1
  fi
else
  # Replace the root's contents, marker included: the restored database
  # records the root ID that is inside the archive.
  find "$fs_root" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
  tar -C "$fs_root" -xf "$backup_dir/objects-fs.tar"
fi
# Starting through Compose re-evaluates the completed migrate dependency on
# some Compose releases. The restored dump already contains its schema, so
# restart only the existing Gateway container.
notarx_container=$("${compose[@]}" ps -aq notarx)
test -n "$notarx_container"
docker start "$notarx_container" >/dev/null
ready_status=000
notarx_url=
for _ in $(seq 1 80); do
  notarx_binding=$("${compose[@]}" port notarx 8080 | tail -n 1)
  if [[ -n "$notarx_binding" ]]; then
    notarx_url="http://127.0.0.1:${notarx_binding##*:}"
    ready_status=$(curl --noproxy '*' --silent --show-error --output /dev/null --write-out '%{http_code}' \
      --max-time 2 "$notarx_url/readyz" 2>/dev/null || true)
    [[ "$ready_status" == 204 ]] && break
  fi
  sleep 0.25
done
[[ "$ready_status" == 204 ]] || { printf 'Restored Gateway readiness returned HTTP %s at %s.\n' "$ready_status" "${notarx_url:-unresolved endpoint}" >&2; exit 1; }
printf 'Restore completed from: %s\n' "$backup_dir"
