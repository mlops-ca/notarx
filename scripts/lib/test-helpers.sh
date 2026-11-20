# shellcheck shell=bash
# Sourced by the script tests of the release channels (render-compose-bundle-test.sh,
# render-homebrew-formula-test.sh, install-snippet-test.sh). The caller creates `workdir` first.
#
#   expect_ok <name> <command...>             the command succeeds
#   expect_fail <name> <pattern> <command...> the command fails and its output matches the pattern
#   expect_eq <name> <got> <want>             two strings are equal
#   expect_has <file> <text>                  the file contains the fixed text
#   fail <message>                            records a failed check
#   finish <test name>                        reports and exits non-zero when a check failed

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
expect_eq() {
  cases=$((cases + 1))
  [[ "$2" == "$3" ]] || fail "$1: got [$2], want [$3]"
}
expect_has() {
  cases=$((cases + 1))
  grep -q -F -- "$2" "$1" || fail "$1 does not contain: $2"
}
finish() {
  if [[ "$failures" -ne 0 ]]; then
    printf '%d of %d %s check(s) failed\n' "$failures" "$cases" "$1" >&2
    exit 1
  fi
  printf '%s passed (%d checks).\n' "$1" "$cases"
}
