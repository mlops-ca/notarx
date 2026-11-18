#!/usr/bin/env bash
# Release plan (ADR 0024): the one place a release version is computed. It is
# read-only: it creates no tag and changes nothing. Output is key=value lines
# (usable as $GITHUB_OUTPUT): skip, channel, version, core, sha, previous.
#
#   scripts/release-plan.sh nightly
#   scripts/release-plan.sh stable [--nightly-tag vX.Y.Z-nightly.N] [--version-override X.Y.Z]
#   scripts/release-plan.sh green <sha>     # exit 0 when the required checks are green
#
# Environment: RELEASE_PLAN_REPO (repository, default the current directory),
# RELEASE_PLAN_REF (main branch ref, default HEAD), GIT_CLIFF (binary, default the
# pinned one from scripts/install-git-cliff.sh), RELEASE_PLAN_CHECKS_DIR (tests: a
# directory of <sha> files listing successful check names instead of the API).
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=${RELEASE_PLAN_REPO:-$PWD}
ref=${RELEASE_PLAN_REF:-HEAD}
config=$script_dir/../cliff.toml
required_file=${RELEASE_PLAN_REQUIRED_CHECKS:-$script_dir/release-required-checks.txt}
stable_pattern='^v[0-9]+\.[0-9]+\.[0-9]+$'
nightly_pattern='^v[0-9]+\.[0-9]+\.[0-9]+-nightly\.[0-9]+$'

fail() {
  printf 'release-plan: %s\n' "$1" >&2
  exit 1
}

repo_git() { git -C "$repo" "$@"; }

cliff() {
  if [[ -z "${GIT_CLIFF:-}" ]]; then
    GIT_CLIFF=$("$script_dir/install-git-cliff.sh")
  fi
  "$GIT_CLIFF" --config "$config" --repository "$repo" "$@"
}

# Release tags sorted by SemVer precedence, ascending: for one core the nightlies
# come before the stable tag, and nightly counters compare numerically.
sorted_tags() {
  local filter=$1
  { repo_git tag --list 'v*' | grep -E "$filter" || true; } | awk -F'[v.-]' '
    {
      # fields: "", major, minor, patch, [nightly, N]
      stable = ($5 == "") ? 1 : 0
      printf "%09d %09d %09d %d %09d %s\n", $2, $3, $4, stable, $6, $0
    }' | sort | awk '{ print $6 }'
}

latest_tag() { sorted_tags "$1" | tail -n 1; }

emit() {
  printf '%s=%s\n' "$1" "$2"
}

skip() {
  printf 'release-plan: skipping: %s\n' "$1" >&2
  emit skip true
  emit channel "$2"
  emit version ""
  emit core ""
  emit sha "${3:-}"
  emit previous ""
  exit 0
}

# Number of releasing commits (feat, fix, perf, revert or breaking) in a range.
releasing_commits() {
  cliff --tag-pattern "$stable_pattern" --context "$1" | jq '[.[].commits | length] | add // 0'
}

checks_names() {
  local sha=$1
  if [[ -n "${RELEASE_PLAN_CHECKS_DIR:-}" ]]; then
    cat "$RELEASE_PLAN_CHECKS_DIR/$sha" 2>/dev/null || true
    return 0
  fi
  [[ -n "${GITHUB_REPOSITORY:-}" ]] || fail 'GITHUB_REPOSITORY is not set'
  gh api "repos/$GITHUB_REPOSITORY/commits/$sha/check-runs?per_page=100" \
    --jq '.check_runs[] | select(.conclusion == "success") | .name'
}

