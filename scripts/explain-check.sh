#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

compose=(docker compose --env-file /dev/null --project-name notarx-explain -f compose.integration.yml)
cleanup() {
  "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker volume create notarx-go-mod >/dev/null
docker volume create notarx-go-build >/dev/null
cleanup
"${compose[@]}" up -d --wait postgres
"${compose[@]}" run --rm --no-deps migrate
NOTARX_EXPLAIN_SCALE=1 NOTARX_EXPLAIN_PROFILE="${NOTARX_EXPLAIN_PROFILE:-tiny}" \
  "${compose[@]}" run --rm --no-deps -e NOTARX_EXPLAIN_SCALE -e NOTARX_EXPLAIN_PROFILE test \
  go test -count=1 -tags=integration -run '^TestHotQueriesKeepTheirPlans$' ./internal/repository
