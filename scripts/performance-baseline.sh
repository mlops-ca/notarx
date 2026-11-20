#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

# --load PROFILE runs the mixed load profile of ADR 0023 section 11 against a stack seeded
# with that datagen profile and writes docs/perf/PROFILE.json (make perf-load) instead of
# the artifact-size and single-endpoint baseline.
load_profile=""
case "${1:-}" in
  "") ;;
  --load)
    load_profile=${2:-}
    case "$load_profile" in
      tiny | freetier | target) ;;
      *)
        printf '%s\n' 'Usage: performance-baseline.sh [--load tiny|freetier|target]' >&2
        exit 2
        ;;
    esac
    ;;
  *)
    printf '%s\n' 'Usage: performance-baseline.sh [--load tiny|freetier|target]' >&2
    exit 2
    ;;
esac

required_binaries=(ab awk cmp curl dd docker go gzip jq openssl python3 rg sort wc)
if [[ -n $load_profile ]]; then
  required_binaries=(awk curl docker go jq openssl python3 sort wc)
fi
for binary in "${required_binaries[@]}"; do
  if ! command -v "$binary" >/dev/null 2>&1; then
    printf 'Performance baseline requires %s.\n' "$binary" >&2
    exit 1
  fi
done

if ! docker compose version >/dev/null 2>&1; then
  printf '%s\n' 'Performance baseline requires Docker Compose v2.' >&2
  exit 1
fi

read -r -a generated_ports <<<"$(python3 - <<'PY'
import socket

sockets = []
try:
    for _ in range(6):
        sock = socket.socket()
        sock.bind(("127.0.0.1", 0))
        sockets.append(sock)
    print(" ".join(str(sock.getsockname()[1]) for sock in sockets))
finally:
    for sock in sockets:
        sock.close()
