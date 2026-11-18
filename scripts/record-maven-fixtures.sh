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
workdir=$(mktemp -d)
mirror_pid=""
cleanup() {
  local status=$?
  if [[ -n "$mirror_pid" ]]; then
    kill "$mirror_pid" 2>/dev/null || true
    wait "$mirror_pid" 2>/dev/null || true
  fi
  rm -rf "$workdir"
  trap - EXIT
  exit "$status"
}
trap cleanup EXIT

# Go client tests: the tests that start the fixture record what they miss.
# A renamed test would make go test pass without running anything, so require both.
go test -v -count=1 -tags=mavenclient ./internal/app \
  -run '^TestMavenClient(SnapshotHistoryTakeoverAndDeploy|HostedPOMCompatibilityColdCache)$' | tee "$workdir/go-test.log"
[[ $(grep -c '^--- PASS: TestMavenClient' "$workdir/go-test.log") -ge 2 ]] || {
  printf 'Expected both Maven client tests to run and pass\n' >&2
  exit 1
}

# Gradle: resolve a plugin from the plugin portal route and a dependency from
# the Central route through the recording mirror. The end-to-end flows join
# here when they use the mirror (#357).
go build -o "$workdir/maven-fixture-mirror" ./cmd/maven-fixture-mirror
"$workdir/maven-fixture-mirror" >"$workdir/mirror.url" 2>"$workdir/mirror.log" &
mirror_pid=$!
for _ in $(seq 1 100); do
  [[ -s "$workdir/mirror.url" ]] && break
  sleep 0.1
done
mirror_url=$(head -n 1 "$workdir/mirror.url")
[[ -n "$mirror_url" ]] || {
  cat "$workdir/mirror.log" >&2
  exit 1
}
printf 'Recording through %s\n' "$mirror_url"

project="$workdir/gradle-project"
mkdir -p "$project"
cat >"$project/settings.gradle" <<EOF
pluginManagement {
  repositories {
    maven {
      url = uri('${mirror_url}/gradle-plugins')
      allowInsecureProtocol = true
    }
  }
}
rootProject.name = 'record-fixtures'
EOF
cat >"$project/build.gradle" <<EOF
plugins {
  id 'java'
  id 'org.gradle.toolchains.foojay-resolver-convention' version '0.9.0' apply false
}
repositories {
  maven {
    url = uri('${mirror_url}/maven2')
    allowInsecureProtocol = true
  }
}
dependencies { implementation 'commons-io:commons-io:2.16.1' }
tasks.register('resolveAll') { doLast { configurations.runtimeClasspath.files.each { println it } } }
EOF
(cd "$project" && GRADLE_USER_HOME="$workdir/gradle-user-home" gradle --no-daemon --console=plain resolveAll)

# Recorded bytes must match the checksums the upstreams published.
go test -count=1 ./internal/mavenfixture/...
