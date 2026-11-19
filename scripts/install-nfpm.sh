#!/usr/bin/env bash
# Installs the pinned nfpm (scripts/nfpm.version) with a SHA-256 check and prints the
# path of the binary (ADR 0025). Idempotent: reuses a cached copy. nfpm is
# standalone (no GoReleaser Pro) and packages one architecture per invocation.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
version=$(tr -d '[:space:]' < "$root/scripts/nfpm.version")
cache=${NFPM_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/notarx/nfpm}/$version
binary=$cache/nfpm

if [[ -x "$binary" ]] && "$binary" --version 2>&1 | grep -Fq "$version"; then
  printf '%s\n' "$binary"
  exit 0
fi

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64)
    target=Linux_x86_64
    expected=2afaa5ffb5e3493402e31e7c67a5a1fb42133e3f66fc315ca0051934125561c7
    ;;
  Linux/aarch64 | Linux/arm64)
    target=Linux_arm64
    expected=d4eee71839e92c68cbd3b101640bad32d2856fc551acae0635be9118e48c9d9c
    ;;
  Darwin/x86_64)
    target=Darwin_x86_64
    expected=182ba6c5ded85c87bf276dd80c0ecfd8a2f8b25f7db6ac5fb999557c5b4258ce
    ;;
  Darwin/arm64)
    target=Darwin_arm64
    expected=27728af45782962ab59a8ff13ae041c7d955fe51dd94ab7883d9c5966a233f5d
    ;;
  *)
    printf 'no pinned nfpm build for %s/%s\n' "$(uname -s)" "$(uname -m)" >&2
    exit 1
    ;;
esac

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
archive=nfpm_${version}_$target.tar.gz
curl --fail --silent --show-error --location --retry 3 \
  --output "$workdir/$archive" \
  "https://github.com/goreleaser/nfpm/releases/download/v$version/$archive"
actual=$(shasum -a 256 "$workdir/$archive" | cut -d' ' -f1)
if [[ "$actual" != "$expected" ]]; then
  printf 'nfpm checksum mismatch for %s\n' "$archive" >&2
  exit 1
fi
tar -xzf "$workdir/$archive" -C "$workdir" nfpm
mkdir -p "$cache"
install -m 0755 "$workdir/nfpm" "$binary"
printf '%s\n' "$binary"
