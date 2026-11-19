#!/usr/bin/env bash
# Builds the native packages of ADR 0025 by repacking the release archives, so the
# packaged binary is byte-identical to the archived (and tested) one.
#
#   scripts/build-packages.sh --version V --assets DIR --output DIR [--console]
#
# DIR holds notarx_V_linux_<arch>.tar.gz and notarxctl_V_linux_<arch>.tar.gz for this host's
# architecture (and notarx-console_V_web.tar.gz with --console). Writes, per package:
#   notarx_V_<arch>.deb                notarx-V.<rpmarch>.rpm
#   notarxctl_V_<arch>.deb             notarxctl-V.<rpmarch>.rpm
#   notarx-console_V_all.deb           notarx-console-V.noarch.rpm      (--console)
# Packages are not signed (ADR 0025 amendment); integrity is SHA256SUMS plus attestations.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
version=
assets=
output=
with_console=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) version=${2:-}; shift 2 ;;
    --assets) assets=${2:-}; shift 2 ;;
    --output) output=${2:-}; shift 2 ;;
    --console) with_console=1; shift ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done
[[ -n "$version" && -d "$assets" && -n "$output" ]] || { printf 'usage: build-packages.sh --version V --assets DIR --output DIR [--console]\n' >&2; exit 2; }
if [[ "$(uname -s)" != Linux ]]; then
  printf 'build-packages: Linux packages are built on Linux runners only (no cross-compilation, ADR 0016)\n' >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64) arch=amd64 rpm_arch=x86_64 ;;
  aarch64|arm64) arch=arm64 rpm_arch=aarch64 ;;
  *) printf 'build-packages: unsupported architecture %s\n' "$(uname -m)" >&2; exit 1 ;;
esac

eval "$("$root/scripts/package-version.sh" "$version")"
nfpm=${NFPM:-$("$root/scripts/install-nfpm.sh")}

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
mkdir -p "$output"
output=$(cd "$output" && pwd)
assets=$(cd "$assets" && pwd)

extract() { # extract <archive> <dir>
  [[ -f "$1" ]] || { printf 'build-packages: missing archive %s\n' "$1" >&2; exit 1; }
  mkdir -p "$2"
  tar -xzf "$1" -C "$2"
}
extract "$assets/notarx_${version}_linux_$arch.tar.gz" "$workdir/server"
extract "$assets/notarxctl_${version}_linux_$arch.tar.gz" "$workdir/cli"
if [[ "$with_console" -eq 1 ]]; then
  extract "$assets/notarx-console_${version}_web.tar.gz" "$workdir/console"
fi

# Reproducible package metadata: file times follow the commit when the tree is a checkout.
if [[ -z "${SOURCE_DATE_EPOCH:-}" ]]; then
  SOURCE_DATE_EPOCH=$(git -C "$root" log -1 --format=%ct 2>/dev/null || date +%s)
fi
export SOURCE_DATE_EPOCH

pack() { # pack <config name> <stage> <packager> <target file> <package arch>
  local config=$1 stage=$2 packager=$3 target=$4 package_arch=$5
  local package_version package_release
  if [[ "$packager" == deb ]]; then package_version=$DEB_VERSION package_release=$DEB_RELEASE; else package_version=$RPM_VERSION package_release=$RPM_RELEASE; fi
  (cd "$root" && VERSION=$package_version RELEASE=$package_release ARCH=$package_arch STAGE=$stage \
    "$nfpm" package --config "packaging/nfpm-$config.yaml" --packager "$packager" --target "$output/$target")
}

pack notarx "$workdir/server" deb "notarx_${version}_$arch.deb" "$arch"
pack notarx "$workdir/server" rpm "notarx-$version.$rpm_arch.rpm" "$arch"
pack notarxctl "$workdir/cli" deb "notarxctl_${version}_$arch.deb" "$arch"
pack notarxctl "$workdir/cli" rpm "notarxctl-$version.$rpm_arch.rpm" "$arch"
if [[ "$with_console" -eq 1 ]]; then
  pack notarx-console "$workdir/console" deb "notarx-console_${version}_all.deb" all
  pack notarx-console "$workdir/console" rpm "notarx-console-$version.noarch.rpm" all
fi
printf 'Built notarx %s packages in %s.\n' "$version" "$output"
