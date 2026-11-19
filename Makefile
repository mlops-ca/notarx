.DEFAULT_GOAL := help

GO_IMAGE := golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280
LINT_IMAGE := golangci/golangci-lint:v2.15.0
OPENAPI_TOOLS := tools/openapi
CONSOLE_DIR := console
OPENAPI_SOURCE := api/openapi/native-hosted.yaml
OPENAPI_BUNDLE := api/openapi/native-hosted-v1.json

# notarxctl release identity. The client takes the VERSION file value (0.0.0-dev in the
# tree; release builds stamp the real value with scripts/stamp-version.sh, ADR 0024)
# and the Git revision, injected through buildinfo exactly like the release
# archives. Override on the command line: make VERSION=0.3.0-nightly.2 build.
VERSION ?= $(shell tr -d '[:space:]' < VERSION)
CLI_REVISION ?= $(shell git rev-parse --verify HEAD 2>/dev/null || printf 'unknown')
CLI_LDFLAGS = -s -w -X github.com/mlops-ca/notarx/internal/buildinfo.injectedVersion=$(VERSION) -X github.com/mlops-ca/notarx/internal/buildinfo.injectedRevision=$(CLI_REVISION)
.PHONY: i18n-release-check i18n-tooling-test help dev-bootstrap dev dev-status dev-down local-dev-test rustfs-only-check kubernetes-local-check kubernetes-local-up kubernetes-local-status kubernetes-local-verify kubernetes-local-down reference-scanner-smoke cargo-contract openapi-dependency-test openapi-tools-ready console-codegen-ready raw-e2e conan-e2e native-maven-e2e native-oci-e2e native-raw-e2e native-npm-e2e native-pypi-e2e native-go-e2e native-apt-e2e apt-signer-rotation-e2e readiness-e2e resolver-rotation-e2e service-account-rotation-e2e oci-performance-e2e performance-baseline cache-operations-e2e backup-restore-readiness backup-transfer-test upgrade-readiness release-readiness-check release-version-check stamp-version-test release-plan-test release-pipeline-test release-workflow-check release-artifacts release-artifacts-test package-check package-install-test packages-signing-drill packages-environments-test docs-check commit-policy-check identity-check external-network-test-check preflight migration-check evidence up down test api-contract cli-contract-check cli-test cli-e2e cli-runtime-e2e integration-test integration-down lint spellcheck vet race coverage dependency-audit fmt cli-build build docker-build docker-test console-docker-build cli-docker-build migrate backup-drill restore-drill console-build console-typecheck console-check console-test console-api-check console-e2e console-visual console-visual-update openapi-bundle openapi-generate-admin openapi-check

help:
	@printf '%s\n' 'Targets: native-rubygems-e2e, native-helm-e2e, native-oci-discovery-e2e, nexus-migration-client-e2e, nexus-inventory-scan-e2e, native-nuget-e2e, nexus-oci-migration-e2e, nexus-conan-migration-e2e, nexus-npm-migration-e2e, nexus-pypi-migration-e2e, private-upstream-auth-e2e, private-upstream-formats-e2e, dev-bootstrap, dev, dev-status, dev-down, kubernetes-local-check, kubernetes-local-up, kubernetes-local-status, kubernetes-local-verify, kubernetes-local-down, reference-scanner-smoke, cargo-contract, up, down, test, api-contract, cli-contract-check, cli-test, cli-e2e, cli-runtime-e2e, integration-test, integration-down, lint, spellcheck, vet, race, coverage, dependency-audit, fmt, cli-build, build, docker-build, docker-test, console-docker-build, cli-docker-build, migrate, backup-drill, restore-drill, preflight, migration-check, evidence, raw-e2e, conan-e2e, native-maven-e2e, native-oci-e2e, native-raw-e2e, native-npm-e2e, native-pypi-e2e, native-go-e2e, native-apt-e2e, apt-signer-rotation-e2e, readiness-e2e, resolver-rotation-e2e, service-account-rotation-e2e, oci-performance-e2e, performance-baseline, cache-operations-e2e, backup-restore-readiness, backup-transfer-test, published-native-backup-test, oci-free-tier-check, oci-free-tier-images-check, oci-free-tier-smoke, oci-free-tier-tofu-check, upgrade-readiness, release-readiness-check, release-version-check, stamp-version-test, release-plan-test, release-pipeline-test, release-workflow-check, release-artifacts, release-artifacts-test, package-check, package-install-test, packages-signing-drill, packages-environments-test, docs-check, commit-policy-check, identity-check, external-network-test-check, console-build, console-typecheck, console-check, console-test, i18n-release-check, i18n-tooling-test, console-api-check, console-e2e, console-visual, console-visual-update, openapi-bundle, openapi-generate-admin, openapi-check, native-build-check'

