#!/usr/bin/env bash
# Fixture tests for scripts/release-plan.sh and the git-cliff bump contract
# (ADR 0024). Every fixture is a throwaway repository; nothing here touches the
# checkout. The pinned git-cliff is installed on first use.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

GIT_CLIFF=$("$root/scripts/install-git-cliff.sh")
export GIT_CLIFF
export GIT_AUTHOR_NAME=fixture GIT_AUTHOR_EMAIL=fixture@example.test
export GIT_COMMITTER_NAME=fixture GIT_COMMITTER_EMAIL=fixture@example.test
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

failures=0
cases=0

fail() {
  failures=$((failures + 1))
  printf 'FAIL: %s\n' "$1" >&2
}

expect_eq() {
  cases=$((cases + 1))
  if [[ "$2" != "$3" ]]; then
    fail "$1: got [$2], want [$3]"
  fi
}

# --- fixture helpers --------------------------------------------------------

repo=
new_repo() {
  repo=$(mktemp -d "$workdir/repo.XXXXXX")
  export RELEASE_PLAN_CHECKS_DIR=$repo.checks
  mkdir -p "$RELEASE_PLAN_CHECKS_DIR"
  git -C "$repo" init -q -b main
  git -C "$repo" commit -q --allow-empty -m "chore: root"
}

commit() {
  git -C "$repo" commit -q --allow-empty -m "$1"
}

tag() { git -C "$repo" tag "$1" "${2:-HEAD}"; }
head_sha() { git -C "$repo" rev-parse HEAD; }

