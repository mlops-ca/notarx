#!/usr/bin/env bash
# Fixtures for scripts/ci-changes.sh path classification.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

check() { # <expected go,console,docs,ci,images> <paths...>
  local expected=$1; shift
  local out
  out=$(printf '%s\n' "$@" | scripts/ci-changes.sh --files | cut -d= -f2 | paste -sd, -)
  if [[ "$out" != "$expected" ]]; then
    printf 'FAIL %s: got %s, want %s\n' "$*" "$out" "$expected" >&2
    exit 1
  fi
}

check false,false,true,false,false docs/adr/0016-x.md README.md
check false,false,true,false,false LICENSE docs/ci.zh-CN.md
check true,false,false,false,false internal/foo/bar.go migrations/001.sql
check false,true,false,false,false console/src/App.tsx
check true,true,false,false,false api/openapi/management.yaml
check true,true,true,true,true .github/workflows/ci.yml
check true,true,true,true,true Makefile
check true,true,true,true,true scripts/ci-changes.sh
check true,true,false,false,true Dockerfile
check true,true,false,false,true deploy/oci/compose.yml
check true,true,true,true,true some-new-dir/file
check true,false,true,false,false internal/a.go docs/a.md
check true,false,false,false,false internal/x/template.md
check false,true,false,false,false console/src/notes.md
check true,false,true,false,false CHANGELOG.md

# No range resolves to the full run.
[[ "$(scripts/ci-changes.sh | cut -d= -f2 | paste -sd, -)" == true,true,true,true,true ]]
printf 'ci-changes classification checks passed.\n'
