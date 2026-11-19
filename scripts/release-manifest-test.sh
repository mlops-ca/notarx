#!/usr/bin/env bash
# Tests scripts/release-manifest.sh and scripts/verify-release-attestations.sh on
# throwaway directories with a stub gh (ADR 0024). Nothing here touches the checkout.
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

sha=0123456789abcdef0123456789abcdef01234567
d1=sha256:$(printf 'a%.0s' $(seq 64))
d2=sha256:$(printf 'b%.0s' $(seq 64))
d3=sha256:$(printf 'c%.0s' $(seq 64))
two='[{"image":"notarx","file":"Dockerfile"},{"image":"notarx-console","file":"Dockerfile.console"}]'
three='[{"image":"notarx","file":"Dockerfile"},{"image":"notarx-console","file":"Dockerfile.console"},{"image":"notarx-scanner","file":"Dockerfile.scanner"}]'

new_assets() {
  assets=$(mktemp -d "$workdir/assets.XXXXXX")
  printf 'one' >"$assets/notarx_1.0.0_linux_amd64.tar.gz"
  printf 'two' >"$assets/notarxctl_1.0.0_linux_amd64.tar.gz"
}

manifest() {
  bash "$root/scripts/release-manifest.sh" --version 0.3.0-nightly.2 --core 0.3.0 --sha "$sha" \
    --built-at 2026-11-18T03:00:00Z --assets "$assets" "$@"
}

# --- release-manifest.sh ----------------------------------------------------

new_assets
expect_ok 'manifest with two images' manifest --images-json "$two" --image "notarx=$d1" --image "notarx-console=$d2"
cases=$((cases + 1))
jq -e --arg d1 "$d1" --arg d2 "$d2" --arg sha "$sha" '
  .version == "0.3.0-nightly.2" and .core == "0.3.0" and .sha == $sha and .built_at == "2026-11-18T03:00:00Z"
  and .images == {"notarx": $d1, "notarx-console": $d2}
  and (.assets | map(.name)) == ["notarx_1.0.0_linux_amd64.tar.gz", "notarxctl_1.0.0_linux_amd64.tar.gz"]
  and (has("verified_nightly") | not)' "$assets/release-manifest.json" >/dev/null || fail 'manifest content is wrong'
cases=$((cases + 1))
want=$(printf 'one' | shasum -a 256 | cut -d' ' -f1)
jq -e --arg h "$want" '.assets[0].sha256 == $h' "$assets/release-manifest.json" >/dev/null || fail 'asset checksum is not the sha256 of the archive'
cases=$((cases + 1))
(cd "$assets" && shasum -a 256 -c SHA256SUMS >/dev/null) || fail 'SHA256SUMS does not verify'

new_assets
expect_ok 'manifest with the scanner image of ADR 0030' manifest --images-json "$three" \
  --image "notarx=$d1" --image "notarx-console=$d2" --image "notarx-scanner=$d3"
cases=$((cases + 1))
jq -e '.images | keys == ["notarx", "notarx-console", "notarx-scanner"]' "$assets/release-manifest.json" >/dev/null || fail 'the scanner image is not recorded'

new_assets
expect_fail 'missing image digest' 'differ from the published list' manifest --images-json "$three" --image "notarx=$d1" --image "notarx-console=$d2"
expect_fail 'unlisted image' 'differ from the published list' manifest --images-json "$two" --image "notarx=$d1" --image "notarx-console=$d2" --image "extra=$d3"
expect_fail 'duplicate image' 'recorded twice' manifest --images-json "$two" --image "notarx=$d1" --image "notarx=$d1"
expect_fail 'bad digest' 'no usable index digest' manifest --images-json "$two" --image "notarx=sha256:abc" --image "notarx-console=$d2"
expect_fail 'version outside its core' 'does not belong to core' bash "$root/scripts/release-manifest.sh" --version 0.4.0-nightly.1 --core 0.3.0 \
  --sha "$sha" --built-at 2026-11-18T03:00:00Z --assets "$assets" --images-json "$two" --image "notarx=$d1" --image "notarx-console=$d2"
expect_fail 'short sha' 'full commit SHA' bash "$root/scripts/release-manifest.sh" --version 0.3.0 --core 0.3.0 \
  --sha abc --built-at 2026-11-18T03:00:00Z --assets "$assets" --images-json "$two" --image "notarx=$d1" --image "notarx-console=$d2"
empty=$(mktemp -d "$workdir/empty.XXXXXX")
expect_fail 'no archives' 'no archive found' bash "$root/scripts/release-manifest.sh" --version 0.3.0 --core 0.3.0 \
  --sha "$sha" --built-at 2026-11-18T03:00:00Z --assets "$empty" --images-json "$two" --image "notarx=$d1" --image "notarx-console=$d2"

new_assets
expect_ok 'stable manifest records the verified nightly' bash "$root/scripts/release-manifest.sh" --version 0.3.0 --core 0.3.0 \
  --sha "$sha" --built-at 2026-11-18T03:00:00Z --assets "$assets" --images-json "$two" --image "notarx=$d1" --image "notarx-console=$d2" \
  --verified-nightly v0.3.0-nightly.2
