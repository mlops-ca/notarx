#!/usr/bin/env bash
# Identity gate (ADR 0013, "Identity", slice 9). Fails when an upstream identity
# string appears in a tracked file outside the allow-list: the upstream owner,
# the upstream product name in its kebab and snake spellings, the old env prefix,
# the old CLI name and the old metric/ledger prefix.
#
# Usage: scripts/identity-check.sh [--root DIR] [--list-base | --list-extra]
#   --list-base   print the allow-list that ADR 0013 fixes (one pathspec per line)
#   --list-extra  print the documented additions, as "pathspec<TAB>reason"
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
mode=check
while (($# > 0)); do
  case "$1" in
    --root) root=$(cd "$2" && pwd); shift 2 ;;
    --list-base) mode=base; shift ;;
    --list-extra) mode=extra; shift ;;
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
  $'scripts/rename-module.sh\tThe cherry-pick rewrite tool (ADR 0013 Relationship with upstream) must name the old identity.'
  $'scripts/identity-check.sh\tThis gate spells the patterns it searches for.'
  $'scripts/identity-check-test.sh\tThe fixture test plants those patterns on purpose.'
)

case "$mode" in
  base) printf '%s\n' "${base_allow[@]}"; exit 0 ;;
  extra) printf '%s\n' "${extra_allow[@]}"; exit 0 ;;
esac

pattern='ccsert|artifact-gateway|artifact_gateway|GATEWAY_|agctl|gateway_'

specs=(.)
for entry in "${base_allow[@]}"; do specs+=(":(exclude)$entry"); done
for entry in "${extra_allow[@]}"; do specs+=(":(exclude)${entry%%$'\t'*}"); done

cd "$root"
# Line-level exemptions, applied after the search:
#  - lines with CJK text: existing zh-CN strings are frozen (ADR 0014);
#  - the link target of ADR 0007, whose file name keeps the old CLI name;
#  - the literal upstream trailer that cherry-picks carry (ADR 0013).
found=$(git grep -InE -e "$pattern" -- "${specs[@]}" | PATTERN="$pattern" perl -CSD -ne '
  use utf8;
  my ($path, $line, $text) = split(/:/, $_, 3);
  next if $text =~ /\p{Han}/;
  my $t = $text;
  $t =~ s{adr/0007-agctl-cli-shape}{}g;
  $t =~ s{Upstream: ccsert/artifact-gateway\@}{}g;
  next unless $t =~ /$ENV{PATTERN}/;
  print;
' || true)

if [[ -n "$found" ]]; then
  printf '%s\n' "$found" >&2
  printf 'identity-check: upstream identity strings found outside the allow-list (ADR 0013).\n' >&2
  exit 1
fi
printf 'identity-check: ok\n'
