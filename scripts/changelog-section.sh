#!/usr/bin/env bash
# Renders and applies the CHANGELOG.md section of a stable release (ADR 0024 slice 6). The section
# is the git-cliff output for the commits between the previous stable tag (or the fork commit) and
# the stable tag, so it matches the release notes; the date is the tag's, so a re-run renders the
# same text.
#
#   scripts/changelog-section.sh render --version X.Y.Z [--base <ref>] [--repo <dir>]
#   scripts/changelog-section.sh apply  --version X.Y.Z [--base <ref>] [--repo <dir>] --file CHANGELOG.md
#
# apply puts the section before the newest existing "## " heading (after the preamble when there is
# none) and does nothing when the file already holds "## [X.Y.Z]", so it is idempotent.
# GIT_CLIFF overrides the git-cliff binary (default: the pinned one).
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# Temp files of apply, removed on every exit (a failing render included).
section_file='' out_file=''
trap 'rm -f "$section_file" "$out_file"' EXIT

fail() {
  printf 'changelog-section: %s\n' "$1" >&2
  exit 1
}

[[ $# -gt 0 ]] || fail 'usage: changelog-section.sh render|apply --version X.Y.Z [--base ref] [--repo dir] [--file f]'
command=$1
shift

version='' base='' repo=$PWD file=''
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || fail "$1 needs a value"
  case $1 in
    --version) version=$2 ;;
    --base) base=$2 ;;
    --repo) repo=$2 ;;
    --file) file=$2 ;;
    *) fail "unknown argument $1" ;;
  esac
  shift 2
done
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "--version must be X.Y.Z, got '$version'"
git -C "$repo" rev-parse --verify --quiet "refs/tags/v$version" >/dev/null || fail "tag v$version does not exist in $repo"

render() {
  local cliff section
  cliff=${GIT_CLIFF:-$("$script_dir/install-git-cliff.sh")}
  # git-cliff's diagnostics reach the log; a failing call fails the job instead of rendering a placeholder.
  section=$("$cliff" --config "$script_dir/../cliff.toml" --repository "$repo" --strip all "${base:+$base..}v$version") ||
    fail "git-cliff failed for ${base:+$base..}v$version"
  # An empty output is a valid range without any releasing commit: the heading alone, then the placeholder.
  if [[ -z "$section" ]]; then
    section=$(printf '## [%s] - %s' "$version" "$(git -C "$repo" log -1 --format=%cs "v$version")")
  fi
  # A heading without any entry (only non-releasing commits) gets a placeholder.
  if [[ -z "$(sed -e '1d' <<<"$section" | tr -d '[:space:]')" ]]; then
    section=$(printf '%s\n\nNo user-facing changes.' "$(head -n 1 <<<"$section")")
  fi
  # Every section ends with exactly one blank line.
  printf '%s\n\n' "$section"
}

apply() {
  [[ -n "$file" && -f "$file" ]] || fail "--file '$file' not found"
  if grep -q -E "^## \[$(printf '%s' "$version" | sed -e 's/\./\\./g')\]" "$file"; then
    printf 'changelog-section: %s already records %s\n' "$file" "$version"
    return 0
  fi
  local next
  section_file=$(mktemp)
  out_file=$(mktemp)
  render >"$section_file"
  next=$(grep -n -m 1 '^## ' "$file" | cut -d: -f1 || true)
  if [[ -n "$next" ]]; then
    { head -n $((next - 1)) "$file"; cat "$section_file"; tail -n +"$next" "$file"; } >"$out_file"
  else
    { cat "$file"; printf '\n'; cat "$section_file"; } >"$out_file"
  fi
  cat "$out_file" >"$file"
}

case $command in
  render) render ;;
  apply) apply ;;
  *) fail "unknown command $command" ;;
esac
