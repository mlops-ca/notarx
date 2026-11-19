#!/usr/bin/env bash
# Structural checks of .github/workflows/release.yml (ADR 0024, slice 4): the rules that
# cannot be exercised without publishing. Needs yq (mikefarah) and jq.
set -euo pipefail

command -v yq >/dev/null 2>&1 && yq --version 2>&1 | grep -q mikefarah || {
  printf 'release-workflow-check: needs mikefarah yq (https://github.com/mikefarah/yq) and jq\n' >&2
  exit 1
}
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
file=${RELEASE_WORKFLOW_FILE:-$root/.github/workflows/release.yml}
json=$(yq -o=json '.' "$file")
failures=0

check() { # check <description> <jq filter that must be true>
  if ! jq -e "$2" <<<"$json" >/dev/null; then
    printf 'FAIL: %s\n' "$1" >&2
    failures=$((failures + 1))
  fi
}

check 'triggers are exactly schedule and workflow_dispatch (no tag or push trigger)' '.on | keys == ["schedule", "workflow_dispatch"]'
check 'dispatch defaults to a dry run' '.on.workflow_dispatch.inputs.dry_run.default == true'
check 'concurrency group release without cancellation' '.concurrency.group == "release" and .concurrency["cancel-in-progress"] == false'
check 'default permissions are read-only' '.permissions == {"contents": "read"}'
check 'the ADR job graph exists: plan, gates, build, publish, prune' '["plan","gates","build","publish","prune"] - (.jobs | keys) == []'
check 'verify-candidate is gone' '.jobs | has("verify-candidate") | not'
check 'plan is read-only' '[.jobs.plan.permissions[]] | all(. == "read")'
check 'only publish and prune get contents: write' '[.jobs | to_entries[] | select(.value.permissions.contents == "write") | .key] | sort == ["prune", "publish"]'
check 'only the image build and prune get packages: write' '[.jobs | to_entries[] | select(.value.permissions.packages == "write") | .key] | sort == ["build-images", "prune"]'
check 'only the attest jobs get id-token and attestations: write' '[.jobs | to_entries[] | select(.value.permissions["id-token"] == "write" or .value.permissions.attestations == "write") | .key] | sort == ["build-images", "publish"]'
check 'publish needs the build, prune needs publish' '(.jobs.publish.needs | index("build")) != null and (.jobs.prune.needs | index("publish")) != null'
check 'build needs gates through both reusable calls' '(.jobs["build-archives"].needs | index("gates")) != null and (.jobs["build-images"].needs | index("gates")) != null'
check 'the gates run the complete shared suite on the planned commit, every class, mainline scope' '.jobs["gates-suite"] | .uses == "./.github/workflows/test-suite.yml" and .with.scope == "mainline" and .with.sha == "${{ needs.plan.outputs.sha }}" and ([.with.go, .with.console, .with.docs, .with.images] | all(. == true))'
check 'the gate job requires the suite' '(.jobs.gates.needs | index("gates-suite")) != null'
check 'the plan reads no check runs' '(.jobs.plan.permissions | has("checks") | not)'
check 'the tag and pre-release are created by the last step of publish' '.jobs.publish.steps | last | (.run | contains("gh release create")) and (.run | contains("--prerelease")) and (.run | contains("--target \"$SHA\""))'
check 'no other step creates a tag or release' '[.jobs | to_entries[] | .value.steps // [] | .[] | .run // "" | select(contains("gh release create") or contains("git tag") or contains("git push"))] | length == 1'
check 'the archives and both package formats are attested and attached to the release' '(.jobs.publish.steps | map(select(.uses // "" | startswith("actions/attest-build-provenance")) | .with["subject-path"]) | join("\n") | contains("dist/*.tar.gz") and contains("dist/*.deb") and contains("dist/*.rpm")) and (.jobs.publish.steps | last | .run | contains("dist/*.deb") and contains("dist/*.rpm"))'
check 'every action is pinned by commit SHA' '[.jobs[] | (.uses // empty), (.steps // [] | .[] | .uses // empty)] | map(select(startswith("./") | not)) | all(test("@[0-9a-f]{40}( |$)"))'
check 'a dry run still runs the gates and the build, and only publish needs publish=true' '(.jobs["gates-suite"].if | contains("outputs.skip != '"'"'true'"'"'")) and (.jobs.gates.if | contains("outputs.skip != '"'"'true'"'"'")) and (.jobs.publish.if | contains("outputs.publish == '"'"'true'"'"'"))'
check 'a dry run pushes no image' '.jobs["build-images"].with["dry-run"] | contains("outputs.publish != '"'"'true'"'"'")'
check 'image tags are nightly and the version' '.jobs.plan.steps | map(.run // "") | any(contains("echo nightly"))'
check 'prune never runs for a skipped or failed nightly' '.jobs.prune.if | contains("needs.publish.result == '"'"'success'"'"'")'

if [[ "$failures" -ne 0 ]]; then
  printf '%d release workflow check(s) failed\n' "$failures" >&2
  exit 1
fi
printf 'release-workflow-check passed.\n'
