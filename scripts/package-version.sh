#!/usr/bin/env bash
# Maps a stamped release version to the per-format package version strings (ADR 0025).
# nfpm runs with version_schema none, so these strings reach the packages verbatim.
#
#   scripts/package-version.sh 0.2.0-nightly.3
#
# prints KEY=VALUE lines: DEB_VERSION DEB_RELEASE RPM_VERSION RPM_RELEASE.
#   stable  0.2.0            deb 0.2.0-1                 rpm 0.2.0-1
#   nightly 0.2.0-nightly.3  deb 0.2.0~nightly.3-1       rpm 0.2.0-0.nightly.3
# Debian sorts a tilde before the end of the string and RPM sorts Release 0.x below 1,
# so every pre-release sorts below the release it precedes and nightly.N sorts numerically.
set -euo pipefail

if [[ $# -ne 1 ]]; then
  printf 'usage: %s <version>\n' "$0" >&2
  exit 2
fi
version=$1
if [[ ! "$version" =~ ^([0-9]+\.[0-9]+\.[0-9]+)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]]; then
  printf 'not a SemVer version without build metadata: %s\n' "$version" >&2
  exit 2
fi
core=${BASH_REMATCH[1]}
pre=${BASH_REMATCH[2]#-}
# Hyphens are not allowed inside an RPM Release and would split a Debian revision.
pre=${pre//-/.}

if [[ -z "$pre" ]]; then
  printf 'DEB_VERSION=%s\nDEB_RELEASE=1\nRPM_VERSION=%s\nRPM_RELEASE=1\n' "$core" "$core"
else
  printf 'DEB_VERSION=%s~%s\nDEB_RELEASE=1\nRPM_VERSION=%s\nRPM_RELEASE=0.%s\n' "$core" "$pre" "$core" "$pre"
fi
