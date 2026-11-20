#!/usr/bin/env bash
# Smoke test of the OCI free-tier reference stack (ADR 0017 slice 6): starts
# deploy/oci-free-tier/compose.yml, pushes and pulls an OCI image and a Raw file
# through Caddy (HTTPS, Caddy's local CA for "localhost"), then removes the stack.
#
# Environment:
#   NOTARX_IMAGE, NOTARX_CONSOLE_IMAGE  images to run (default: the compose defaults)
#   NOTARX_SMOKE_KEEP=1                 leave the stack running for inspection
#   NOTARX_SMOKE_URL                    test an already running stack (no start/stop);
#                                       needs NOTARX_ADMIN_TOKEN, add NOTARX_SMOKE_INSECURE=1
#                                       for a self-signed certificate
# Needs docker compose, curl, python3, openssl and sha256sum (or shasum).
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"
for binary in curl python3 openssl; do command -v "$binary" >/dev/null || { printf 'The smoke test requires %s\n' "$binary" >&2; exit 1; }; done
sha256() { if command -v sha256sum >/dev/null; then sha256sum | awk '{print $1}'; else shasum -a 256 | awk '{print $1}'; fi; }

workdir=$(mktemp -d)
project="notarx-smoke-$RANDOM"
compose_up=0
cleanup() {
  local status=$?
  if [[ "$compose_up" == 1 ]]; then
    if [[ "$status" != 0 ]]; then
      docker compose --project-name "$project" --env-file "$workdir/.env" -f deploy/oci-free-tier/compose.yml logs --tail=100 >&2 || true
    fi
    if [[ "${NOTARX_SMOKE_KEEP:-0}" != 1 ]]; then
      docker compose --project-name "$project" --env-file "$workdir/.env" -f deploy/oci-free-tier/compose.yml down --volumes --remove-orphans >/dev/null 2>&1 || true
      # The object root is owned by the image's nonroot user.
      docker run --rm -v "$workdir/data:/data" --entrypoint sh "${NOTARX_POSTGRES_IMAGE:-postgres:18-alpine}" -c 'rm -rf /data/* /data/.[!.]*' >/dev/null 2>&1 || true
    fi
  fi
  [[ "${NOTARX_SMOKE_KEEP:-0}" == 1 && "$compose_up" == 1 ]] || rm -rf "$workdir"
  exit "$status"
}
trap cleanup EXIT

if [[ -z "${NOTARX_SMOKE_URL:-}" ]]; then
  command -v docker >/dev/null || { echo 'The smoke test requires docker compose' >&2; exit 1; }
  admin_token=$(openssl rand -hex 24)
  mkdir -p "$workdir/data/postgres" "$workdir/data/objects"
  chmod 0700 "$workdir/data/objects"
  # The notarx image runs as nonroot (65532) and keeps the object root private.
  docker run --rm -v "$workdir/data:/data" --entrypoint sh "${NOTARX_POSTGRES_IMAGE:-postgres:18-alpine}" -c 'chown 65532:65532 /data/objects'
  cat >"$workdir/.env" <<ENV
NOTARX_DOMAIN=localhost
NOTARX_ACME_EMAIL=smoke@example.invalid
NOTARX_POSTGRES_PASSWORD=$(openssl rand -hex 16)
NOTARX_ADMIN_TOKEN=$admin_token
NOTARX_RESOLVER_TOKEN=$(openssl rand -hex 16)
NOTARX_DATA_DIR=$workdir/data
NOTARX_MIGRATIONS_DIR=$repo_root/migrations/postgres
NOTARX_MIGRATION_RUNNER=$repo_root/scripts/run-migrations.sh
ENV
  [[ -z "${NOTARX_IMAGE:-}" ]] || echo "NOTARX_IMAGE=$NOTARX_IMAGE" >>"$workdir/.env"
  [[ -z "${NOTARX_CONSOLE_IMAGE:-}" ]] || echo "NOTARX_CONSOLE_IMAGE=$NOTARX_CONSOLE_IMAGE" >>"$workdir/.env"
  compose_up=1
  docker compose --project-name "$project" --env-file "$workdir/.env" -f deploy/oci-free-tier/compose.yml up -d --wait --wait-timeout 300
  base_url=https://localhost
  insecure=1
else
  admin_token=${NOTARX_ADMIN_TOKEN:?set NOTARX_ADMIN_TOKEN for a running stack}
  base_url=${NOTARX_SMOKE_URL%/}
  insecure=${NOTARX_SMOKE_INSECURE:-0}
fi

