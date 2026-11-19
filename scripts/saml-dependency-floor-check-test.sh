#!/usr/bin/env bash
# Fixture tests for scripts/saml-dependency-floor-check.sh: negative fixtures must fail,
# the pinned minimum and newer versions must pass, and the repository go.mod must pass.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
check=$root/scripts/saml-dependency-floor-check.sh
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

write_mod() { # name dsig-version saml-version
  printf 'module example.test/fixture\n\ngo 1.27\n\nrequire (\n' >"$work/$1"
  [[ "$2" == none ]] || printf '\tgithub.com/russellhaering/goxmldsig %s\n' "$2" >>"$work/$1"
  [[ "$3" == none ]] || printf '\tgithub.com/russellhaering/gosaml2 %s // indirect\n' "$3" >>"$work/$1"
  printf ')\n' >>"$work/$1"
}
expect_pass() {
  "$check" "$work/$1" >/dev/null 2>&1 || { printf 'FAIL %s: expected success\n' "$1" >&2; exit 1; }
  printf 'ok   %s\n' "$1"
}
expect_fail() {
  local output
  if output=$("$check" "$work/$1" 2>&1); then printf 'FAIL %s: expected failure\n' "$1" >&2; exit 1; fi
  [[ "$output" == *"$2"* ]] || { printf 'FAIL %s: output lacks %s\n%s\n' "$1" "$2" "$output" >&2; exit 1; }
  printf 'ok   %s\n' "$1"
}

write_mod pinned.mod v1.6.1 v0.12.0;   expect_pass pinned.mod
write_mod newer.mod v1.10.0 v0.13.2;   expect_pass newer.mod
write_mod low-dsig.mod v1.6.0 v0.12.0; expect_fail low-dsig.mod goxmldsig
write_mod low-dsig-minor.mod v1.5.9 v0.12.0; expect_fail low-dsig-minor.mod goxmldsig
write_mod low-saml.mod v1.6.1 v0.11.9; expect_fail low-saml.mod gosaml2
write_mod low-saml-numeric.mod v1.6.1 v0.9.0; expect_fail low-saml-numeric.mod gosaml2
write_mod missing.mod none v0.12.0;    expect_fail missing.mod "not required"
"$check" "$root/go.mod" >/dev/null
printf 'ok   repository go.mod\n'
