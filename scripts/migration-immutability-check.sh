#!/usr/bin/env bash
# Migration immutability and sequencing guard (ADR 0015, "Migrations after the
# cut-over"; ADR 0024 version policy).
#
# Usage: scripts/migration-immutability-check.sh [--root DIR] [--dir RELATIVE_DIR]
#
#   --root  repository to check (default: the repository of this script)
#   --dir   migration directory relative to the root (default: migrations)
#
# Two rules:
#
#   Sequencing (always enforced). Every *.sql file is named
#   NNNNNN_<snake_case>.sql, and the 6-digit prefixes are unique, strictly
#   increasing and gap-free from 000001, so the old 000045 and 000188
#   collisions cannot recur.
#
#   Immutability (enforced once a stable release tag exists). A file that
#   exists at the latest stable tag must have no diff against that tag in the
#   working tree: no edit, no deletion. "Latest stable" means the highest tag
#   matching ^v[0-9]+\.[0-9]+\.[0-9]+$; nightly pre-releases
#   (vX.Y.Z-nightly.N, ADR 0024) are ignored, so the immutability rule is a
#   no-op while only nightly tags exist. New files are always allowed. The
#   check needs the tags: use a full clone or fetch them first.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
dir=migrations

while [[ $# -gt 0 ]]; do
  case "$1" in
    --root) root=$(cd "${2:?--root needs a value}" && pwd); shift 2 ;;
    --dir) dir=${2:?--dir needs a value}; dir=${dir%/}; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo/p' "$0" | sed '$d;s/^# \{0,1\}//'; exit 0 ;;
    *) printf 'Unknown argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done

failures=0
fail() {
  printf 'migration-check: %s\n' "$*" >&2
  failures=$((failures + 1))
}

[[ -d "$root/$dir" ]] || { printf 'migration-check: %s/%s does not exist\n' "$root" "$dir" >&2; exit 2; }

# --- Sequencing --------------------------------------------------------------
name_pattern='^[0-9]{6}_[a-z0-9]+(_[a-z0-9]+)*\.sql$'
expected=1
previous=""
count=0
while IFS= read -r path; do
  [[ -n "$path" ]] || continue
  name=${path##*/}
  count=$((count + 1))
  if [[ ! "$name" =~ $name_pattern ]]; then
    fail "$name does not match NNNNNN_<snake_case>.sql"
    continue
  fi
  prefix=${name:0:6}
  number=$((10#$prefix))
  if [[ "$prefix" == "$previous" ]]; then
    fail "duplicate prefix $prefix ($name)"
    continue
  fi
  previous=$prefix
  if [[ "$number" -lt "$expected" ]]; then
    fail "$name is out of sequence (expected prefix $(printf '%06d' "$expected") or higher)"
  elif [[ "$number" -gt "$expected" ]]; then
    fail "gap before $name (expected prefix $(printf '%06d' "$expected"))"
    expected=$((number + 1))
  else
    expected=$((expected + 1))
  fi
done < <(find "$root/$dir" -maxdepth 1 -type f -name '*.sql' | LC_ALL=C sort)

if [[ "$count" -eq 0 ]]; then
  fail "$dir holds no *.sql files"
fi

# --- Immutability ------------------------------------------------------------
stable_pattern='^v[0-9]+\.[0-9]+\.[0-9]+$'
tag=$(git -C "$root" tag --list 'v*' | grep -E "$stable_pattern" | sort -V | tail -n 1 || true)
if [[ -z "$tag" ]]; then
  printf 'migration-check: no stable release tag yet; released files are not frozen\n'
else
  released=0
  while IFS= read -r path; do
    [[ -n "$path" ]] || continue
    released=$((released + 1))
    if ! git -C "$root" diff --quiet "$tag" -- "$path"; then
      if [[ -e "$root/$path" ]]; then
        fail "$path changed since $tag; released migrations are immutable (add a new migration instead)"
      else
        fail "$path was removed since $tag; released migrations are immutable"
      fi
    fi
  done < <(git -C "$root" ls-tree -r --name-only "$tag" -- "$dir/" | grep -E '\.sql$' || true)
  printf 'migration-check: %s released files at %s checked\n' "$released" "$tag"
fi

if [[ "$failures" -ne 0 ]]; then
  printf 'migration-check: %s problem(s) found\n' "$failures" >&2
  exit 1
fi
printf 'migration-check: %s migration file(s) in order\n' "$count"
