# shellcheck shell=bash
# Sourced by the scripts that fetch pinned third-party tools from GitHub releases.
#
# github_release_download <owner/repo> <tag> <asset> <destination-file>
#
# GitHub rejects some unauthenticated release-asset downloads from shared CI runner
# IPs with a 403, which curl --retry does not retry. With GH_TOKEN or GITHUB_TOKEN set
# and the gh CLI installed (every hosted runner has it) the asset is fetched through
# the authenticated API, with retries. Without a token (local runs) it falls back to a
# plain curl of the public URL. Callers still verify a pinned checksum afterwards.
github_release_download() {
  local repo=$1 tag=$2 asset=$3 destination=$4 attempt dir
  if [[ -n "${GH_TOKEN:-${GITHUB_TOKEN:-}}" ]] && command -v gh > /dev/null 2>&1; then
    dir=$(mktemp -d)
    for attempt in 1 2 3; do
      if GH_TOKEN=${GH_TOKEN:-${GITHUB_TOKEN:-}} gh release download "$tag" --repo "$repo" \
        --pattern "$asset" --dir "$dir" --clobber > /dev/null 2>&1 && [[ -f "$dir/$asset" ]]; then
        mv -f -- "$dir/$asset" "$destination"
        rm -rf "$dir"
        return 0
      fi
      sleep $((attempt * 2))
    done
    rm -rf "$dir"
    printf 'authenticated download of %s %s %s failed\n' "$repo" "$tag" "$asset" >&2
    return 1
  fi
  curl --disable --proto '=https' --tlsv1.2 --fail --silent --show-error --location \
    --retry 3 --connect-timeout 15 --max-time 300 \
    --output "$destination" "https://github.com/$repo/releases/download/$tag/$asset"
}