dev-bootstrap:
	@./scripts/local-dev.sh bootstrap-env

dev:
	@./scripts/local-dev.sh start

dev-status:
	@./scripts/local-dev.sh status

dev-down:
	@./scripts/local-dev.sh stop

local-dev-test:
	@./scripts/local-dev-test.sh

rustfs-only-check:
	@./scripts/rustfs-only-contract-test.sh

kubernetes-local-check:
	@./scripts/kubernetes-local-manifest-test.sh
	@./scripts/kubernetes-local-test.sh

kubernetes-local-up:
	@./scripts/kubernetes-local.sh up

kubernetes-local-status:
	@./scripts/kubernetes-local.sh status

kubernetes-local-verify:
	@./scripts/kubernetes-local.sh verify

kubernetes-local-down:
	@./scripts/kubernetes-local.sh down

reference-scanner-smoke:
	@./scripts/reference-scanner-smoke.sh

cargo-contract:
	@CARGO_REQUIRED=1 CARGO_EXPECTED_VERSION=1.96.0 go test -count=1 ./internal/protocol/cargo ./internal/protocol/identity
	@CARGO_REQUIRED=1 go test -count=1 ./internal/app -run 'TestNativeCargoOfficialClientHostedFlow|TestCargoProxyOfficialSourceReplacementOnlineAndOffline|TestCargoGroupOfficialAlternateRegistry'

openapi-dependency-test:
	@bash ./scripts/openapi-dependency-test.sh

openapi-tools-ready:
	@bash ./scripts/check-node-dependencies.sh $(OPENAPI_TOOLS) redocly

console-codegen-ready:
	@bash ./scripts/check-node-dependencies.sh $(CONSOLE_DIR) openapi-ts prettier

openapi-bundle: openapi-tools-ready
	@$(OPENAPI_TOOLS)/node_modules/.bin/redocly bundle $(OPENAPI_SOURCE) --output $(OPENAPI_BUNDLE) --ext json

openapi-generate-admin: openapi-bundle
	@$(OPENAPI_TOOLS)/node_modules/.bin/redocly bundle api/openapi/management-runtime.yaml --output api/openapi/management-runtime-v1.json --ext json
	@./scripts/generate-admin-openapi.sh

openapi-check: openapi-generate-admin console-codegen-ready
	@npm --prefix $(CONSOLE_DIR) run check:api
	@go test ./contracts
	@git diff --exit-code -- $(OPENAPI_BUNDLE) api/openapi/management-runtime-v1.json console/src/client internal/admin/openapi/generated.go

console-build:
	@cd console && npm run build

console-typecheck:
	@cd console && npm run typecheck

console-check:
	@cd console && npm run lint
	@cd console && npm run format:check
	@cd console && npm run i18n:prune:check

console-test:
	@cd console && npm run test:coverage

# The fr-CA release gate (ADR 0014): fails while any fr-CA key is a draft or a
# critical key has not passed the LLM review. Not part of `make test`: it is
# meant to fail on a release candidate until the review has run.
i18n-release-check:
	@node ./scripts/i18n-release-check.mjs

i18n-tooling-test:
	@node --test ./scripts/i18n-draft.test.mjs ./scripts/i18n-release-check.test.mjs ./scripts/i18n-review.test.mjs

console-api-check:
	@cd console && npm run check:api

console-e2e:
	@./scripts/console-e2e.sh

console-visual:
	@./scripts/console-visual.sh

console-visual-update:
	@./scripts/console-visual.sh --update-snapshots=all

up:
	@./scripts/local-dev.sh guard
	@docker compose --env-file .env -f compose.yml up --build --wait --remove-orphans

down:
	@docker compose --env-file .env -f compose.yml down

