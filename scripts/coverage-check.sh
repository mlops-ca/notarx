#!/usr/bin/env bash
# Go coverage gate: the repository-wide floor and the per-package floors of
# scripts/coverage-packages.txt, all read from one coverage profile.
#
#   scripts/coverage-check.sh           run the unit tests once with -coverprofile
#   scripts/coverage-check.sh --race    the same single run with the race detector on
#                                       (the CI quality job: unit tests run once, docs/ci.md)
#   scripts/coverage-check.sh --profile FILE
#                                       check an existing profile without running tests
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

minimum=${NOTARX_MIN_COVERAGE:-40.0}
race=()
profile=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --race) race=(-race); shift ;;
    --profile)
      [[ $# -ge 2 ]] || { printf 'coverage-check: --profile needs a file\n' >&2; exit 2; }
      profile=$2; shift 2 ;;
    *) printf 'coverage-check: unknown argument %s\n' "$1" >&2; exit 2 ;;
  esac
done
if [[ -n "$profile" && ${#race[@]} -gt 0 ]]; then
  printf 'coverage-check: --race runs the tests, --profile reads an existing profile; pass one of them\n' >&2
  exit 2
fi

if [[ -z "$profile" ]]; then
  profile=$(mktemp)
  test_output=$(mktemp)
  trap 'rm -f "$profile" "$test_output"' EXIT
  packages=$(go list ./... | grep -v '/console/node_modules/')
  # One run produces the profile every floor below reads; nothing reruns the tests.
  # -count=1: a restored build cache (CI) must not replay an earlier pass.
  # shellcheck disable=SC2086 # one argument per package
  if ! go test -count=1 ${race[@]+"${race[@]}"} $packages -coverprofile="$profile" >"$test_output" 2>&1; then
    cat "$test_output" >&2
    exit 1
  fi
fi
[[ -s "$profile" ]] || { printf 'coverage-check: profile %s is empty or missing\n' "$profile" >&2; exit 1; }

# Statement coverage of the profile, in total and per package directory: covered
# statements over all statements, the figure go test -cover and go tool cover -func
# print. Without -coverpkg every block belongs to the package whose tests ran it.
# Output lines: "total <percent>" and "<import path> <percent>".
module=$(go list -m)
coverage=$(awk '
  NR == 1 && /^mode:/ { next }
  {
    split($1, location, ":")
    file = location[1]
    package = file
    sub(/\/[^\/]*$/, "", package)
    # A block can appear more than once in a merged profile; count it once and
    # treat it as covered when any record ran it.
    key = $1
    if (!(key in statements)) { statements[key] = $2; owner[key] = package }
    if ($3 + 0 > 0) covered[key] = 1
  }
  END {
    for (key in statements) {
      all += statements[key]
      per_all[owner[key]] += statements[key]
      if (key in covered) { hit += statements[key]; per_hit[owner[key]] += statements[key] }
    }
    printf "total %.1f\n", (all ? 100 * hit / all : 0)
    for (package in per_all) printf "%s %.1f\n", package, (per_all[package] ? 100 * per_hit[package] / per_all[package] : 0)
  }
' "$profile")

# Every floor is checked and reported before the script fails, so one run lists
# every gap.
status=0
actual=$(printf '%s\n' "$coverage" | awk '$1 == "total" { print $2 }')
if awk -v actual="$actual" -v minimum="$minimum" 'BEGIN { exit !(actual + 0 >= minimum + 0) }'; then
  printf 'Go coverage gate passed: %.1f%% >= %.1f%%.\n' "$actual" "$minimum"
else
  printf 'Go coverage %.1f%% is below the required %.1f%%.\n' "$actual" "$minimum" >&2
  status=1
fi

while read -r package package_minimum; do
  [[ -n "${package:-}" && "${package:0:1}" != "#" ]] || continue
  import_path="$module/${package#./}"
  package_actual=$(printf '%s\n' "$coverage" | awk -v package="$import_path" '$1 == package { print $2 }')
  if [[ -z "$package_actual" ]]; then
    printf 'Could not read coverage for %s: the profile has no statements for it.\n' "$package" >&2
    status=1
    continue
  fi
  if ! awk -v actual="$package_actual" -v minimum="$package_minimum" 'BEGIN { exit !(actual + 0 >= minimum + 0) }'; then
    printf '%s coverage %.1f%% is below the required %.1f%%.\n' "$package" "$package_actual" "$package_minimum" >&2
    status=1
    continue
  fi
  printf '%s coverage gate passed: %.1f%% >= %.1f%%.\n' "$package" "$package_actual" "$package_minimum"
done < scripts/coverage-packages.txt
exit $status