# Marks a commit's required checks green (or a subset when names are given).
green() {
  local sha=${1:-$(head_sha)}
  shift || true
  if [[ $# -eq 0 ]]; then
    printf '%s\n' quality contract-and-test console 'mainline-assets / assemble-release' 'mainline-images / image-manifest (notarx)' 'mainline-images / image-manifest (notarx-console)' > "$RELEASE_PLAN_CHECKS_DIR/$sha"
  else
    printf '%s\n' "$@" > "$RELEASE_PLAN_CHECKS_DIR/$sha"
  fi
}

plan() {
  RELEASE_PLAN_REPO="$repo" RELEASE_PLAN_REF=main bash "$root/scripts/release-plan.sh" "$@" 2>"$workdir/stderr"
}

field() { printf '%s\n' "$1" | sed -n "s/^$2=//p"; }

# Runs a plan that must succeed and compares the fields given as key=value.
expect_plan() {
  local label=$1 output pair
  shift
  if ! output=$(plan "${PLAN_ARGS[@]}"); then
    cases=$((cases + 1))
    fail "$label: plan failed: $(cat "$workdir/stderr")"
    return
  fi
  for pair in "$@"; do
    expect_eq "$label ${pair%%=*}" "$(field "$output" "${pair%%=*}")" "${pair#*=}"
  done
}

expect_refused() {
  local label=$1
  shift
  cases=$((cases + 1))
  if plan "$@" >/dev/null; then
    fail "$label: plan succeeded, want a refusal"
  fi
}

PLAN_ARGS=()
nightly() { PLAN_ARGS=(nightly); expect_plan "$@"; }
stable() {
  local label=$1
  shift
  PLAN_ARGS=(stable)
  if [[ ${#STABLE_ARGS[@]} -gt 0 ]]; then PLAN_ARGS+=("${STABLE_ARGS[@]}"); fi
  expect_plan "$label" "$@"
}
STABLE_ARGS=()

# --- 0.x bump rules, straight from git-cliff --------------------------------

cliff_next() {
  "$GIT_CLIFF" --config "$root/cliff.toml" --repository "$repo" --tag-pattern '^v[0-9]+\.[0-9]+\.[0-9]+$' --bumped-version 2>/dev/null | tail -n 1
}

expect_eq 'pinned git-cliff version' "$("$GIT_CLIFF" --version)" "git-cliff $(tr -d '[:space:]' < "$root/scripts/git-cliff.version")"

check_bump() { # label expected commit...
  local label=$1 want=$2
  shift 2
  new_repo
  commit 'feat: first'
  tag v0.1.0
  local message
  for message in "$@"; do commit "$message"; done
  expect_eq "bump: $label" "$(cliff_next)" "$want"
}
check_bump 'feat bumps minor' v0.2.0 'feat(oci): add range reads'
check_bump 'fix bumps patch' v0.1.1 'fix(oci): handle nil'
check_bump 'perf bumps patch' v0.1.1 'perf(db): speed up lookups'
check_bump 'revert bumps patch' v0.1.1 'revert: undo the cache change'
check_bump 'breaking bumps minor' v0.2.0 'fix(api)!: rename the route'
check_bump 'breaking of a non-releasing type bumps minor' v0.2.0 'refactor(api)!: rename the route'
check_bump 'feat beats fix' v0.2.0 'fix: a' 'feat: b' 'fix: c'
check_bump 'BREAKING CHANGE footer bumps minor' v0.2.0 $'docs(api): note\n\nBREAKING CHANGE: the old route is gone'
for type in docs test refactor build ci chore style; do
  check_bump "$type never bumps" v0.1.0 "$type: a change" "$type(deps): scoped change"
done
check_bump 'unconventional commits are ignored' v0.1.0 'Merge branch x' 'update stuff'
check_bump 'major stays 0 on breaking' v0.2.0 'feat!: big change'

# --- nightly plans ----------------------------------------------------------

new_repo; commit 'docs: only docs'; green
nightly 'no releasing commit skips' skip=true
nightly 'skip leaves no version' version=

new_repo; commit 'feat(oci): first feature'; green
nightly 'first nightly before any stable' skip=false channel=nightly core=0.1.0 version=0.1.0-nightly.1 sha="$(head_sha)" previous=
tag v0.1.0-nightly.1
nightly 'repeated commit skips' skip=true
commit 'docs: more docs'; green
nightly 'no new releasing commit skips' skip=true
commit 'fix(oci): bug'; green
nightly 'second nightly keeps the core' skip=false core=0.1.0 version=0.1.0-nightly.2 previous=v0.1.0-nightly.1
tag v0.1.0-nightly.2
commit 'fix: another bug'; green
tag v0.1.0-nightly.7
git -C "$repo" tag -d v0.1.0-nightly.1 v0.1.0-nightly.2 >/dev/null
commit 'feat: new work'; green
nightly 'counter is the maximum after pruned tags' version=0.1.0-nightly.8
git -C "$repo" tag -d v0.1.0-nightly.7 >/dev/null
nightly 'counter restarts when every tag is pruned' version=0.1.0-nightly.1
tag v0.1.0-nightly.10
tag v0.1.0-nightly.9 HEAD
commit 'fix: x'; green
nightly 'counter compares numerically, not as text' version=0.1.0-nightly.11

# A stable tag moves the core by the commits since it; nightly tags are invisible.
new_repo; commit 'feat: first'; green; tag v0.1.0; tag v0.1.1-nightly.4
commit 'fix: patch work'; green
nightly 'fix after stable bumps patch' core=0.1.1 version=0.1.1-nightly.5 previous=v0.1.1-nightly.4
commit 'feat: minor work'; green
nightly 'feat after stable bumps minor and resets the counter' core=0.2.0 version=0.2.0-nightly.1 previous=v0.1.1-nightly.4
commit 'refactor(api)!: breaking'; green
nightly 'breaking after stable bumps minor, not major' core=0.2.0
new_repo; commit 'feat: first'; green; tag v0.1.0
commit 'chore: housekeeping'; commit 'ci: pipeline'; green
nightly 'only non-releasing commits after a stable skip' skip=true
commit 'perf: faster'; green
nightly 'perf releases a patch' core=0.1.1
new_repo; commit 'feat: first'; green; tag v0.1.0
commit 'revert: undo'; green
nightly 'revert releases a patch' core=0.1.1

# Green required checks decide the commit.
new_repo; commit 'feat: a'; older=$(head_sha); green "$older"
commit 'fix: b'
nightly 'newest commit without green checks falls back to the last green one' sha="$older" core=0.1.0
green "$(head_sha)" quality contract-and-test console 'mainline-assets / assemble-release' 'mainline-images / image-manifest (notarx)'
nightly 'a missing second image check is not green' sha="$older"
green "$(head_sha)"
nightly 'green tip is picked' sha="$(head_sha)"
new_repo; commit 'feat: a'; green; for _ in 1 2 3 4 5 6 7 8 9 10; do commit 'docs: filler'; done
nightly 'a green commit older than 10 first-parent commits is not considered' skip=true
new_repo; commit 'feat: a'
nightly 'no green commit skips' skip=true

# --- stable plans -----------------------------------------------------------

new_repo; commit 'feat: first'; green; tag v0.1.0-nightly.1; nightly_sha=$(head_sha)
commit 'docs: after the nightly'; green
STABLE_ARGS=(--nightly-tag v0.1.0-nightly.1)
stable 'nightly tag resolves to its commit and core' skip=false channel=stable version=0.1.0 core=0.1.0 sha="$nightly_sha" previous= nightly_tag=v0.1.0-nightly.1
STABLE_ARGS=()
stable 'the latest nightly is the default' version=0.1.0 sha="$nightly_sha"
STABLE_ARGS=(--nightly-tag v0.1.0-nightly.1 --version-override 0.2.0)
expect_refused 'version_override other than 0.1.0 before any stable tag' stable --nightly-tag v0.1.0-nightly.1 --version-override 0.2.0
STABLE_ARGS=(--nightly-tag v0.1.0-nightly.1 --version-override 0.1.0)
stable 'version_override 0.1.0 before any stable tag' version=0.1.0
tag v0.1.0 "$nightly_sha"
expect_refused 'an existing stable tag is refused' stable --nightly-tag v0.1.0-nightly.1
expect_refused 'a stable tag name is not a nightly' stable --nightly-tag v0.1.0
expect_refused 'a missing tag is refused' stable --nightly-tag v0.9.9-nightly.1
expect_refused 'an option without a value is refused' stable --nightly-tag

commit 'feat: next'; green; tag v0.2.0-nightly.1
STABLE_ARGS=(--nightly-tag v0.2.0-nightly.1)
stable 'a later stable promotes its own nightly' version=0.2.0 core=0.2.0 previous=v0.1.0
STABLE_ARGS=(--nightly-tag v0.2.0-nightly.1 --version-override 1.0.0)
stable 'version_override 1.0.0 is allowed once a stable tag exists' version=1.0.0 core=0.2.0
expect_refused 'version_override must be newer than the latest stable' stable --nightly-tag v0.2.0-nightly.1 --version-override 0.0.9
expect_refused 'version_override must be X.Y.Z' stable --nightly-tag v0.2.0-nightly.1 --version-override 1.0

git -C "$repo" checkout -q -b side
commit 'feat: side work'; green; tag v0.3.0-nightly.1
git -C "$repo" checkout -q main
expect_refused 'a nightly outside main is refused' stable --nightly-tag v0.3.0-nightly.1

new_repo; commit 'feat: a'; tag v0.1.0-nightly.1
expect_refused 'a stable run needs green required checks' stable --nightly-tag v0.1.0-nightly.1

# --- the fork point: upstream history is never parsed ------------------------

new_repo; commit 'feat: an upstream feature'; git -C "$repo" commit -q --allow-empty -m 'Update the upstream readme'
fork=$(head_sha); commit 'chore: first notarx change'; green
upstream_doc=$repo.upstream.md
printf '%s\n' "- Last upstream commit before the first notarx commit: \`$fork\`" > "$upstream_doc"
expect_eq 'the fork commit is read from the upstream document' "$(RELEASE_PLAN_REPO="$repo" RELEASE_PLAN_UPSTREAM_DOC="$upstream_doc" bash "$root/scripts/release-plan.sh" fork)" "$fork"
log=$(RELEASE_PLAN_REPO="$repo" RELEASE_PLAN_UPSTREAM_DOC="$upstream_doc" bash "$root/scripts/release-plan.sh" nightly 2>&1)
expect_eq 'an upstream releasing commit before the fork does not release' "$(grep -c '^skip=true$' <<<"$log")" 1
commit 'feat: first notarx feature'; green
log=$(RELEASE_PLAN_REPO="$repo" RELEASE_PLAN_UPSTREAM_DOC="$upstream_doc" bash "$root/scripts/release-plan.sh" nightly 2>&1)
expect_eq 'a releasing commit after the fork releases' "$(grep -c '^version=0.1.0-nightly.1$' <<<"$log")" 1
expect_eq 'the range base is the fork commit before the first release' "$(grep '^base=' <<<"$log")" "base=$fork"
cases=$((cases + 1))
if grep -q 'Update the upstream readme' <<<"$log"; then
  fail 'a pre-fork commit was parsed and warned about'
fi
printf '%s\n' '- Last upstream commit before the first notarx commit: `0000000000000000000000000000000000000000`' > "$upstream_doc"
expect_eq 'a fork commit missing from the repository bounds nothing' "$(RELEASE_PLAN_REPO="$repo" RELEASE_PLAN_UPSTREAM_DOC="$upstream_doc" bash "$root/scripts/release-plan.sh" fork)" ''

# --- the required-checks gate -----------------------------------------------

new_repo; commit 'feat: a'; sha=$(head_sha); green "$sha"
cases=$((cases + 1))
RELEASE_PLAN_REPO="$repo" bash "$root/scripts/release-plan.sh" green "$sha" >/dev/null 2>&1 || fail 'green rejected a green commit'
printf '%s\n' quality > "$RELEASE_PLAN_CHECKS_DIR/$sha"
cases=$((cases + 1))
if RELEASE_PLAN_REPO="$repo" bash "$root/scripts/release-plan.sh" green "$sha" >/dev/null 2>&1; then
  fail 'green accepted a commit that lacks required checks'
fi
cases=$((cases + 1))
if grep -n -E 'quality|contract-and-test|mainline-assets' "$root/.github/workflows/release.yml" | grep -v '^.*#' | grep -q 'for required in'; then
  fail 'release.yml still hard-codes required check names'
fi

if [[ "$failures" -ne 0 ]]; then
  printf '%d of %d release-plan checks failed\n' "$failures" "$cases" >&2
  exit 1
fi
printf 'release-plan-test passed (%d checks).\n' "$cases"
