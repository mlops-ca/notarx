#!/usr/bin/env bash
# Classify the files a change touches so CI only runs what the change needs.
#
# Usage: ci-changes.sh [<base-sha> <head-sha>]     (git diff base...head)
#        ci-changes.sh --files < list-of-paths      (classify a path list)
#
# Prints go=, console=, docs=, ci=, images=, arm64= as true/false lines (suitable
# for $GITHUB_OUTPUT). arm64 marks an architecture-sensitive change: it turns on the
# arm64 contract lanes and arm64 lint of pull request CI (ADR 0016, amendment of
# 2026-11-19). With no range, or when the range cannot be resolved, every
# class is true (tags, manual runs). Any path that matches no
# known class also turns everything on, so a new top-level directory can never
# silently skip a job. See docs/ci.md.
set -euo pipefail

emit() { printf '%s=%s\n' "$1" "$2"; }

all_true() {
  for k in go console docs ci images arm64; do emit "$k" true; done
}

if [[ "${1:-}" == "--files" ]]; then
  files=$(cat)
elif [[ $# -ge 2 && -n "$1" && -n "$2" && "$1" != 0000000000000000000000000000000000000000 ]]; then
  if ! files=$(git diff --name-only --no-renames "$1...$2" 2>/dev/null) &&
     ! files=$(git diff --name-only --no-renames "$1" "$2" 2>/dev/null); then
    all_true
    exit 0
  fi
else
  all_true
  exit 0
fi

go=false console=false docs=false ci=false images=false arm64=false
while IFS= read -r path; do
  [[ -z "$path" ]] && continue
  case "$path" in
    .github/*|Makefile|scripts/*|packaging/*)
      ci=true ;;
    # Architecture-specific Go sources and assembly, and the dependency set (a module
    # can carry its own per-architecture code): Go, and arch-sensitive.
    *_amd64.go|*_arm64.go|*_amd64_test.go|*_arm64_test.go|*_amd64.s|*_arm64.s|go.mod|go.sum)
      go=true arm64=true ;;
    # Code directories first, so a Markdown fixture under them is never "docs".
    console/*)
      console=true ;;
    api/*)
      go=true console=true ;;
    *.go|go.mod|go.sum|migrations/*|internal/*|cmd/*|contracts/*|examples/*)
      go=true ;;
    Dockerfile*|compose*.yml|deploy/*)
      images=true ;;
    # release-version-check (quality) reads these.
    CHANGELOG*.md|docs/cli.md|docs/cli.zh-CN.md|VERSION)
      docs=true go=true ;;
    docs/*|*.md|LICENSE|NOTICE)
      docs=true ;;
    *)
      # Unknown path: run everything.
      go=true console=true docs=true ci=true images=true arm64=true ;;
  esac
done <<<"$files"

# CI, build files and images affect every job that executes the product, and both
# change how it is built or run per architecture.
if [[ "$ci" == true ]]; then
  go=true console=true docs=true images=true
fi
if [[ "$images" == true ]]; then
  go=true console=true arm64=true
fi

emit go "$go"; emit console "$console"; emit docs "$docs"; emit ci "$ci"; emit images "$images"; emit arm64 "$arm64"
