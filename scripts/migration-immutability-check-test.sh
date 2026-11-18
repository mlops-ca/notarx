#!/usr/bin/env bash
# Fixture tests for scripts/migration-immutability-check.sh: each scenario builds
# a throwaway repository, so no network, Docker or real tags are involved.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
check=$root/scripts/migration-immutability-check.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_NAME=fixture GIT_AUTHOR_EMAIL=fixture@example.test
export GIT_COMMITTER_NAME=fixture GIT_COMMITTER_EMAIL=fixture@example.test

repo=""
new_repo() {
  repo=$work/$1
  mkdir -p "$repo/migrations"
  git -C "$repo" init -q -b main
  printf -- '-- +goose Up\nCREATE TABLE a (id int);\n' >"$repo/migrations/000001_baseline.sql"
  commit "baseline"
}
commit() {
  git -C "$repo" add -A
  git -C "$repo" commit -q -m "$1"
}
tag() { git -C "$repo" tag "$1"; }

passed=0
expect_pass() {
  local label=$1 output
  if ! output=$("$check" --root "$repo" 2>&1); then
    printf 'FAIL %s: expected success\n%s\n' "$label" "$output" >&2
    exit 1
  fi
  passed=$((passed + 1))
  printf 'ok   %s\n' "$label"
}
expect_fail() {
  local label=$1 needle=$2 output
  if output=$("$check" --root "$repo" 2>&1); then
    printf 'FAIL %s: expected failure\n%s\n' "$label" "$output" >&2
    exit 1
  fi
  if [[ "$output" != *"$needle"* ]]; then
    printf 'FAIL %s: output lacks "%s"\n%s\n' "$label" "$needle" "$output" >&2
    exit 1
  fi
  passed=$((passed + 1))
  printf 'ok   %s\n' "$label"
}

# --- sequencing, no tags ---
new_repo clean
printf 'SELECT 2;\n' >"$repo/migrations/000002_add_things.sql"
commit "second"
expect_pass "contiguous files and no tag"

new_repo duplicate
printf 'SELECT 2;\n' >"$repo/migrations/000002_one.sql"
printf 'SELECT 2;\n' >"$repo/migrations/000002_two.sql"
commit "dup"
expect_fail "duplicate prefix" "duplicate prefix 000002"

new_repo gap
printf 'SELECT 3;\n' >"$repo/migrations/000003_skips_two.sql"
commit "gap"
expect_fail "gap in prefixes" "gap before 000003_skips_two.sql"

new_repo badname
printf 'SELECT 2;\n' >"$repo/migrations/000002_BadName.sql"
commit "bad"
expect_fail "name not snake_case" "000002_BadName.sql does not match"

new_repo noprefix
printf 'SELECT 2;\n' >"$repo/migrations/2_short.sql"
commit "short"
expect_fail "short numeric prefix" "2_short.sql does not match"

new_repo nofirst
git -C "$repo" mv migrations/000001_baseline.sql migrations/000002_baseline.sql
commit "renumber"
expect_fail "sequence must start at 000001" "gap before 000002_baseline.sql"

new_repo empty
git -C "$repo" rm -q migrations/000001_baseline.sql
mkdir -p "$repo/migrations" && : >"$repo/migrations/.keep"
commit "empty"
expect_fail "empty directory" "holds no *.sql files"

new_repo badname_then_good
printf 'SELECT 2;\n' >"$repo/migrations/000002_BadName.sql"
printf 'SELECT 3;\n' >"$repo/migrations/000003_fine.sql"
commit "bad then good"
output=$("$check" --root "$repo" 2>&1) && { echo "FAIL badname_then_good: expected failure" >&2; exit 1; }
if [[ "$output" == *"gap before"* ]]; then
  printf 'FAIL one bad name cascaded into a gap error\n%s\n' "$output" >&2
  exit 1
fi
passed=$((passed + 1))
printf 'ok   %s\n' "a bad name does not cascade into gap errors"

new_repo duplicate_then_next
printf 'SELECT 2;\n' >"$repo/migrations/000002_one.sql"
printf 'SELECT 2;\n' >"$repo/migrations/000002_two.sql"
printf 'SELECT 3;\n' >"$repo/migrations/000003_next.sql"
commit "dup then next"
output=$("$check" --root "$repo" 2>&1) && { echo "FAIL duplicate_then_next: expected failure" >&2; exit 1; }
if [[ "$output" == *"gap before"* || "$output" == *"out of sequence"* ]]; then
  printf 'FAIL a duplicate prefix cascaded\n%s\n' "$output" >&2
  exit 1