test:
	@python3 ./scripts/email-compose-check.py --render-only
	@bash ./scripts/readiness-image-test.sh
	@./scripts/integration-isolation-test.sh
	@./scripts/migration-immutability-check-test.sh
	@./scripts/identity-check-test.sh
	@./scripts/local-dev-test.sh
	@./scripts/rustfs-only-contract-test.sh
	@./scripts/run-rustfs-test.sh
	@./scripts/backup-drill-filesystem-test.sh
	@bash ./scripts/openapi-dependency-test.sh
	@./scripts/release-readiness-check.sh
	@./scripts/docs-capability-check.sh
	@node --test ./scripts/i18n-draft.test.mjs ./scripts/i18n-release-check.test.mjs ./scripts/i18n-review.test.mjs
	@python3 -m unittest scripts/maven_proxy_fixture_test.py

ci-local:
	@./scripts/ci-local.sh

ci-local-full:
	@./scripts/ci-local.sh --job all --keep-going

external-network-test-check:
	@python3 ./scripts/check-no-external-network-tests.py

identity-check:
	@./scripts/identity-check.sh

commit-policy-check:
	@node --test ./scripts/commit-policy-check.test.mjs
	@if [ -n "$$TITLE" ]; then node ./scripts/commit-policy-check.mjs --title "$$TITLE"; fi

docs-check:
	@./scripts/docs-capability-check.sh
	@docker run --rm -v "$(CURDIR):/src" -w /src $(GO_IMAGE) sh -ec 'go list ./... | grep -v "/console/node_modules/" | xargs go test'

api-contract:
	@docker run --rm -v "$(CURDIR):/src" -w /src $(GO_IMAGE) go test ./contracts

cli-contract-check:
	@node --test ./scripts/cli-contract-check.test.mjs
	@node ./scripts/cli-contract-check.mjs

cli-test:
	@go test ./cmd/notarxctl/... ./internal/notarxctl/...

cli-e2e:
	@./scripts/cli-e2e.sh

cli-runtime-e2e:
	@python3 ./scripts/cli-runtime-e2e.py

integration-test:
	@./scripts/integration-test.sh

.PHONY: nexus-migration-client-e2e nexus-inventory-scan-e2e nexus-oci-migration-e2e nexus-conan-migration-e2e private-upstream-auth-e2e private-upstream-formats-e2e native-nuget-e2e
nexus-migration-client-e2e:
	@./scripts/nexus-migration-client-e2e.sh

nexus-inventory-scan-e2e:
	@./scripts/nexus-inventory-scan-e2e.sh

nexus-oci-migration-e2e:
	@./scripts/nexus-oci-migration-e2e.sh

nexus-conan-migration-e2e:
	@./scripts/nexus-conan-migration-e2e.sh

.PHONY: nexus-npm-migration-e2e
nexus-npm-migration-e2e:
	@./scripts/nexus-npm-migration-e2e.sh

.PHONY: nexus-pypi-migration-e2e
nexus-pypi-migration-e2e:
	@./scripts/nexus-pypi-migration-e2e.sh

private-upstream-auth-e2e:
	@./scripts/private-upstream-auth-e2e.sh

private-upstream-formats-e2e:
	@./scripts/private-upstream-formats-e2e.sh

native-nuget-e2e:
	@./scripts/native-nuget-e2e.sh

.PHONY: native-helm-e2e
native-helm-e2e:
	@./scripts/native-helm-e2e.sh

.PHONY: native-rubygems-e2e
native-rubygems-e2e:
	@python3 -B -m unittest discover -s scripts/tests -p test_native_rubygems_cleanup.py
	@./scripts/native-rubygems-e2e.sh

.PHONY: native-oci-discovery-e2e
native-oci-discovery-e2e:
	@./scripts/native-oci-discovery-e2e.sh

.PHONY: email-compose-check email-compose-render-check
email-compose-check:
	@python3 ./scripts/email-compose-check.py

email-compose-render-check:
	@python3 ./scripts/email-compose-check.py --render-only

integration-down:
	@docker volume create notarx-go-mod >/dev/null
	@docker volume create notarx-go-build >/dev/null
	@docker compose --env-file /dev/null --project-name notarx-integration -f compose.integration.yml down -v --remove-orphans

spellcheck:
	@./scripts/spellcheck.sh

architecture-check:
	@go run ./tools/architecture
	@./scripts/check-no-anonymous-read.sh
	@go test ./tools/architecture
	@python3 scripts/check-no-cjk-go.py
	@python3 -B -m unittest discover -s scripts/tests -p test_check_no_cjk_go.py
	@python3 -B -m unittest discover -s scripts/tests -p test_check_no_chinese.py

