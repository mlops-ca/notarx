#!/usr/bin/env bash
# Release plan (ADR 0024): the one place a release version is computed. It is
# read-only: it creates no tag and changes nothing. Output is key=value lines
# (usable as $GITHUB_OUTPUT): skip, channel, version, core, sha, previous, base (where
# commit ranges start: previous, or the fork commit before the first release).
#
#   scripts/release-plan.sh nightly
#   scripts/release-plan.sh stable [--nightly-tag vX.Y.Z-nightly.N] [--version-override X.Y.Z]
#
#   scripts/release-plan.sh fork      # print the fork commit (empty when it is not in the repository)
#
# Commit history before the fork commit (the upstream history, docs/upstream.md) is never
# parsed: every range starts at the previous release or, before the first one, at the fork.
#
# Environment: RELEASE_PLAN_REPO (repository, default the current directory),
# RELEASE_PLAN_REF (main branch ref, default HEAD), GIT_CLIFF (binary, default the
# pinned one from scripts/install-git-cliff.sh).
#
# The plan never reads check runs (ADR 0024, amendment of 2026-11-19): pushes to main run
# no tests, so the release run's own gates test the planned commit before anything is built
# or published.
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=${RELEASE_PLAN_REPO:-$PWD}
ref=${RELEASE_PLAN_REF:-HEAD}
config=$script_dir/../cliff.toml
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

# The last upstream commit before the first notarx commit, read from docs/upstream.md.
# Prints nothing when the repository does not contain it (a test repository).
fork_commit() {
  local file=${RELEASE_PLAN_UPSTREAM_DOC:-$script_dir/../docs/upstream.md} commit
  [[ -f "$file" ]] || fail "$file not found"
  commit=$(sed -n 's/^- Last upstream commit before the first notarx commit: `\([0-9a-f]\{40\}\)`.*/\1/p' "$file")
  [[ -n "$commit" ]] || fail "no fork commit in $file"
  if repo_git cat-file -e "$commit^{commit}" 2>/dev/null; then
    printf '%s' "$commit"
  fi
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

normalise() { printf '%s' "$1" | sed -e 's/^v//'; }

plan_nightly() {
  local stable_tag latest_nightly sha core number previous count fork
  stable_tag=$(latest_tag "$stable_pattern")

  # The newest commit on main. The release run's gates test exactly this commit.
  sha=$(repo_git rev-parse --verify --quiet "$ref^{commit}") || fail "cannot resolve $ref"
  fork=$(fork_commit)

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
    count=$(releasing_commits "${fork:+$fork..}$sha")
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
  emit base "${previous:-$fork}"
}

plan_stable() {
  local nightly_tag= override= stable_tag fork newest_nightly core version sha previous
  while [[ $# -gt 0 ]]; do
    case $1 in
      --nightly-tag | --version-override)
        [[ $# -ge 2 ]] || fail "$1 needs a value"
        if [[ "$1" == --nightly-tag ]]; then nightly_tag=$2; else override=$2; fi
        shift 2
        ;;
      *) fail "unknown argument $1" ;;
    esac
  done
  stable_tag=$(latest_tag "$stable_pattern")
  [[ -n "$nightly_tag" ]] || nightly_tag=$(latest_tag "$nightly_pattern")
  [[ -n "$nightly_tag" ]] || fail 'no nightly tag to promote'
  [[ "$nightly_tag" =~ $nightly_pattern ]] || fail "$nightly_tag is not a nightly tag"
  repo_git rev-parse --verify --quiet "refs/tags/$nightly_tag" >/dev/null || fail "tag $nightly_tag does not exist"

  sha=$(repo_git rev-list -n 1 "$nightly_tag")
  newest_nightly=$(latest_tag "$nightly_pattern")
  if [[ "$newest_nightly" != "$nightly_tag" ]]; then
    printf 'release-plan: notice: %s is not the newest nightly (%s); promoting the commit of %s\n' "$nightly_tag" "$newest_nightly" "$nightly_tag" >&2
  fi
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
  # No check-run lookup: the nightly was published only after its release run's gates passed
  # on this very commit, so promoting that commit reuses those results.
  previous=$stable_tag

  emit skip false
  emit channel stable
  emit version "$version"
  emit core "$core"
  emit sha "$sha"
  emit previous "$previous"
  emit base "${previous:-$(fork_commit)}"
  emit nightly_tag "$nightly_tag"
}

mode=${1:-}
[[ $# -gt 0 ]] && shift
case "$mode" in
  nightly) plan_nightly "$@" ;;
  stable) plan_stable "$@" ;;
  fork) fork_commit ;;
  *) fail 'usage: release-plan.sh nightly | stable [options] | fork' ;;
esac
