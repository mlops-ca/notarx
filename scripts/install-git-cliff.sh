#!/usr/bin/env bash
# Installs the pinned git-cliff (scripts/git-cliff.version) with a checksum
# check and prints the path of the binary. Idempotent: reuses a cached copy.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=lib/github-release.sh
source "$root/scripts/lib/github-release.sh"
version=$(tr -d '[:space:]' < "$root/scripts/git-cliff.version")
cache=${GIT_CLIFF_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/notarx/git-cliff}/$version
binary=$cache/git-cliff

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64)
    target=x86_64-unknown-linux-gnu
    expected=26d1f7c8ea2400f2ccb0b2e7f321635f600b583bf4cd1f7afaa5e8c24068ad1088a2504e0a4d9f4333d4bb20a80329327d9462634343ee3af91e1a7fbdc6f18a
    ;;
  Linux/aarch64 | Linux/arm64)
    target=aarch64-unknown-linux-gnu
    expected=fd27768ea76fea8c3ba3f3247fef2f6604988b629f15e48bf40be5eaa08b8fef2e4087cc9c8af16bc522bc19cc369611fa7692101b513f63469ad58522708600
    ;;
  Darwin/x86_64)
    target=x86_64-apple-darwin
    expected=4d32c50a010cbff65e46f1f36347c2dd1a20f17efea7b77115d3166146ad467dc803cd07b839fc35e0a6639aa8d1af9eab7cf8f7984bcd819513f46a396ff801
    ;;
  Darwin/arm64)
    target=aarch64-apple-darwin
    expected=6ec30660233fcd73b92e9d888965945b6435d6fcd7120df8d7c0799faa46cf06d563d49de721ccf4387e6d61bdc5b37c8eb73273c7d9a0883b90cd9857c6ad9f
    ;;
  *)
    printf 'no pinned git-cliff build for %s/%s\n' "$(uname -s)" "$(uname -m)" >&2
    exit 1
    ;;
esac

# The cache directory (which CI restores with actions/cache) holds only the archive and
# the extracted binary is rebuilt from it on every run, so a stale or tampered cache
# entry can never be executed: the archive must match the pinned SHA-512 first.
mkdir -p "$cache"
archive=git-cliff-$version-$target.tar.gz
cached=$cache/$archive
if [[ ! -f "$cached" ]] || [[ "$(shasum -a 512 "$cached" | cut -d' ' -f1)" != "$expected" ]]; then
  workdir=$(mktemp -d)
  trap 'rm -rf "$workdir"' EXIT
  github_release_download orhun/git-cliff "v$version" "$archive" "$workdir/$archive"
  actual=$(shasum -a 512 "$workdir/$archive" | cut -d' ' -f1)
  if [[ "$actual" != "$expected" ]]; then
    printf 'git-cliff checksum mismatch for %s\n' "$archive" >&2
    exit 1
  fi
  mv -f -- "$workdir/$archive" "$cached"
fi
tar -xzf "$cached" -C "$cache" --strip-components=1 "git-cliff-$version/git-cliff"
printf '%s\n' "$binary"
