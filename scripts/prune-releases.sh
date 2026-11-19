#!/usr/bin/env bash
# Nightly release retention (ADR 0024, slice 7). Dry run by default.
#
#   scripts/prune-releases.sh [--apply] [--keep 14] [--retained-out FILE]
#
# Keeps: the newest 14 nightlies, the newest nightly of every core, and every nightly whose
# commit equals a stable tag's commit (the verified ones, computed from git, never stored).
# Every other nightly release is removed with "gh release delete --cleanup-tag"; a nightly tag
# without a release is deleted through the refs API. Stable releases and tags are never
# touched: only tags that match vX.Y.Z-nightly.N are candidates. --retained-out writes the
# retained nightly versions (no v prefix), the input of scripts/prune-ghcr.mjs.
# Environment: PRUNE_REPO (git directory, default .), GH_CLI (gh binary), GITHUB_REPOSITORY.
set -euo pipefail

repo=${PRUNE_REPO:-.}
gh_cli=${GH_CLI:-gh}
apply=false
keep=14
retained_out=''
nightly_pattern='^v[0-9]+\.[0-9]+\.[0-9]+-nightly\.[0-9]+$'
stable_pattern='^v[0-9]+\.[0-9]+\.[0-9]+$'

fail() {
  printf 'prune-releases: %s\n' "$1" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case $1 in
    --apply)
      apply=true
      shift
      ;;
    --keep | --retained-out)
      [[ $# -ge 2 ]] || fail "$1 needs a value"
      if [[ "$1" == --keep ]]; then keep=$2; else retained_out=$2; fi
      shift 2
      ;;
    *) fail "unknown argument $1" ;;
  esac
done
[[ "$keep" =~ ^[0-9]+$ && "$keep" -ge 1 ]] || fail "--keep must be a positive integer, got '$keep'"

# Nightly tags newest first by SemVer precedence: core numerically, then counter numerically.
nightlies=$(
  { git -C "$repo" tag --list 'v*-nightly.*' | grep -E "$nightly_pattern" || true; } |
    awk -F'[v.-]' '{ printf "%09d %09d %09d %09d %s\n", $2, $3, $4, $6, $0 }' |
    sort -r | awk '{ print $5 }'
)
if [[ -n "$retained_out" ]]; then : >"$retained_out"; fi
if [[ -z "$nightlies" ]]; then
  printf 'no nightly tags; nothing to prune\n'
  exit 0
fi

stable_commits=$(
  { git -C "$repo" tag --list 'v*' | grep -E "$stable_pattern" || true; } |
    while IFS= read -r stable; do git -C "$repo" rev-list -n 1 "$stable"; done
)

release_tags=''
if [[ "$apply" == true ]]; then
  release_tags=$("$gh_cli" release list --limit 1000 --json tagName --jq '.[].tagName')
fi

count=0
deleted=0
seen_cores=' '
while IFS= read -r tag; do
  count=$((count + 1))
  core=${tag#v}
  core=${core%%-nightly.*}
  commit=$(git -C "$repo" rev-list -n 1 "$tag")
  why=''
  if [[ "$seen_cores" != *" $core "* ]]; then
    why="newest of core $core"
    seen_cores="$seen_cores$core "
  elif [[ "$count" -le "$keep" ]]; then
    why="newest $keep"
  elif grep -q -F -x "$commit" <<<"$stable_commits"; then
    why='verified by a stable tag'
  fi
  if [[ -n "$why" ]]; then
    printf 'keep %s (%s)\n' "$tag" "$why"
    if [[ -n "$retained_out" ]]; then printf '%s\n' "${tag#v}" >>"$retained_out"; fi
    continue
  fi
  deleted=$((deleted + 1))
  if [[ "$apply" != true ]]; then
    printf 'would delete %s\n' "$tag"
  elif grep -q -F -x "$tag" <<<"$release_tags"; then
    printf 'delete %s\n' "$tag"
    "$gh_cli" release delete "$tag" --cleanup-tag --yes
  else
    printf 'delete tag %s (no release)\n' "$tag"
    "$gh_cli" api --method DELETE "repos/${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is not set}/git/refs/tags/$tag"
  fi
done <<<"$nightlies"

if [[ "$apply" != true ]]; then
  printf 'dry run: %d nightly releases would be deleted; pass --apply to delete\n' "$deleted"
fi
