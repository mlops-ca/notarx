#!/usr/bin/env bash
# Stable promotion helpers (ADR 0024 slice 5). The stable release rebuilds the commit of a
# verified nightly; these steps check the nightly's record, publish the stable release and move
# the moving pointers. Every step is idempotent so that "Re-run failed jobs" converges.
#
#   promote-release.sh verify --repo R --nightly-tag T --sha S --dir D [--attempts N] [--delay S]
#       Downloads the nightly's release-manifest.json and assets, requires the manifest to record
#       commit S and version T, and runs scripts/verify-release-attestations.sh over every image
#       digest and archive. Fails without a manifest, with a failing attestation or a wrong commit.
#   promote-release.sh create --repo R --version X.Y.Z --sha S --notes-file F --assets-dir D
#       Creates vX.Y.Z as a draft release with the assets, then publishes it, which creates the tag
#       on S. A leftover draft of a failed run is replaced; a published release on S is kept.
#   promote-release.sh finish --repo R --version X.Y.Z --nightly-tag T
#       Edits the verified nightly's notes to point at the stable release (once).
#   promote-release.sh latest --repo R --version X.Y.Z --registry ghcr.io/owner --images-json J
#       Moves :latest of every image to :X.Y.Z, unless a newer stable release exists.
#
# GH_CLI and DOCKER override the gh and docker binaries (tests).
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
gh_cli=${GH_CLI:-gh}
docker_cli=${DOCKER:-docker}

fail() {
  printf 'promote-release: %s\n' "$1" >&2
  exit 1
}

[[ $# -gt 0 ]] || fail 'usage: promote-release.sh verify|create|finish|latest [options]'
command=$1
shift

repo='' nightly='' sha='' dir='' version='' notes='' registry='' images_json='' attempts=5 delay=15
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || fail "$1 needs a value"
  case $1 in
    --repo) repo=$2 ;;
    --nightly-tag) nightly=$2 ;;
    --sha) sha=$2 ;;
    --dir | --assets-dir) dir=$2 ;;
    --version) version=$2 ;;
    --notes-file) notes=$2 ;;
    --registry) registry=$2 ;;
    --images-json) images_json=$2 ;;
    --attempts) attempts=$2 ;;
    --delay) delay=$2 ;;
    *) fail "unknown argument $1" ;;
  esac
  shift 2
done

