#!/usr/bin/env bash
# Lists the milestones that were completed since the previous stable release, as a release-notes
# section (ADR 0024: "the stable release notes list the milestones whose last issue closed since
# the previous stable"). A milestone counts when it is closed, has no open issue, has at least one
# closed issue (an empty milestone completed no work) and its closing
# time is after --since (an RFC 3339 UTC time; empty lists every closed milestone). Prints nothing
# when there is none.
#
#   scripts/release-milestones.sh --repo owner/repo [--since 2026-11-19T10:00:00Z]
#
# GH_CLI overrides the gh binary (tests).
set -euo pipefail

fail() {
  printf 'release-milestones: %s\n' "$1" >&2
  exit 1
}

repo='' since=''
while [[ $# -gt 0 ]]; do
  [[ $# -ge 2 ]] || fail "$1 needs a value"
  case $1 in
    --repo) repo=$2 ;;
    --since) since=$2 ;;
    *) fail "unknown argument $1" ;;
  esac
  shift 2
done
[[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail "--repo must be owner/repo, got '$repo'"
[[ -z "$since" || "$since" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]+Z$ ]] || fail "--since must be RFC 3339 UTC, got '$since'"

gh_cli=${GH_CLI:-gh}
raw=$("$gh_cli" api --paginate "repos/$repo/milestones?state=closed&per_page=100") || fail 'cannot list the milestones'
# --paginate prints one JSON array per page.
listed=$(jq -s -r --arg since "$since" '
  [.[] | if type == "array" then .[] else . end
   | select(.state == "closed" and .open_issues == 0 and .closed_issues > 0 and .closed_at != null and ($since == "" or .closed_at > $since))]
  | sort_by(.closed_at)
  | map("- **" + .title + "**" + (if (.description // "") != "" then ": " + (.description | gsub("\\s+"; " ")) else "" end) + " ([milestone](" + .html_url + "))")
  | .[]' <<<"$raw") || fail 'unreadable milestone list'
if [[ -n "$listed" ]]; then
  printf '### Milestones completed\n\n%s\n' "$listed"
fi
