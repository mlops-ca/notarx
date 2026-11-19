#!/usr/bin/env bash
# Writes SHA256SUMS and release-manifest.json for a release (ADR 0024).
#
#   scripts/release-manifest.sh --version X.Y.Z-nightly.N --core X.Y.Z --sha <sha> \
#     --built-at <RFC 3339 UTC> --assets <dir> \
#     --images-json '[{"image":"notarx",...},...]' --image notarx=sha256:<hex> ... \
#     [--verified-nightly vX.Y.Z-nightly.N]
#
# <dir> holds the archives; SHA256SUMS is rewritten over them. The manifest is
# {version, core, sha, built_at, images: {name: index digest}, assets: [{name,
# sha256}]} (plus verified_nightly for a stable release) and lands in <dir> as
# release-manifest.json. --images-json is the list of published images the
# caller built (the single list that image-build.yml consumes); the script fails
# unless every one of them has exactly one --image digest, so an image that was
# built but forgotten (or the other way round) never ships unrecorded.
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

fail() {
  printf 'release-manifest: %s\n' "$1" >&2
  exit 1
}

version=''
core=''
sha=''
built_at=''
assets=''
images_json=''
verified=''
declare -a image_args=()
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || fail "$1 needs a value"
  case $1 in
    --version) version=$2 ;;
    --core) core=$2 ;;
    --sha) sha=$2 ;;
    --built-at) built_at=$2 ;;
    --assets) assets=$2 ;;
    --images-json) images_json=$2 ;;
    --image) image_args+=("$2") ;;
    --verified-nightly) verified=$2 ;;
    *) fail "unknown argument $1" ;;
  esac
  shift 2
done

[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-nightly\.[0-9]+)?$ ]] || fail "version must be X.Y.Z or X.Y.Z-nightly.N, got '$version'"
[[ "$core" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "core must be X.Y.Z, got '$core'"
[[ "$version" == "$core" || "$version" == "$core-nightly."* ]] || fail "version $version does not belong to core $core"
[[ "$sha" =~ ^[0-9a-f]{40}$ ]] || fail "sha must be a full commit SHA, got '$sha'"
[[ "$built_at" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || fail "built-at must be RFC 3339 UTC, got '$built_at'"
[[ -d "$assets" ]] || fail "assets directory '$assets' does not exist"
[[ -n "$images_json" ]] || fail '--images-json is required'
[[ -z "$verified" || "$verified" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-nightly\.[0-9]+$ ]] || fail "verified-nightly must be a nightly tag, got '$verified'"

want=$(jq -r '[.[].image] | sort | join(" ")' <<<"$images_json") || fail '--images-json is not valid JSON'
[[ -n "$want" ]] || fail '--images-json lists no image'

images='{}'
for pair in ${image_args[@]+"${image_args[@]}"}; do
  name=${pair%%=*}
  digest=${pair#*=}
  [[ "$pair" == *=* && -n "$name" ]] || fail "--image expects name=digest, got '$pair'"
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "image $name has no usable index digest: '$digest'"
  jq -e --arg n "$name" 'has($n) | not' <<<"$images" >/dev/null || fail "image $name is recorded twice"
  images=$(jq -c --arg n "$name" --arg d "$digest" '. + {($n): $d}' <<<"$images")
done
got=$(jq -r 'keys | join(" ")' <<<"$images")
[[ "$got" == "$want" ]] || fail "recorded images [$got] differ from the published list [$want]"

"$script_dir/write-release-checksums.sh" "$assets"
[[ -s "$assets/SHA256SUMS" ]] || fail "no archive found in $assets"

# One {name, sha256} entry per line of SHA256SUMS ("<hex>  <name>").
asset_list=$(awk 'NF == 2 { printf "%s\t%s\n", $2, $1 }' "$assets/SHA256SUMS" |
  jq -R -s -c 'split("\n") | map(select(length > 0) | split("\t") | {name: .[0], sha256: .[1]})')
jq -e 'length > 0 and all(.sha256 | test("^[0-9a-f]{64}$"))' <<<"$asset_list" >/dev/null || fail 'SHA256SUMS holds an unusable line'

jq -n -S \
  --arg version "$version" --arg core "$core" --arg sha "$sha" --arg built_at "$built_at" \
  --arg verified "$verified" --argjson images "$images" --argjson assets "$asset_list" \
  '{version: $version, core: $core, sha: $sha, built_at: $built_at, images: $images, assets: $assets}
   + (if $verified == "" then {} else {verified_nightly: $verified} end)' > "$assets/release-manifest.json"
