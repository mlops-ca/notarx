#!/usr/bin/env bash
# Workload identity federation end to end (ADR 0019): a GitHub-Actions-style mock issuer over HTTPS and a
# Kubernetes projected service account token drive docker login, the RFC 8693 token exchange and
# notarxctl auth login against a real gateway process, with issuers and policies managed through notarxctl.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

for binary in curl go python3; do command -v "$binary" >/dev/null || { printf 'Workload identity E2E requires %s\n' "$binary" >&2; exit 1; }; done
free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }
server_port=$(free_port)
issuer_port=$(free_port)
notarx_url="http://127.0.0.1:${server_port}"
workdir=$(mktemp -d)
fixture_pid=""
cleanup() { local status=$?; [[ -z "$fixture_pid" ]] || { kill "$fixture_pid" 2>/dev/null || true; wait "$fixture_pid" 2>/dev/null || true; }; rm -rf "$workdir"; trap - EXIT; exit "$status"; }
trap cleanup EXIT

go build -o "$workdir/fixture" ./cmd/workload-identity-fixture
go build -o "$workdir/notarxctl" ./cmd/notarxctl
FIXTURE_DIR="$workdir" LISTEN_ADDR="127.0.0.1:${server_port}" ISSUER_ADDR="127.0.0.1:${issuer_port}" NOTARX_AUDIENCE="$notarx_url" "$workdir/fixture" >"$workdir/fixture.log" 2>&1 &
fixture_pid=$!
until curl --silent --show-error --fail "$notarx_url/livez" >/dev/null 2>&1; do kill -0 "$fixture_pid" 2>/dev/null || { cat "$workdir/fixture.log" >&2; exit 1; }; sleep 0.1; done
issuer_url=$(cat "$workdir/issuer.url")
until curl --silent --fail --cacert "$workdir/ca.pem" "$issuer_url/.well-known/openid-configuration" >/dev/null 2>&1; do kill -0 "$fixture_pid" 2>/dev/null || { cat "$workdir/fixture.log" >&2; exit 1; }; sleep 0.1; done

export NOTARXCTL_SERVER="$notarx_url"
export NOTARXCTL_TOKEN="fixture-admin"
export NOTARXCTL_CONFIG="$workdir/notarxctl.yaml"
ctl() { "$workdir/notarxctl" "$@"; }
json_field() { python3 -c 'import json,sys; d=json.load(sys.stdin)["data"]; print(d[sys.argv[1]])' "$1"; }
fail() { printf 'workload identity E2E: %s\n' "$*" >&2; cat "$workdir/fixture.log" >&2; exit 1; }
status() { curl --silent --show-error --output "$workdir/response" --write-out '%{http_code}' "$@"; }
expect() { local want=$1; shift; local got; got=$(status "$@"); [[ "$got" == "$want" ]] || { printf 'Expected HTTP %s, got %s: %s\n' "$want" "$got" "$*" >&2; cat "$workdir/response" >&2; exit 1; }; }
urlencode() { python3 -c 'import sys,urllib.parse; print(urllib.parse.quote(sys.argv[1], safe=""))' "$1"; }

# The service account the workloads act as, and its trust: GitHub Actions on repository 1296269, environment
# packages-publish, ref main; and the Kubernetes service account team-a/builder.
account_id=$(ctl service-account create feeds-publisher --json | json_field id)
github_id=$(ctl workload issuer create github --kind github_actions --issuer "$issuer_url" --audience "$notarx_url" \
  --ca-bundle-file "$workdir/ca.pem" --allow-private-network --json | json_field id)
ctl workload issuer probe --id "$github_id" | grep -q 'github-key' || fail 'the issuer probe did not report the key id'
ctl workload policy create gh-publish --issuer "$github_id" --service-account "$account_id" \
  --condition repository_id:eq:1296269 --condition environment:eq:packages-publish --condition ref:eq:refs/heads/main \
  --max-token-ttl 5m --json >"$workdir/policy.json"
policy_id=$(json_field id <"$workdir/policy.json")
# A mutable-name-only or owner-only policy must be refused by the server.
if ctl workload policy create owner-only --issuer "$github_id" --service-account "$account_id" --condition repository_owner_id:eq:9919 >/dev/null 2>&1; then
  fail 'a policy satisfied by the owner id alone was accepted'
fi

