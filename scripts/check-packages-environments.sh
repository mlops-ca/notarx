#!/usr/bin/env bash
# Read-only check of the GitHub environments that guard package signing and
# publishing (ADR 0025 slice 4). Requires gh (authenticated with repository
# admin read access) and jq.
#
# Usage: check-packages-environments.sh [--repo owner/name] [--require-secrets]
set -euo pipefail

repo=${GITHUB_REPOSITORY:-mlops-ca/notarx}
require_secrets=0
tag_pattern='v*'

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) repo=${2:?--repo needs a value}; shift 2 ;;
    --require-secrets) require_secrets=1; shift ;;
    -h|--help) sed -n '2,7p' "$0"; exit 0 ;;
    *) printf 'unknown argument %s\n' "$1" >&2; exit 2 ;;
  esac
done

for tool in gh jq; do
  command -v "$tool" >/dev/null 2>&1 || { printf 'missing required tool %s\n' "$tool" >&2; exit 2; }
done

failures=0
fail() { printf 'FAIL: %s\n' "$*" >&2; failures=$((failures + 1)); }
ok() { printf 'ok: %s\n' "$*"; }

api() { gh api -H 'Accept: application/vnd.github+json' "$@"; }

# Names of the secrets each environment must hold, and must never hold.
required_secret() {
  case "$1" in
    packages-signing) printf 'PACKAGES_SIGNING_SUBKEY' ;;
    packages-publish) printf 'CLOUDFLARE_API_TOKEN' ;;
  esac
}
forbidden_pattern() {
  case "$1" in
    packages-signing) printf '^CLOUDFLARE|^CF_|^R2_|^AWS_' ;;
    packages-publish) printf 'SIGNING|GPG|PGP' ;;
  esac
}

check_environment() {
  local env=$1 settings policies secrets

  if ! settings=$(api "repos/$repo/environments/$env" 2>/dev/null); then
    fail "$env: environment does not exist (or cannot be read)"
    return
  fi
  ok "$env: exists"

  if jq -e '[.protection_rules[]? | select(.type == "required_reviewers") | .reviewers[]?] | length >= 2' >/dev/null <<<"$settings"; then
    ok "$env: at least two required reviewers"
  else
    fail "$env: needs required reviewers (at least two, so self-review prevention still leaves an approver)"
  fi

  if jq -e '[.protection_rules[]? | select(.type == "required_reviewers") | .prevent_self_review] | any' >/dev/null <<<"$settings"; then
    ok "$env: self-review prevented"
  else
    fail "$env: self-review is allowed"
  fi

  if jq -e '.deployment_branch_policy.custom_branch_policies == true' >/dev/null <<<"$settings"; then
    ok "$env: deployments restricted to custom policies"
  else
    fail "$env: deployment_branch_policy must use custom policies (tags only)"
    return
  fi

  if ! policies=$(api "repos/$repo/environments/$env/deployment-branch-policies" 2>/dev/null); then
    fail "$env: cannot read deployment policies"
    return
  fi
  if jq -e --arg p "$tag_pattern" '[.branch_policies[]? | select(.type == "tag" and .name == $p)] | length == 1' >/dev/null <<<"$policies"; then
    ok "$env: tag policy $tag_pattern"
  else
    fail "$env: needs exactly the tag policy $tag_pattern"
  fi
  if jq -e '[.branch_policies[]? | select(.type != "tag")] | length == 0' >/dev/null <<<"$policies"; then
    ok "$env: no branch policies"
  else
    fail "$env: branch policies are not allowed, only the release tag pattern"
  fi

  if ! secrets=$(api --paginate "repos/$repo/environments/$env/secrets" --jq '.secrets[].name' 2>/dev/null); then
    fail "$env: cannot read environment secrets"
    return
  fi
  if [[ -n "$secrets" ]] && grep -Eq "$(forbidden_pattern "$env")" <<<"$secrets"; then
    fail "$env: holds a secret that belongs to the other environment"
  else
    ok "$env: no secret from the other environment"
  fi
  if [[ $require_secrets -eq 1 ]]; then
    if grep -Fxq "$(required_secret "$env")" <<<"$secrets"; then
      ok "$env: secret $(required_secret "$env") present"
    else
      fail "$env: secret $(required_secret "$env") missing"
    fi
  fi
}

for env in packages-signing packages-publish; do
  check_environment "$env"
done

# Signing and publishing secrets must not also exist at repository level, where
# every workflow job could read them.
if repo_secrets=$(api --paginate "repos/$repo/actions/secrets" --jq '.secrets[].name' 2>/dev/null); then
  if grep -Eq '^(PACKAGES_SIGNING_|CLOUDFLARE_API_TOKEN$)' <<<"$repo_secrets"; then
    fail "repository: package signing or Cloudflare secret exists outside its environment"
  else
    ok "repository: no package secret outside its environment"
  fi
else
  fail "repository: cannot read repository secrets"
fi

if [[ $failures -gt 0 ]]; then
  printf '%d check(s) failed for %s\n' "$failures" "$repo" >&2
  exit 1
fi
printf 'all package environment checks passed for %s\n' "$repo"