fi
passed=$((passed + 1))
printf 'ok   %s\n' "a duplicate prefix does not cascade"

# --- immutability ---
new_repo nightly_only
tag v0.1.0-nightly.1
tag v0.2.0-nightly.3
printf -- '-- edited\n' >>"$repo/migrations/000001_baseline.sql"
commit "edit baseline while only nightly tags exist"
expect_pass "nightly tags only: released files are not frozen"

new_repo stable_changed
tag v0.1.0
printf -- '-- edited\n' >>"$repo/migrations/000001_baseline.sql"
commit "edit released baseline"
expect_fail "released file changed after a stable tag" "000001_baseline.sql changed since v0.1.0"

new_repo stable_uncommitted
tag v0.1.0
printf -- '-- edited\n' >>"$repo/migrations/000001_baseline.sql"
expect_fail "uncommitted edit of a released file" "changed since v0.1.0"

new_repo stable_deleted
printf 'SELECT 2;\n' >"$repo/migrations/000002_keep.sql"
commit "second"
tag v0.1.0
git -C "$repo" rm -q migrations/000002_keep.sql
commit "delete released"
expect_fail "released file removed after a stable tag" "000002_keep.sql was removed since v0.1.0"

new_repo stable_added
tag v0.1.0
printf 'SELECT 2;\n' >"$repo/migrations/000002_new_work.sql"
commit "additive migration"
expect_pass "new migration after a stable tag"

new_repo stable_unreleased_edit
tag v0.1.0
printf 'SELECT 2;\n' >"$repo/migrations/000002_new_work.sql"
commit "additive migration"
printf 'SELECT 22;\n' >"$repo/migrations/000002_new_work.sql"
commit "edit a file that was never released"
expect_pass "unreleased file may still change"

new_repo stable_with_nightly
tag v0.1.0
printf 'SELECT 2;\n' >"$repo/migrations/000002_new_work.sql"
commit "additive migration"
tag v0.2.0-nightly.1
printf -- '-- edited\n' >>"$repo/migrations/000001_baseline.sql"
commit "edit baseline"
expect_fail "nightly tag newer than the stable tag is ignored" "changed since v0.1.0"

new_repo version_sort
printf 'SELECT 2;\n' >"$repo/migrations/000002_ninth.sql"
commit "ninth"
tag v0.9.0
printf 'SELECT 3;\n' >"$repo/migrations/000003_tenth.sql"
commit "tenth"
tag v0.10.0
printf -- '-- edited\n' >>"$repo/migrations/000003_tenth.sql"
commit "edit file released in v0.10.0"
expect_fail "latest stable tag is chosen by version, not by name" "000003_tenth.sql changed since v0.10.0"

new_repo stable_enforcing_after_nightly
tag v0.1.0-nightly.1
expect_pass "no-op while only a nightly tag exists"
tag v0.1.0
printf -- '-- edited\n' >>"$repo/migrations/000001_baseline.sql"
commit "edit after the first stable tag"
expect_fail "enforcing once a stable tag exists" "changed since v0.1.0"

# A shallow clone that lacks the tags must not silently disable the guard.
new_repo shallow_source
tag v0.1.0
shallow=$work/shallow_clone
git clone -q --depth 1 --no-tags "file://$repo" "$shallow"
repo=$shallow
expect_fail "shallow clone without tags" "shallow clone"

# A tree that is not a git checkout still gets the sequencing rules and says so.
new_repo not_a_checkout
rm -rf "$repo/.git"
out=$("$check" --root "$repo" 2>&1) || { printf 'FAIL not a checkout: %s\n' "$out" >&2; exit 1; }
[[ "$out" == *"SKIPPED the immutability check"* ]] || { printf 'FAIL not a checkout: no notice\n%s\n' "$out" >&2; exit 1; }
passed=$((passed + 1))
printf 'ok   %s\n' "a tree without .git skips immutability loudly"

printf 'migration-immutability-check fixtures passed (%s scenarios).\n' "$passed"
