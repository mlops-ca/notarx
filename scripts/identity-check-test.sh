#!/usr/bin/env bash
# Fixture tests for scripts/identity-check.sh. Each scenario builds a throwaway
# repository, so no network, Docker or real history is involved. The patterns
# are assembled from parts so this file stays greppable by the gate it tests.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
check=$root/scripts/identity-check.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

patterns=("cc""sert" "artifact""-gateway" "artifact""_gateway" "GATE""WAY_X" "ag""ctl" "gate""way_total" "Artifact"" Gateway" "artifact"" gateway" "ARTIFACT"" GATEWAY" "a""g-restore-x" "A""G_ISOLATION_LOG")
joined=$(printf '%s ' "${patterns[@]}")

repo=""
new_repo() {
  repo=$work/$1
  mkdir -p "$repo"
  git -C "$repo" init -q -b main
}
plant() { # path text
  mkdir -p "$repo/$(dirname "$1")"
  printf '%s\n' "$2" >"$repo/$1"
  git -C "$repo" add -f "$1"
}
passed=0
expect_pass() {
  local label=$1 output
  if ! output=$("$check" --root "$repo" 2>&1); then
    printf 'FAIL %s: expected success\n%s\n' "$label" "$output" >&2
    exit 1
  fi
  passed=$((passed + 1)); printf 'ok   %s\n' "$label"
}
expect_fail() {
  local label=$1 needle=$2 output
  if output=$("$check" --root "$repo" 2>&1); then
    printf 'FAIL %s: expected failure\n' "$label" >&2
    exit 1
  fi
  if [[ "$output" != *"$needle"* ]]; then
    printf 'FAIL %s: output lacks %s\n%s\n' "$label" "$needle" "$output" >&2
    exit 1
  fi
  passed=$((passed + 1)); printf 'ok   %s\n' "$label"
}

# The allow-list is exactly the one in ADR 0013 (Decision, "Identity").
expected_base=$'LICENSE\nNOTICE\ndocs/upstream.md\nCHANGELOG*\ndocs/adr\n*.zh-CN.md'
if [[ "$("$check" --list-base)" != "$expected_base" ]]; then
  printf 'FAIL base allow-list differs from ADR 0013:\n%s\n' "$("$check" --list-base)" >&2
  exit 1
fi
passed=$((passed + 1)); printf 'ok   base allow-list matches ADR 0013\n'

# Every documented addition carries a reason.
while IFS=$'\t' read -r spec reason; do
  [[ -n "$spec" && -n "$reason" ]] || { printf 'FAIL extra allow-list entry without a reason: %s\n' "$spec" >&2; exit 1; }
done < <("$check" --list-extra; "$check" --list-ag)
passed=$((passed + 1)); printf 'ok   every extra allow-list entry has a reason\n'

# Every abbreviation-only allowance names something that is tracked.
while IFS=$'\t' read -r spec _; do
  [[ -n "$(git -C "$root" ls-files -- "$spec")" ]] || { printf 'FAIL ag allow-list entry matches no tracked file: %s\n' "$spec" >&2; exit 1; }
done < <("$check" --list-ag)
passed=$((passed + 1)); printf 'ok   every ag allow-list entry names tracked files\n'

new_repo clean
plant README.md 'notarx is the product name'
expect_pass 'a clean tree passes'

for pattern in "${patterns[@]}"; do
  new_repo "flagged-$pattern"
  plant docs/guide.md "uses $pattern here"
  expect_fail "flags $pattern in a doc" 'docs/guide.md'
done

new_repo allow-base
plant LICENSE "$joined"
plant NOTICE "$joined"
plant docs/upstream.md "$joined"
plant CHANGELOG.md "$joined"
plant CHANGELOG-old.md "$joined"
plant docs/adr/0001-x.md "$joined"
plant docs/adr/nested/0002-y.md "$joined"
plant docs/guide.zh-CN.md "$joined"
expect_pass 'the ADR allow-list is accepted'

# Look-alike paths are not on the allow-list.
for path in LICENSE.txt NOTICE.md docs/upstream.md.bak docs/upstream/other.md docs/adrs/x.md docs/guide.zh-TW.md src/CHANGELOG.md; do
  new_repo "lookalike-$(printf '%s' "$path" | tr '/.' '__')"
  plant "$path" "$joined"
  expect_fail "does not allow $path" "$path"
done

while IFS=$'\t' read -r spec reason; do
  new_repo "extra-$(printf '%s' "$spec" | tr '/.*' '___')"
  path=${spec//\*/x}
  plant "$path" "$joined"
  expect_pass "documented addition $spec is accepted"
done < <("$check" --list-extra)

# Frozen zh-CN text and the two literal exemptions.
new_repo line-exemptions
plant web/messages.ts "  key: \"$(printf '\xe6\x97\xa5 ')ag""ctl\","
plant docs/prose.md "$(printf '\xe6\x97\xa5 ')ag""ctl"
plant docs/links.md 'See [ADR 0007](adr/0007-ag''ctl-cli-shape.md).'
plant CONTRIBUTING.md 'Upstream: cc''sert/artifact''-gateway@<sha>'
expect_pass 'CJK lines, the ADR 0007 link and the trailer literal are exempt'

new_repo cjk-string-does-not-hide-its-neighbours
plant docs/site-map.json "{ \"zh-CN\": \"$(printf '\xe6\x97\xa5 ')ag""ctl\", \"en\": \"run ag""ctl\" }"
expect_fail 'a CJK string does not hide another string on the line' 'docs/site-map.json'

new_repo link-is-not-a-blanket
plant docs/links.md 'See [ADR 0007](adr/0007-ag''ctl-cli-shape.md) and run ag''ctl.'
expect_fail 'the ADR 0007 link does not hide another match on the line' 'docs/links.md'

# The short abbreviation is matched as a whole word only, and the Console keeps
# its internal CSS names.
new_repo ag-word-boundary
plant docs/words.md 'flag-name, stag_A, the --flag-x switch and a g-b'
plant console/src/styles.css '.a''g-card { color: var(--a''g-border-default); }'
plant DESIGN.md 'use a''g-page-stack'
expect_pass 'longer words and the internal Console CSS names are accepted'

new_repo ag-in-go
plant internal/backupops/x.go 'project := "a''g-restore-x"'
expect_fail 'flags the short abbreviation in Go code' 'internal/backupops/x.go'
new_repo ag-digit-and-lower
plant docs/a.md 'fixture a''g-1 and a''g-x'
plant scripts/y.sh 'export A''G_1=1 A''G_foo=2'
expect_fail 'flags digits and lower-case letters after the abbreviation' 'docs/a.md'
new_repo ag-in-script
plant scripts/x.sh 'export A''G_LOG=1'
expect_fail 'flags the upper-case abbreviation in a script' 'scripts/x.sh'
new_repo ag-in-console-lookalike
plant consolex/x.css '.a''g-card {}'
expect_fail 'the Console allowance does not cover look-alike paths' 'consolex/x.css'

# Binary files are not searched.
new_repo binary
printf 'cc\0sert artifact-gateway\n' >"$repo/blob.bin"
git -C "$repo" add blob.bin
expect_pass 'binary files are skipped'

printf 'identity-check-test: %d checks passed\n' "$passed"
