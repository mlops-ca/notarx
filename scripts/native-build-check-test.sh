#!/usr/bin/env bash
# Self-test of scripts/native-build-check.sh: the compliant fixture tree passes,
# each negative fixture is detected under its own rule, the macos-15-intel
# calendar guard follows the injected date, and the real tree is clean.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
check="$root/scripts/native-build-check.sh"
fixtures="$root/scripts/testdata/native-build"
log=$(mktemp)
trap 'rm -f "$log"' EXIT
export NATIVE_BUILD_CHECK_TODAY=2026-11-18

fail() { printf 'native-build-check-test: %s\n' "$*" >&2; exit 1; }

# run_check ROOT [DATE]: the fixture trees use an empty allowlist.
run_check() {
  NATIVE_BUILD_CHECK_TODAY=${2:-$NATIVE_BUILD_CHECK_TODAY} bash "$check" --root "$1" --allowlist /dev/null >"$log" 2>&1
}

run_check "$fixtures/compliant" || { cat "$log" >&2; fail 'the compliant fixture tree was rejected'; }

# fixture directory -> rule it must trip.
expect() {
  local name=$1 rule=$2 date=${3:-}
  if run_check "$fixtures/violations/$name" "$date"; then
    fail "fixture $name was not detected"
  fi
  grep -q "^$rule: " "$log" || { cat "$log" >&2; fail "fixture $name did not trip rule $rule"; }
}
expect platform-pin platform-pin
expect target-var target-var
expect buildplatform target-var
expect goos-dockerfile goos-goarch
expect goarch-makefile goos-goarch
expect goos-script goos-goarch
expect goarch-workflow goos-goarch
expect qemu-action qemu
expect qemu-user-static qemu
expect binfmt qemu
expect self-hosted-scalar self-hosted
expect self-hosted-list self-hosted
expect self-hosted-flow self-hosted
expect multi-platform-comma multi-platform
expect multi-platform-block multi-platform
expect cgo-cross cgo-cross
expect ubuntu-latest ubuntu-latest
expect macos-intel macos-intel 2027-09-01
expect macos-intel macos-intel 2030-01-01

# The calendar guard passes until the day before the label retires.
run_check "$fixtures/violations/macos-intel" 2027-08-31 || { cat "$log" >&2; fail 'macos-15-intel was rejected before 2027-09-01'; }

# Every violation directory has an expectation above.
for dir in "$fixtures"/violations/*/; do
  name=$(basename "$dir")
  grep -q "^expect $name " "${BASH_SOURCE[0]}" || fail "fixture $name has no expectation in the self-test"
done

# The allowlist skips listed paths and only those.
allow=$(mktemp)
printf 'Dockerfile\n' > "$allow"
bash "$check" --root "$fixtures/violations/platform-pin" --allowlist "$allow" >/dev/null 2>&1 || { rm -f "$allow"; fail 'an allowlisted file was still scanned'; }
printf 'Makefile\n' > "$allow"
if bash "$check" --root "$fixtures/violations/platform-pin" --allowlist "$allow" >/dev/null 2>&1; then rm -f "$allow"; fail 'an unrelated allowlist entry hid a violation'; fi
rm -f "$allow"

# The real tree is compliant, today and with the fixtures allowlisted.
bash "$check" >"$log" 2>&1 || { cat "$log" >&2; fail 'the repository tree violates the native-build rules'; }

printf 'native-build-check self-test passed.\n'
