#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

for binary in curl go python3 sha256sum; do command -v "$binary" >/dev/null || { printf 'Native OCI E2E requires %s\n' "$binary" >&2; exit 1; }; done
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
notarx_url="http://127.0.0.1:${port}"
workdir=$(mktemp -d)
fixture_pid=""
cleanup() { local status=$?; [[ -z "$fixture_pid" ]] || { kill "$fixture_pid" 2>/dev/null || true; wait "$fixture_pid" 2>/dev/null || true; }; rm -rf "$workdir"; trap - EXIT; exit "$status"; }
trap cleanup EXIT
go build -o "$workdir/native-oci-fixture" ./cmd/native-oci-fixture
WORKLOAD_TOKEN_FILE="$workdir/workload.jwt" LISTEN_ADDR="127.0.0.1:${port}" "$workdir/native-oci-fixture" >"$workdir/notarx.log" 2>&1 &
fixture_pid=$!
until curl --silent --show-error --fail "$notarx_url/livez" >/dev/null; do kill -0 "$fixture_pid" 2>/dev/null || { cat "$workdir/notarx.log" >&2; exit 1; }; sleep 0.1; done

status() { curl --silent --show-error --output "$workdir/response" --write-out '%{http_code}' "$@"; }
expect() { local want=$1; shift; local got; got=$(status "$@"); [[ "$got" == "$want" ]] || { printf 'Expected HTTP %s, got %s: %s\n' "$want" "$got" "$*" >&2; cat "$workdir/response" >&2; exit 1; }; }
expect 200 --user fixture:fixture-secret "$notarx_url/auth/token"
token=$(sed -n 's/.*"token":"\([^"]*\)".*/\1/p' "$workdir/response")
test -n "$token"
auth=(-H "Authorization: Bearer $token")
headers="$workdir/headers"

blob='native oci protocol artifact'
digest="sha256:$(printf '%s' "$blob" | sha256sum | awk '{print $1}')"
expect 202 "${auth[@]}" -D "$headers" -X POST "$notarx_url/v2/team/widget/blobs/uploads/"
location=$(awk 'tolower($1)=="location:" {sub(/\r$/, "", $2); print $2}' "$headers")
test -n "$location"
expect 202 "${auth[@]}" -H 'Content-Range: 0-5' -X PATCH --data-binary "${blob:0:6}" "$notarx_url$location"
expect 201 "${auth[@]}" -H "Content-Range: 6-$((${#blob}-1))" -X PUT --data-binary "${blob:6}" "$notarx_url$location?digest=$digest"
expect 206 "${auth[@]}" -H 'Range: bytes=7-9' "$notarx_url/v2/team/widget/blobs/$digest"
[[ $(cat "$workdir/response") == 'oci' ]] || { printf 'Range body mismatch: %q\n' "$(cat "$workdir/response")" >&2; exit 1; }
expect 201 "${auth[@]}" -X POST "$notarx_url/v2/team/other/blobs/uploads/?mount=$digest&from=team/widget"
manifest='{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}'
expect 201 "${auth[@]}" -D "$headers" -H 'Content-Type: application/vnd.oci.image.manifest.v1+json' -X PUT --data-binary "$manifest" "$notarx_url/v2/team/widget/manifests/latest"
manifest_digest=$(awk 'tolower($1)=="docker-content-digest:" {sub(/\r$/, "", $2); print $2}' "$headers")
test -n "$manifest_digest"
expect 200 "${auth[@]}" "$notarx_url/v2/team/widget/tags/list?n=1"
grep -q '"latest"' "$workdir/response"
expect 406 "${auth[@]}" -H 'Accept: text/plain' "$notarx_url/v2/team/widget/manifests/latest"
expect 200 "${auth[@]}" -I "$notarx_url/v2/team/widget/manifests/latest"
expect 202 "${auth[@]}" -D "$headers" -X POST "$notarx_url/v2/team/widget/blobs/uploads/"
cancel_location=$(awk 'tolower($1)=="location:" {sub(/\r$/, "", $2); print $2}' "$headers")
test -n "$cancel_location"
expect 204 "${auth[@]}" -X DELETE "$notarx_url$cancel_location"
expect 404 "${auth[@]}" -X PATCH --data-binary 'cancelled' "$notarx_url$cancel_location"
expect 202 "${auth[@]}" -X DELETE "$notarx_url/v2/team/widget/manifests/$manifest_digest"
expect 404 "${auth[@]}" "$notarx_url/v2/team/widget/manifests/latest"

# Workload identity federation (ADR 0019): docker login -u oidc -p <workload JWT> is Basic credentials on /auth/token.
workload_jwt=$(cat "$workdir/workload.jwt")
test -n "$workload_jwt"
expect 200 --user "oidc:$workload_jwt" "$notarx_url/auth/token"
workload_token=$(sed -n 's/.*"token":"\([^"]*\)".*/\1/p' "$workdir/response")
test -n "$workload_token"
expect 200 -H "Authorization: Bearer $workload_token" "$notarx_url/v2/"
expect 200 --user "publish:$workload_jwt" "$notarx_url/auth/token"
expect 401 --user "deploy:$workload_jwt" "$notarx_url/auth/token"
# A token with a damaged signature (the last base64url character carries padding bits, so damage earlier ones).
expect 401 --user "oidc:${workload_jwt:0:${#workload_jwt}-20}AAAAAAAAAAAAAAAAAAAA" "$notarx_url/auth/token"
printf 'Native OCI Registry V2 E2E passed through %s\n' "$notarx_url"
