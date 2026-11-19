#!/usr/bin/env bash
# Builds the three native packages locally and asserts their contents (ADR 0025).
#
#   scripts/package-check.sh [--output DIR]                  build + assert (stable and nightly versions)
#   scripts/package-check.sh --verify --packages DIR --assets DIR --version V [--console]
#                                                            assert already built packages only
#
# Needs Linux with dpkg-deb, rpm, rpm2cpio and cpio (apt-get install rpm cpio), plus Go and
# nfpm (scripts/install-nfpm.sh) for the build mode. Asserts, per package: Package, Version
# and Release fields (dpkg-deb -f, rpm -qp), Architecture, Recommends, file modes and owners,
# the config file, the packaged binary hashes (byte-identical to the archive), and that no
# maintainer script applies migrations or starts, enables or restarts the service.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
verify_only=0
packages=
assets=
version=
with_console=0
output=

while [[ $# -gt 0 ]]; do
  case "$1" in
    --verify) verify_only=1; shift ;;
    --packages) packages=${2:-}; shift 2 ;;
    --assets) assets=${2:-}; shift 2 ;;
    --version) version=${2:-}; shift 2 ;;
    --console) with_console=1; shift ;;
    --output) output=${2:-}; shift 2 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done

if [[ "$(uname -s)" != Linux ]]; then
  printf 'package-check: needs a Linux host (native packages, ADR 0016)\n' >&2
  exit 1
fi
for tool in dpkg-deb rpm rpm2cpio cpio sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || { printf 'package-check: %s is required (apt-get install dpkg rpm cpio)\n' "$tool" >&2; exit 1; }
done

case "$(uname -m)" in
  x86_64) arch=amd64 rpm_arch=x86_64 ;;
  aarch64|arm64) arch=arm64 rpm_arch=aarch64 ;;
  *) printf 'package-check: unsupported architecture %s\n' "$(uname -m)" >&2; exit 1 ;;
esac

failures=0
fail() { printf 'FAIL: %s\n' "$*" >&2; failures=$((failures + 1)); }
expect_eq() { # expect_eq <description> <expected> <actual>
  if [[ "$2" != "$3" ]]; then fail "$1: expected '$2', got '$3'"; fi
}
expect_line() { # expect_line <description> <fixed text that must occur> <haystack>
  if ! grep -Fq -- "$2" <<<"$3"; then fail "$1: '$2' not found"; fi
}

workdir=$(mktemp -d)
cleanup() { rm -rf "$workdir"; }
trap cleanup EXIT

