# shellcheck shell=bash
# Sourced by the scripts that write deterministic release tarballs.
#
# create_tar_gz <source-dir> <destination.tar.gz>
#
# Every entry gets a fixed mtime, uid 0 and gid 0 and the members are sorted, so the same
# tree always gives the same bytes. normalize_stage touches the source tree in place.

normalize_stage() {
  find "$1" -exec touch -h -t 200001010000.00 {} +
}

create_tar_gz() {
  local source=$1
  local destination=$2
  normalize_stage "$source"
  if tar --version 2>/dev/null | grep -Fq 'GNU tar'; then
    (
      cd "$source" || return 1
      find . -print | LC_ALL=C sort | tar \
        --no-recursion \
        --format ustar \
        --owner 0 \
        --group 0 \
        --numeric-owner \
        -cf - \
        -T - | gzip -n > "$destination"
    )
    return
  fi
  (
    cd "$source" || return 1
    # BSD tar's --format ustar leaves size/mtime terminated with spaces;
    # Go reports those headers as unknown. Write canonical USTAR here so the
    # same strict distribution acceptance applies on non-GNU build hosts.
    python3 - <<'PY' | gzip -n > "$destination"
import sys, tarfile
from pathlib import Path

def canonical(info):
    info.uid = info.gid = 0
    info.uname = info.gname = 'root'
    return info

with tarfile.open(fileobj=sys.stdout.buffer, mode='w|', format=tarfile.USTAR_FORMAT) as archive:
    for name in ['.'] + sorted('./' + str(p) for p in Path('.').rglob('*')):
        archive.add(name, arcname=name, recursive=False, filter=canonical)
PY
  )
}
