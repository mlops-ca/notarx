#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

for binary in curl mvn gradle go python3; do
  command -v "$binary" >/dev/null || {
    printf 'Native Maven E2E requires %s\n' "$binary" >&2
    exit 1
  }
done

# Maven plugins and dependencies come from the recorded Central fixture through
# a loopback mirror (docs/ci.md, "Re-record the Maven and Gradle fixtures"), and
# Maven refuses every other non-loopback host. The Gradle steps resolve only from
# the Gateway under test and are not network-guarded yet. A path that was not
# recorded answers 404 and is reported.
# Mandatory real clients: snapshot history/takeover and unchanged Hosted POMs from an empty cache.
go test -v -count=1 -tags=mavenclient ./internal/app -run '^TestMavenClient(SnapshotHistoryTakeoverAndDeploy|HostedPOMCompatibilityColdCache)$'

port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
notarx_url="http://127.0.0.1:${port}"
workdir=$(mktemp -d)
fixture_pid=""
mirror_pid=""

cleanup() {
  local status=$?
  if [[ -n "$fixture_pid" ]]; then
    kill "$fixture_pid" 2>/dev/null || true
    wait "$fixture_pid" 2>/dev/null || true
  fi
  if [[ -n "$mirror_pid" ]]; then
    kill "$mirror_pid" 2>/dev/null || true
    wait "$mirror_pid" 2>/dev/null || true
  fi
  rm -rf "$workdir"
  trap - EXIT
  exit "$status"
}
trap cleanup EXIT

go build -o "$workdir/maven-fixture-mirror" ./cmd/maven-fixture-mirror
"$workdir/maven-fixture-mirror" >"$workdir/mirror.url" 2>"$workdir/mirror.log" &
mirror_pid=$!
for _ in $(seq 1 100); do
  [[ -s "$workdir/mirror.url" ]] && break
  sleep 0.1
