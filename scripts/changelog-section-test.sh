#!/usr/bin/env bash
# Tests scripts/changelog-section.sh and scripts/release-milestones.sh on throwaway repositories
# and a stub gh (ADR 0024 slice 6). The pinned git-cliff is installed on first use.
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
  [[ "$2" == "$3" ]] || fail "$1: got [$2], want [$3]"
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

repo=$workdir/repo
git init -q -b main "$repo"
c() { git -C "$repo" commit -q --allow-empty -m "$1"; }
c 'chore: root'
git -C "$repo" tag base
c 'feat(oci): add pull-through'
c 'fix(db): repair the baseline'
c 'docs: reword'
git -C "$repo" tag v0.1.0-nightly.1
git -C "$repo" tag v0.1.0
c 'feat(npm)!: new layout'
git -C "$repo" tag v0.2.0

section() { bash "$root/scripts/changelog-section.sh" render --repo "$repo" "$@"; }

# --- render ------------------------------------------------------------------
out=$(section --version 0.1.0 --base base)
expect_eq 'the section starts with the version heading' "$(head -n 1 <<<"$out" | sed -e 's/ - .*//')" '## [0.1.0]'
cases=$((cases + 1))
grep -q -F '*(oci)* Add pull-through' <<<"$out" || fail 'the feat commit is missing'
grep -q -F '*(db)* Repair the baseline' <<<"$out" || fail 'the fix commit is missing'
if grep -q -F 'Reword' <<<"$out"; then fail 'a non-releasing docs commit is listed'; fi
if grep -q -i 'nightly' <<<"$out"; then fail 'a nightly tag leaked into the section'; fi

# The section is exactly what git-cliff prints for the range (the release notes use the same call).
want=$("$GIT_CLIFF" --config "$root/cliff.toml" --repository "$repo" --strip all base..v0.1.0 2>/dev/null)
expect_eq 'the section equals the git-cliff output' "$(section --version 0.1.0 --base base)" "$(printf '%s\n' "$want")"

out=$(section --version 0.2.0 --base v0.1.0)
cases=$((cases + 1))
grep -q -F 'New layout' <<<"$out" || fail 'the range since the previous stable lacks its commit'
if grep -q -F 'pull-through' <<<"$out"; then fail 'the previous stable range leaked an older commit'; fi

expect_fail 'a missing tag is refused' 'does not exist' section --version 0.9.0
expect_fail 'a bad version is refused' 'must be X.Y.Z' section --version 0.1

# --- apply -------------------------------------------------------------------
file=$workdir/CHANGELOG.md
printf '# Changelog\n\nPreamble text.\n' >"$file"
bash "$root/scripts/changelog-section.sh" apply --repo "$repo" --version 0.1.0 --base base --file "$file"
expect_eq 'the preamble is kept' "$(head -n 3 "$file" | tr '\n' '|')" '# Changelog||Preamble text.|'
cases=$((cases + 1))
grep -q '^## \[0.1.0\]' "$file" || fail 'the first section was not appended after the preamble'
once=$(cat "$file")
bash "$root/scripts/changelog-section.sh" apply --repo "$repo" --version 0.1.0 --base base --file "$file"
expect_eq 'applying twice changes nothing' "$(cat "$file")" "$once"

bash "$root/scripts/changelog-section.sh" apply --repo "$repo" --version 0.2.0 --base v0.1.0 --file "$file"
expect_eq 'the newest section comes first' "$(grep '^## ' "$file" | sed -e 's/ - .*//' | tr '\n' ' ')" '## [0.2.0] ## [0.1.0] '
cases=$((cases + 1))
grep -q 'Preamble text' "$file" || fail 'the preamble was lost'
expect_fail 'apply needs an existing file' 'not found' bash "$root/scripts/changelog-section.sh" apply --repo "$repo" --version 0.1.0 --file "$workdir/none.md"

# A stable release without a user-facing change still gets a section.
git -C "$repo" commit -q --allow-empty -m 'chore: nothing'
git -C "$repo" tag v0.2.1
out=$(section --version 0.2.1 --base v0.2.0)
cases=$((cases + 1))
grep -q 'No user-facing changes' <<<"$out" || fail 'an empty range has no placeholder'

printf '#!/usr/bin/env bash\necho "cliff: broken config" >&2\nexit 3\n' >"$workdir/broken-cliff"
chmod +x "$workdir/broken-cliff"
expect_fail 'a failing git-cliff fails the render with its diagnostic' 'cliff: broken config' env GIT_CLIFF="$workdir/broken-cliff" bash "$root/scripts/changelog-section.sh" render --version 0.2.1 --base v0.2.0 --repo "$repo"

# --- release-milestones.sh ---------------------------------------------------
cat >"$workdir/gh" <<'STUB'
#!/usr/bin/env bash
cat "$MILESTONES"
STUB
chmod +x "$workdir/gh"
export GH_CLI=$workdir/gh MILESTONES=$workdir/milestones.json
cat >"$MILESTONES" <<'JSON'
[
  {"title":"Foundation","description":"Fork charter,\nlocales and baseline","state":"closed","open_issues":0,"closed_issues":3,"closed_at":"2026-11-20T10:00:00Z","html_url":"https://example.test/m/2"},
  {"title":"Data scale","description":"","state":"closed","open_issues":0,"closed_issues":3,"closed_at":"2026-12-01T10:00:00Z","html_url":"https://example.test/m/3"}
]
[
  {"title":"Formats","description":"More formats","state":"closed","open_issues":0,"closed_issues":3,"closed_at":"2026-10-01T10:00:00Z","html_url":"https://example.test/m/4"},
  {"title":"Backlog","description":"x","state":"closed","open_issues":2,"closed_issues":1,"closed_at":"2026-12-02T10:00:00Z","html_url":"https://example.test/m/5"},
  {"title":"Empty","description":"x","state":"closed","open_issues":0,"closed_issues":0,"closed_at":"2026-11-25T10:00:00Z","html_url":"https://example.test/m/7"},
  {"title":"Open","description":"x","state":"open","open_issues":0,"closed_issues":3,"closed_at":null,"html_url":"https://example.test/m/6"}
]
JSON
milestones() { bash "$root/scripts/release-milestones.sh" --repo o/r "$@"; }
out=$(milestones --since 2026-11-01T00:00:00Z)
expect_eq 'milestones closed since the previous stable, oldest first' "$out" '### Milestones completed

- **Foundation**: Fork charter, locales and baseline ([milestone](https://example.test/m/2))
- **Data scale** ([milestone](https://example.test/m/3))'
expect_eq 'no previous stable lists every completed milestone' "$(milestones | grep -c '^- ')" 3
expect_eq 'nothing completed since a late date prints nothing' "$(milestones --since 2027-01-01T00:00:00Z)" ''
cases=$((cases + 1))
if milestones | grep -q Empty; then fail 'a closed milestone without any issue is listed'; fi
expect_fail 'a bad repository is refused' 'owner/repo' bash "$root/scripts/release-milestones.sh" --repo nope
expect_fail 'a bad date is refused' 'RFC 3339' milestones --since yesterday

if [[ "$failures" -ne 0 ]]; then
  printf '%d of %d changelog case(s) failed\n' "$failures" "$cases" >&2
  exit 1
fi
printf 'changelog-section-test passed (%d cases).\n' "$cases"
