#!/usr/bin/env bash
# Verifies the build attestations of a release against this repository's release
# workflows (ADR 0024). Every image digest and every archive named in
# release-manifest.json must verify; one failure fails the whole check.
#
#   scripts/verify-release-attestations.sh --repo owner/repo --manifest release-manifest.json \
#     --assets <dir> [--registry ghcr.io/owner] [--images-workflow image-build.yml] \
#     [--assets-workflow release.yml] [--source-ref refs/tags/vX.Y.Z]
#
# The signer of an attestation made inside a reusable workflow is the called
# workflow, and --signer-workflow must be repo-qualified (owner/repo/path); a bare
# path fails verification. --signer-repo alone is never used because it accepts any
# workflow of the repository. --source-ref is added only when a tag is pinned.
# GH_CLI overrides the gh binary (tests).
set -euo pipefail

fail() {
  printf 'verify-release-attestations: %s\n' "$1" >&2
  exit 1
}

repo= manifest= assets= registry= images_workflow=image-build.yml assets_workflow=release.yml source_ref=
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || fail "$1 needs a value"
  case $1 in
    --repo) repo=$2 ;;
    --manifest) manifest=$2 ;;
    --assets) assets=$2 ;;
    --registry) registry=$2 ;;
    --images-workflow) images_workflow=$2 ;;
    --assets-workflow) assets_workflow=$2 ;;
    --source-ref) source_ref=$2 ;;
    *) fail "unknown argument $1" ;;
  esac
  shift 2
done

[[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail "--repo must be owner/repo, got '$repo'"
[[ -f "$manifest" ]] || fail "manifest '$manifest' not found"
[[ -d "$assets" ]] || fail "assets directory '$assets' not found"
[[ "$images_workflow" != */* && "$assets_workflow" != */* ]] || fail 'workflow names are file names under .github/workflows, not paths'
[[ -n "$registry" ]] || registry=ghcr.io/${repo%%/*}
gh_cli=${GH_CLI:-gh}

verify() {
  local subject=$1 workflow=$2
  local -a args=(attestation verify "$subject" --repo "$repo" --signer-workflow "$repo/.github/workflows/$workflow")
  if [[ -n "$source_ref" ]]; then args+=(--source-ref "$source_ref"); fi
  printf 'verifying %s against %s\n' "$subject" "$workflow"
  "$gh_cli" "${args[@]}" >/dev/null || fail "attestation of $subject does not verify against $repo/.github/workflows/$workflow"
}

jq -e '(.images | length > 0) and (.assets | length > 0)' "$manifest" >/dev/null || fail 'the manifest lists no image or no asset'

while IFS=$'\t' read -r name digest; do
  verify "oci://$registry/$name@$digest" "$images_workflow"
done < <(jq -r '.images | to_entries[] | [.key, .value] | @tsv' "$manifest")

while IFS= read -r name; do
  [[ -f "$assets/$name" ]] || fail "asset $name from the manifest is missing in $assets"
  verify "$assets/$name" "$assets_workflow"
done < <(jq -r '.assets[].name' "$manifest")