# --- assertions over one version ---------------------------------------------------------
check_version() { # check_version <version> <packages dir> <assets dir> <console 0|1>
  local version=$1 pkgs=$2 assets=$3 console=$4
  local DEB_VERSION DEB_RELEASE RPM_VERSION RPM_RELEASE
  eval "$("$root/scripts/package-version.sh" "$version")"
  local scratch="$workdir/check-$version"
  mkdir -p "$scratch/server" "$scratch/cli"
  tar -xzf "$assets/notarx_${version}_linux_$arch.tar.gz" -C "$scratch/server"
  tar -xzf "$assets/notarxctl_${version}_linux_$arch.tar.gz" -C "$scratch/cli"
  local server_sha cli_sha
  server_sha=$(sha256sum "$scratch/server/notarx" | cut -d' ' -f1)
  cli_sha=$(sha256sum "$scratch/cli/notarxctl" | cut -d' ' -f1)

  local names=(notarx notarxctl)
  if [[ "$console" -eq 1 ]]; then names+=(notarx-console); fi
  local name deb rpmf deb_arch rpm_pkg_arch
  for name in "${names[@]}"; do
    deb_arch=$arch rpm_pkg_arch=$rpm_arch
    if [[ "$name" == notarx-console ]]; then deb_arch=all rpm_pkg_arch=noarch; fi
    deb="$pkgs/${name}_${version}_$deb_arch.deb"
    rpmf="$pkgs/$name-$version.$rpm_pkg_arch.rpm"
    [[ -f "$deb" ]] || { fail "missing $deb"; continue; }
    [[ -f "$rpmf" ]] || { fail "missing $rpmf"; continue; }

    expect_eq "$name deb Package" "$name" "$(dpkg-deb -f "$deb" Package)"
    expect_eq "$name deb Version" "$DEB_VERSION-$DEB_RELEASE" "$(dpkg-deb -f "$deb" Version)"
    expect_eq "$name deb Architecture" "$deb_arch" "$(dpkg-deb -f "$deb" Architecture)"
    expect_eq "$name rpm Name" "$name" "$(rpm -qp --qf '%{NAME}' "$rpmf")"
    expect_eq "$name rpm Version" "$RPM_VERSION" "$(rpm -qp --qf '%{VERSION}' "$rpmf")"
    expect_eq "$name rpm Release" "$RPM_RELEASE" "$(rpm -qp --qf '%{RELEASE}' "$rpmf")"
    expect_eq "$name rpm Arch" "$rpm_pkg_arch" "$(rpm -qp --qf '%{ARCH}' "$rpmf")"

    local deb_list rpm_list rpm_attrs scripts_dir
    deb_list=$(dpkg-deb -c "$deb")
    rpm_list=$(rpm -qpl "$rpmf")
    rpm_attrs=$(rpm -qp --qf '[%{FILEMODES:perms} %{FILEUSERNAME} %{FILEGROUPNAME} %{FILENAMES}\n]' "$rpmf")
    scripts_dir="$workdir/scripts-$version-$name"
    mkdir -p "$scripts_dir"
    dpkg-deb -e "$deb" "$scripts_dir"

    case "$name" in
      notarx)
        expect_eq "notarx deb Recommends" "postgresql-client" "$(dpkg-deb -f "$deb" Recommends)"
        expect_line "notarx rpm Recommends" "postgresql" "$(rpm -qp --recommends "$rpmf")"
        expect_line 'notarx deb binary mode' '-rwxr-xr-x root/root' "$(grep -F ' ./usr/bin/notarx' <<<"$deb_list" | grep -v migrate)"
        expect_line 'notarx deb env mode' '-rw-r----- root/notarx' "$(grep -F ' ./etc/notarx/notarx.env' <<<"$deb_list")"
        expect_line 'notarx deb unit' './usr/lib/systemd/system/notarx.service' "$deb_list"
        expect_line 'notarx deb sysusers' './usr/lib/sysusers.d/notarx.conf' "$deb_list"
        expect_line 'notarx deb migrate wrapper' './usr/bin/notarx-migrate' "$deb_list"
        expect_line 'notarx deb runner' './usr/share/notarx/run-migrations.sh' "$deb_list"
        expect_line 'notarx deb baseline' './usr/share/notarx/migrations/000001_baseline.sql' "$deb_list"
        expect_eq 'notarx deb conffiles' '/etc/notarx/notarx.env' "$(cat "$scripts_dir/conffiles")"
        expect_line 'notarx rpm env mode' '-rw-r----- root notarx /etc/notarx/notarx.env' "$rpm_attrs"
        expect_line 'notarx rpm binary mode' '-rwxr-xr-x root root /usr/bin/notarx' "$rpm_attrs"
        expect_line 'notarx rpm unit' '/usr/lib/systemd/system/notarx.service' "$rpm_list"
        expect_line 'notarx rpm baseline' '/usr/share/notarx/migrations/000001_baseline.sql' "$rpm_list"
        expect_line 'notarx rpm config flag' '/etc/notarx/notarx.env' "$(rpm -qpc "$rpmf")"
        if ! rpm -qp --qf '[%{FILEFLAGS:fflags} %{FILENAMES}\n]' "$rpmf" |
          awk '$2 == "/etc/notarx/notarx.env" && $1 ~ /c/ && $1 ~ /n/ { found = 1 } END { exit !found }'; then
          fail 'notarx rpm: /etc/notarx/notarx.env is not config(noreplace)'
        fi
        expect_eq 'notarx deb binary hash' "$server_sha" "$(dpkg-deb --fsys-tarfile "$deb" | tar -xO ./usr/bin/notarx | sha256sum | cut -d' ' -f1)"
        expect_eq 'notarx rpm binary hash' "$server_sha" "$(rpm2cpio "$rpmf" | cpio -i --quiet --to-stdout ./usr/bin/notarx | sha256sum | cut -d' ' -f1)"
        expect_eq 'notarx deb runner hash' "$(sha256sum "$scratch/server/run-migrations.sh" | cut -d' ' -f1)" \
          "$(dpkg-deb --fsys-tarfile "$deb" | tar -xO ./usr/share/notarx/run-migrations.sh | sha256sum | cut -d' ' -f1)"
        check_scripts "$name" "$scripts_dir" "$rpmf"
        ;;
      notarxctl)
        expect_line 'notarxctl deb binary mode' '-rwxr-xr-x root/root' "$(grep -F ' ./usr/bin/notarxctl' <<<"$deb_list")"
        expect_line 'notarxctl rpm binary mode' '-rwxr-xr-x root root /usr/bin/notarxctl' "$rpm_attrs"
        expect_eq 'notarxctl deb file count' 1 "$(grep -c -v '/$' <<<"$deb_list")"
        expect_eq 'notarxctl deb binary hash' "$cli_sha" "$(dpkg-deb --fsys-tarfile "$deb" | tar -xO ./usr/bin/notarxctl | sha256sum | cut -d' ' -f1)"
        expect_eq 'notarxctl rpm binary hash' "$cli_sha" "$(rpm2cpio "$rpmf" | cpio -i --quiet --to-stdout ./usr/bin/notarxctl | sha256sum | cut -d' ' -f1)"
        ;;
      notarx-console)
        expect_eq 'console deb Recommends' nginx "$(dpkg-deb -f "$deb" Recommends)"
        expect_line 'console rpm Recommends' nginx "$(rpm -qp --recommends "$rpmf")"
        expect_line 'console deb assets' './usr/share/notarx/console/index.html' "$deb_list"
        expect_line 'console deb nginx example' './usr/share/doc/notarx-console/examples/nginx.conf' "$deb_list"
        expect_line 'console rpm assets' '/usr/share/notarx/console/index.html' "$rpm_list"
        expect_line 'console rpm nginx example' '/usr/share/doc/notarx-console/examples/nginx.conf' "$rpm_list"
        expect_line 'console nginx proxy' 'proxy_pass http://127.0.0.1:8080;' \
          "$(dpkg-deb --fsys-tarfile "$deb" | tar -xO ./usr/share/doc/notarx-console/examples/nginx.conf)"
        ;;
    esac
  done
}

