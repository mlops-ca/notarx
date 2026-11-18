#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
version=
revision=
output=
skip_console=0
targets=()

usage() {
  printf '%s\n' \
    'Usage: build-release-artifacts.sh --version VERSION --revision SHA --output DIR [--target GOOS/GOARCH] [--skip-console]' \
    'Builds for the host only (go env GOOS/GOARCH); --target must equal the host (no cross-compilation, ADR 0016).'
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      version=${2:-}
      shift 2
      ;;
    --revision)
      revision=${2:-}
      shift 2
      ;;
    --output)
      output=${2:-}
      shift 2
      ;;
    --target)
      targets+=("${2:-}")
      shift 2
      ;;
    --skip-console)
      skip_console=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'Unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
  printf 'Version must be SemVer without a v prefix: %s\n' "$version" >&2
  exit 2
fi
if [[ ! "$revision" =~ ^[0-9a-f]{7,64}$ ]]; then
  printf 'Revision must be a lowercase hexadecimal Git SHA: %s\n' "$revision" >&2
  exit 2
fi
if [[ -z "$output" ]]; then
  printf '%s\n' 'Output directory is required.' >&2
  exit 2
fi
if [[ -e "$output" && -n "$(find "$output" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]]; then
  printf 'Output directory must be empty: %s\n' "$output" >&2
  exit 2
fi
mkdir -p "$output"
output=$(cd "$output" && pwd)
cd "$root"

normalize_stage() {
  find "$1" -exec touch -h -t 200001010000.00 {} +
}

create_tar_gz() {
  local source=$1
  local destination=$2
  normalize_stage "$source"
  if tar --version 2>/dev/null | grep -Fq 'GNU tar'; then
    (
      cd "$source"
      find . -print | LC_ALL=C sort | tar \
        --no-recursion \
        --format ustar \
        --owner 0 \
        --group 0 \
        --numeric-owner \
        -cf - \
        -T - | gzip -n > "$destination"
    )
    return
  fi
  (
    cd "$source"
    # BSD tar's --format ustar leaves size/mtime terminated with spaces;
    # Go reports those headers as unknown. Write canonical USTAR here so the
    # same strict distribution acceptance applies on non-GNU build hosts.
    python3 - <<'PY' | gzip -n > "$destination"
import sys, tarfile
from pathlib import Path

def canonical(info):
    info.uid = info.gid = 0
    info.uname = info.gname = 'root'
    return info

with tarfile.open(fileobj=sys.stdout.buffer, mode='w|', format=tarfile.USTAR_FORMAT) as archive:
    for name in ['.'] + sorted('./' + str(p) for p in Path('.').rglob('*')):
        archive.add(name, arcname=name, recursive=False, filter=canonical)
PY
  )
}

host_goos=$(go env GOOS)
host_goarch=$(go env GOARCH)
host_target="$host_goos/$host_goarch"
if [[ ! "$host_target" =~ ^(linux|darwin)/(amd64|arm64)$ ]]; then
  printf 'Unsupported release host: %s\n' "$host_target" >&2
  exit 2
fi
# Releases are built natively on each runner: a target other than the host would
# be a cross build, which ADR 0016 forbids, so it is rejected rather than built.
if [[ ${#targets[@]} -eq 0 ]]; then
  targets=("$host_target")
fi
for target in "${targets[@]}"; do
  if [[ "$target" != "$host_target" ]]; then
    printf 'Foreign release target %s rejected: this host builds %s only (no cross-compilation).\n' "$target" "$host_target" >&2
    exit 2
  fi
done

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
ldflags="-s -w -X github.com/mlops-ca/notarx/internal/buildinfo.injectedVersion=$version -X github.com/mlops-ca/notarx/internal/buildinfo.injectedRevision=$revision"

for target in "${targets[@]}"; do
  goos=$host_goos
  goarch=$host_goarch
  stage="$workdir/notarx_${version}_${goos}_${goarch}"
  mkdir -p "$stage"
  CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "$ldflags" -o "$stage/notarx" ./cmd/notarx
  CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "$ldflags" -o "$stage/gateway-healthcheck" ./cmd/healthcheck
  cp -R "$root/migrations" "$stage/migrations"
  cp "$root/scripts/run-migrations.sh" "$stage/run-migrations.sh"
  cp "$root/.env.example" "$stage/artifact-gateway.env.example"
  chmod +x "$stage/run-migrations.sh"
  printf 'version=%s\nrevision=%s\ntarget=%s/%s\n' "$version" "$revision" "$goos" "$goarch" > "$stage/VERSION.txt"
  printf '%s\n' \
    'notarx installation materials' \
    '' \
    '1. Verify SHA256SUMS before extracting this archive.' \
    '2. Configure PostgreSQL and S3-compatible object storage using artifact-gateway.env.example.' \
    '3. Install PostgreSQL psql, export PGHOST/PGUSER/PGDATABASE/PGPASSWORD, then run:' \
    '   MIGRATION_DIR=./migrations ./run-migrations.sh' \
    '4. Export the required NOTARX_* variables and start ./notarx.' \
    '' \
    "Documentation: https://github.com/mlops-ca/notarx/blob/$revision/docs/getting-started.md" \
    > "$stage/INSTALL.txt"

  archive="$output/notarx_${version}_${goos}_${goarch}.tar.gz"
  create_tar_gz "$stage" "$archive"

  # Backup's native profile supports Linux. Verify the actual packaged bytes
  # statically on the build host; a self-built non-trimpath fixture or runtime
  # `version` output must not substitute for this distribution acceptance gate.
  if [[ "$goos" == "linux" ]]; then
    BACKUPOPS_BUILT_RELEASE_DIR="$stage" \
      BACKUPOPS_BUILT_ARCHIVE="$archive" \
      BACKUPOPS_BUILT_VERSION="$version" \
      BACKUPOPS_BUILT_REVISION="$revision" \
      BACKUPOPS_BUILT_PLATFORM="$target" \
      go test -count=1 ./internal/backupops -run '^TestBuiltNativeArchiveIdentity$'
  fi

  # The CLI client ships as its own archive: notarxctl is the client for CI,
  # platform management, and agents, so it must not be mixed into the server
  # archives whose INSTALL.txt and future native backup profile cover server
  # materials only. The version and revision are injected with the same
  # ldflags, and write-release-checksums.sh covers the new archives unchanged.
  notarxctl_stage="$workdir/notarxctl_${version}_${goos}_${goarch}"
  mkdir -p "$notarxctl_stage"
  CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "$ldflags" -o "$notarxctl_stage/notarxctl" ./cmd/notarxctl
  printf 'version=%s\nrevision=%s\ntarget=%s/%s\n' "$version" "$revision" "$goos" "$goarch" > "$notarxctl_stage/VERSION.txt"
  printf '%s\n' \
    'notarx CLI installation materials' \
    '' \
    'This archive carries only the notarxctl client; server materials live in the' \
    'notarx_ archive of the same release.' \
    '' \
    '1. Verify SHA256SUMS before extracting this archive.' \
    '2. Run ./notarxctl version to confirm the client version and revision.' \
    '3. Configure a profile in ~/.config/notarxctl/config.yaml (mode 0600) or export NOTARXCTL_SERVER and NOTARXCTL_TOKEN.' \
    '4. Point the client at a deployment, for example ./notarxctl info --server https://gateway.example.com' \
    '' \
    "Documentation: https://github.com/mlops-ca/notarx/blob/$revision/docs/cli.md" \
    > "$notarxctl_stage/INSTALL.txt"

  create_tar_gz "$notarxctl_stage" "$output/notarxctl_${version}_${goos}_${goarch}.tar.gz"
done

if [[ "$skip_console" -eq 0 ]]; then
  npm --prefix "$root/console" ci --ignore-scripts --no-audit --no-fund
  npm --prefix "$root/console" run build
  console_stage="$workdir/artifact-gateway-console_${version}_web"
  mkdir -p "$console_stage"
  cp -R "$root/console/dist/." "$console_stage/"
  printf 'version=%s\nrevision=%s\n' "$version" "$revision" > "$console_stage/VERSION.txt"
  create_tar_gz "$console_stage" "$output/artifact-gateway-console_${version}_web.tar.gz"

  openapi_stage="$workdir/artifact-gateway-openapi_${version}"
  mkdir -p "$openapi_stage"
  cp "$root/api/openapi/native-hosted-v1.json" "$openapi_stage/"
  cp "$root/api/openapi/management-runtime-v1.json" "$openapi_stage/"
  printf 'version=%s\nrevision=%s\n' "$version" "$revision" > "$openapi_stage/VERSION.txt"
  create_tar_gz "$openapi_stage" "$output/artifact-gateway-openapi_${version}.tar.gz"
fi

bash "$root/scripts/write-release-checksums.sh" "$output"

printf 'Built notarx %s release artifacts in %s.\n' "$version" "$output"
