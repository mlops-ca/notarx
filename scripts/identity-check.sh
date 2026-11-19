#!/usr/bin/env bash
# Identity gate (ADR 0013, "Identity", slice 9). Fails when an upstream identity
# string appears in a tracked file outside the allow-list: the upstream owner,
# the upstream product name in its kebab, snake, spaced and camel spellings, the old env prefix,
# the old CLI name, the old metric/ledger prefix and the short `ag-` / `AG_`
# abbreviation of the old product name.
#
# Usage: scripts/identity-check.sh [--root DIR] [--list-base | --list-extra]
#   --list-base   print the allow-list that ADR 0013 fixes (one pathspec per line)
#   --list-extra  print the documented additions, as "pathspec<TAB>reason"
#   --list-ag     print the additions that apply to the short abbreviation only
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
mode=check
while (($# > 0)); do
  case "$1" in
    --root) root=$(cd "$2" && pwd); shift 2 ;;
    --list-base) mode=base; shift ;;
    --list-extra) mode=extra; shift ;;
    --list-ag) mode=ag; shift ;;
    *) printf 'identity-check: unknown argument %s\n' "$1" >&2; exit 2 ;;
  esac
done

# The allow-list exactly as ADR 0013 states it: licence and attribution files,
# the upstream record, the changelog preamble, ADRs (they quote history and old
# names, including the file name of ADR 0007) and the frozen zh-CN legacy twins.
base_allow=(
  'LICENSE'
  'NOTICE'
  'docs/upstream.md'
  'CHANGELOG*'
  'docs/adr'
  '*.zh-CN.md'
)

# Documented additions. Each one names the legacy thing it must keep spelling.
extra_allow=(
  $'.cspell/project-words.txt\tThe spelling dictionary lists the upstream owner that the allow-listed files quote.'
  $'docs/release-records\tDated evidence of upstream builds; rewriting the names would falsify the record.'
  $'docs/reports\tDated measurement reports taken on upstream builds.'
  $'internal/config/legacy_env*\tThe closed list of pre-fork environment names that startup refuses (ADR 0013 Security considerations).'
  $'scripts/derive-legacy-env*\tDerives that closed list from the pre-fork config loader.'
  $'internal/backupops/release.go\tRecovery must verify releases published under the pre-fork module path.'
  $'internal/backupops/release_module_test.go\tCovers the pre-fork module path verification above.'
  $'internal/backupops/testdata/published-native-provenance.json\tProvenance of pre-fork published release archives used by recovery tests.'
  $'scripts/published-native-backup-test.sh\tDownloads those pre-fork published release archives from the upstream repository.'
  $'migrations/000001_baseline.sql\tThe generated baseline keeps the pre-fork column defaults; migration 000013 changes them.'
  $'migrations/000013_brand_defaults.sql\tMoves stored pre-fork default names to the notarx ones, so it names the old default.'
  $'internal/refaptsigner/testdata\tSigned APT fixtures; editing the Origin field would break their signatures.'
  $'scripts/rename-module.sh\tThe cherry-pick rewrite tool (ADR 0013 Relationship with upstream) must name the old identity.'
  $'scripts/identity-check.sh\tThis gate spells the patterns it searches for.'
  $'scripts/identity-check-test.sh\tThe fixture test plants those patterns on purpose.'
)

# Additions that apply to the short abbreviation only. The Console keeps its
# internal `ag-` CSS classes and `--ag-` custom properties (a rename would touch
# thousands of selectors and the frozen zh-CN design notes that quote them); its
# localStorage keys `ag.console.*` are not matched by the pattern and stay too.
ag_allow=(
  $'console\tConsole CSS classes, custom properties and e2e selectors are internal and not renamed.'
  $'DESIGN.md\tDesign notes quote the internal Console CSS names.'
  $'.impeccable\tDesign tooling records quote the internal Console CSS names.'
  $'.agents/skills/console-layout-guardrails\tThe layout guardrails name the internal Console CSS classes.'
  $'scripts/email-console-browser-check.mjs\tBrowser check selecting internal Console CSS classes.'
)

case "$mode" in
  ag) printf '%s\n' "${ag_allow[@]}"; exit 0 ;;
  base) printf '%s\n' "${base_allow[@]}"; exit 0 ;;
  extra) printf '%s\n' "${extra_allow[@]}"; exit 0 ;;
esac

pattern='ccsert|[Aa]rtifact[ _-]?[Gg]ateway|ARTIFACT GATEWAY|GATEWAY_|agctl|gateway_'
# The short abbreviation must not be glued to a longer word (`flag-x`, `stag_A`).
ag_pattern='(^|[^[:alnum:]_])(ag-[[:alnum:]]|AG_[[:alnum:]])'

specs=(.)
for entry in "${base_allow[@]}"; do specs+=(":(exclude)$entry"); done
for entry in "${extra_allow[@]}"; do specs+=(":(exclude)${entry%%$'\t'*}"); done
ag_specs=("${specs[@]}")
for entry in "${ag_allow[@]}"; do ag_specs+=(":(exclude)${entry%%$'\t'*}"); done

cd "$root"
# Line-level exemptions, applied after the search:
#  - frozen zh-CN text (ADR 0014): a quoted string that contains CJK text is
#    dropped before matching, and a CJK line without quotes (prose) is skipped;
#    any other string on the same line is still checked;
#  - the link target of ADR 0007, whose file name keeps the old CLI name;
#  - the literal upstream trailer that cherry-picks carry (ADR 0013).
scan() { # pattern pathspec...
  local scan_pattern=$1; shift
  git grep -InE -e "$scan_pattern" -- "$@" | PATTERN="$scan_pattern" perl -CSD -ne '
  use utf8;
  my ($path, $line, $text) = split(/:/, $_, 3);
  my $t = $text;
  if ($t =~ /\p{Han}/) {
    next unless $t =~ /["\x27\x60]/;
    $t =~ s/"[^"]*\p{Han}[^"]*"//g;
    $t =~ s/\x27[^\x27]*\p{Han}[^\x27]*\x27//g;
    $t =~ s/\x60[^\x60]*\p{Han}[^\x60]*\x60//g;
  }
  $t =~ s{adr/0007-agctl-cli-shape}{}g;
  $t =~ s{Upstream: ccsert/artifact-gateway\@}{}g;
  next unless $t =~ /$ENV{PATTERN}/;
  print;
' || true
}
found=$(scan "$pattern" "${specs[@]}"; scan "$ag_pattern" "${ag_specs[@]}")

if [[ -n "$found" ]]; then
  printf '%s\n' "$found" >&2
  printf 'identity-check: upstream identity strings found outside the allow-list (ADR 0013).\n' >&2
  exit 1
fi
printf 'identity-check: ok\n'
