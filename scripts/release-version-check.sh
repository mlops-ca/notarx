#!/usr/bin/env bash
set -euo pipefail

root=${RELEASE_VERSION_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
cd "$root"

# The committed tree carries the development version 0.0.0-dev (ADR 0024); release
# builds stamp the real version with scripts/stamp-version.sh and pass it here.
usage() {
  printf 'usage: %s [--expect <version>]\n' "$0" >&2
  exit 2
}
expect=
while [[ $# -gt 0 ]]; do
  case $1 in
    --expect)
      [[ $# -ge 2 ]] || usage
      expect=$2
      shift 2
      ;;
    *) usage ;;
  esac
done

version=$(tr -d '[:space:]' < VERSION)
semver='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
if [[ ! "$version" =~ $semver ]]; then
  printf 'VERSION is not a SemVer version: %s\n' "$version" >&2
  exit 1
fi
if [[ -n "$expect" && "$version" != "$expect" ]]; then
  printf 'VERSION is %s, expected %s.\n' "$version" "$expect" >&2
  exit 1
fi

check_value() {
  local label=$1
  local actual=$2
  if [[ "$actual" != "$version" ]]; then
    printf '%s version is %s, expected %s.\n' "$label" "$actual" "$version" >&2
    exit 1
  fi
}

check_value 'Console package' "$(node -p "require('./console/package.json').version")"
check_value 'Console lockfile' "$(node -p "require('./console/package-lock.json').version")"
check_value 'OpenAPI tools package' "$(node -p "require('./tools/openapi/package.json').version")"
check_value 'OpenAPI tools lockfile' "$(node -p "require('./tools/openapi/package-lock.json').version")"

for contract in api/openapi/native-hosted.yaml api/openapi/management-runtime.yaml api/openapi/management.yaml; do
  contract_version=$(awk '/^info:$/ { in_info = 1; next } in_info && /^  version:/ { print $2; exit }' "$contract")
  check_value "$contract" "$contract_version"
done
for bundle in api/openapi/native-hosted-v1.json api/openapi/management-runtime-v1.json; do
  check_value "$bundle" "$(node -p "require('./$bundle').info.version")"
done

# The notarxctl client takes its version from this same VERSION file. Keep its
# distribution channels linked: the release builder must emit version-stamped
# client archives, and the module install path documented for users must be the
# path go.mod declares. The packaged client's runtime version is verified by
# scripts/build-release-artifacts-test.sh.
test -d cmd/notarxctl
grep -Fq 'notarxctl_${version}_' scripts/build-release-artifacts.sh
cli_module=$(awk '/^module / { print $2; exit }' go.mod)
if [[ "$cli_module" != "github.com/mlops-ca/notarx" ]]; then
  printf 'go.mod module %s is not github.com/mlops-ca/notarx (ADR 0013).\n' "$cli_module" >&2
  exit 1
fi
# go install resolves the module path as a repository, so it must be the GitHub
# repository that serves the tags (ADR 0008). CI names it; a checkout falls back
# to its origin remote and skips the check when neither is known.
source_repository=${GITHUB_REPOSITORY:-}
if [[ -z "$source_repository" ]]; then
  origin_url=$(git remote get-url origin 2>/dev/null || true)
  if [[ "$origin_url" =~ github\.com[:/]([^/]+/[^/]+)$ ]]; then
    source_repository=${BASH_REMATCH[1]%.git}
  fi
fi
if [[ -n "$source_repository" && "$cli_module" != "github.com/$source_repository" ]]; then
  printf 'go.mod module %s does not match the source repository github.com/%s.\n' "$cli_module" "$source_repository" >&2
  exit 1
fi
for doc in docs/cli.md; do
  if ! grep -Fq "go install $cli_module/cmd/notarxctl@" "$doc"; then
    printf '%s must document the module install path %s/cmd/notarxctl.\n' "$doc" "$cli_module" >&2
    exit 1
  fi
done

printf 'Release version check passed: %s.\n' "$version"
