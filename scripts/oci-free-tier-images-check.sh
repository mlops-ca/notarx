#!/usr/bin/env bash
# Every third-party image the reference deployments run must publish a
# linux/arm64 manifest (ADR 0017 slice 6): the Oracle Always Free shape is Ampere
# A1. Inspects the manifest list of each image in compose.yml (including the
# pinned RustFS digest) and deploy/oci-free-tier/compose.yml with
# docker buildx imagetools inspect. Images of this project (ghcr.io/mlops-ca) are
# built per architecture and merged by the release pipeline (ADR 0016) and are
# gated there; set NOTARX_CHECK_OWN_IMAGES=1 to inspect them here too.
#
# Needs docker with buildx and network access to the registries.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"
command -v docker >/dev/null || { echo 'oci-free-tier images check requires docker' >&2; exit 1; }

placeholder_environment=$(mktemp)
trap 'rm -f "$placeholder_environment"' EXIT
cat >"$placeholder_environment" <<'ENV'
NOTARX_DOMAIN=notarx.example.invalid
NOTARX_ACME_EMAIL=ops@example.invalid
NOTARX_POSTGRES_PASSWORD=placeholder-postgres
NOTARX_ADMIN_TOKEN=placeholder-admin
NOTARX_RESOLVER_TOKEN=placeholder-resolver
RUSTFS_ACCESS_KEY=placeholder
RUSTFS_SECRET_KEY=placeholder-secret
RUSTFS_RPC_SECRET=placeholder-rpc
ENV

images=$(
  for file in compose.yml deploy/oci-free-tier/compose.yml; do
    docker compose --env-file "$placeholder_environment" -f "$file" --profile '*' config --images
  done | sort -u
)
[[ -n "$images" ]] || { echo 'no images found in the compose files' >&2; exit 1; }

status=0
while IFS= read -r image; do
  [[ -n "$image" ]] || continue
  case "$image" in
    ghcr.io/mlops-ca/*)
      [[ "${NOTARX_CHECK_OWN_IMAGES:-0}" == 1 ]] || { printf 'skip  %s (project image, gated by the release pipeline)\n' "$image"; continue; } ;;
    *@sha256:*) ;;
    *:local|*:latest) printf 'skip  %s (built locally)\n' "$image"; continue ;;
  esac
  if ! manifest=$(docker buildx imagetools inspect --raw "$image" 2>/dev/null); then
    printf 'FAIL  %s: manifest could not be read\n' "$image" >&2; status=1; continue
  fi
  if python3 -c '
import json, sys
index = json.load(sys.stdin)
platforms = {(m.get("platform", {}).get("os"), m.get("platform", {}).get("architecture")) for m in index.get("manifests", [])}
sys.exit(0 if ("linux", "arm64") in platforms else 1)
' <<<"$manifest"; then
    printf 'ok    %s has linux/arm64\n' "$image"
  else
    printf 'FAIL  %s has no linux/arm64 manifest; re-pin to a multi-arch release\n' "$image" >&2; status=1
  fi
done <<<"$images"
exit "$status"
