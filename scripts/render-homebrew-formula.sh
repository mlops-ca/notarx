#!/usr/bin/env bash
# Renders the Homebrew formula of notarxctl (ADR 0025 slice 7) from the SHA256SUMS of a stable
# release.
#
#   scripts/render-homebrew-formula.sh --version X.Y.Z --sums SHA256SUMS [--repo owner/repo] [--output FILE]
#
# The url of each of the four archives (darwin and linux, amd64 and arm64) is the release asset
# notarxctl_X.Y.Z_<os>_<arch>.tar.gz and its sha256 is read from SHA256SUMS; a missing or
# duplicated archive fails. The formula's test block runs "notarxctl version". Without --output
# the formula goes to stdout. The tap (mlops-ca/homebrew-tap) only ever receives stable versions.
set -euo pipefail

fail() {
  printf 'render-homebrew-formula: %s\n' "$1" >&2
  exit 1
}

version=''
sums=''
repo=mlops-ca/notarx
output=''
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || fail "$1 needs a value"
  case $1 in
    --version) version=$2 ;;
    --sums) sums=$2 ;;
    --repo) repo=$2 ;;
    --output) output=$2 ;;
    *) fail "unknown argument $1" ;;
  esac
  shift 2
done

[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "version must be a stable X.Y.Z, got '$version'"
[[ -f "$sums" ]] || fail "--sums '$sums' is not a file"
[[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail "--repo must be owner/repo, got '$repo'"

sum_of() { # sum_of <os> <arch>
  local name=notarxctl_${version}_$1_$2.tar.gz found
  found=$(awk -v n="$name" 'NF == 2 && $2 == n { print $1 }' "$sums")
  [[ -n "$found" ]] || fail "SHA256SUMS has no entry for $name"
  [[ "$(wc -l <<<"$found" | tr -d ' ')" == 1 ]] || fail "SHA256SUMS lists $name more than once"
  [[ "$found" =~ ^[0-9a-f]{64}$ ]] || fail "the checksum of $name is not a SHA-256 digest: '$found'"
  printf '%s' "$found"
}

base=https://github.com/$repo/releases/download/v$version
# Resolved up front: a failure inside the command substitutions of the template would not stop the script.
sum_darwin_arm64=$(sum_of darwin arm64)
sum_darwin_amd64=$(sum_of darwin amd64)
sum_linux_arm64=$(sum_of linux arm64)
sum_linux_amd64=$(sum_of linux amd64)
block() { # block <os> <arch> <sha256>
  printf '      url "%s/notarxctl_%s_%s_%s.tar.gz"\n      sha256 "%s"\n' "$base" "$version" "$1" "$2" "$3"
}

formula=$(
  cat <<HEAD
class Notarxctl < Formula
  desc "Command-line client for the notarx artifact repository"
  homepage "https://github.com/$repo"
  version "$version"
  license "MIT"

  on_macos do
    on_arm do
$(block darwin arm64 "$sum_darwin_arm64")
    end
    on_intel do
$(block darwin amd64 "$sum_darwin_amd64")
    end
  end

  on_linux do
    on_arm do
$(block linux arm64 "$sum_linux_arm64")
    end
    on_intel do
$(block linux amd64 "$sum_linux_amd64")
    end
  end

  def install
    bin.install "notarxctl"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/notarxctl version")
  end
end
HEAD
)

if [[ -n "$output" ]]; then
  mkdir -p "$(dirname "$output")"
  printf '%s\n' "$formula" >"$output"
else
  printf '%s\n' "$formula"
fi