# No migration and no start logic in any maintainer script (ADR 0025).
check_scripts() { # check_scripts <name> <deb scripts dir> <rpm file>
  local name=$1 dir=$2 rpmf=$3 script text
  for script in preinst postinst prerm; do
    [[ -f "$dir/$script" ]] || { fail "$name deb is missing $script"; continue; }
    # Operator hints are printf lines; they may name the commands they tell a human to run.
    text=$(grep -Ev '^[[:space:]]*(printf|echo)[[:space:]]' "$dir/$script" || true)
    if grep -Eq 'run-migrations|psql|notarx migrate|pg_dump' <<<"$text"; then fail "$name $script applies or references migrations"; fi
    if grep -Eq 'systemctl[[:space:]]+(--[a-z-]+[[:space:]]+)*(start|enable|restart|reload-or-restart|reload-or-try-restart)\b' <<<"$text"; then
      fail "$name $script starts, enables or restarts the service"
    fi
    if grep -E 'notarx-migrate' <<<"$text" | grep -Ev -- '--check' | grep -q .; then
      fail "$name $script runs notarx-migrate without --check"
    fi
  done
  if ! grep -q 'try-restart' "$dir/postinst" || ! grep -q -- 'notarx-migrate --check' "$dir/postinst"; then
    fail "$name postinst must gate try-restart on notarx-migrate --check"
  fi
  local rpm_scripts
  rpm_scripts=$(rpm -qp --scripts "$rpmf" | grep -Ev '^[[:space:]]*(printf|echo)[[:space:]]' || true)
  expect_line "$name rpm scriptlets" 'groupadd --system notarx' "$rpm_scripts"
  if grep -Eq 'run-migrations|psql' <<<"$rpm_scripts"; then fail "$name rpm scriptlets reference migrations"; fi
  if grep -Eq 'systemctl[[:space:]]+(start|enable|restart)\b' <<<"$rpm_scripts"; then fail "$name rpm scriptlets start or enable the service"; fi
}

if [[ "$verify_only" -eq 1 ]]; then
  [[ -d "$packages" && -d "$assets" && -n "$version" ]] || { printf 'usage: package-check.sh --verify --packages DIR --assets DIR --version V [--console]\n' >&2; exit 2; }
  check_version "$version" "$(cd "$packages" && pwd)" "$(cd "$assets" && pwd)" "$with_console"
else
  versions=(${PACKAGE_CHECK_VERSIONS:-0.2.0 0.2.0-nightly.3})
  revision=0123456789abcdef0123456789abcdef01234567
  if [[ -n "$output" ]]; then mkdir -p "$output"; output=$(cd "$output" && pwd); fi
  for version in "${versions[@]}"; do
    assets="$workdir/assets-$version"
    stub="$workdir/console-stub-$version"
    out=$workdir/packages-$version
    [[ -n "$output" ]] && out=$output/$version
    mkdir -p "$assets" "$stub/assets"
    "$root/scripts/build-release-artifacts.sh" --version "$version" --revision "$revision" --output "$assets" --skip-console >/dev/null
    printf '<!doctype html><title>notarx console stub</title>\n' > "$stub/index.html"
    printf 'stub\n' > "$stub/assets/stub.txt"
    tar -czf "$assets/notarx-console_${version}_web.tar.gz" -C "$stub" .
    "$root/scripts/build-packages.sh" --version "$version" --assets "$assets" --output "$out" --console >/dev/null
    printf 'checking packages for %s\n' "$version"
    check_version "$version" "$out" "$assets" 1
  done
fi

if [[ "$failures" -ne 0 ]]; then
  printf '%d package check(s) failed\n' "$failures" >&2
  exit 1
fi
printf 'package-check passed.\n'
