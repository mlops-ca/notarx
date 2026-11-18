#!/usr/bin/env bash
set -euo pipefail

# Spell-check Console messages and English documentation against the en-CA
# dictionary (ADR 0014), so US spellings and cherry-picked upstream copy fail
# mechanically. zh-CN twins are not checked. Needs `npm ci` in console/.

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

cspell() {
  console/node_modules/.bin/cspell lint --config cspell.json --no-progress --no-summary "$@"
}

# The fixtures prove the check bites: US spellings must fail, en-CA must pass.
if cspell scripts/testdata/spellcheck/us-spelling.txt >/dev/null 2>&1; then
  printf 'spellcheck: the US spelling fixture passed; the en-CA dictionary is not active.\n' >&2
  exit 1
fi
cspell scripts/testdata/spellcheck/en-ca-spelling.txt >/dev/null

cspell "console/src/messages/*.en-CA.ts" "docs/**/*.md" "*.md"
