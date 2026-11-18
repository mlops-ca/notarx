#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

NOTARX_GO_CLI_E2E=1 \
go test ./internal/app \
	  -run '^TestNativeGoRealClient(Downloads(ThroughProxyAndOfflineCache|HostedPublication)|VerifiesChecksumsThroughSumDBMirror)$' \
  -count=1 -v