sqllint-check:
	@go run ./tools/sqllint/cmd
	@go test ./tools/sqllint/...

architecture-metrics:
	@go run ./tools/architecture -metrics

lint: architecture-check sqllint-check commit-policy-check external-network-test-check identity-check spellcheck
	@docker run --rm -v "$(CURDIR):/src" -w /src $(LINT_IMAGE) golangci-lint run

vet:
	@packages="$$(go list ./... | grep -v '/console/node_modules/')"; go vet $$packages

race:
	@go test -race ./internal/...

coverage:
	@./scripts/coverage-check.sh

dependency-audit:
	@node --test scripts/npm-audit-policy.test.mjs
	@go run golang.org/x/vuln/cmd/govulncheck@v1.10.0 ./...
	@node ./scripts/npm-audit-check.mjs console tools/openapi

fmt:
	@docker run --rm -v "$(CURDIR):/src" -w /src $(GO_IMAGE) gofmt -w cmd internal

cli-build:
	@mkdir -p dist
	@set -eu; goos=$$(go env GOOS); goarch=$$(go env GOARCH); \
		CGO_ENABLED=0 go build -trimpath -buildvcs=false \
			-ldflags '$(CLI_LDFLAGS)' -o dist/notarxctl_$${goos}_$${goarch} ./cmd/notarxctl; \
		printf 'built dist/notarxctl_%s_%s (version %s)\n' "$$goos" "$$goarch" "$(VERSION)"

build:
	@docker build -t notarx:dev .

docker-build:
	@docker build -t notarx:dev .

docker-test:
	@docker build --target test -f Dockerfile .
	@docker build --target test -f Dockerfile.apt-signer .
	@docker build --target test -f Dockerfile.scanner .

console-docker-build:
	@docker build -f Dockerfile.console -t notarx-console:dev .

cli-docker-build:
	@docker build -f Dockerfile.notarxctl \
		--build-arg VERSION="$(VERSION)" \
		--build-arg REVISION="$(CLI_REVISION)" \
		-t notarxctl:dev .

migrate:
	@docker compose --env-file .env -f compose.yml run --rm migrate

backup-drill:
	@./scripts/backup-drill.sh

restore-drill:
	@./scripts/restore-drill.sh "$(BACKUP_DIR)"

migration-check:
	@./scripts/migration-immutability-check.sh

preflight: commit-policy-check native-build-check migration-check
	@docker compose --env-file .env -f compose.yml run --rm --no-deps notarx preflight run --format json

evidence:
	@test -n "$(NOTARX_URL)" || { printf '%s\n' 'set NOTARX_URL'; exit 2; }
	@test -n "$(EVIDENCE_OUTPUT)" || { printf '%s\n' 'set EVIDENCE_OUTPUT'; exit 2; }
	@test -n "$$NOTARX_EVIDENCE_ADMIN_TOKEN" || { printf '%s\n' 'set NOTARX_EVIDENCE_ADMIN_TOKEN'; exit 2; }
	@go run ./cmd/notarx evidence collect --gateway-url "$(NOTARX_URL)" --output "$(EVIDENCE_OUTPUT)" --revision "$(GIT_REVISION)" --image "$(IMAGE_DIGEST)"

raw-e2e:
	@./scripts/raw-e2e.sh

conan-e2e:
	@./scripts/native-conan-e2e.sh

native-maven-e2e:
	@./scripts/native-maven-e2e.sh

native-oci-e2e:
	@./scripts/native-oci-e2e.sh

native-raw-e2e:
	@./scripts/native-raw-e2e.sh

native-npm-e2e:
	@./scripts/native-npm-e2e.sh

native-pypi-e2e:
	@./scripts/native-pypi-e2e.sh

native-go-e2e:
	@./scripts/native-go-e2e.sh

native-apt-e2e:
	@./scripts/native-apt-e2e.sh

apt-signer-rotation-e2e:
	@./scripts/apt-signer-rotation-e2e.sh

readiness-e2e:
	@./scripts/readiness-e2e.sh

resolver-rotation-e2e:
	@./scripts/resolver-rotation-e2e.sh

service-account-rotation-e2e:
	@./scripts/service-account-rotation-e2e.sh

oci-performance-e2e:
	@./scripts/oci-performance-e2e.sh

performance-baseline:
	@./scripts/performance-baseline.sh

cache-operations-e2e:
	@./scripts/cache-operations-e2e.sh

