#!/usr/bin/env bash
# Tests scripts/repo-governance-apply.sh against a stub gh that keeps a tiny fake
# repository in a directory: idempotence, the effective output and the argument checks.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
export STATE=$workdir/state
mkdir -p "$STATE/rulesets"
printf '{"allow_squash_merge":false,"allow_merge_commit":true,"allow_rebase_merge":true}' >"$STATE/repo.json"
printf '[{"title":"Foundation"}]' >"$STATE/milestones.json"
: >"$STATE/calls.log"

stub=$workdir/gh
cat >"$stub" <<'STUB'
#!/usr/bin/env bash
# gh api [--method M] <path> [--input -]
shift
method=GET
if [[ "$1" == --method ]]; then method=$2; shift 2; fi
path=$1
body=''
if [[ "${2:-}" == --input ]]; then body=$(cat); fi
printf '%s %s\n' "$method" "$path" >>"$STATE/calls.log"
case "$method $path" in
  "GET repos/o/r") cat "$STATE/repo.json" ;;
  "PATCH repos/o/r") jq -s -c '.[0] * .[1]' "$STATE/repo.json" - <<<"$body" >"$STATE/repo.new" && mv "$STATE/repo.new" "$STATE/repo.json" ;;
  "GET repos/o/r/rulesets?per_page=100") jq -s -c '[.[] | {id, name}]' "$STATE"/rulesets/*.json 2>/dev/null || echo '[]' ;;
  "GET repos/o/r/rulesets/"*) cat "$STATE/rulesets/${path##*/}.json" ;;
  "POST repos/o/r/rulesets")
    id=$(($(ls "$STATE/rulesets" | wc -l) + 1))
    jq -c --argjson id "$id" '. + {id: $id}' <<<"$body" >"$STATE/rulesets/$id.json" ;;
  "PUT repos/o/r/rulesets/"*)
    id=${path##*/}
    jq -c --argjson id "$id" '. + {id: $id}' <<<"$body" >"$STATE/rulesets/$id.json" ;;
  "PUT repos/o/r/environments/stable-release") printf '%s' "$body" >"$STATE/environment.json" ;;
  "GET repos/o/r/environments/stable-release") cat "$STATE/environment.json" 2>/dev/null || exit 1 ;;
  "GET users/"*) echo '{"id": 4242}' ;;
  "GET repos/o/r/milestones?state=all&per_page=100") cat "$STATE/milestones.json" ;;
  "POST repos/o/r/milestones") jq -c --argjson m "$body" '. + [$m]' "$STATE/milestones.json" >"$STATE/m.new" && mv "$STATE/m.new" "$STATE/milestones.json" ;;
  *) echo "unexpected call: $method $path" >&2; exit 1 ;;
esac
STUB
chmod +x "$stub"
export GH_CLI=$stub

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
apply() { bash "$root/scripts/repo-governance-apply.sh" --repo o/r --app-id 77 "$@"; }

first=$(apply --reviewer octocat)
snapshot() { (cd "$STATE" && cat repo.json milestones.json environment.json rulesets/*.json | shasum -a 256); }
before=$(snapshot)
second=$(apply --reviewer octocat)
expect_eq 'state is unchanged by a second run' "$(snapshot)" "$before"
expect_eq 'three rulesets, no duplicates' "$(find "$STATE/rulesets" -name '*.json' | wc -l | tr -d ' ')" 3
expect_eq 'second run updates instead of creating' "$(grep -c 'ruleset .*: updated' <<<"$second")" 3
expect_eq 'first run creates' "$(grep -c 'ruleset .*: created' <<<"$first")" 3
expect_eq 'squash only' "$(jq -c '[.allow_squash_merge, .allow_merge_commit, .allow_rebase_merge]' "$STATE/repo.json")" '[true,false,false]'
expect_eq 'squash message defaults' "$(jq -r '.squash_merge_commit_title + "/" + .squash_merge_commit_message' "$STATE/repo.json")" 'PR_TITLE/PR_BODY'
expect_eq 'exactly the nine themed milestones' "$(jq -r '[.[].title] | sort | join("|")' "$STATE/milestones.json")" \
  'Backlog|Data scale|Formats|Foundation|Identity & federation|MySQL HeatWave|Promotion chains|Public feeds|Supply-chain security'
expect_eq 'an existing milestone is not recreated' "$(grep -c -x 'POST repos/o/r/milestones' "$STATE/calls.log")" 8
expect_eq 'stable tags exclude nightlies, no bypass' \
  "$(jq -c '[.name, .conditions.ref_name.include[0], .conditions.ref_name.exclude[0], ([.rules[].type] | join(",")), (.bypass_actors | length)]' "$STATE/rulesets/1.json")" \
  '["stable-tags","refs/tags/v*","refs/tags/v*-nightly.*","update,deletion",0]'
expect_eq 'nightly tags bypass only for the App' \
  "$(jq -c '[.name, .bypass_actors[0].actor_id, .bypass_actors[0].actor_type, ([.rules[].type] | join(","))]' "$STATE/rulesets/2.json")" \
  '["nightly-tags",77,"Integration","update,deletion"]'
expect_eq 'tag creation restricted to the App' \
  "$(jq -c '[.name, .bypass_actors[0].actor_id, ([.rules[].type] | join(","))]' "$STATE/rulesets/3.json")" '["tag-creation",77,"creation"]'
expect_eq 'reviewer is recorded on the environment' "$(jq -c '.reviewers' "$STATE/environment.json")" '[{"type":"User","id":4242}]'
apply >/dev/null
expect_eq 'a re-run without --reviewer keeps the reviewers' "$(jq -c '.reviewers' "$STATE/environment.json")" '[{"type":"User","id":4242}]'
rm "$STATE/environment.json"
apply >/dev/null
expect_eq 'a missing environment is created without reviewers' "$(jq -c '.' "$STATE/environment.json")" '{"wait_timer":0}'
expect_eq 'effective rulesets are printed' "$(grep -c '^ruleset ' <<<"$second" | tr -d ' ')" 6
expect_eq 'effective output names the squash defaults' "$(grep -c 'allow_merge_commit=false allow_rebase_merge=false squash_merge_commit_title=PR_TITLE' <<<"$second")" 1

for args in "--repo o/r" "--app-id 7" "--repo o/r --app-id x" "--bogus 1"; do
  cases=$((cases + 1))
  # shellcheck disable=SC2086
  if bash "$root/scripts/repo-governance-apply.sh" $args >/dev/null 2>&1; then fail "accepted: $args"; fi
done

if [[ "$failures" -ne 0 ]]; then
  printf '%d of %d governance checks failed\n' "$failures" "$cases" >&2
  exit 1
fi
printf 'repo-governance-apply-test passed (%d checks).\n' "$cases"
