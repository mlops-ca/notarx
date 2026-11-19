#!/usr/bin/env bash
# Installs the pinned nfpm (scripts/nfpm.version) with a SHA-256 check and prints the
# path of the binary (ADR 0025). Idempotent: reuses a cached copy. nfpm is
# standalone (no GoReleaser Pro) and packages one architecture per invocation.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=lib/github-release.sh
source "$root/scripts/lib/github-release.sh"
version=$(tr -d '[:space:]' < "$root/scripts/nfpm.version")
cache=${NFPM_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/notarx/nfpm}/$version
binary=$cache/nfpm

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

# The cache directory (which CI restores with actions/cache) holds only the archive and
# the extracted binary is rebuilt from it on every run, so a stale or tampered cache
# entry can never be executed: the archive must match the pinned SHA-256 first.
mkdir -p "$cache"
archive=nfpm_${version}_$target.tar.gz
cached=$cache/$archive
if [[ ! -f "$cached" ]] || [[ "$(shasum -a 256 "$cached" | cut -d' ' -f1)" != "$expected" ]]; then
  workdir=$(mktemp -d)
  trap 'rm -rf "$workdir"' EXIT
  github_release_download goreleaser/nfpm "v$version" "$archive" "$workdir/$archive"
  actual=$(shasum -a 256 "$workdir/$archive" | cut -d' ' -f1)
  if [[ "$actual" != "$expected" ]]; then
    printf 'nfpm checksum mismatch for %s\n' "$archive" >&2
    exit 1
  fi
  mv -f -- "$workdir/$archive" "$cached"
fi
tar -xzf "$cached" -C "$cache" nfpm
printf '%s\n' "$binary"
