#!/usr/bin/env bash
# Checks the parts of scripts/egress-guard.sh that need no root: the unguarded
# pass-through and the usage error. Enforcement is proven in CI by the guard's own probe.
set -euo pipefail
guard=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/egress-guard.sh

[[ "$(env -u NOTARX_EGRESS_GUARD "$guard" printf '%s-%s' a b)" == "a-b" ]]
status=0
env -u NOTARX_EGRESS_GUARD "$guard" bash -c 'exit 7' || status=$?
[[ $status -eq 7 ]]
status=0
NOTARX_EGRESS_GUARD=1 "$guard" 2>/dev/null || status=$?
[[ $status -eq 2 ]]
printf 'egress guard test passed\n'
