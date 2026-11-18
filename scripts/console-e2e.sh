#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
env_file=${NOTARX_ENV_FILE:-"$root/.env"}
test -f "$env_file" || { printf '%s\n' 'Console E2E requires a configured environment file.' >&2; exit 1; }

# shellcheck disable=SC1090
source "$env_file"
notarx_port=${NOTARX_HTTP_PORT:-8080}
curl --silent --show-error --fail "http://127.0.0.1:${notarx_port}/readyz" >/dev/null
console_port=$(python3 -c 'import socket; listener = socket.socket(); listener.bind(("127.0.0.1", 0)); print(listener.getsockname()[1]); listener.close()')
# CONSOLE_E2E_SPECS (selected specs) and CONSOLE_E2E_SHARD (i/N) come from the CI matrix.
# Specs become anchored, escaped path filters: Playwright treats positional
# arguments as regular expressions.
e2e_args=()
for spec in ${CONSOLE_E2E_SPECS:-}; do
  e2e_args+=("$(printf '%s' "$spec" | sed -e 's/[.[\*^$()+?{|]/\\&/g' -e 's/$/$/')")
done
if [[ -n "${CONSOLE_E2E_SHARD:-}" ]]; then
  e2e_args+=("--shard=$CONSOLE_E2E_SHARD")
  # A selected subset can leave a shard without specs.
  if [[ ${#e2e_args[@]} -gt 1 ]]; then e2e_args+=(--pass-with-no-tests); fi
fi
NOTARX_ADMIN_TOKEN="$NOTARX_ADMIN_TOKEN" PLAYWRIGHT_PORT="$console_port" VITE_NOTARX_PROXY_TARGET="http://127.0.0.1:${notarx_port}" npm --prefix "$root/console" run e2e -- ${e2e_args[@]+"${e2e_args[@]}"}