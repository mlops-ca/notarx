#!/usr/bin/env bash
# Tests scripts/render-homebrew-formula.sh (ADR 0025 slice 7) on a synthetic SHA256SUMS: the four
# archives, their checksums, the test block, the refusals and (when ruby is installed) the syntax.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

failures=0
cases=0
fail() {
  failures=$((failures + 1))
  printf 'FAIL: %s\n' "$1" >&2
}
expect_ok() {
  local name=$1
  shift
  cases=$((cases + 1))
  "$@" >"$workdir/out" 2>&1 || fail "$name: expected success, got: $(cat "$workdir/out")"
}
expect_fail() {
  local name=$1 pattern=$2
  shift 2
  cases=$((cases + 1))
  if "$@" >"$workdir/out" 2>&1; then
    fail "$name: expected failure"
  elif ! grep -q -E "$pattern" "$workdir/out"; then
    fail "$name: failure message does not match [$pattern]: $(cat "$workdir/out")"
  fi
}
expect_has() {
  cases=$((cases + 1))
  grep -q -F -- "$2" "$1" || fail "$1 does not contain: $2"
}

render=$root/scripts/render-homebrew-formula.sh
sums=$workdir/SHA256SUMS
: >"$sums"
for target in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
  printf '%s  notarxctl_0.2.0_%s.tar.gz\n' "$(printf '%s' "$target" | shasum -a 256 | cut -d' ' -f1)" "$target" >>"$sums"
  printf '%s  notarx_0.2.0_%s.tar.gz\n' "$(printf 'server %s' "$target" | shasum -a 256 | cut -d' ' -f1)" "$target" >>"$sums"
done
printf '%s  notarx_0.2.0_amd64.deb\n' "$(printf 'deb' | shasum -a 256 | cut -d' ' -f1)" >>"$sums"

expect_ok 'renders a formula' "$render" --version 0.2.0 --sums "$sums" --output "$workdir/Formula/notarxctl.rb"
formula=$workdir/Formula/notarxctl.rb
expect_has "$formula" 'class Notarxctl < Formula'
expect_has "$formula" 'version "0.2.0"'
expect_has "$formula" 'license "MIT"'
expect_has "$formula" 'bin.install "notarxctl"'
expect_has "$formula" 'shell_output("#{bin}/notarxctl version")'
for target in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
  expect_has "$formula" "url \"https://github.com/mlops-ca/notarx/releases/download/v0.2.0/notarxctl_0.2.0_$target.tar.gz\""
  expect_has "$formula" "sha256 \"$(printf '%s' "$target" | shasum -a 256 | cut -d' ' -f1)\""
done
# The server archives and packages never leak into the CLI formula.
cases=$((cases + 1))
if grep -q -E 'notarx_0|\.deb' "$formula"; then fail 'the formula names a server archive or a package'; fi
# Each os/arch block pairs with its own archive.
cases=$((cases + 1))
[[ "$(awk '/on_macos do/,/on_linux do/' "$formula" | grep -c darwin_)" == 2 ]] || fail 'the on_macos block does not hold the two darwin archives'
cases=$((cases + 1))
[[ "$(awk '/on_linux do/,/def install/' "$formula" | grep -c linux_)" == 2 ]] || fail 'the on_linux block does not hold the two linux archives'
if command -v ruby >/dev/null 2>&1; then
  expect_ok 'the formula is valid Ruby' ruby -c "$formula"
fi
expect_ok 'stdout without --output' "$render" --version 0.2.0 --sums "$sums" --repo example/notarx
cases=$((cases + 1))
# Captured first: grep -q in a pipe can end the writer early, which pipefail turns into a failure.
repo_formula=$("$render" --version 0.2.0 --sums "$sums" --repo example/notarx)
grep -q 'github.com/example/notarx/releases/download/v0.2.0/' <<<"$repo_formula" || fail '--repo is not used'

expect_fail 'a nightly version' 'stable X.Y.Z' "$render" --version 0.2.0-nightly.3 --sums "$sums"
expect_fail 'no sums file' 'is not a file' "$render" --version 0.2.0 --sums "$workdir/none"
expect_fail 'a version without archives' 'no entry for notarxctl_0.3.0_darwin_arm64' "$render" --version 0.3.0 --sums "$sums"
grep -v notarxctl_0.2.0_linux_arm64 "$sums" >"$workdir/short"
expect_fail 'a missing archive' 'no entry for notarxctl_0.2.0_linux_arm64' "$render" --version 0.2.0 --sums "$workdir/short"
{ cat "$sums"; grep notarxctl_0.2.0_linux_arm64 "$sums"; } >"$workdir/dup"
expect_fail 'a duplicated archive' 'more than once' "$render" --version 0.2.0 --sums "$workdir/dup"
sed 's/^[0-9a-f]\{64\}  notarxctl_0.2.0_darwin_arm64/zz  notarxctl_0.2.0_darwin_arm64/' "$sums" >"$workdir/bad"
expect_fail 'a malformed checksum' 'not a SHA-256 digest' "$render" --version 0.2.0 --sums "$workdir/bad"
expect_fail 'a bad repo' 'owner/repo' "$render" --version 0.2.0 --sums "$sums" --repo nope
expect_fail 'an unknown argument' 'unknown argument' "$render" --bogus x

if [[ "$failures" -ne 0 ]]; then
  printf '%d of %d render-homebrew-formula check(s) failed\n' "$failures" "$cases" >&2
  exit 1
fi
printf 'render-homebrew-formula-test passed (%d checks).\n' "$cases"
