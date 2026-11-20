#!/usr/bin/env bash
# Tests scripts/lib/install-snippet.sh against docs/install.md (ADR 0025 slice 8): the install
# snippets of the page resolve to the file names the package builder writes, and a malformed
# snippet is refused. Docker-free; scripts/package-install-test.sh runs the commands in containers.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
# shellcheck source=lib/install-snippet.sh
source "$root/scripts/lib/install-snippet.sh"

failures=0
cases=0
fail() {
  failures=$((failures + 1))
  printf 'FAIL: %s\n' "$1" >&2
}
expect_eq() {
  cases=$((cases + 1))
  [[ "$2" == "$3" ]] || fail "$1: got [$2], want [$3]"
}

doc=$root/docs/install.md
version=0.2.0
stage=$workdir/stage
mkdir "$stage"
touch "$stage/notarx_${version}_amd64.deb" "$stage/notarxctl_${version}_amd64.deb" "$stage/notarx-console_${version}_all.deb"
touch "$stage/notarx-${version}.x86_64.rpm" "$stage/notarxctl-${version}.x86_64.rpm" "$stage/notarx-console-${version}.noarch.rpm"

expect_eq 'deb with the console' "$(install_commands "$doc" deb "$stage" "$version" amd64)" "apt-get install -y -qq ./notarx_0.2.0_amd64.deb
apt-get install -y -qq ./notarx-console_0.2.0_all.deb
apt-get install -y -qq ./notarxctl_0.2.0_amd64.deb"
expect_eq 'rpm with the console' "$(install_commands "$doc" rpm "$stage" "$version" x86_64)" "dnf install -y -q ./notarx-0.2.0.x86_64.rpm
dnf install -y -q ./notarx-console-0.2.0.noarch.rpm
dnf install -y -q ./notarxctl-0.2.0.x86_64.rpm"

# The console package is built once on the amd64 leg: without it the line is skipped.
rm "$stage/notarx-console_${version}_all.deb" "$stage/notarx-console-${version}.noarch.rpm"
expect_eq 'deb without the console' "$(install_commands "$doc" deb "$stage" "$version" amd64)" "apt-get install -y -qq ./notarx_0.2.0_amd64.deb
apt-get install -y -qq ./notarxctl_0.2.0_amd64.deb"
expect_eq 'a nightly version keeps its stamp' "$(touch "$stage/notarx_0.2.0-nightly.3_arm64.deb" && install_commands "$doc" deb "$stage" 0.2.0-nightly.3 arm64)" "apt-get install -y -qq ./notarx_0.2.0-nightly.3_arm64.deb"

cases=$((cases + 1))
if install_commands "$doc" nope "$stage" "$version" amd64 >/dev/null 2>&1; then fail 'an unknown snippet id was accepted'; fi
cat >"$workdir/bad.md" <<'DOC'
<!-- install-snippet: deb -->
```sh
sudo curl -fsSL https://example.invalid/key | sudo tee /etc/apt/trusted.gpg
```
DOC
cases=$((cases + 1))
if install_commands "$workdir/bad.md" deb "$stage" "$version" amd64 >/dev/null 2>&1; then fail 'an unsupported snippet line was accepted'; fi

if [[ "$failures" -ne 0 ]]; then
  printf '%d of %d install-snippet check(s) failed\n' "$failures" "$cases" >&2
  exit 1
fi
printf 'install-snippet-test passed (%d checks).\n' "$cases"
