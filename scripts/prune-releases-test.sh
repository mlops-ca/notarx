#!/usr/bin/env bash
# Fixture tests for scripts/prune-releases.sh (ADR 0024): throwaway git repository and a stub gh.
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
expect_eq() {
  cases=$((cases + 1))
  [[ "$2" == "$3" ]] || fail "$1: got [$2], want [$3]"
}

repo=$workdir/repo
git init -q -b main "$repo"
n=0
commit() {
  n=$((n + 1))
  git -C "$repo" commit -q --allow-empty -m "feat: change $n"
}
tag() { git -C "$repo" tag "$1"; }

# Core 0.1.0: nightlies 1..3, the stable tag v0.1.0 on the commit of nightly 3.
# Core 0.2.0: nightlies 1..20 (counters are numeric: 10 ranks above 9).
for i in 1 2 3; do
  commit
  tag "v0.1.0-nightly.$i"
done
tag v0.1.0
for i in $(seq 1 20); do
  commit
  tag "v0.2.0-nightly.$i"
done

stub=$workdir/gh
cat >"$stub" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_LOG"
if [[ "$1 $2" == 'release list' ]]; then
  # Every tag has a release except v0.2.0-nightly.1.
  git -C "$PRUNE_REPO" tag --list 'v*' | grep -v -x 'v0.2.0-nightly.1'
fi
STUB
chmod +x "$stub"
export STUB_LOG=$workdir/gh.log PRUNE_REPO=$repo GH_CLI=$stub GITHUB_REPOSITORY=mlops-ca/notarx

# Kept: 0.2.0 nightlies 20..7 (newest 14), 0.1.0 nightly 3 (newest of its core, and verified).
# Deleted: 0.2.0 nightlies 6..1 and 0.1.0 nightlies 2 and 1.
want_deleted='v0.1.0-nightly.1 v0.1.0-nightly.2 v0.2.0-nightly.1 v0.2.0-nightly.2 v0.2.0-nightly.3 v0.2.0-nightly.4 v0.2.0-nightly.5 v0.2.0-nightly.6'

: >"$STUB_LOG"
out=$(bash "$root/scripts/prune-releases.sh" --retained-out "$workdir/retained")
expect_eq 'dry run never calls gh' "$(cat "$STUB_LOG")" ''
expect_eq 'dry run deletion set' "$(grep '^would delete' <<<"$out" | sed -e 's/would delete //' | sort | tr '\n' ' ' | sed -e 's/ $//')" "$want_deleted"
expect_eq 'retained count' "$(wc -l <"$workdir/retained" | tr -d ' ')" 15
expect_eq 'retained list has no v prefix' "$(grep -c '^v' "$workdir/retained" || true)" 0
expect_eq 'counter maximum kept' "$(grep -c -x '0.2.0-nightly.20' "$workdir/retained")" 1
expect_eq 'newest of an older core kept' "$(grep -c -x '0.1.0-nightly.3' "$workdir/retained")" 1
expect_eq 'numeric counters (10 is kept, 6 is not)' "$(grep -c -x -e '0.2.0-nightly.10' -e '0.2.0-nightly.6' "$workdir/retained")" 1
expect_eq 'dry run is the default' "$(grep -c 'dry run' <<<"$out")" 1

# A nightly whose commit equals a stable tag is kept even when old and not newest of its core.
git -C "$repo" tag v0.1.0-nightly.0 "v0.1.0-nightly.1"
git -C "$repo" tag v0.1.0 -f "v0.1.0-nightly.1" >/dev/null
out=$(bash "$root/scripts/prune-releases.sh")
expect_eq 'verified nightly is kept' "$(grep -c -e 'keep v0.1.0-nightly.1 (verified by a stable tag)' <<<"$out")" 1
git -C "$repo" tag -d v0.1.0-nightly.0 >/dev/null
git -C "$repo" tag v0.1.0 -f "v0.1.0-nightly.3" >/dev/null

: >"$STUB_LOG"
bash "$root/scripts/prune-releases.sh" --apply >"$workdir/apply.out"
expect_eq 'apply deletes with --cleanup-tag' "$(grep -c '^release delete v0.* --cleanup-tag --yes$' "$STUB_LOG")" 7
expect_eq 'a tag without release goes through the refs API' "$(grep -c -x 'api --method DELETE repos/mlops-ca/notarx/git/refs/tags/v0.2.0-nightly.1' "$STUB_LOG")" 1
expect_eq 'stable tags are never deleted' "$(grep -c -E 'v[0-9]+\.[0-9]+\.[0-9]+( |$)' "$STUB_LOG" | tr -d ' ')" 0
expect_eq 'no bulk or untagged call' "$(grep -c -i 'untagged' "$STUB_LOG" || true)" 0

if bash "$root/scripts/prune-releases.sh" --keep 0 >/dev/null 2>&1; then fail '--keep 0 accepted'; fi
cases=$((cases + 1))

empty=$workdir/empty
git init -q -b main "$empty"
expect_eq 'no nightlies is a no-op' "$(PRUNE_REPO=$empty bash "$root/scripts/prune-releases.sh" --retained-out "$workdir/r2")" 'no nightly tags; nothing to prune'

if [[ "$failures" -ne 0 ]]; then
  printf '%d of %d prune-releases checks failed\n' "$failures" "$cases" >&2
  exit 1
fi
printf 'prune-releases-test passed (%d checks).\n' "$cases"
