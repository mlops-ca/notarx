#!/usr/bin/env bash
# Tests scripts/check-packages-environments.sh against a stub gh.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/bin" "$work/fx"

# The stub serves fixtures from $FX by request path; a missing file is a 404.
cat >"$work/bin/gh" <<'STUB'
#!/usr/bin/env bash
path=""; jqexpr=""
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
  case "${args[i]}" in
    -H) i=$((i + 1)) ;;
    --jq) jqexpr=${args[i + 1]}; i=$((i + 1)) ;;
    -*) ;;
    *) path=${args[i]} ;;
  esac
done
file="$FX/$(printf '%s' "$path" | tr '/' '_')"
[[ -f "$file" ]] || { echo '{"message":"Not Found"}' >&2; exit 1; }
if [[ -n "$jqexpr" ]]; then jq -r "$jqexpr" "$file"; else cat "$file"; fi
STUB
chmod +x "$work/bin/gh"

# EXTRA_POLICY (optional, "type:name") adds a second deployment policy; REVIEWER_TYPE (default User).
fixture() { # env-name self-review reviewers policy-type policy-name secrets...
  local env=$1 self=$2 reviewers=$3 ptype=$4 pname=$5; shift 5
  local r='[]'
  [[ $reviewers -gt 0 ]] && r=$(jq -nc --argjson n "$reviewers" --arg t "${REVIEWER_TYPE:-User}" '[range($n) | {type:$t, reviewer:{id:(if $t == "Team" then 1 else . end)}}]')
  jq -n --argjson self "$self" --argjson r "$r" \
    '{name:"x", protection_rules:[{type:"required_reviewers", prevent_self_review:$self, reviewers:$r}],
      deployment_branch_policy:{protected_branches:false, custom_branch_policies:true}}' \
    >"$FX/repos_o_r_environments_$env"
  jq -n --arg t "$ptype" --arg n "$pname" --arg x "${EXTRA_POLICY:-}" \
    '{branch_policies:([{name:$n, type:$t}] + (if $x == "" then [] else [{type:($x | split(":")[0]), name:($x | split(":")[1])}] end))}' \
    >"$FX/repos_o_r_environments_${env}_deployment-branch-policies"
  printf '%s\n' "$@" | jq -R . | jq -sc '{secrets: map({name: .}) | map(select(.name != ""))}' \
    >"$FX/repos_o_r_environments_${env}_secrets"
}

run() {
  PATH="$work/bin:$PATH" FX="$FX" bash "$root/scripts/check-packages-environments.sh" --repo o/r "$@"
}
expect_pass() { run "$@" >"$work/out" 2>&1 || { cat "$work/out"; printf 'expected pass: %s\n' "$label" >&2; exit 1; }; }
expect_fail() { # pattern args...
  local pat=$1; shift
  if run "$@" >"$work/out" 2>&1; then printf 'expected failure: %s\n' "$label" >&2; exit 1; fi
  grep -q -- "$pat" "$work/out" || { cat "$work/out"; printf 'missing %s: %s\n' "$pat" "$label" >&2; exit 1; }
}

good() {
  FX="$work/fx"; rm -f "$FX"/*
  fixture packages-signing true 2 tag 'v*' PACKAGES_SIGNING_SUBKEY
  fixture packages-publish true 2 tag 'v*' CLOUDFLARE_API_TOKEN
  echo '{"secrets":[{"name":"OTHER"}]}' >"$FX/repos_o_r_actions_secrets"
  echo '{"secrets":[]}' >"$FX/repos_o_r_actions_organization-secrets"
}

label="valid settings"; good; expect_pass; expect_pass --require-secrets
label="missing environment"; good; rm "$FX/repos_o_r_environments_packages-publish"
expect_fail 'packages-publish: environment does not exist'
label="one reviewer"; good; fixture packages-signing true 1 tag 'v*' PACKAGES_SIGNING_SUBKEY
expect_fail 'packages-signing: needs at least two individual'
label="self review"; good; fixture packages-publish false 2 tag 'v*' CLOUDFLARE_API_TOKEN
expect_fail 'packages-publish: self-review is allowed'
label="branch policy"; good; fixture packages-signing true 2 branch main PACKAGES_SIGNING_SUBKEY
expect_fail 'needs exactly the tag policy'
label="extra branch policy"; good; EXTRA_POLICY=branch:main fixture packages-signing true 2 tag 'v*' PACKAGES_SIGNING_SUBKEY
expect_fail 'branch policies are not allowed'
label="team reviewers only"; good; REVIEWER_TYPE=Team fixture packages-signing true 2 tag 'v*' PACKAGES_SIGNING_SUBKEY
expect_fail 'needs at least two individual'
label="wrong tag pattern"; good; fixture packages-signing true 2 tag '*' PACKAGES_SIGNING_SUBKEY
expect_fail 'needs exactly the tag policy'
label="cloudflare in signing"; good; fixture packages-signing true 2 tag 'v*' PACKAGES_SIGNING_SUBKEY CLOUDFLARE_API_TOKEN
expect_fail 'packages-signing: holds a secret'
label="signing key in publish"; good; fixture packages-publish true 2 tag 'v*' CLOUDFLARE_API_TOKEN PACKAGES_SIGNING_SUBKEY
expect_fail 'packages-publish: holds a secret'
label="secret missing"; good; fixture packages-publish true 2 tag 'v*'
expect_pass; expect_fail 'CLOUDFLARE_API_TOKEN missing' --require-secrets
label="repository secret"; good; echo '{"secrets":[{"name":"PACKAGES_SIGNING_SUBKEY"}]}' >"$FX/repos_o_r_actions_secrets"
expect_fail 'actions/secrets: package signing'
label="organization secret"; good; echo '{"secrets":[{"name":"CLOUDFLARE_API_TOKEN"}]}' >"$FX/repos_o_r_actions_organization-secrets"
expect_fail 'actions/organization-secrets: package signing'
label="unreadable organization secrets"; good; rm "$FX/repos_o_r_actions_organization-secrets"
expect_fail 'actions/organization-secrets: cannot be read'

printf 'check-packages-environments tests passed\n'
