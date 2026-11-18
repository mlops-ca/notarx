#!/usr/bin/env bash
# Renders deploy/oci-free-tier/compose.yml with docker compose and runs the
# static checks (scripts/oci-free-tier-check.py) on the rendered model and on
# the files the release bundle renderer consumes. ADR 0017 slice 6.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"
command -v docker >/dev/null || { echo 'oci-free-tier config check requires docker compose' >&2; exit 1; }

environment_file=$(mktemp)
trap 'rm -f "$environment_file"' EXIT
cat >"$environment_file" <<'ENV'
NOTARX_DOMAIN=notarx.example.invalid
NOTARX_ACME_EMAIL=ops@example.invalid
NOTARX_POSTGRES_PASSWORD=placeholder-postgres
NOTARX_ADMIN_TOKEN=placeholder-admin
NOTARX_RESOLVER_TOKEN=placeholder-resolver
ENV

docker compose --env-file "$environment_file" -f deploy/oci-free-tier/compose.yml config --quiet
docker compose --env-file "$environment_file" -f deploy/oci-free-tier/compose.yml config --format json |
  python3 scripts/oci-free-tier-check.py --directory deploy/oci-free-tier
