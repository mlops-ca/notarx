#!/usr/bin/env bash
# Tests scripts/stamp-version.sh and scripts/release-version-check.sh on a copy of
# the tree, so the checkout is never modified (ADR 0024).
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
cd "$root"

if [[ ! -x tools/openapi/node_modules/.bin/redocly ]]; then
  npm --prefix tools/openapi ci --ignore-scripts --no-audit --no-fund >/dev/null
fi

copy="$workdir/tree"
mkdir -p "$copy/scripts" "$copy/console" "$copy/tools/openapi" "$copy/cmd/notarxctl" "$copy/docs"
cp VERSION go.mod "$copy/"
cp -R api "$copy/api"
cp console/package.json console/package-lock.json "$copy/console/"
cp tools/openapi/package.json tools/openapi/package-lock.json "$copy/tools/openapi/"
cp scripts/build-release-artifacts.sh scripts/release-version-check.sh scripts/stamp-version.sh "$copy/scripts/"
cp docs/cli.md "$copy/docs/"

snapshot() {
  (cd "$copy" && find . -type f -print0 | sort -z | xargs -0 shasum -a 256)
}

check() {
  RELEASE_VERSION_ROOT="$copy" bash "$root/scripts/release-version-check.sh" "$@"
}

fail() {
  printf 'stamp-version-test: %s\n' "$1" >&2
  exit 1
}

# The committed tree is at the development version and passes in dev mode.
check >/dev/null || fail 'the committed tree fails release-version-check'
check --expect 0.0.0-dev >/dev/null || fail 'dev tree rejects --expect 0.0.0-dev'
if check --expect 0.3.0-nightly.2 >/dev/null 2>&1; then
  fail 'an unstamped tree passed --expect 0.3.0-nightly.2'
fi

version=0.3.0-nightly.2
before=$(snapshot)
STAMP_ROOT="$copy" OPENAPI_TOOLS="$root/tools/openapi" bash "$root/scripts/stamp-version.sh" "$version" >/dev/null
after=$(snapshot)

check --expect "$version" >/dev/null || fail "stamped tree fails --expect $version"
if check --expect 0.3.0 >/dev/null 2>&1; then
  fail 'stamped nightly tree passed --expect 0.3.0'
fi

changed=$({ diff <(printf '%s\n' "$before") <(printf '%s\n' "$after") || true; } | sed -n 's/^> [0-9a-f]* *//p' | sort)
expected=$(printf '%s\n' \
  ./VERSION \
  ./api/openapi/management-runtime-v1.json \
  ./api/openapi/management-runtime.yaml \
  ./api/openapi/management.yaml \
  ./api/openapi/native-hosted-v1.json \
  ./api/openapi/native-hosted.yaml \
  ./console/package-lock.json \
  ./console/package.json \
  ./tools/openapi/package-lock.json \
  ./tools/openapi/package.json | sort)
[[ "$changed" == "$expected" ]] || fail "stamping changed unexpected files:
$changed"

# Stamping a stable version replaces the nightly one and is accepted.
STAMP_ROOT="$copy" OPENAPI_TOOLS="$root/tools/openapi" bash "$root/scripts/stamp-version.sh" 0.3.0 >/dev/null
check --expect 0.3.0 >/dev/null || fail 'stable stamp fails --expect 0.3.0'

if STAMP_ROOT="$copy" bash "$root/scripts/stamp-version.sh" v0.3.0 >/dev/null 2>&1; then
  fail 'stamp-version accepted a v prefix'
fi

# A binary built with the stamped value reports it.
go build -ldflags "-X github.com/mlops-ca/notarx/internal/buildinfo.injectedVersion=$version" -o "$workdir/notarx" ./cmd/notarx
"$workdir/notarx" version | grep -F "$version" >/dev/null || fail 'the built binary does not report the stamped version'

printf '%s\n' 'stamp-version-test passed.'