github_token() { curl --silent --show-error --fail --cacert "$workdir/ca.pem" -H 'Authorization: bearer fixture-actions-request-token' \
  "$issuer_url/token?audience=$(urlencode "$notarx_url")${1:-}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["value"])'; }
jwt=$(github_token)

# docker login -u oidc -p <jwt>: Basic credentials on the OCI token endpoint, then a registry call with the token.
expect 200 --user "oidc:$jwt" "$notarx_url/auth/token"
registry_token=$(sed -n 's/.*"token":"\([^"]*\)".*/\1/p' "$workdir/response")
test -n "$registry_token"
expect 200 -H "Authorization: Bearer $registry_token" "$notarx_url/v2/"
# A policy name as the username pins that policy; another name does not match.
expect 200 --user "gh-publish:$jwt" "$notarx_url/auth/token"
expect 401 --user "no-such-policy:$jwt" "$notarx_url/auth/token"
# The raw JWT is also a bearer credential on the management API.
expect 200 -H "Authorization: Bearer $jwt" "$notarx_url/api/v2/identity"
grep -q 'workload_federation' "$workdir/response" || fail 'the bearer JWT did not authenticate as workload_federation'

# RFC 8693 token exchange for chatty clients.
exchange() { status -X POST --data-urlencode 'grant_type=urn:ietf:params:oauth:grant-type:token-exchange' \
  --data-urlencode "subject_token=$1" --data-urlencode 'subject_token_type=urn:ietf:params:oauth:token-type:jwt' "${@:2}" "$notarx_url/auth/token-exchange"; }
[[ $(exchange "$jwt") == 200 ]] || { cat "$workdir/response" >&2; fail 'the token exchange failed'; }
access_token=$(python3 -c 'import json,sys; d=json.load(sys.stdin); assert d["token_type"]=="Bearer" and d["expires_in"]<=300; print(d["access_token"])' <"$workdir/response")
expect 200 -H "Authorization: Bearer $access_token" "$notarx_url/api/v2/identity"
# An exchanged token cannot be exchanged again.
[[ $(exchange "$access_token") == 400 ]] || fail 'an exchanged token was exchanged again'
grep -q 'invalid_grant' "$workdir/response" || fail 'the re-exchange was not invalid_grant'
# Another repository's workflow gets the same uniform answer as an expired or forged token.
wrong=$(github_token '&repository_id=999')
[[ $(exchange "$wrong") == 400 ]] || fail 'a token of another repository was exchanged'
cp "$workdir/response" "$workdir/wrong-response"
[[ $(exchange "${jwt:0:${#jwt}-20}AAAAAAAAAAAAAAAAAAAA") == 400 ]] || fail 'a forged token was exchanged'
cmp "$workdir/response" "$workdir/wrong-response" || fail 'failures are distinguishable'
# POST /auth/token with the Docker token-spec grants is left alone.
post_status=$(status -X POST -d 'grant_type=password&username=u&password=p&service=notarx' "$notarx_url/auth/token")
[[ "$post_status" != 200 ]] || fail 'POST /auth/token changed'

# notarxctl login with the workload token, as a CI job would.
printf '%s' "$jwt" >"$workdir/github.jwt"
ctl auth login --workload-token-file "$workdir/github.jwt" --workload-policy gh-publish --secret-output "$workdir/session.token" --json >/dev/null
session=$(tr -d '\n' <"$workdir/session.token")
expect 200 -H "Authorization: Bearer $session" "$notarx_url/api/v2/identity"
# The dry-run tester names the failing condition.
printf '%s' "$wrong" >"$workdir/wrong.jwt"
ctl workload policy test --token-file "$workdir/wrong.jwt" | grep -q 'condition 0 on repository_id failed' || fail 'the tester did not name the failing condition'
ctl workload policy test --token-file "$workdir/github.jwt" | grep -q 'matched policy' || fail 'the tester did not match the policy'

# Kubernetes: a projected service account token (audience, 600 s) and a static key set, as for a private API server.
k8s_id=$(ctl workload issuer create cluster --kind kubernetes --issuer https://kubernetes.default.svc.cluster.local \
  --audience "$notarx_url" --jwks-mode static --static-jwks-file "$workdir/k8s-jwks.json" --json | json_field id)
ctl workload policy create k8s-builder --issuer "$k8s_id" --service-account "$account_id" \
  --condition sub:eq:system:serviceaccount:team-a:builder --json >/dev/null
ctl auth login --workload-token-file "$workdir/k8s/token" --workload-policy k8s-builder --secret-output "$workdir/k8s-session.token" --json >/dev/null
expect 200 -H "Authorization: Bearer $(tr -d '\n' <"$workdir/k8s-session.token")" "$notarx_url/api/v2/identity"
expect 200 --user "oidc:$(cat "$workdir/k8s/token")" "$notarx_url/auth/token"

# Revocation: disabling the policy refuses its tokens, including the one already exchanged.
ctl workload policy update "$policy_id" --state disabled --json >/dev/null
[[ $(exchange "$jwt") == 400 ]] || fail 'a disabled policy still exchanged a token'
expect 401 -H "Authorization: Bearer $access_token" "$notarx_url/api/v2/identity"
printf 'Workload identity federation E2E passed through %s\n' "$notarx_url"
