#!/usr/bin/env bash
# SAML dependency floor (ADR 0018 section 3). Fails when go.mod resolves
# github.com/russellhaering/goxmldsig below v1.6.1 or github.com/russellhaering/gosaml2
# below v0.12.0 (CVE-2026-33487 and the August 2026 advisories), and when either module
# is missing once internal/identity/saml imports it.
#
# Usage: scripts/saml-dependency-floor-check.sh [go.mod path]
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
gomod=${1:-$root/go.mod}
[[ -f "$gomod" ]] || { printf 'saml-dependency-floor-check: %s not found\n' "$gomod" >&2; exit 2; }

floors=(
  'github.com/russellhaering/goxmldsig v1.6.1'
  'github.com/russellhaering/gosaml2 v0.12.0'
)

# version_ge A B: true when semantic version A >= B (both vMAJOR.MINOR.PATCH, optional suffix).
version_ge() {
  local a=${1#v} b=${2#v}
  a=${a%%[-+]*}; b=${b%%[-+]*}
  [[ "$(printf '%s\n%s\n' "$a" "$b" | sort -V | head -n1)" == "$b" ]]
}

status=0
for floor in "${floors[@]}"; do
  module=${floor% *}
  minimum=${floor#* }
  # Both the single-line form (require x v) and the block form list the resolved version.
  resolved=$(awk -v m="$module" '
    $1 == "require" && $2 == m { print $3; exit }
    $1 == m && $2 ~ /^v[0-9]/ { print $2; exit }
  ' "$gomod")
  if [[ -z "$resolved" ]]; then
    printf 'saml-dependency-floor-check: %s is not required by %s (floor %s)\n' "$module" "$gomod" "$minimum" >&2
    status=1
  elif ! version_ge "$resolved" "$minimum"; then
    printf 'saml-dependency-floor-check: %s resolves to %s, below the floor %s\n' "$module" "$resolved" "$minimum" >&2
    status=1
  fi
done
if ((status != 0)); then
  exit "$status"
fi
printf 'saml-dependency-floor-check: ok\n'