curl_args=(--silent --show-error)
[[ "$insecure" != 1 ]] || curl_args+=(--insecure)
auth=(-H "Authorization: Bearer $admin_token")
request() { curl "${curl_args[@]}" "${auth[@]}" "$@"; }
status_of() { curl "${curl_args[@]}" "${auth[@]}" --output "$workdir/response" --dump-header "$workdir/headers" --write-out '%{http_code}' "$@"; }
expect() { local want=$1 got; shift; got=$(status_of "$@"); [[ "$got" == "$want" ]] || { printf 'Expected HTTP %s, got %s: %s\n' "$want" "$got" "$*" >&2; cat "$workdir/response" >&2 || true; exit 1; }; }

# Caddy may need a moment to obtain its certificate.
for _ in $(seq 1 60); do
  [[ $(curl "${curl_args[@]}" --output /dev/null --write-out '%{http_code}' "$base_url/readyz" 2>/dev/null || true) == 204 ]] && break
  sleep 2
done
expect 204 "$base_url/readyz"
[[ $(curl "${curl_args[@]}" --output /dev/null --write-out '%{http_code}' "$base_url/") == 200 ]] || { echo 'The Console is not served through Caddy' >&2; exit 1; }
# Operational endpoints are not public.
[[ $(curl "${curl_args[@]}" --output /dev/null --write-out '%{http_code}' "$base_url/metrics") == 404 ]] || { echo '/metrics is reachable through Caddy' >&2; exit 1; }

run_id=$RANDOM
create_repository() {
  expect 201 -H 'Content-Type: application/json' -H "Idempotency-Key: smoke-$1-$run_id" \
    --data "$(printf '{"name":"%s","format":"%s"}' "$2" "$1")" "$base_url/api/v2/repositories"
}

# Raw file through Caddy.
raw_repository="smoke-raw-$run_id"
create_repository raw "$raw_repository"
raw_body="oci free tier raw smoke $run_id"
expect 201 -X PUT -H 'Content-Type: application/octet-stream' --data-binary "$raw_body" "$base_url/raw/$raw_repository/smoke/file.txt"
expect 200 "$base_url/raw/$raw_repository/smoke/file.txt"
[[ $(<"$workdir/response") == "$raw_body" ]] || { echo 'Raw file pulled through Caddy differs from the pushed one' >&2; exit 1; }

# OCI image through Caddy: config blob and manifest, then pull both back.
oci_repository="smoke-oci-$run_id"
create_repository oci "$oci_repository"
config_body="{\"architecture\":\"arm64\",\"os\":\"linux\",\"rootfs\":{\"type\":\"layers\",\"diff_ids\":[]},\"run\":\"$run_id\"}"
config_digest="sha256:$(printf '%s' "$config_body" | sha256)"
expect 202 -X POST "$base_url/v2/$oci_repository/team/widget/blobs/uploads/"
location=$(tr -d '\r' <"$workdir/headers" | awk 'tolower($1)=="location:" {print $2}')
[[ -n "$location" ]] || { echo 'The blob upload returned no Location header' >&2; exit 1; }
[[ "$location" == http* ]] && location=/${location#*://*/}
expect 201 -X PUT --data-binary "$config_body" "$base_url$location?digest=$config_digest"
manifest="{\"schemaVersion\":2,\"mediaType\":\"application/vnd.oci.image.manifest.v1+json\",\"config\":{\"mediaType\":\"application/vnd.oci.image.config.v1+json\",\"digest\":\"$config_digest\",\"size\":${#config_body}},\"layers\":[]}"
expect 201 -H 'Content-Type: application/vnd.oci.image.manifest.v1+json' -X PUT --data-binary "$manifest" "$base_url/v2/$oci_repository/team/widget/manifests/smoke"
manifest_digest=$(tr -d '\r' <"$workdir/headers" | awk 'tolower($1)=="docker-content-digest:" {print $2}')
[[ "$manifest_digest" == "sha256:$(printf '%s' "$manifest" | sha256)" ]] || { echo 'The manifest digest does not match the pushed manifest' >&2; exit 1; }
expect 200 -H 'Accept: application/vnd.oci.image.manifest.v1+json' "$base_url/v2/$oci_repository/team/widget/manifests/smoke"
[[ $(<"$workdir/response") == "$manifest" ]] || { echo 'The manifest pulled through Caddy differs from the pushed one' >&2; exit 1; }
expect 200 "$base_url/v2/$oci_repository/team/widget/blobs/$config_digest"
[[ $(<"$workdir/response") == "$config_body" ]] || { echo 'The config blob pulled through Caddy differs from the pushed one' >&2; exit 1; }

printf 'OCI free-tier smoke passed through %s: Raw file and OCI image pushed and pulled.\n' "$base_url"
