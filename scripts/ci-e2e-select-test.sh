#!/usr/bin/env bash
# Fixtures for scripts/ci-e2e-select.sh.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
export CI_E2E_SELECT_ASSUME_EXISTS=1

check() { # <expected specs value> <paths...>
  local expected=$1; shift
  local out
  out=$(printf '%s\n' "$@" | scripts/ci-e2e-select.sh --files)
  if [[ "$out" != "specs=$expected" ]]; then
    printf 'FAIL %s: got %s, want specs=%s\n' "$*" "$out" "$expected" >&2
    exit 1
  fi
}

check "e2e/a.spec.ts" console/e2e/a.spec.ts
check "e2e/a.spec.ts e2e/b.spec.ts" console/e2e/a.spec.ts console/e2e/b.spec.ts
check "" console/e2e/a.spec.ts console/src/App.tsx
check "" console/e2e/support/helpers.ts
check "" console/e2e/visual/x.visual.spec.ts
check "" internal/foo.go
check "" console/package.json
check "" ""

# Selected paths must classify as console-only, the class that enables console-e2e.
[[ "$(printf '%s\n' console/e2e/a.spec.ts | scripts/ci-changes.sh --files | paste -sd, -)" == go=false,console=true,docs=false,ci=false,images=false ]]

# A spec that no longer exists is uncertain: full suite.
unset CI_E2E_SELECT_ASSUME_EXISTS
[[ "$(printf '%s\n' console/e2e/definitely-missing.spec.ts | scripts/ci-e2e-select.sh --files)" == "specs=" ]]
# No range: full suite.
[[ "$(scripts/ci-e2e-select.sh)" == "specs=" ]]
printf 'ci-e2e-select checks passed.\n'