done
mirror_url=$(head -n 1 "$workdir/mirror.url")
if [[ "$mirror_url" != http://127.0.0.1:* ]] || ! kill -0 "$mirror_pid" 2>/dev/null; then
  cat "$workdir/mirror.log" >&2
  exit 1
fi
printf 'Maven Central fixture mirror: %s (a miss is logged in %s)\n' "$mirror_url" "$workdir/mirror.log"
mirror_xml="<mirror><id>central-fixture</id><mirrorOf>central</mirrorOf><url>${mirror_url}/maven2</url></mirror>"
# Outside record mode every non-loopback host is refused, so a stray external request fails.
blocked_proxies=""
gradle_jvm_options=""
if [[ "${MAVEN_CENTRAL_FIXTURE_RECORD:-}" != "1" ]]; then
  blocked_proxies="<proxies>"
  for protocol in http https; do
    blocked_proxies+="<proxy><id>no-outbound-$protocol</id><active>true</active><protocol>$protocol</protocol><host>127.0.0.1</host><port>9</port><nonProxyHosts>127.0.0.1|localhost|[::1]</nonProxyHosts></proxy>"
  done
  blocked_proxies+="</proxies>"
  # Gradle has no settings file for this: the same refusal goes in as JVM proxy
  # properties, so a Gradle step that names an external host (a plugin portal or
  # Central repository) fails at once instead of reaching the network.
  gradle_jvm_options="-Dhttp.proxyHost=127.0.0.1 -Dhttp.proxyPort=9 -Dhttps.proxyHost=127.0.0.1 -Dhttps.proxyPort=9 -Dhttp.nonProxyHosts=127.0.0.1|localhost|[::1]"
fi
# Prints the re-record hint for every unrecorded path the mirror answered 404 to
# and returns 1 when there was one. A failing Maven run reports and fails with
# Maven's status; a miss Maven tolerated is reported at the end as a warning.
fixture_misses() {
  if grep -q 'Maven fixture miss' "$workdir/mirror.log"; then
    printf 'Maven Central fixture misses (re-record with the record-maven-fixtures workflow, docs/ci.md):\n' >&2
    grep 'Maven fixture miss' "$workdir/mirror.log" >&2
    return 1
  fi
}

go build -o "$workdir/native-maven-fixture" ./cmd/native-maven-fixture
LISTEN_ADDR="127.0.0.1:${port}" "$workdir/native-maven-fixture" >"$workdir/notarx.log" 2>&1 &
fixture_pid=$!
until curl --silent --show-error --fail "$notarx_url/livez" >/dev/null; do
  if ! kill -0 "$fixture_pid" 2>/dev/null; then
    cat "$workdir/notarx.log" >&2
    exit 1
  fi
  sleep 0.1
done

status() {
  curl --silent --show-error --output "$workdir/last-response" --write-out '%{http_code}' "$@"
}

expect_status() {
  local expected=$1
  shift
  local actual
  actual=$(status "$@")
  if [[ "$actual" != "$expected" ]]; then
    printf 'Expected HTTP %s, got %s: %s\n' "$expected" "$actual" "$*" >&2
    cat "$workdir/last-response" >&2
    cat "$workdir/notarx.log" >&2
    exit 1
  fi
}

basic=(--user fixture:fixture-secret)

# Strict publication remains available as an explicit repository policy. A
# complete coordinate stays invisible until the Gateway commit, while client
# checksum sidecars and metadata remain non-authoritative assertions.
strict_pom='<project><modelVersion>4.0.0</modelVersion><groupId>org.example</groupId><artifactId>strict-widget</artifactId><version>0.0.1</version></project>'
expect_status 201 "${basic[@]}" -X PUT --data-binary "$strict_pom" "$notarx_url/repository/maven/strict-deploys/org/example/strict-widget/0.0.1/strict-widget-0.0.1.pom"
expect_status 201 "${basic[@]}" -X PUT --data-binary strict-bytecode "$notarx_url/repository/maven/strict-deploys/org/example/strict-widget/0.0.1/strict-widget-0.0.1.jar"
expect_status 201 "${basic[@]}" -X PUT --data-binary deadbeef "$notarx_url/repository/maven/strict-deploys/org/example/strict-widget/0.0.1/strict-widget-0.0.1.jar.sha256"
expect_status 201 "${basic[@]}" -X PUT --data-binary '<metadata/>' "$notarx_url/repository/maven/strict-deploys/org/example/strict-widget/maven-metadata.xml"
expect_status 404 "${basic[@]}" "$notarx_url/repository/maven/strict-deploys/org/example/strict-widget/0.0.1/strict-widget-0.0.1.jar"
expect_status 200 "${basic[@]}" -H 'Idempotency-Key: strict-release' -H 'Content-Type: application/json' \
  --data '{"expectedAssetNames":["strict-widget-0.0.1.pom","strict-widget-0.0.1.jar"]}' \
  "$notarx_url/repository/maven/strict-deploys/coordinates/org.example:strict-widget:0.0.1:commit"
expect_status 200 "${basic[@]}" "$notarx_url/repository/maven/strict-deploys/org/example/strict-widget/0.0.1/strict-widget-0.0.1.jar"
expect_status 200 "${basic[@]}" -H 'Idempotency-Key: strict-release' -H 'Content-Type: application/json' \
  --data '{"expectedAssetNames":["strict-widget-0.0.1.jar","strict-widget-0.0.1.pom"]}' \
  "$notarx_url/repository/maven/strict-deploys/coordinates/org.example:strict-widget:0.0.1:commit"
expect_status 409 "${basic[@]}" -H 'Idempotency-Key: strict-release-other' -H 'Content-Type: application/json' \
  --data '{"expectedAssetNames":["strict-widget-0.0.1.pom","strict-widget-0.0.1.jar"]}' \
  "$notarx_url/repository/maven/strict-deploys/coordinates/org.example:strict-widget:0.0.1:commit"

maven_dir="$workdir/maven"
mkdir -p "$maven_dir"
cat >"$maven_dir/settings.xml" <<EOF
<settings><mirrors>${mirror_xml}</mirrors>${blocked_proxies}<servers><server><id>native</id><username>fixture</username><password>fixture-secret</password></server></servers></settings>
EOF
cat >"$maven_dir/pom.xml" <<EOF
<project xmlns="http://maven.apache.org/POM/4.0.0"><modelVersion>4.0.0</modelVersion><groupId>org.example</groupId><artifactId>maven-widget</artifactId><version>1.2.3</version><distributionManagement><repository><id>native</id><url>${notarx_url}/repository/maven/deploys</url></repository></distributionManagement><build><plugins><plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-resources-plugin</artifactId><version>3.4.0</version></plugin><plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-compiler-plugin</artifactId><version>3.15.0</version></plugin><plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-surefire-plugin</artifactId><version>3.5.4</version></plugin><plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-jar-plugin</artifactId><version>3.5.0</version></plugin><plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-install-plugin</artifactId><version>3.1.4</version></plugin><plugin><groupId>org.apache.maven.plugins</groupId><artifactId>maven-deploy-plugin</artifactId><version>3.1.4</version></plugin></plugins></build></project>
EOF
mvn_status=0
(cd "$maven_dir" && mvn --batch-mode --settings settings.xml -Dmaven.repo.local="$workdir/maven-deploy-repository" deploy) || mvn_status=$?
if [[ $mvn_status -ne 0 ]]; then fixture_misses || true; exit "$mvn_status"; fi
expect_status 200 "${basic[@]}" "$notarx_url/repository/maven/deploys/org/example/maven-widget/1.2.3/maven-widget-1.2.3.jar"
expect_status 409 "${basic[@]}" -H 'Idempotency-Key: direct-mode-does-not-commit' -H 'Content-Type: application/json' \
  --data '{"expectedAssetNames":["maven-widget-1.2.3.pom","maven-widget-1.2.3.jar"]}' \
  "$notarx_url/repository/maven/deploys/coordinates/org.example:maven-widget:1.2.3:commit"

resolve_dir="$workdir/maven-resolve"
mkdir -p "$resolve_dir"
cat >"$resolve_dir/pom.xml" <<EOF
<project xmlns="http://maven.apache.org/POM/4.0.0"><modelVersion>4.0.0</modelVersion><groupId>fixture</groupId><artifactId>resolver</artifactId><version>1</version><repositories><repository><id>native</id><url>${notarx_url}/maven/maven-chain</url></repository></repositories><dependencies><dependency><groupId>org.example</groupId><artifactId>maven-widget</artifactId><version>1.2.3</version></dependency></dependencies></project>
EOF
(cd "$resolve_dir" && mvn --batch-mode --settings "$maven_dir/settings.xml" -Dmaven.repo.local="$workdir/maven-resolve-repository" org.apache.maven.plugins:maven-dependency-plugin:3.8.1:resolve) || mvn_status=$?
if [[ $mvn_status -ne 0 ]]; then fixture_misses || true; exit "$mvn_status"; fi

gradle_dir="$workdir/gradle"
mkdir -p "$gradle_dir/src/main/java/org/example"
cat >"$gradle_dir/settings.gradle" <<'EOF'
rootProject.name = 'gradle-widget'
EOF
cat >"$gradle_dir/src/main/java/org/example/Widget.java" <<'EOF'
package org.example; public final class Widget { }
EOF
cat >"$gradle_dir/build.gradle" <<EOF
plugins {
  id 'java'
  id 'maven-publish'
}
group = 'org.example'
version = '2.0.0-SNAPSHOT'
repositories {
  maven {
    url = uri('${notarx_url}/maven/maven-chain')
    credentials { username = 'fixture'; password = 'fixture-secret' }
    allowInsecureProtocol = true
  }
}
publishing {
  publications {
    create('maven', MavenPublication) {
      from components.java
    }
  }
  repositories {
    maven {
      url = uri('${notarx_url}/repository/maven/deploys')
      credentials { username = 'fixture'; password = 'fixture-secret' }
      allowInsecureProtocol = true
    }
  }
}
configurations { nativeResolve }
dependencies { nativeResolve 'org.example:gradle-widget:2.0.0-SNAPSHOT' }
tasks.register('resolveNative') { doLast { configurations.nativeResolve.files.each { println it } } }
EOF
if ! (cd "$gradle_dir" && JAVA_TOOL_OPTIONS="$gradle_jvm_options" GRADLE_USER_HOME="$workdir/gradle-user-home" gradle --no-daemon --quiet publish); then
  cat "$workdir/notarx.log" >&2
  exit 1
fi
(cd "$gradle_dir" && JAVA_TOOL_OPTIONS="$gradle_jvm_options" GRADLE_USER_HOME="$workdir/gradle-user-home" gradle --no-daemon --quiet resolveNative | grep -F 'gradle-widget-2.0.0-SNAPSHOT.jar')

expect_status 200 "${basic[@]}" "$notarx_url/repository/maven/deploys/org/example/maven-widget/1.2.3/maven-widget-1.2.3.jar.sha256"
expect_status 200 "${basic[@]}" "$notarx_url/repository/maven/deploys/org/example/gradle-widget/2.0.0-SNAPSHOT/maven-metadata.xml"
expect_status 200 "${basic[@]}" "$notarx_url/maven/maven-chain/org/example/maven-widget/1.2.3/maven-widget-1.2.3.jar.sha256"
expect_status 200 "${basic[@]}" "$notarx_url/maven/maven-chain/org/example/gradle-widget/2.0.0-SNAPSHOT/maven-metadata.xml"

fixture_misses || printf 'warning: Maven tolerated the fixture misses above; record them before they matter.\n' >&2
printf 'Native Maven/Gradle E2E passed through Hosted publication and multi-Proxy Group reads; release, SNAPSHOT, checksums and strict Gateway opt-in passed through %s\n' "$notarx_url"
