#!/usr/bin/env bash
# Select the Console Playwright specs a change affects.
#
# Usage: ci-e2e-select.sh [<base-sha> <head-sha>]   (git diff base...head)
#        ci-e2e-select.sh --files < list-of-paths
#
# Prints specs=<space separated spec paths relative to console/>. An empty
# value means "run the full suite". A subset is chosen only when every changed
# path is an existing top-level console/e2e/*.spec.ts file; any other path, a
# deleted spec, or an unresolvable range is uncertain and falls back to the
# full suite. See docs/ci.md.
set -euo pipefail

emit() { printf 'specs=%s\n' "$1"; }

if [[ "${1:-}" == "--files" ]]; then
  files=$(cat)
elif [[ $# -ge 2 && -n "$1" && -n "$2" && "$1" != 0000000000000000000000000000000000000000 ]]; then
  if ! files=$(git diff --name-only --no-renames "$1...$2" 2>/dev/null) &&
     ! files=$(git diff --name-only --no-renames "$1" "$2" 2>/dev/null); then
    emit ""
    exit 0
  fi
else
  emit ""
  exit 0
fi

specs=()
while IFS= read -r path; do
  [[ -z "$path" ]] && continue
  if [[ "$path" =~ ^console/e2e/[^/]+\.spec\.ts$ && ( -f "$path" || "${CI_E2E_SELECT_ASSUME_EXISTS:-}" == 1 ) ]]; then
    specs+=("${path#console/}")
  else
    emit ""
    exit 0
  fi
done <<<"$files"

if [[ ${#specs[@]} -eq 0 ]]; then
  emit ""
else
  emit "${specs[*]}"
fi
