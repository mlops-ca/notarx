#!/usr/bin/env bash
# Native-build guard (ADR 0016): notarx performs no cross-compilation and no
# emulation anywhere in build, test or release. This check fails when the tree
# reintroduces either. See docs/native-builds.md.
#
# Usage: native-build-check.sh [--root DIR] [--allowlist FILE]
#
# Environment: NATIVE_BUILD_CHECK_TODAY=YYYY-MM-DD injects the date for the
# macos-15-intel calendar guard (tests); it defaults to today in UTC.
#
# Rules (each failure line starts with the rule name):
#   platform-pin   --platform= on a FROM line of a Dockerfile*
#   target-var     TARGETARCH, TARGETOS or BUILDPLATFORM in a Dockerfile*
#   goos-goarch    GOOS= / GOARCH= assignment in Dockerfile*, Makefile, scripts/,
#                  .github/workflows/ (reading them with go env is allowed)
#   qemu           docker/setup-qemu-action, qemu-user-static, tonistiigi/binfmt
#   macos-intel    the macos-15-intel runner label on or after 2027-09-01
#   self-hosted    self-hosted in any runs-on value
#   multi-platform a platforms: key listing more than one platform
#   cgo-cross      CGO_ENABLED=1 together with a CC= cross compiler
#   ubuntu-latest  the floating ubuntu-latest runner label (pinned labels only)
#
# The allowlist (scripts/native-build-allowlist.txt) lists path prefixes that are
# skipped: only this script, its self-test and the negative fixtures.
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$script_dir/.." && pwd)
allowlist=
allowlist_set=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --root) root=$(cd "${2:?--root needs a directory}" && pwd); shift 2 ;;
    --allowlist) allowlist=${2:?--allowlist needs a file}; allowlist_set=1; shift 2 ;;
    -h|--help) sed -n '2,24p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) printf 'Unknown argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done
if [[ "$allowlist_set" -eq 0 ]]; then
  allowlist="$script_dir/native-build-allowlist.txt"
fi