cases=$((cases + 1))
jq -e '.verified_nightly == "v0.3.0-nightly.2"' "$assets/release-manifest.json" >/dev/null || fail 'verified_nightly is missing'

# --- verify-release-attestations.sh -----------------------------------------

# A stub gh that accepts only the repo-qualified workflow identity of the image
# workflow for oci:// subjects and of release.yml for files, like the real one.
stub=$workdir/gh
cat >"$stub" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_LOG"
[[ "$1 $2" == 'attestation verify' ]] || exit 9
subject=$3
shift 3
repo= signer=
while [[ $# -gt 0 ]]; do
  case $1 in
    --repo) repo=$2 ;;
    --signer-workflow) signer=$2 ;;
    --signer-repo) exit 8 ;;
  esac
  shift 2
done
[[ "$repo" == mlops-ca/notarx ]] || exit 1
case "$subject" in
  oci://*) [[ "$signer" == mlops-ca/notarx/.github/workflows/image-build.yml ]] ;;
  *) [[ "$signer" == mlops-ca/notarx/.github/workflows/release.yml ]] ;;
esac
STUB
chmod +x "$stub"
export STUB_LOG=$workdir/gh.log
verify() {
  GH_CLI=$stub bash "$root/scripts/verify-release-attestations.sh" --repo mlops-ca/notarx --manifest "$assets/release-manifest.json" --assets "$assets" --attempts 1 --delay 0 "$@"
}

new_assets
manifest --images-json "$three" --image "notarx=$d1" --image "notarx-console=$d2" --image "notarx-scanner=$d3" >/dev/null
: >"$STUB_LOG"
expect_ok 'verify accepts the repo-qualified identities' verify
cases=$((cases + 1))
test "$(grep -c 'oci://ghcr.io/mlops-ca/' "$STUB_LOG")" = 3 || fail 'not every image of the manifest was verified'
cases=$((cases + 1))
test "$(grep -c -v 'oci://' "$STUB_LOG")" = 2 || fail 'not every archive of the manifest was verified'
cases=$((cases + 1))
! grep -q -e '--signer-repo' "$STUB_LOG" || fail 'verification must not use --signer-repo alone'
cases=$((cases + 1))
! grep -q -e '--source-ref' "$STUB_LOG" || fail '--source-ref must only be passed when a tag is pinned'

: >"$STUB_LOG"
expect_ok 'verify pins a tag when asked' verify --source-ref refs/tags/v0.3.0-nightly.2
cases=$((cases + 1))
grep -q -e '--source-ref refs/tags/v0.3.0-nightly.2' "$STUB_LOG" || fail '--source-ref was not passed'
expect_fail 'bare-path workflow is refused up front' 'not paths' verify --images-workflow .github/workflows/image-build.yml
expect_fail 'attestation from another workflow is rejected' 'does not verify' verify --images-workflow other.yml
expect_fail 'another repository is rejected' 'does not verify' bash -c "GH_CLI=$stub bash $root/scripts/verify-release-attestations.sh --repo mlops-ca/other --manifest $assets/release-manifest.json --assets $assets --attempts 1 --delay 0"
# A fresh attestation that is not visible yet is retried: the first call of each subject fails.
flaky=$workdir/gh-flaky
cat >"$flaky" <<'FLAKY'
#!/usr/bin/env bash
seen=$STUB_SEEN/$(printf '%s' "$3" | shasum | cut -d' ' -f1)
if [[ ! -e "$seen" ]]; then : >"$seen"; exit 1; fi
exit 0
FLAKY
chmod +x "$flaky"
mkdir "$workdir/seen"
cases=$((cases + 1))
STUB_SEEN=$workdir/seen GH_CLI=$flaky bash "$root/scripts/verify-release-attestations.sh" --repo mlops-ca/notarx --manifest "$assets/release-manifest.json" \
  --assets "$assets" --attempts 2 --delay 0 >/dev/null 2>&1 || fail 'a transient miss was not retried'
rm -rf "$workdir/seen"
mkdir "$workdir/seen"
cases=$((cases + 1))
if STUB_SEEN=$workdir/seen GH_CLI=$flaky bash "$root/scripts/verify-release-attestations.sh" --repo mlops-ca/notarx --manifest "$assets/release-manifest.json" \
  --assets "$assets" --attempts 1 --delay 0 >/dev/null 2>&1; then fail 'a miss with a single attempt passed'; fi
expect_fail 'bad attempts value' 'attempts must be' verify --attempts 0
rm "$assets/notarxctl_1.0.0_linux_amd64.tar.gz"
expect_fail 'a missing archive fails' 'is missing' verify

if [[ "$failures" -ne 0 ]]; then
  printf '%d of %d release-manifest checks failed\n' "$failures" "$cases" >&2
  exit 1
fi
printf 'release-manifest-test passed (%d checks).\n' "$cases"
