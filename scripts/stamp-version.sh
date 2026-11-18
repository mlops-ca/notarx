#!/usr/bin/env bash
# Stamp a release version into the build workspace (ADR 0024). The committed tree
# carries 0.0.0-dev; every build leg runs this before make so that no release
# commit is needed. Nothing stamped is committed.
#
#   scripts/stamp-version.sh 0.3.0-nightly.2
#
# Rewrites VERSION, the Console and OpenAPI tools packages and lockfiles, the
# OpenAPI info.version of the three contracts, and regenerates the two OpenAPI
# bundles that embed the version.
set -euo pipefail

root=${STAMP_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
openapi_tools=${OPENAPI_TOOLS:-$root/tools/openapi}

if [[ $# -ne 1 ]]; then
  printf 'usage: %s <version>\n' "$0" >&2
  exit 2
fi
version=$1
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]]; then
  printf 'not a SemVer version without build metadata: %s\n' "$version" >&2
  exit 2
fi

cd "$root"
printf '%s\n' "$version" > VERSION

for package_dir in console tools/openapi; do
  npm --prefix "$package_dir" version "$version" --no-git-tag-version --allow-same-version >/dev/null
done

for contract in api/openapi/native-hosted.yaml api/openapi/management-runtime.yaml api/openapi/management.yaml; do
  STAMP_VERSION=$version yq -i '.info.version = strenv(STAMP_VERSION)' "$contract"
done

redocly=$openapi_tools/node_modules/.bin/redocly
if [[ ! -x "$redocly" ]]; then
  printf 'OpenAPI tools are not installed (%s); run npm --prefix tools/openapi ci\n' "$redocly" >&2
  exit 1
fi
"$redocly" bundle api/openapi/native-hosted.yaml --output api/openapi/native-hosted-v1.json --ext json >/dev/null 2>&1
"$redocly" bundle api/openapi/management-runtime.yaml --output api/openapi/management-runtime-v1.json --ext json >/dev/null 2>&1

printf 'Stamped version %s.\n' "$version"