cd "$root"
today=${NATIVE_BUILD_CHECK_TODAY:-$(date -u +%F)}
if [[ ! "$today" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
  printf 'NATIVE_BUILD_CHECK_TODAY must be YYYY-MM-DD: %s\n' "$today" >&2
  exit 2
fi

allowed=()
if [[ -n "$allowlist" && -f "$allowlist" ]]; then
  while IFS= read -r entry; do
    entry=${entry%%#*}
    entry=$(printf '%s' "$entry" | tr -d '[:space:]')
    if [[ -n "$entry" ]]; then allowed+=("$entry"); fi
  done < "$allowlist"
fi

is_allowed() {
  local path=${1#./} entry
  for entry in "${allowed[@]+"${allowed[@]}"}"; do
    if [[ "$path" == "$entry" || "$entry" == */ && "$path" == "$entry"* ]]; then return 0; fi
  done
  return 1
}

# Collect scanned files into one list per scope.
dockerfiles=()
buildfiles=()
workflows=()
while IFS= read -r path; do
  is_allowed "$path" && continue
  case "$(basename "$path")" in
    Dockerfile*) dockerfiles+=("$path"); buildfiles+=("$path") ;;
  esac
done < <(find . \( -name node_modules -o -name .git -o -name target \) -prune -o -type f -name 'Dockerfile*' -print | LC_ALL=C sort)
if [[ -f Makefile ]] && ! is_allowed Makefile; then buildfiles+=(Makefile); fi
while IFS= read -r path; do
  is_allowed "$path" || buildfiles+=("$path")
done < <(if [[ -d scripts ]]; then find scripts -type f -not -path '*/node_modules/*' -print | LC_ALL=C sort; fi)
while IFS= read -r path; do
  if ! is_allowed "$path"; then workflows+=("$path"); buildfiles+=("$path"); fi
done < <(if [[ -d .github/workflows ]]; then find .github/workflows -type f \( -name '*.yml' -o -name '*.yaml' \) -print | LC_ALL=C sort; fi)

failures=0
fail() {
  printf '%s: %s: %s\n' "$1" "$2" "$3" >&2
  failures=$((failures + 1))
}

# scan RULE REGEX FILE... : report every matching line (extended regex).
scan() {
  local rule=$1 regex=$2 file line
  shift 2
  for file in "$@"; do
    while IFS= read -r line; do
      [[ -n "$line" ]] && fail "$rule" "$file" "$line"
    done < <(grep -nEI -- "$regex" "$file" 2>/dev/null || true)
  done
}

if [[ ${#dockerfiles[@]} -gt 0 ]]; then
  scan platform-pin '^[[:space:]]*FROM[[:space:]].*--platform=' "${dockerfiles[@]}"
  scan target-var '\b(TARGETARCH|TARGETOS|BUILDPLATFORM)\b' "${dockerfiles[@]}"
fi
if [[ ${#buildfiles[@]} -gt 0 ]]; then
  scan goos-goarch '(^|[^A-Za-z0-9_])(GOARCH|GOOS)=' "${buildfiles[@]}"
  scan qemu 'docker/setup-qemu-action|qemu-user-static|tonistiigi/binfmt' "${buildfiles[@]}"
  for file in "${buildfiles[@]}"; do
    if grep -qE 'CGO_ENABLED=1' "$file" && grep -qE '(^|[^A-Za-z0-9_])CC=' "$file"; then
      fail cgo-cross "$file" 'CGO_ENABLED=1 combined with CC= (a cross compiler)'
    fi
  done
fi
if [[ ${#workflows[@]} -gt 0 ]]; then
  scan ubuntu-latest '(^|[^A-Za-z0-9_-])ubuntu-latest([^A-Za-z0-9_-]|$)' "${workflows[@]}"
  if [[ ! "$today" < 2027-09-01 ]]; then
    scan macos-intel "macos-15-intel" "${workflows[@]}"
  fi
  for file in "${workflows[@]}"; do
    # runs-on may be a scalar, a flow list or a block list; inspect the whole value.
    while IFS= read -r hit; do
      fail self-hosted "$file" "$hit"
    done < <(awk '
      function indent(s) { match(s, /^ */); return RLENGTH }
      /^[[:space:]]*runs-on:/ { block = 1; base = indent($0); n = NR; value = $0
        if (value ~ /self-hosted/) print n ": " value; next }
      block { if ($0 ~ /^[[:space:]]*$/) next
              if (indent($0) <= base) { block = 0 }
              else if ($0 ~ /self-hosted/) print NR ": " $0 }' "$file")
    # platforms: with a comma or a block of several platforms.
    while IFS= read -r hit; do
      fail multi-platform "$file" "$hit"
    done < <(awk '
      function indent(s) { match(s, /^ */); return RLENGTH }
      /^[[:space:]]*platforms:/ {
        value = $0; sub(/^[[:space:]]*platforms:[[:space:]]*/, "", value)
        sub(/[[:space:]]+#.*$/, "", value)
        if (value ~ /^[|>]/) { block = 1; base = indent($0); count = 0; start = NR; text = $0; next }
        block = 0
        gsub(/[$][{][{][^}]*[}][}]/, "X", value); gsub(/[[:space:]]+$/, "", value)
        if (value ~ /[, ]/) print NR ": " $0
        next }
      block { if ($0 ~ /^[[:space:]]*$/) next
              if (indent($0) <= base) { if (count > 1) print start ": " text; block = 0 }
              else { count += gsub(/[^,[:space:]]+/, "&") } }
      END { if (block && count > 1) print start ": " text }' "$file")
  done
fi

if [[ "$failures" -gt 0 ]]; then
  printf 'native-build-check: %d violation(s). ADR 0016 forbids cross-compilation and emulation; build and test natively on each runner (docs/native-builds.md).\n' "$failures" >&2
  exit 1
fi
printf 'native-build-check: ok (%d build files scanned).\n' "${#buildfiles[@]}"
