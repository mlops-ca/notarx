#!/usr/bin/env bash
# Fixture tests for scripts/baseline-gate.sh (ADR 0024 slice 5): throwaway git repositories.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
export GIT_AUTHOR_NAME=fixture GIT_AUTHOR_EMAIL=fixture@example.test
export GIT_COMMITTER_NAME=fixture GIT_COMMITTER_EMAIL=fixture@example.test
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

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

repo=
new_repo() {
  repo=$(mktemp -d "$workdir/repo.XXXXXX")
  git -C "$repo" init -q -b main
  mkdir "$repo/migrations"
}
commit_all() {
  git -C "$repo" add -A
  git -C "$repo" commit -q -m "$1"
}
gate() { bash "$root/scripts/baseline-gate.sh" --repo "$repo" "$@"; }
head_sha() { git -C "$repo" rev-parse HEAD; }

new_repo
printf 'create table t();\n' >"$repo/migrations/000001_baseline.sql"
commit_all 'feat(db): baseline'
good=$(head_sha)
expect_ok 'exactly the baseline' gate --sha "$good"
expect_ok 'override 0.1.0 is accepted' gate --sha "$good" --version-override 0.1.0
expect_fail 'override other than 0.1.0 is rejected' 'must be 0\.1\.0' gate --sha "$good" --version-override 0.2.0
expect_fail 'override 1.0.0 is rejected before the first stable' 'must be 0\.1\.0' gate --sha "$good" --version-override 1.0.0
expect_fail 'an unknown commit is rejected' 'not in' gate --sha 0123456789abcdef0123456789abcdef01234567
expect_fail 'the sha is required' 'sha is required' gate

printf 'alter table t add column c int;\n' >"$repo/migrations/000002_more.sql"
commit_all 'feat(db): second migration'
expect_fail 'an extra migration is rejected' 'more than the baseline.*000002_more\.sql' gate --sha "$(head_sha)"
expect_ok 'the earlier commit still passes' gate --sha "$good"

new_repo
printf 'x\n' >"$repo/migrations/000002_more.sql"
commit_all 'feat(db): no baseline'
expect_fail 'a missing baseline is rejected' '000001_baseline\.sql is missing' gate --sha "$(head_sha)"

new_repo
printf 'x\n' >"$repo/README"
commit_all 'docs: no migrations'
expect_fail 'no migrations at all is rejected' 'holds no file' gate --sha "$(head_sha)"

# After the first stable tag the immutability check governs: any layout and override pass here.
new_repo
printf 'create table t();\n' >"$repo/migrations/000001_baseline.sql"
printf 'x\n' >"$repo/migrations/000002_more.sql"
commit_all 'feat(db): two migrations'
git -C "$repo" tag v0.1.0
expect_ok 'after a stable tag the gate passes' gate --sha "$(head_sha)" --version-override 0.2.0
git -C "$repo" tag v0.2.0-nightly.1
expect_ok 'a nightly tag alone is not a stable tag' gate --sha "$(head_sha)"

if [[ "$failures" -ne 0 ]]; then
  printf '%d of %d baseline-gate case(s) failed\n' "$failures" "$cases" >&2
  exit 1
fi
printf 'baseline-gate-test passed (%d cases).\n' "$cases"
