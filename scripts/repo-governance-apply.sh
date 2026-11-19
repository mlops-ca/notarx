#!/usr/bin/env bash
# Applies the repository governance of ADR 0024 (needs repository admin). Idempotent:
# a second run changes nothing and prints the same effective settings.
#
#   scripts/repo-governance-apply.sh --repo owner/name --app-id <release App id> [--reviewer login]...
#
# Applies: squash-only merges (default message = PR title and body); rulesets stable-tags,
# nightly-tags and tag-creation; the stable-release environment; the themed milestones.
# The release App must be installed first (docs/releasing.md): tag creation is restricted to
# it, so applying the rulesets without it would lock every release out. GH_CLI overrides gh.
set -euo pipefail

gh_cli=${GH_CLI:-gh}
repo=''
app_id=''
reviewers=()

fail() {
  printf 'repo-governance-apply: %s\n' "$1" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || fail "$1 needs a value"
  case $1 in
    --repo) repo=$2 ;;
    --app-id) app_id=$2 ;;
    --reviewer) reviewers+=("$2") ;;
    *) fail "unknown argument $1" ;;
  esac
  shift 2
done
[[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail '--repo owner/name is required'
[[ "$app_id" =~ ^[0-9]+$ ]] || fail '--app-id (the numeric release App id) is required; register the App first'

# The themed milestones (ADR 0024, decision D18): exactly these nine, never numbered.
milestones=(
  'Foundation'
  'Data scale'
  'Identity & federation'
  'Promotion chains'
  'MySQL HeatWave'
  'Formats'
  'Public feeds'
  'Supply-chain security'
  'Backlog'
)

get() { "$gh_cli" api "$1"; }
send() { # send <method> <path> <json body>
  printf '%s' "$3" | "$gh_cli" api --method "$1" "$2" --input - >/dev/null
}

# --- merge methods ---------------------------------------------------------------
send PATCH "repos/$repo" '{"allow_squash_merge":true,"allow_merge_commit":false,"allow_rebase_merge":false,"squash_merge_commit_title":"PR_TITLE","squash_merge_commit_message":"PR_BODY"}'
printf 'merge methods: squash only, default message = PR title and body\n'

# --- rulesets ----------------------------------------------------------------------
# ruleset <name> <include> <exclude> <rules json> <bypass json>
ruleset() {
  local name=$1 include=$2 exclude=$3 rules=$4 bypass=$5 body id
  body=$(jq -n -c --arg name "$name" --arg include "$include" --arg exclude "$exclude" \
    --argjson rules "$rules" --argjson bypass "$bypass" '{
      name: $name, target: "tag", enforcement: "active",
      conditions: {ref_name: {include: [$include], exclude: (if $exclude == "" then [] else [$exclude] end)}},
      rules: $rules, bypass_actors: $bypass}')
  id=$(get "repos/$repo/rulesets?per_page=100" | jq -r --arg name "$name" '[.[] | select(.name == $name)] | first | .id // empty')
  if [[ -n "$id" ]]; then
    send PUT "repos/$repo/rulesets/$id" "$body"
    printf 'ruleset %s: updated\n' "$name"
  else
    send POST "repos/$repo/rulesets" "$body"
    printf 'ruleset %s: created\n' "$name"
  fi
}
app_bypass=$(jq -n -c --argjson id "$app_id" '[{actor_id: $id, actor_type: "Integration", bypass_mode: "always"}]')

# Stable tags are immutable for everyone, the App included.
ruleset stable-tags 'refs/tags/v*' 'refs/tags/v*-nightly.*' '[{"type":"update"},{"type":"deletion"}]' '[]'
# Nightly tags cannot be moved; only the release App may delete them (pruning).
ruleset nightly-tags 'refs/tags/v*-nightly.*' '' '[{"type":"update"},{"type":"deletion"}]' "$app_bypass"
# Only the release App creates release tags.
ruleset tag-creation 'refs/tags/v*' '' '[{"type":"creation"}]' "$app_bypass"

# --- environment ---------------------------------------------------------------------
# Reviewers are replaced only when --reviewer is given; a re-run without it must never clear the
# approval gate of an existing environment.
if [[ ${#reviewers[@]} -gt 0 ]]; then
  ids='[]'
  for login in "${reviewers[@]}"; do
    ids=$(jq -c --argjson id "$(get "users/$login" | jq '.id')" '. + [{type: "User", id: $id}]' <<<"$ids")
  done
  send PUT "repos/$repo/environments/stable-release" "$(jq -n -c --argjson reviewers "$ids" '{wait_timer: 0, reviewers: $reviewers}')"
  printf 'environment stable-release: reviewers set\n'
elif get "repos/$repo/environments/stable-release" >/dev/null 2>&1; then
  printf 'environment stable-release: present, left unchanged\n'
else
  send PUT "repos/$repo/environments/stable-release" '{"wait_timer":0}'
  printf 'environment stable-release: created\n'
fi

# --- milestones ----------------------------------------------------------------------
existing=$(get "repos/$repo/milestones?state=all&per_page=100" | jq -r '.[].title')
for title in "${milestones[@]}"; do
  if grep -q -F -x -e "$title" <<<"$existing"; then
    printf 'milestone %s: present\n' "$title"
  else
    send POST "repos/$repo/milestones" "$(jq -n -c --arg title "$title" '{title: $title}')"
    printf 'milestone %s: created\n' "$title"
  fi
done

# --- effective state ---------------------------------------------------------------------
printf '\nEffective settings of %s\n' "$repo"
get "repos/$repo" | jq -r '"allow_squash_merge=\(.allow_squash_merge) allow_merge_commit=\(.allow_merge_commit) allow_rebase_merge=\(.allow_rebase_merge) squash_merge_commit_title=\(.squash_merge_commit_title) squash_merge_commit_message=\(.squash_merge_commit_message)"'
for id in $(get "repos/$repo/rulesets?per_page=100" | jq -r '.[].id'); do
  get "repos/$repo/rulesets/$id" | jq -r '"ruleset \(.name) [\(.enforcement)] include=\(.conditions.ref_name.include|join(",")) exclude=\(.conditions.ref_name.exclude|join(",")) rules=\([.rules[].type]|join(",")) bypass=\([.bypass_actors[]|"\(.actor_type):\(.actor_id)"]|join(",")|if . == "" then "none" else . end)"'
done
