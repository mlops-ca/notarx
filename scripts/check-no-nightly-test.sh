#!/usr/bin/env bash
# Tests scripts/check-no-nightly.sh on throwaway release directories with a stub docker (ADR 0024).
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

failures=0
cases=0
fail() {
  failures=$((failures + 1))
  printf 'FAIL: %s\n' "$1" >&2
}
expect_ok() {
  local name=$1
  shift
  cases=$((cases + 1))
  "$@" >"$workdir/out" 2>&1 || fail "$name: expected success, got: $(cat "$workdir/out")"
}
expect_fail() {
  local name=$1 pattern=$2
  shift 2
  cases=$((cases + 1))
  if "$@" >"$workdir/out" 2>&1; then
    fail "$name: expected failure"
  elif ! grep -q -E "$pattern" "$workdir/out"; then
    fail "$name: failure message does not match [$pattern]: $(cat "$workdir/out")"
  fi
}

# A stub docker whose label payload is read from $DOCKER_LABELS_FILE.
cat >"$workdir/docker" <<'STUB'
#!/usr/bin/env bash
cat "$DOCKER_LABELS_FILE"
STUB
chmod +x "$workdir/docker"
export DOCKER="$workdir/docker"

# new_release <version> <version text of VERSION.txt> <binary text>
dir=
new_release() {
  local version=$1 versiontxt=$2 binary=$3 stage
  dir=$(mktemp -d "$workdir/release.XXXXXX")
  stage=$(mktemp -d "$workdir/stage.XXXXXX")
  mkdir "$stage/notarx_${version}_linux_amd64"
  printf '%s\n' "$versiontxt" >"$stage/notarx_${version}_linux_amd64/VERSION.txt"
  printf '\000\001notarx %s\000' "$binary" >"$stage/notarx_${version}_linux_amd64/notarx"
  tar -czf "$dir/notarx_${version}_linux_amd64.tar.gz" -C "$stage" "notarx_${version}_linux_amd64"
  printf 'deb' >"$dir/notarx_${version}_amd64.deb"
  bash "$root/scripts/write-release-checksums.sh" "$dir"
  printf '{"version":"%s","verified_nightly":"v0.1.0-nightly.1","images":{"notarx":"sha256:%064d"}}\n' "$version" 1 >"$dir/release-manifest.json"
  rm -rf "$stage"
}
check() { bash "$root/scripts/check-no-nightly.sh" --dir "$dir" "$@"; }

new_release 0.1.0 0.1.0 0.1.0
expect_ok 'a clean stable release passes (the manifest may name the verified nightly)' check

new_release 0.1.0 0.1.0-nightly.3 0.1.0
expect_fail 'VERSION.txt reporting a nightly fails' 'VERSION\.txt reports a nightly' check

new_release 0.1.0 0.1.0 0.1.0-nightly.3
expect_fail 'a binary embedding the stamped nightly version fails' 'embeds a stamped nightly' check

new_release 0.1.0 0.1.0 'a dependency says nightly once'
expect_ok 'the bare word inside a binary is not a stamped version' check

new_release 0.1.0-nightly.3 0.1.0 0.1.0
expect_fail 'a nightly in an asset name fails' "asset name.*nightly" check

new_release 0.1.0 0.1.0 0.1.0
printf 'x\n' >"$dir/SHA256SUMS-extra"
mv "$dir/notarx_0.1.0_amd64.deb" "$dir/notarx_0.1.0-nightly.1_amd64.deb"
expect_fail 'a package named for a nightly fails' "asset name.*nightly" check

new_release 0.1.0 0.1.0 0.1.0
printf 'deadbeef  notarx_0.1.0-nightly.1.tar.gz\n' >>"$dir/SHA256SUMS"
expect_fail 'SHA256SUMS naming a nightly fails' 'SHA256SUMS contains nightly' check

mkdir "$workdir/empty"
expect_fail 'an empty directory fails' 'no asset' bash "$root/scripts/check-no-nightly.sh" --dir "$workdir/empty"
expect_fail 'a missing directory fails' 'not a directory' bash "$root/scripts/check-no-nightly.sh" --dir "$workdir/none"
expect_fail 'a manifest without a registry fails' 'needs --registry' bash "$root/scripts/check-no-nightly.sh" --dir "$workdir/empty" --manifest x

new_release 0.1.0 0.1.0 0.1.0
export DOCKER_LABELS_FILE=$workdir/labels
printf '{"linux/amd64":{"config":{"Labels":{"org.opencontainers.image.version":"0.1.0"}}},"linux/arm64":{"config":{"Labels":{"org.opencontainers.image.version":"0.1.0"}}}}\n' >"$DOCKER_LABELS_FILE"
expect_ok 'clean image labels pass' check --manifest "$dir/release-manifest.json" --registry ghcr.io/o
printf '{"linux/amd64":{"config":{"Labels":{"org.opencontainers.image.version":"0.1.0-nightly.2"}}}}\n' >"$DOCKER_LABELS_FILE"
expect_fail 'an image label naming a nightly fails' 'label of image notarx contains nightly' check --manifest "$dir/release-manifest.json" --registry ghcr.io/o

if [[ "$failures" -ne 0 ]]; then
  printf '%d of %d check-no-nightly case(s) failed\n' "$failures" "$cases" >&2
  exit 1
fi
printf 'check-no-nightly-test passed (%d cases).\n' "$cases"