[[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail "--repo must be owner/repo, got '$repo'"
require_nightly() { [[ "$nightly" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-nightly\.[0-9]+$ ]] || fail "--nightly-tag must be vX.Y.Z-nightly.N, got '$nightly'"; }
require_version() { [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "--version must be X.Y.Z, got '$version'"; }
require_sha() { [[ "$sha" =~ ^[0-9a-f]{40}$ ]] || fail "--sha must be a full commit SHA, got '$sha'"; }

verify() {
  require_nightly
  require_sha
  [[ -n "$dir" ]] || fail '--dir is required'
  mkdir -p "$dir"
  local state
  state=$("$gh_cli" release view "$nightly" --repo "$repo" --json isDraft,isPrerelease) || fail "nightly release $nightly does not exist"
  jq -e '.isDraft == false and .isPrerelease == true' <<<"$state" >/dev/null || fail "$nightly is not a published pre-release"

  rm -f "$dir/release-manifest.json"
  "$gh_cli" release download "$nightly" --repo "$repo" --dir "$dir" --pattern release-manifest.json --clobber || true
  [[ -s "$dir/release-manifest.json" ]] || fail "nightly $nightly has no release-manifest.json"
  jq -e --arg sha "$sha" --arg version "${nightly#v}" '.sha == $sha and .version == $version' "$dir/release-manifest.json" >/dev/null ||
    fail "the manifest of $nightly does not record commit $sha and version ${nightly#v}"

  local name
  while IFS= read -r name; do
    "$gh_cli" release download "$nightly" --repo "$repo" --dir "$dir" --pattern "$name" --clobber || fail "cannot download $name of $nightly"
  done < <(jq -r '.assets[].name' "$dir/release-manifest.json")

  "$script_dir/verify-release-attestations.sh" --repo "$repo" --manifest "$dir/release-manifest.json" --assets "$dir" \
    --registry "ghcr.io/${repo%%/*}" --attempts "$attempts" --delay "$delay"
  printf 'promote-release: the record and the attestations of %s verify for %s\n' "$nightly" "$sha"
}

tag_commit() {
  "$gh_cli" api "repos/$repo/git/ref/tags/v$version" --jq .object.sha
}

create() {
  require_version
  require_sha
  [[ -f "$notes" ]] || fail "--notes-file '$notes' not found"
  [[ -d "$dir" ]] || fail "--assets-dir '$dir' is not a directory"
  local tag=v$version state draft
  if state=$("$gh_cli" release view "$tag" --repo "$repo" --json isDraft 2>/dev/null); then
    draft=$(jq -r .isDraft <<<"$state")
    if [[ "$draft" == false ]]; then
      [[ "$(tag_commit)" == "$sha" ]] || fail "$tag already exists on another commit"
      printf 'promote-release: %s is already published on %s\n' "$tag" "$sha"
      return 0
    fi
    # A draft has no tag yet: it is the leftover of a run that failed before publishing.
    "$gh_cli" release delete "$tag" --repo "$repo" --yes
  fi
  local -a assets=()
  local file
  while IFS= read -r file; do assets+=("$file"); done < <(find "$dir" -maxdepth 1 -type f \( -name '*.tar.gz' -o -name '*.deb' -o -name '*.rpm' -o -name SHA256SUMS -o -name release-manifest.json \) | sort)
  [[ "${#assets[@]}" -gt 0 ]] || fail "no release asset in $dir"
  "$gh_cli" release create "$tag" --repo "$repo" --draft --target "$sha" --title "$tag" --notes-file "$notes" "${assets[@]}"
  "$gh_cli" release edit "$tag" --repo "$repo" --draft=false
  [[ "$(tag_commit)" == "$sha" ]] || fail "$tag does not point at $sha after publishing"
  printf 'promote-release: published %s on %s\n' "$tag" "$sha"
}

finish() {
  require_version
  require_nightly
  local body marker
  marker="Promoted to stable v$version"
  body=$("$gh_cli" release view "$nightly" --repo "$repo" --json body --jq .body)
  if grep -q -F "$marker" <<<"$body"; then
    printf 'promote-release: the notes of %s already point at v%s\n' "$nightly" "$version"
    return 0
  fi
  local file
  file=$(mktemp)
  # The release of a nightly was written by the pipeline; only a note is appended.
  {
    printf '%s\n\n' "$body"
    printf '> %s: the commit of this nightly was rebuilt as [v%s](%s/%s/releases/tag/v%s).\n' "$marker" "$version" "${GITHUB_SERVER_URL:-https://github.com}" "$repo" "$version"
  } >"$file"
  "$gh_cli" release edit "$nightly" --repo "$repo" --notes-file "$file"
  rm -f "$file"
}

latest() {
  require_version
  [[ -n "$registry" && -n "$images_json" ]] || fail '--registry and --images-json are required'
  local stable newest
  stable=$("$gh_cli" release list --repo "$repo" --exclude-drafts --exclude-pre-releases --limit 200 --json tagName --jq '.[].tagName') || fail 'cannot list the stable releases'
  grep -q -x "v$version" <<<"$stable" || fail "release v$version is not published yet; :latest moves after the Git tag"
  newest=$(grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' <<<"$stable" | sort -V | tail -n 1)
  if [[ "$newest" != "v$version" ]]; then
    printf 'promote-release: notice: %s is newer than v%s; :latest stays\n' "$newest" "$version"
    return 0
  fi
  local image
  while IFS= read -r image; do
    "$docker_cli" buildx imagetools create --tag "$registry/$image:latest" "$registry/$image:$version"
  done < <(jq -r '.[].image' <<<"$images_json")
}

case $command in
  verify) verify ;;
  create) create ;;
  finish) finish ;;
  latest) latest ;;
  *) fail "unknown command $command" ;;
esac
