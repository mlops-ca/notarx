#!/usr/bin/env bash
# Records the Maven Central and Gradle plugin portal responses the Maven and
# Gradle client tests and end-to-end scripts need into
# internal/mavenfixture/testdata (docs/ci.md, "Re-record the Maven and Gradle
# fixtures"). It needs outbound network, mvn, gradle and go, so it runs in the
# container of .github/maven-fixture-recorder, from the record-maven-fixtures
# workflow. Normal CI never runs it.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

for binary in mvn gradle go; do
  command -v "$binary" >/dev/null || {
    printf 'Recording needs %s\n' "$binary" >&2
    exit 1
  }
done

export MAVEN_CENTRAL_FIXTURE_RECORD=1

# Go client tests: the tests that start the fixture record what they miss.
# The end-to-end flows join here when they use the fixture mirror (#357).
go test -v -count=1 -tags=mavenclient ./internal/app \
  -run '^TestMavenClient(SnapshotHistoryTakeoverAndDeploy|HostedPOMCompatibilityColdCache)$'

# Recorded bytes must match the checksums the upstreams published.
go test -count=1 ./internal/mavenfixture/...
