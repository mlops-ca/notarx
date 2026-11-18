#!/usr/bin/env bash
# Fixture test for the filesystem branch of scripts/backup-drill.sh and
# scripts/restore-drill.sh (ADR 0017): a fake docker and curl stand in for the
# Compose stack, a real directory stands in for the object root.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
mkdir "$workdir/bin"
export DRILL_LOG="$workdir/calls"

cat >"$workdir/bin/docker" <<'SH'
#!/usr/bin/env bash
printf 'docker %s\n' "$*" >>"$DRILL_LOG"
[[ "$1" == compose ]] || exit 0
shift
while [[ "$1" != ps && "$1" != config && "$1" != stop && "$1" != exec && "$1" != port && "$1" != run && "$1" != logs ]]; do shift; done
case "$1" in
  ps) printf 'container-%s\n' "${@: -1}" ;;
  exec) printf 'FAKE-PG-DUMP\n' ;;
  config)
    if [[ -n "${DRILL_NO_FS_CONFIG:-}" ]]; then printf 'services:\n  gateway:\n    environment:\n      NOTARX_OBJECT_STORE_ENDPOINT: http://rustfs:9000\n'; else printf 'services:\n  gateway:\n    environment:\n      NOTARX_OBJECT_STORE_DRIVER: filesystem\n'; fi ;;
  port) printf '127.0.0.1:18999\n' ;;
esac
exit 0
SH
cat >"$workdir/bin/curl" <<'SH'
#!/usr/bin/env bash
printf '204'
SH
chmod +x "$workdir/bin/docker" "$workdir/bin/curl"
export PATH="$workdir/bin:$PATH"

fs_root="$workdir/objects"
mkdir -p "$fs_root/data/native/raw" "$fs_root/meta/native/raw" "$fs_root/tmp"
printf '%s\n' 5e1625e7-9c7b-42b8-9b83-c9a1fc242662 >"$fs_root/.notarx-root"
printf 'object bytes' >"$fs_root/data/native/raw/one"
printf 'sha256:abc' >"$fs_root/meta/native/raw/one.sha256"
printf 'abandoned write' >"$fs_root/tmp/in-flight"
touch "$workdir/env"
export NOTARX_ENV_FILE="$workdir/env" NOTARX_OBJECT_STORE_DRIVER=filesystem NOTARX_OBJECT_STORE_FS_ROOT="$fs_root"

cd "$root"
backup="$workdir/backup"
"$root/scripts/backup-drill.sh" "$backup" >/dev/null

tar -tf "$backup/objects-fs.tar" | grep -Fx './.notarx-root' >/dev/null
tar -tf "$backup/objects-fs.tar" | grep -F 'data/native/raw/one' >/dev/null
if tar -tf "$backup/objects-fs.tar" | grep -F 'in-flight' >/dev/null; then
  echo 'backup-drill archived the abandoned tmp/ write' >&2; exit 1
fi
grep -F 'objects-fs.tar' "$backup/SHA256SUMS" >/dev/null
grep -F 'gateway.dump' "$backup/SHA256SUMS" >/dev/null
if grep -F 'rustfs' "$DRILL_LOG" >/dev/null; then
  echo 'the filesystem drill touched RustFS' >&2; exit 1
fi
grep -F 'compose --env-file' "$DRILL_LOG" | grep -F 'stop gateway' >/dev/null
grep -F 'docker start container-gateway' "$DRILL_LOG" >/dev/null

# Damage the root, then restore it from the archive.
rm -rf "$fs_root/data" "$fs_root/meta"
printf 'a different root' >"$fs_root/.notarx-root"
printf 'stray' >"$fs_root/stray"
: >"$DRILL_LOG"
"$root/scripts/restore-drill.sh" "$backup" >/dev/null
test "$(cat "$fs_root/.notarx-root")" = 5e1625e7-9c7b-42b8-9b83-c9a1fc242662
test "$(cat "$fs_root/data/native/raw/one")" = 'object bytes'
test ! -e "$fs_root/stray"
if grep -F 'rustfs' "$DRILL_LOG" >/dev/null; then
  echo 'the filesystem restore touched RustFS' >&2; exit 1
fi

# A corrupted archive is refused before anything is replaced.
printf 'corrupt' >>"$backup/objects-fs.tar"
printf 'keep me' >"$fs_root/keep"
if "$root/scripts/restore-drill.sh" "$backup" >/dev/null 2>&1; then
  echo 'restore-drill accepted a corrupted archive' >&2; exit 1
fi
test "$(cat "$fs_root/keep")" = 'keep me'

# The Compose project must really run the Gateway on the filesystem driver.
export DRILL_NO_FS_CONFIG=1
if "$root/scripts/backup-drill.sh" "$workdir/b4" >/dev/null 2>&1; then
  echo 'backup-drill accepted a Compose project that is not on the filesystem driver' >&2; exit 1
fi
unset DRILL_NO_FS_CONFIG

# Preconditions fail early.
if NOTARX_OBJECT_STORE_FS_ROOT="$workdir/nothing" "$root/scripts/backup-drill.sh" "$workdir/b2" >/dev/null 2>&1; then
  echo 'backup-drill accepted an uninitialised root' >&2; exit 1
fi
if NOTARX_OBJECT_STORE_DRIVER=tape "$root/scripts/backup-drill.sh" "$workdir/b3" >/dev/null 2>&1; then
  echo 'backup-drill accepted an unknown driver' >&2; exit 1
fi
printf '%s\n' 'backup-drill filesystem fixture passed'
