#!/usr/bin/env bash
# First-release gate (ADR 0024 slice 5, ADR 0015): while no stable tag exists, a stable run is
# only allowed when the commit to release holds exactly one migration, 000001_baseline.sql,
# and the version is 0.1.0. After v0.1.0 exists, scripts/migration-immutability-check.sh
# (ADR 0015) takes over and this gate passes.
#
#   scripts/baseline-gate.sh --sha <commit> [--version-override X.Y.Z] [--repo <dir>]
#
# --repo defaults to the current directory; it must hold the stable tags (fetch-depth 0).
set -euo pipefail

fail() {
  printf 'baseline-gate: %s\n' "$1" >&2
  exit 1
}

sha=''
override=''
repo=$PWD
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || fail "$1 needs a value"
  case $1 in
    --sha) sha=$2 ;;
    --version-override) override=$2 ;;
    --repo) repo=$2 ;;
    *) fail "unknown argument $1" ;;
  esac
  shift 2
done
[[ -n "$sha" ]] || fail '--sha is required'

repo_git() { git -C "$repo" "$@"; }

repo_git cat-file -e "$sha^{commit}" 2>/dev/null || fail "commit $sha is not in $repo"

if [[ -n "$(repo_git tag --list 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' || true)" ]]; then
  printf 'baseline-gate: a stable tag exists; scripts/migration-immutability-check.sh governs the migrations\n'
  exit 0
fi

if [[ -n "$override" && "$override" != 0.1.0 ]]; then
  fail "version_override must be 0.1.0 while no stable tag exists, got $override"
fi

found=$(repo_git ls-tree -r --name-only "$sha" -- migrations/postgres/)
[[ -n "$found" ]] || fail "migrations/postgres/ holds no file at $sha; 000001_baseline.sql is required"
if [[ "$found" != migrations/postgres/000001_baseline.sql ]]; then
  # Every file other than the baseline is reported, so the cause is visible at once.
  extra=$(grep -v -x 'migrations/postgres/000001_baseline.sql' <<<"$found" | paste -sd ' ' -)
  if [[ -z "$extra" ]]; then
    fail "migrations/postgres/ at $sha is not exactly 000001_baseline.sql"
  fi
  if ! grep -q -x 'migrations/postgres/000001_baseline.sql' <<<"$found"; then
    fail "migrations/postgres/000001_baseline.sql is missing at $sha (found: $extra)"
  fi
  fail "migrations/postgres/ at $sha holds more than the baseline before the first stable release: $extra"
fi
printf 'baseline-gate: %s holds exactly migrations/postgres/000001_baseline.sql\n' "$sha"