PY
)"
if [[ ${#generated_ports[@]} != 6 ]]; then
  printf '%s\n' 'Could not allocate six loopback ports for the performance stack.' >&2
  exit 1
fi

export NOTARX_HTTP_PORT=${NOTARX_PERF_NOTARX_PORT:-${generated_ports[0]}}
export NOTARX_POSTGRES_PORT=${NOTARX_PERF_POSTGRES_PORT:-${generated_ports[1]}}
export RUSTFS_API_PORT=${NOTARX_PERF_RUSTFS_PORT:-${generated_ports[2]}}
export RUSTFS_CONSOLE_PORT=${NOTARX_PERF_RUSTFS_CONSOLE_PORT:-${generated_ports[3]}}
raw_upstream_port=${NOTARX_PERF_RAW_UPSTREAM_PORT:-${generated_ports[4]}}
raw_proxy_port=${NOTARX_PERF_RAW_PROXY_PORT:-${generated_ports[5]}}

ports=("$NOTARX_HTTP_PORT" "$NOTARX_POSTGRES_PORT" "$RUSTFS_API_PORT" "$RUSTFS_CONSOLE_PORT" "$raw_upstream_port" "$raw_proxy_port")
if [[ $(printf '%s\n' "${ports[@]}" | sort -u | wc -l | tr -d ' ') != 6 ]]; then
  printf '%s\n' 'Performance baseline ports must be distinct.' >&2
  exit 1
fi

project=${NOTARX_PERF_PROJECT:-notarx-perf-$(date -u +%Y%m%d%H%M%S)-$$}
# The baseline inspects the image Compose builds for this project (in CI lanes, the
# loaded prebuilt image under this project's name; see below).
loaded_image=${NOTARX_IMAGE:-notarx:local}
export NOTARX_IMAGE="${project}-notarx"
image_tagged=0
image_source="compose build of the working tree"
if [[ ! "$project" =~ ^notarx-perf-[a-z0-9-]+$ ]]; then
  printf 'NOTARX_PERF_PROJECT must start with notarx-perf-: %s\n' "$project" >&2
  exit 1
fi

if [[ -n ${NOTARX_PERF_OUTPUT_DIR:-} ]]; then
  output_dir=$NOTARX_PERF_OUTPUT_DIR
  mkdir -p "$output_dir"
else
  output_dir=$(mktemp -d "${TMPDIR:-/tmp}/notarx-performance.XXXXXX")
fi
output_dir=$(cd "$output_dir" && pwd)
private_dir="$output_dir/.private"
mkdir -p "$private_dir"
chmod 700 "$private_dir"
env_file="$private_dir/performance.env"
compose_override="$private_dir/performance.compose.yml"
upstream_dir="$private_dir/raw-upstream"
upstream_cert="$private_dir/raw-upstream.crt"
upstream_key="$private_dir/raw-upstream.key"
stack_started=0
upstream_pid=""

compose=(docker compose -p "$project" --env-file "$env_file" -f compose.yml -f "$compose_override")

cleanup() {
  local exit_code=$?
  set +e
  if [[ $exit_code != 0 ]]; then
    if [[ $stack_started == 1 ]]; then
      "${compose[@]}" logs --no-color notarx >"$output_dir/notarx-failure.log" 2>&1 || true
    fi
    if [[ -f "$private_dir/raw-upstream.log" ]]; then
      cp "$private_dir/raw-upstream.log" "$output_dir/raw-upstream-failure.log"
    fi
  fi
  if [[ $stack_started == 1 ]]; then
    "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1
  fi
  # A leftover tag has a unique per-project name, so it cannot disturb later runs.
  if [[ $image_tagged == 1 ]] && ! docker image rm "$NOTARX_IMAGE" >/dev/null 2>&1; then
    printf 'Performance baseline could not remove its image tag %s.\n' "$NOTARX_IMAGE" >&2
  fi
  if [[ -n "$upstream_pid" ]]; then
    kill "$upstream_pid" >/dev/null 2>&1 || true
    wait "$upstream_pid" 2>/dev/null || true
  fi
  case "$private_dir" in
    "$output_dir"/.private) rm -rf "$private_dir" ;;
  esac
  trap - EXIT
  if [[ $exit_code == 0 ]]; then
    printf 'Performance baseline results: %s\n' "$output_dir"
  else
    printf 'Performance baseline failed; partial results: %s\n' "$output_dir" >&2
  fi
  exit "$exit_code"
}
trap cleanup EXIT

random_hex() {
  openssl rand -hex "$1"
}

admin_token="perf-admin-$(random_hex 24)"
postgres_password=$(random_hex 24)
umask 077
{
  printf 'NOTARX_POSTGRES_PASSWORD=%s\n' "$postgres_password"
  printf 'NOTARX_ADMIN_TOKEN=%s\n' "$admin_token"
  printf 'NOTARX_RESOLVER_TOKEN=%s\n' "perf-resolver-$(random_hex 24)"
  printf 'RUSTFS_ACCESS_KEY=%s\n' "perfaccess$(random_hex 8)"
  printf 'RUSTFS_SECRET_KEY=%s\n' "$(random_hex 24)"
  printf 'RUSTFS_RPC_SECRET=%s\n' "$(random_hex 24)"
  printf 'NOTARX_RAW_PROXY_ALLOWED_HOSTS=host.docker.internal\n'
  printf 'NOTARX_EGRESS_PROXY=http://host.docker.internal:%s\n' "$raw_proxy_port"
  printf 'NOTARX_PERF_CA_FILE=%s\n' "$upstream_cert"
} >"$env_file"

mkdir -p "$upstream_dir"
openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 1 \
  -subj '/CN=host.docker.internal' \
  -addext 'subjectAltName=DNS:host.docker.internal' \
  -addext 'basicConstraints=critical,CA:TRUE' \
  -keyout "$upstream_key" -out "$upstream_cert" >/dev/null 2>&1
chmod 644 "$upstream_cert"
# Compose expands this variable from the generated environment file.
# shellcheck disable=SC2016
printf '%s\n' \
  'services:' \
  '  notarx:' \
  '    environment:' \
  '      NO_PROXY: postgres,rustfs,127.0.0.1,localhost' \
  '      SSL_CERT_FILE: /tmp/notarx-performance-ca.crt' \
  '    volumes:' \
  '      - "${NOTARX_PERF_CA_FILE}:/tmp/notarx-performance-ca.crt:ro"' >"$compose_override"
printf '%s\n' 'ok' >"$upstream_dir/healthz"
python3 - "$raw_upstream_port" "$raw_proxy_port" "$upstream_dir" "$upstream_cert" "$upstream_key" >"$private_dir/raw-upstream.log" 2>&1 <<'PY' &
import functools
import http.server
import select
import socket
import socketserver
import ssl
import sys
import threading

upstream_port, proxy_port, directory, certificate, key = sys.argv[1:]
handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=directory)
server = http.server.ThreadingHTTPServer(("0.0.0.0", int(upstream_port)), handler)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain(certificate, key)
server.socket = context.wrap_socket(server.socket, server_side=True)

class ConnectProxy(socketserver.StreamRequestHandler):
    def handle(self):
        request = self.rfile.readline().decode("ascii", "replace").strip().split()
        while self.rfile.readline().strip():
            pass
        expected = f"host.docker.internal:{upstream_port}"
        if len(request) < 2 or request[0] != "CONNECT" or request[1] != expected:
            self.wfile.write(b"HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
            return
        with socket.create_connection(("127.0.0.1", int(upstream_port))) as remote:
            self.wfile.write(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            peers = {self.connection: remote, remote: self.connection}
            while True:
                readable, _, _ = select.select(list(peers), [], [], 30)
                if not readable:
                    return
                for source in readable:
                    data = source.recv(65536)
                    if not data:
                        return
                    peers[source].sendall(data)

class ThreadingProxy(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True

threading.Thread(target=server.serve_forever, daemon=True).start()
with ThreadingProxy(("0.0.0.0", int(proxy_port)), ConnectProxy) as proxy:
    proxy.serve_forever()
PY
upstream_pid=$!
for ((attempt = 1; attempt <= 50; attempt++)); do
  if curl --noproxy '*' --silent --show-error --fail --cacert "$upstream_cert" \
    --resolve "host.docker.internal:${raw_upstream_port}:127.0.0.1" \
    "https://host.docker.internal:${raw_upstream_port}/healthz" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$upstream_pid" 2>/dev/null; then
    cat "$private_dir/raw-upstream.log" >&2
    exit 1
  fi
  sleep 0.1
done
curl --noproxy '*' --silent --show-error --fail --cacert "$upstream_cert" \
  --resolve "host.docker.internal:${raw_upstream_port}:127.0.0.1" \
  "https://host.docker.internal:${raw_upstream_port}/healthz" >/dev/null

metadata_low_requests=${NOTARX_PERF_METADATA_LOW_REQUESTS:-5000}
metadata_mid_requests=${NOTARX_PERF_METADATA_MID_REQUESTS:-30000}
metadata_high_requests=${NOTARX_PERF_METADATA_HIGH_REQUESTS:-300000}
raw_low_requests=${NOTARX_PERF_RAW_LOW_REQUESTS:-2000}
raw_mid_requests=${NOTARX_PERF_RAW_MID_REQUESTS:-20000}
raw_high_requests=${NOTARX_PERF_RAW_HIGH_REQUESTS:-40000}
raw_large_requests=${NOTARX_PERF_RAW_LARGE_REQUESTS:-32}
raw_proxy_cold_requests=${NOTARX_PERF_RAW_PROXY_COLD_REQUESTS:-8}
raw_large_mib=${NOTARX_PERF_RAW_LARGE_MIB:-64}
low_concurrency=${NOTARX_PERF_LOW_CONCURRENCY:-1}
mid_concurrency=${NOTARX_PERF_MID_CONCURRENCY:-32}
high_concurrency=${NOTARX_PERF_HIGH_CONCURRENCY:-128}
large_concurrency=${NOTARX_PERF_LARGE_CONCURRENCY:-8}
raw_proxy_cold_concurrency=${NOTARX_PERF_RAW_PROXY_COLD_CONCURRENCY:-8}
idle_samples=${NOTARX_PERF_IDLE_SAMPLES:-10}
sample_interval=${NOTARX_PERF_SAMPLE_INTERVAL_SECONDS:-1}

for count in \
  "$metadata_low_requests" "$metadata_mid_requests" "$metadata_high_requests" \
  "$raw_low_requests" "$raw_mid_requests" "$raw_high_requests" \
  "$raw_large_requests" "$raw_proxy_cold_requests" "$raw_large_mib" \
  "$low_concurrency" "$mid_concurrency" "$high_concurrency" "$large_concurrency" "$raw_proxy_cold_concurrency" "$idle_samples"; do
  if [[ ! "$count" =~ ^[1-9][0-9]*$ ]]; then
    printf 'Performance counts must be positive integers: %s\n' "$count" >&2
    exit 1
  fi
done
if (( metadata_low_requests < low_concurrency || metadata_mid_requests < mid_concurrency || metadata_high_requests < high_concurrency || raw_low_requests < low_concurrency || raw_mid_requests < mid_concurrency || raw_high_requests < high_concurrency || raw_large_requests < large_concurrency || raw_proxy_cold_requests < raw_proxy_cold_concurrency )); then
  printf '%s\n' 'Each request count must be greater than or equal to its concurrency.' >&2
  exit 1
fi

bytes_to_mib() {
  awk -v bytes="$1" 'BEGIN { printf "%.2f", bytes / 1048576 }'
}

memory_to_mib() {
  awk -v raw="$1" 'BEGIN {
    value = raw + 0
    if (raw ~ /GiB$/) value *= 1024
    else if (raw ~ /KiB$/) value /= 1024
    else if (raw ~ /GB$/) value *= 953.674316
    else if (raw ~ /MB$/) value *= 0.953674316
    else if (raw ~ /kB$/) value *= 0.000953674316
    else if (raw ~ /B$/ && raw !~ /iB$/) value /= 1048576
    printf "%.2f", value
  }'
}

expect_http() {
  local expected=$1 method=$2 url=$3 output=$4
  shift 4
  local http_code method_args=(--request "$method")
  if [[ "$method" == HEAD ]]; then
    method_args=(--head)
  fi
  http_code=$(curl --noproxy '*' --silent --show-error --output "$output" --write-out '%{http_code}' \
    "${method_args[@]}" "$@" "$url")
  if [[ "$http_code" != "$expected" ]]; then
    printf '%s %s returned HTTP %s; expected %s.\n' "$method" "$url" "$http_code" "$expected" >&2
    sed -n '1,20p' "$output" >&2
    exit 1
  fi
}

if [[ "${NOTARX_COMPOSE_BUILD_FLAG---build}" == --no-build ]]; then
  # CI lanes load the prebuilt image (docs/ci.md, Shared images): give it this
  # project's name instead of rebuilding it from the same Dockerfile. The cleanup
  # removes only that tag; the loaded image keeps its own.
  if ! docker image inspect "$loaded_image" >/dev/null 2>&1; then
    printf 'NOTARX_COMPOSE_BUILD_FLAG=--no-build needs the prebuilt image %s; load it first (docs/ci.md, Shared images).\n' "$loaded_image" >&2
    exit 1
  fi
  printf '%s\n' 'Starting the isolated Gateway/PostgreSQL/RustFS core stack from the loaded image...'
  docker image tag "$loaded_image" "$NOTARX_IMAGE"
  image_tagged=1
  image_source="prebuilt $loaded_image $(docker image inspect --format '{{.Id}}' "$loaded_image")"
  stack_started=1
  "${compose[@]}" up -d --no-build --wait --remove-orphans
else
  printf '%s\n' 'Building and starting the isolated Gateway/PostgreSQL/RustFS core stack...'
  stack_started=1
  "${compose[@]}" up -d --build --wait --remove-orphans
fi

notarx_id=$("${compose[@]}" ps -q notarx)
postgres_id=$("${compose[@]}" ps -q postgres)
rustfs_id=$("${compose[@]}" ps -q rustfs)
for container_id in "$notarx_id" "$postgres_id" "$rustfs_id"; do
  if [[ -z "$container_id" ]]; then
    printf '%s\n' 'Core stack did not expose all expected containers.' >&2
    exit 1
  fi
done

notarx_url="http://127.0.0.1:${NOTARX_HTTP_PORT}"
expect_http 204 GET "$notarx_url/livez" "$private_dir/livez.out"
expect_http 204 GET "$notarx_url/readyz" "$private_dir/readyz.out"

# run_load_profile seeds the stack's database, drives tools/perfload against the gateway
# while sampling the CPU of the postgres container, and writes docs/perf/<profile>.json:
# the perfload report plus the database CPU as a share of the host's CPUs.
run_load_profile() {
  local database_url="postgres://notarx:${postgres_password}@127.0.0.1:${NOTARX_POSTGRES_PORT}/notarx?sslmode=disable"
  local report="$private_dir/perf-load.json" cpu_samples="$private_dir/postgres-cpu.txt"
  local host_cpus sampler_pid publish_repository=perf-load-raw

  printf 'Seeding the %s profile...\n' "$load_profile"
  go run ./tools/datagen/cmd -profile "$load_profile" -database-url "$database_url"
  expect_http 201 POST "$notarx_url/api/v2/repositories" "$private_dir/load-repository.json" \
    -H "Authorization: Bearer $admin_token" \
    -H 'Content-Type: application/json' \
    -H 'Idempotency-Key: perf-load-raw-repository' \
    --data "{\"name\":\"$publish_repository\",\"format\":\"raw\",\"type\":\"hosted\"}"

  : >"$cpu_samples"
  (
    while true; do
      docker stats --no-stream --format '{{.CPUPerc}}' "$postgres_id" >>"$cpu_samples" 2>/dev/null || true
      sleep 1
    done
  ) &
  sampler_pid=$!
  printf 'Running the mixed load profile (%s)...\n' "$load_profile"
  if ! NOTARX_ADMIN_TOKEN="$admin_token" go run ./tools/perfload/cmd -profile "$load_profile" \
    -url "$notarx_url" -database-url "$database_url" -publish-repository "$publish_repository" -output "$report"; then
    kill "$sampler_pid" 2>/dev/null || true
    return 1
  fi
  kill "$sampler_pid" 2>/dev/null || true
  wait "$sampler_pid" 2>/dev/null || true

  host_cpus=$(docker info --format '{{.NCPU}}')
  mkdir -p "$repo_root/docs/perf"
  tr -d '% ' <"$cpu_samples" | awk -v cpus="$host_cpus" '
    $1 ~ /^[0-9.]+$/ { n++; sum += $1; if ($1 > max) max = $1 }
    END { printf "%d %.2f %.2f\n", n, (n ? sum / n / cpus : 0), max / cpus }' >"$private_dir/postgres-cpu-summary.txt"
  read -r cpu_count cpu_avg cpu_max <"$private_dir/postgres-cpu-summary.txt"
  jq --argjson samples "$cpu_count" --argjson avg "$cpu_avg" --argjson max "$cpu_max" --argjson cpus "$host_cpus" \
    --arg commit "$(git rev-parse HEAD)" \
    '. + {gitCommit: $commit, databaseCpu: {samples: $samples, averagePercentOfHost: $avg, maxPercentOfHost: $max, hostCpuCount: $cpus}}' \
    "$report" >"$repo_root/docs/perf/${load_profile}.json"
  jq -e '[.scenarios[].errors] | all(. == 0)' "$repo_root/docs/perf/${load_profile}.json" >/dev/null ||
    { printf '%s\n' 'The load profile recorded failed requests.' >&2; return 1; }
  printf 'Load profile report: docs/perf/%s.json\n' "$load_profile"
}

if [[ -n $load_profile ]]; then
  run_load_profile
  exit 0
fi

{
  worktree_dirty=false
  if [[ -n $(git status --porcelain) ]]; then
    worktree_dirty=true
  fi
  printf 'captured_at_utc=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'git_commit=%s\n' "$(git rev-parse HEAD)"
  printf 'git_worktree_dirty=%s\n' "$worktree_dirty"
  printf 'host_uname=%s\n' "$(uname -a)"
  printf 'go=%s\n' "$(go version)"
  printf 'docker=%s\n' "$(docker version --format 'client={{.Client.Version}} server={{.Server.Version}}')"
  docker info --format 'docker_server_arch={{.Architecture}} docker_server_cpus={{.NCPU}} docker_server_memory_bytes={{.MemTotal}}'
  printf 'compose_project=%s\n' "$project"
  printf 'core_services=notarx,postgres,rustfs\n'
  printf 'notarx_image_source=%s\n' "$image_source"
  printf 'raw_upstream=https://host.docker.internal:%s\n' "$raw_upstream_port"
  printf 'raw_egress_proxy=http://host.docker.internal:%s\n' "$raw_proxy_port"
} >"$output_dir/environment.txt"

printf '%s\n' 'Measuring the shipped stripped Linux binaries and the runtime image...'
printf 'component,os,arch,form,bytes,mib\n' >"$output_dir/artifact-sizes.csv"
# The binaries are copied out of the runtime image notarx was built into, so
# they are the shipped bytes of the image's own (native) architecture; nothing is
# cross-compiled (ADR 0016).
notarx_image="${project}-notarx"
image_arch=$(docker image inspect --format '{{.Architecture}}' "$notarx_image")
extract_id=$(docker create "$notarx_image")
for component in notarx healthcheck; do
  image_path=/notarx
  [[ "$component" == healthcheck ]] && image_path=/notarx-healthcheck
  binary_file="$private_dir/${component}-linux-${image_arch}"
  docker cp "$extract_id:$image_path" "$binary_file"
  binary_bytes=$(wc -c <"$binary_file" | tr -d ' ')
  gzip -9 -c "$binary_file" >"${binary_file}.gz"
  gzip_bytes=$(wc -c <"${binary_file}.gz" | tr -d ' ')
  printf '%s,linux,%s,stripped-static-binary,%s,%s\n' "$component" "$image_arch" "$binary_bytes" "$(bytes_to_mib "$binary_bytes")" >>"$output_dir/artifact-sizes.csv"
  printf '%s,linux,%s,gzip-9,%s,%s\n' "$component" "$image_arch" "$gzip_bytes" "$(bytes_to_mib "$gzip_bytes")" >>"$output_dir/artifact-sizes.csv"
done
docker rm "$extract_id" >/dev/null

image_bytes=$(docker image inspect --format '{{.Size}}' "$notarx_image")
image_archive="$private_dir/notarx-image.tar.gz"
docker save "$notarx_image" | gzip -1 >"$image_archive"
archive_bytes=$(wc -c <"$image_archive" | tr -d ' ')
printf 'notarx-runtime-image,linux,%s,uncompressed-content,%s,%s\n' "$image_arch" "$image_bytes" "$(bytes_to_mib "$image_bytes")" >>"$output_dir/artifact-sizes.csv"
printf 'notarx-runtime-image,linux,%s,docker-save-gzip-1,%s,%s\n' "$image_arch" "$archive_bytes" "$(bytes_to_mib "$archive_bytes")" >>"$output_dir/artifact-sizes.csv"

capture_snapshot() {
  local output=$1 sample=$2 service container_id line usage cpu memory
  for service in notarx postgres rustfs; do
    case "$service" in
      notarx) container_id=$notarx_id ;;
      postgres) container_id=$postgres_id ;;
      rustfs) container_id=$rustfs_id ;;
    esac
    line=$(docker stats --no-stream --format '{{.MemUsage}}|{{.CPUPerc}}' "$container_id")
    usage=${line%%|*}
    cpu=${line#*|}
    memory=${usage%% / *}
    printf '%s,%s,%s,%s\n' "$sample" "$service" "$(memory_to_mib "$memory")" "${cpu%%%}" >>"$output"
  done
}

sample_fixed() {
  local output=$1 samples=$2 sample
  printf 'sample,service,memory_mib,cpu_percent\n' >"$output"
  for ((sample = 1; sample <= samples; sample++)); do
    capture_snapshot "$output" "$sample"
    if (( sample < samples )); then
      sleep "$sample_interval"
    fi
  done
}

printf '%s\n' 'Allowing the healthy stack to settle before idle sampling...'
sleep 15
sample_fixed "$output_dir/idle-stats.csv" "$idle_samples"

repository_response="$private_dir/repository.json"
expect_http 201 POST "$notarx_url/api/v2/repositories" "$repository_response" \
  -H "Authorization: Bearer $admin_token" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: performance-baseline-raw-repository' \
  --data '{"name":"perf-raw","format":"raw","type":"hosted"}'
jq -e '.id | strings | length > 0' "$repository_response" >/dev/null
proxy_repository_response="$private_dir/proxy-repository.json"
expect_http 201 POST "$notarx_url/api/v2/repositories" "$proxy_repository_response" \
  -H "Authorization: Bearer $admin_token" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: performance-baseline-raw-proxy-repository' \
  --data "{\"name\":\"perf-raw-proxy\",\"format\":\"raw\",\"type\":\"proxy\",\"endpoint\":\"https://host.docker.internal:${raw_upstream_port}\",\"allowedHosts\":[\"host.docker.internal\"]}"
jq -e '.id | strings | length > 0' "$proxy_repository_response" >/dev/null

payload="$private_dir/payload-64k.bin"
dd if=/dev/urandom of="$payload" bs=65536 count=1 status=none
raw_url="$notarx_url/raw/perf-raw/performance/payload-64k.bin"
expect_http 201 PUT "$raw_url" "$private_dir/upload.out" \
  -H "Authorization: Bearer $admin_token" \
  -H 'Content-Type: application/octet-stream' \
  --data-binary "@$payload"
metadata_url="$notarx_url/api/v2/repositories"
expect_http 200 GET "$metadata_url" "$private_dir/metadata-warmup.json" -H "Authorization: Bearer $admin_token"
expect_http 200 GET "$raw_url" "$private_dir/raw-warmup.bin" -H "Authorization: Bearer $admin_token"
cmp "$payload" "$private_dir/raw-warmup.bin"

mkdir -p "$upstream_dir/performance"
large_payload="$upstream_dir/performance/payload-${raw_large_mib}m.bin"
dd if=/dev/urandom of="$large_payload" bs=1048576 count="$raw_large_mib" status=none
raw_large_url="$notarx_url/raw/perf-raw/performance/payload-${raw_large_mib}m.bin"
raw_proxy_cold_url="$notarx_url/raw/perf-raw-proxy/performance/payload-${raw_large_mib}m.bin"
expect_http 201 PUT "$raw_large_url" "$private_dir/upload-large.out" \
  -H "Authorization: Bearer $admin_token" \
  -H 'Content-Type: application/octet-stream' \
  --data-binary "@$large_payload"
expect_http 200 GET "$raw_large_url" "$private_dir/raw-large-warmup.bin" -H "Authorization: Bearer $admin_token"
cmp "$large_payload" "$private_dir/raw-large-warmup.bin"

printf 'workload,concurrency,requests,completed,failed,rps,p50_ms,p95_ms,p99_ms,duration_s,transfer_kib_s\n' >"$output_dir/benchmark-results.csv"

ab_value() {
  local label=$1 file=$2
  awk -v label="$label" 'index($0, label) == 1 { value=$0; sub("^[^:]*:[[:space:]]*", "", value); sub(/[[:space:]].*$/, "", value); print value; exit }' "$file"
}

ab_percentile() {
  local percentile=$1 file=$2
  awk -v percentile="${percentile}%" '$1 == percentile { print $2; exit }' "$file"
}

run_ab_case() {
  local workload=$1 concurrency=$2 requests=$3 url=$4 stats_output=${5:-}
  local slug=${workload//[^a-zA-Z0-9]/-}
  local ab_output="$private_dir/ab-${slug}-c${concurrency}.txt"
  local ab_pid sample=1 complete failed non_2xx rps p50 p95 p99 duration transfer

  printf 'Running %-10s concurrency=%-3s requests=%s...\n' "$workload" "$concurrency" "$requests"
  if [[ -n "$stats_output" ]]; then
    printf 'sample,service,memory_mib,cpu_percent\n' >"$stats_output"
    ab -q -k -n "$requests" -c "$concurrency" -H "Authorization: Bearer $admin_token" "$url" >"$ab_output" 2>&1 &
    ab_pid=$!
    while kill -0 "$ab_pid" 2>/dev/null; do
      capture_snapshot "$stats_output" "$sample"
      sample=$((sample + 1))
      sleep "$sample_interval"
    done
    if ! wait "$ab_pid"; then
      sed -n '1,160p' "$ab_output" >&2
      exit 1
    fi
  else
    if ! ab -q -k -n "$requests" -c "$concurrency" -H "Authorization: Bearer $admin_token" "$url" >"$ab_output" 2>&1; then
      sed -n '1,160p' "$ab_output" >&2
      exit 1
    fi
  fi

  complete=$(ab_value 'Complete requests:' "$ab_output")
  failed=$(ab_value 'Failed requests:' "$ab_output")
  non_2xx=$(ab_value 'Non-2xx responses:' "$ab_output")
  non_2xx=${non_2xx:-0}
  if [[ "$complete" != "$requests" || "$failed" != 0 || "$non_2xx" != 0 ]]; then
    printf '%s c=%s completed=%s failed=%s non_2xx=%s.\n' "$workload" "$concurrency" "$complete" "$failed" "$non_2xx" >&2
    sed -n '1,160p' "$ab_output" >&2
    exit 1
  fi
  rps=$(ab_value 'Requests per second:' "$ab_output")
  duration=$(ab_value 'Time taken for tests:' "$ab_output")
  transfer=$(ab_value 'Transfer rate:' "$ab_output")
  p50=$(ab_percentile 50 "$ab_output")
  p95=$(ab_percentile 95 "$ab_output")
  p99=$(ab_percentile 99 "$ab_output")
  printf '%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n' \
    "$workload" "$concurrency" "$requests" "$complete" "$failed" "$rps" "$p50" "$p95" "$p99" "$duration" "$transfer" \
    >>"$output_dir/benchmark-results.csv"
}

expect_http 200 HEAD "$raw_proxy_cold_url" "$private_dir/raw-proxy-head.out" -H "Authorization: Bearer $admin_token"
run_ab_case metadata "$low_concurrency" "$metadata_low_requests" "$metadata_url"
run_ab_case metadata "$mid_concurrency" "$metadata_mid_requests" "$metadata_url"
run_ab_case metadata "$high_concurrency" "$metadata_high_requests" "$metadata_url" "$output_dir/load-stats-metadata-c${high_concurrency}.csv"
run_ab_case raw-64k "$low_concurrency" "$raw_low_requests" "$raw_url"
run_ab_case raw-64k "$mid_concurrency" "$raw_mid_requests" "$raw_url"
run_ab_case raw-64k "$high_concurrency" "$raw_high_requests" "$raw_url" "$output_dir/load-stats-raw-64k-c${high_concurrency}.csv"
run_ab_case "raw-${raw_large_mib}m" "$large_concurrency" "$raw_large_requests" "$raw_large_url" "$output_dir/load-stats-raw-${raw_large_mib}m-c${large_concurrency}.csv"
run_ab_case "raw-proxy-cold-${raw_large_mib}m" "$raw_proxy_cold_concurrency" "$raw_proxy_cold_requests" "$raw_proxy_cold_url" "$output_dir/load-stats-raw-proxy-cold-${raw_large_mib}m-c${raw_proxy_cold_concurrency}.csv"

upstream_path="/performance/payload-${raw_large_mib}m.bin"
upstream_gets=$(rg -c "\"GET ${upstream_path} HTTP/" "$private_dir/raw-upstream.log" || true)
if [[ "$upstream_gets" != 1 ]]; then
  printf 'Raw Proxy cold miss triggered %s upstream GET requests; expected exactly 1.\n' "${upstream_gets:-0}" >&2
  cat "$private_dir/raw-upstream.log" >&2
  exit 1
fi
kill "$upstream_pid"
wait "$upstream_pid" 2>/dev/null || true
upstream_pid=""
expect_http 200 GET "$raw_proxy_cold_url" "$private_dir/raw-proxy-cached.bin" -H "Authorization: Bearer $admin_token"
cmp "$large_payload" "$private_dir/raw-proxy-cached.bin"
printf 'path,method,upstream_requests,cache_replay_after_upstream_stop\n%s,GET,%s,true\n' "$upstream_path" "$upstream_gets" >"$output_dir/raw-proxy-upstream.csv"

printf 'phase,service,min_memory_mib,avg_memory_mib,max_memory_mib,max_cpu_percent\n' >"$output_dir/resource-summary.csv"
summarize_service() {
  local phase=$1 file=$2 service=$3
  awk -F, -v phase="$phase" -v service="$service" '
    NR > 1 && $2 == service {
      count++
      memory = $3 + 0
      cpu = $4 + 0
      sum += memory
      if (count == 1 || memory < min) min = memory
      if (count == 1 || memory > max) max = memory
      if (count == 1 || cpu > max_cpu) max_cpu = cpu
    }
    END {
      if (count > 0) printf "%s,%s,%.2f,%.2f,%.2f,%.2f\n", phase, service, min, sum / count, max, max_cpu
    }
  ' "$file" >>"$output_dir/resource-summary.csv"
}

for service in notarx postgres rustfs; do
  summarize_service idle "$output_dir/idle-stats.csv" "$service"
  summarize_service "metadata-c${high_concurrency}" "$output_dir/load-stats-metadata-c${high_concurrency}.csv" "$service"
  summarize_service "raw-64k-c${high_concurrency}" "$output_dir/load-stats-raw-64k-c${high_concurrency}.csv" "$service"
  summarize_service "raw-${raw_large_mib}m-c${large_concurrency}" "$output_dir/load-stats-raw-${raw_large_mib}m-c${large_concurrency}.csv" "$service"
  summarize_service "raw-proxy-cold-${raw_large_mib}m-c${raw_proxy_cold_concurrency}" "$output_dir/load-stats-raw-proxy-cold-${raw_large_mib}m-c${raw_proxy_cold_concurrency}.csv" "$service"
done

expect_http 204 GET "$notarx_url/livez" "$private_dir/livez-after-load.out"
expect_http 204 GET "$notarx_url/readyz" "$private_dir/readyz-after-load.out"
notarx_logs="$private_dir/notarx.log"
"${compose[@]}" logs --no-color notarx >"$notarx_logs"
if rg -i 'panic|fatal' "$notarx_logs" >/dev/null 2>&1; then
  printf '%s\n' 'Gateway logs contain panic or fatal output after the benchmark.' >&2
  rg -in 'panic|fatal' "$notarx_logs" >&2
  exit 1
fi

printf '%s\n' 'Benchmark summary:'
awk -F, 'NR == 1 { next } { printf "  %-10s c=%-3s rps=%-10s p95=%sms failed=%s\n", $1, $2, $6, $8, $5 }' "$output_dir/benchmark-results.csv"
