#!/usr/bin/env bash
# Release upgrade gate: previous notarx release -> candidate, with synthetic
# resources only and no .env.
#
# Usage: scripts/upgrade-readiness.sh [--from-tag vX.Y.Z]
#
# The previous release is a stable notarx tag matching ^v[0-9]+\.[0-9]+\.[0-9]+$
# (nightly pre-releases are not upgrade sources). Without --from-tag the highest
# stable tag is used; NOTARX_UPGRADE_FROM_TAG is an alternative to the flag.
# While no stable tag exists there is nothing to upgrade from and the gate
# reports that and succeeds; it becomes meaningful once a second release exists
# (0.2.0). Until then `make backup-transfer-test` runs the same properties
# against a synthetic 000002 migration (TestLocalReleaseUpgrade).
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

stable_pattern='^v[0-9]+\.[0-9]+\.[0-9]+$'
tag=${NOTARX_UPGRADE_FROM_TAG:-}
while [[ $# -gt 0 ]]; do
  case "$1" in
    --from-tag) tag=${2:?--from-tag needs a value}; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo/p' "$0" | sed '$d;s/^# \{0,1\}//'; exit 0 ;;
    *) printf 'Unknown argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done

if [[ -z "$tag" ]]; then
  tag=$(git tag --list 'v*' | grep -E "$stable_pattern" | sort -V | tail -n 1 || true)
  if [[ -z "$tag" ]]; then
    if [[ "$(git rev-parse --is-shallow-repository)" == true ]]; then
      # A shallow clone may simply lack the tags; do not pass vacuously.
      printf 'upgrade-readiness: shallow clone without release tags; run git fetch --tags --unshallow.\n' >&2
      exit 1
    fi
    printf 'upgrade-readiness: no stable notarx release tag exists; nothing to upgrade from (not a pass of the upgrade gate).\n'
    exit 0
  fi
fi
if [[ ! "$tag" =~ $stable_pattern ]]; then
  printf 'upgrade-readiness: %s is not a stable release tag (vX.Y.Z).\n' "$tag" >&2
  exit 2
fi
git rev-parse --verify --quiet "$tag^{commit}" >/dev/null || {
  printf 'upgrade-readiness: release tag %s is not available in this clone.\n' "$tag" >&2
  exit 1
}

printf 'upgrade-readiness: %s -> candidate\n' "$tag"
BACKUPOPS_UPGRADE_FROM_TAG="$tag" \
BACKUPOPS_DOCKER_TEST=1 \
BACKUPOPS_DOCKER_CONTEXT="$(docker context show)" \
  go test -v -count=1 ./internal/backupops -run '^TestLocalReleaseUpgrade$'