# Succeeds when every entry of the required-checks list has a successful run.
checks_green() {
  local names line name count have
  names=$(checks_names "$1")
  while IFS= read -r line || [[ -n "$line" ]]; do
    line=${line%%#*}
    line=$(printf '%s' "$line" | sed -e 's/[[:space:]]*$//')
    [[ -n "$line" ]] || continue
    if [[ "$line" =~ ^(.+)\*\ ([0-9]+)$ ]]; then
      name=${BASH_REMATCH[1]}
      count=${BASH_REMATCH[2]}
      have=$(printf '%s\n' "$names" | awk -v prefix="$name" 'index($0, prefix) == 1 { n++ } END { print n + 0 }')
      [[ "$have" -ge "$count" ]] || return 1
    else
      printf '%s\n' "$names" | grep -q -F -x -e "$line" || return 1
    fi
  done < "$required_file"
}

normalise() { printf '%s' "$1" | sed -e 's/^v//'; }

plan_nightly() {
  local stable_tag latest_nightly sha candidate core number previous count
  stable_tag=$(latest_tag "$stable_pattern")

  # The newest of the last 10 first-parent commits with green required checks.
  sha=
  for candidate in $(repo_git rev-list --first-parent -n 10 "$ref"); do
    if checks_green "$candidate"; then
      sha=$candidate
      break
    fi
  done
  [[ -n "$sha" ]] || skip 'no commit among the last 10 on main has green required checks' nightly

  latest_nightly=$(latest_tag "$nightly_pattern")
  if [[ -n "$latest_nightly" && "$(repo_git rev-list -n 1 "$latest_nightly")" == "$sha" ]]; then
    skip "$sha was already released as $latest_nightly" nightly "$sha"
  fi

  # Releasing commits since the previous release (stable or nightly), whichever
  # is newer: a night without them publishes nothing.
  previous=$(sorted_tags "$stable_pattern|$nightly_pattern" | tail -n 1)
  if [[ -n "$previous" ]]; then
    count=$(releasing_commits "$previous..$sha")
    [[ "$count" -gt 0 ]] || skip "no releasing commit between $previous and $sha" nightly "$sha"
  else
    count=$(releasing_commits "$sha")
    [[ "$count" -gt 0 ]] || skip "no releasing commit up to $sha" nightly "$sha"
  fi

  if [[ -n "$stable_tag" ]]; then
    core=$(normalise "$(cliff --tag-pattern "$stable_pattern" --bumped-version "$stable_tag..$sha" 2>/dev/null | tail -n 1)")
    [[ "$core" != "$(normalise "$stable_tag")" ]] || skip "no releasing commit between $stable_tag and $sha" nightly "$sha"
  else
    # Before any stable tag the core is fixed at 0.1.0.
    core=0.1.0
  fi
  [[ "$core" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "git-cliff produced an unusable core: $core"

  # The counter is the maximum of the existing ones plus one, never a count, so a
  # pruned tag cannot reuse a number.
  number=$(sorted_tags "^v$(printf '%s' "$core" | sed -e 's/\./\\./g')-nightly\\.[0-9]+$" | tail -n 1 | sed -e 's/.*-nightly\.//')
  number=$((${number:-0} + 1))
  version=$core-nightly.$number
  if repo_git rev-parse --verify --quiet "refs/tags/v$version" >/dev/null; then
    fail "tag v$version already exists"
  fi

  emit skip false
  emit channel nightly
  emit version "$version"
  emit core "$core"
  emit sha "$sha"
  emit previous "$previous"
}

plan_stable() {
  local nightly_tag= override= stable_tag core version sha previous
  while [[ $# -gt 0 ]]; do
    case $1 in
      --nightly-tag) nightly_tag=$2; shift 2 ;;
      --version-override) override=$2; shift 2 ;;
      *) fail "unknown argument $1" ;;
    esac
  done
  stable_tag=$(latest_tag "$stable_pattern")
  [[ -n "$nightly_tag" ]] || nightly_tag=$(latest_tag "$nightly_pattern")
  [[ -n "$nightly_tag" ]] || fail 'no nightly tag to promote'
  [[ "$nightly_tag" =~ $nightly_pattern ]] || fail "$nightly_tag is not a nightly tag"
  repo_git rev-parse --verify --quiet "refs/tags/$nightly_tag" >/dev/null || fail "tag $nightly_tag does not exist"

  sha=$(repo_git rev-list -n 1 "$nightly_tag")
  core=$(printf '%s' "${nightly_tag#v}" | sed -e 's/-nightly\..*//')
  version=$core
  if [[ -n "$override" ]]; then
    [[ "$override" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "version_override must be X.Y.Z, got $override"
    if [[ -z "$stable_tag" && "$override" != 0.1.0 ]]; then
      fail "version_override must be 0.1.0 while no stable tag exists, got $override"
    fi
    version=$override
  fi
  if repo_git rev-parse --verify --quiet "refs/tags/v$version" >/dev/null; then
    fail "tag v$version already exists"
  fi
  if [[ -n "$stable_tag" ]]; then
    local highest
    highest=$(printf '%s\n%s\n' "$stable_tag" "v$version" | sort -V | tail -n 1)
    [[ "$highest" == "v$version" ]] || fail "v$version is not newer than $stable_tag"
  fi
  repo_git merge-base --is-ancestor "$sha" "$ref" || fail "$sha ($nightly_tag) is not an ancestor of main"
  checks_green "$sha" || fail "the required checks are not green on $sha"
  previous=$stable_tag

  emit skip false
  emit channel stable
  emit version "$version"
  emit core "$core"
  emit sha "$sha"
  emit previous "$previous"
  emit nightly_tag "$nightly_tag"
}

mode=${1:-}
[[ $# -gt 0 ]] && shift
case "$mode" in
  nightly) plan_nightly "$@" ;;
  stable) plan_stable "$@" ;;
  green)
    [[ $# -eq 1 ]] || fail 'usage: release-plan.sh green <sha>'
    checks_green "$1" || fail "the required checks are not green on $1"
    ;;
  *) fail 'usage: release-plan.sh nightly | stable [options] | green <sha>' ;;
esac