backup-restore-readiness:
	@./scripts/backup-restore-readiness.sh

upgrade-readiness:
	@./scripts/upgrade-readiness.sh

release-readiness-check:
	@./scripts/release-readiness-check.sh

release-version-check:
	@./scripts/release-version-check.sh $(if $(EXPECT_VERSION),--expect $(EXPECT_VERSION))

stamp-version-test:
	@./scripts/stamp-version-test.sh

release-plan-test:
	@./scripts/release-plan-test.sh

# Release pipeline pieces that cannot run a real release (ADR 0024): manifest and attestation
# verification, retention (releases, tags, GHCR mark and sweep) and repository governance.
release-pipeline-test:
	@./scripts/release-manifest-test.sh
	@./scripts/prune-releases-test.sh
	@node --test ./scripts/prune-ghcr.test.mjs
	@./scripts/repo-governance-apply-test.sh

# Structure of release.yml; needs mikefarah yq (preinstalled on hosted runners).
release-workflow-check:
	@./scripts/release-workflow-check.sh

release-artifacts:
	@test -n "$(RELEASE_VERSION)" || { printf '%s\n' 'set RELEASE_VERSION'; exit 2; }
	@test -n "$(RELEASE_REVISION)" || { printf '%s\n' 'set RELEASE_REVISION'; exit 2; }
	@./scripts/build-release-artifacts.sh --version "$(RELEASE_VERSION)" --revision "$(RELEASE_REVISION)" --output "$${RELEASE_OUTPUT:-dist}"

release-artifacts-test:
	@./scripts/build-release-artifacts-test.sh

# Native packages (ADR 0025): build the three packages for this Linux host from the release
# archives and assert contents, modes, Recommends, Version/Release and maintainer scripts.
# Needs dpkg-deb, rpm, cpio and network access for the pinned nfpm.
package-check:
	@./scripts/package-check.sh

# Installs the packages built by package-check in debian:12 and rockylinux:9 containers and
# drives notarx-migrate against a PostgreSQL 16 container (needs Docker).
package-install-test:
	@dir=$(mktemp -d) && ./scripts/package-check.sh --output "$dir"; rc=$?; \
	if [ $rc -eq 0 ]; then ./scripts/package-install-test.sh --packages "$dir/0.2.0-nightly.3"; rc=$?; fi; \
	rm -rf "$dir"; exit $rc

packages-signing-drill:
	@./scripts/packages-signing-drill.sh

packages-environments-test:
	@./scripts/check-packages-environments-test.sh

# Explicit opt-in real transfer fixture; no server .env or existing data targets.
backup-transfer-test:
	@BACKUPOPS_DOCKER_TEST=1 BACKUPOPS_DOCKER_CONTEXT="$$(docker context show)" go test -v -count=1 -timeout 60m ./internal/backupops -run '^(TestLocalRecoveryIntegration|TestLocalReleaseUpgrade|TestFilesystemExport.*|TestEmptyFilesystemExportAndRestore|TestRestoreFilesystemObjects.*|TestBackupCLIRestoresObjects.*)$$'

.PHONY: oci-free-tier-check oci-free-tier-images-check oci-free-tier-smoke oci-free-tier-tofu-check
# Reference deployment for one OCI Always Free A1 VM (ADR 0017, deploy/oci-free-tier).
# The check needs docker compose and PyYAML; the images check needs buildx and the
# network; the smoke starts the stack, so it needs the images (docs/deployment/oci-free-tier.md).
oci-free-tier-check:
	@python3 ./scripts/oci-free-tier-check-test.py
	@./scripts/oci-free-tier-config-check.sh

oci-free-tier-images-check:
	@./scripts/oci-free-tier-images-check.sh

oci-free-tier-smoke:
	@./scripts/oci-free-tier-smoke.sh

# OpenTofu module (optional): needs tofu and tflint on the PATH and the registry.
oci-free-tier-tofu-check:
	@./scripts/oci-free-tier-tofu-check.sh

.PHONY: published-native-backup-test
published-native-backup-test:
	@./scripts/published-native-backup-test.sh --recovery

.PHONY: architecture-check sqllint-check architecture-metrics spellcheck native-build-check

# ADR 0016: no cross-compilation or emulation anywhere. The guard is self-tested
# against negative fixtures first, then run over the tree (also part of preflight).
native-build-check:
	@./scripts/native-build-check-test.sh
