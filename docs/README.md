# Artifact Gateway documentation

[简体中文](README.zh-CN.md) · [Project README](../README.md)

Artifact Gateway is a lightweight, protocol-native artifact repository. Its
control plane relies on PostgreSQL as its only coordination and database
dependency; immutable bytes live in S3-compatible object storage.

This index follows the framework-neutral navigation contract in
[`site-map.json`](site-map.json). Capability claims are current only when they
agree with executable tests and the
[protocol compatibility baseline](protocol-compatibility.md).

- [Nexus source discovery and native mapping](nexus-source-discovery.md)
- [Durable Nexus migration batches](nexus-migration-batches.md)
- [Console Nexus migration and acceptance observations](nexus-migration-console.md)

## Start here

- [Project overview](../README.md) — positioning, capabilities, quick start, and lightweight deployment boundary.
- [CI path classes and the ci-ok gate](ci.md) — which jobs a change triggers and the single required check.
- [Native builds, runner matrix and local builds](native-builds.md) — no cross-compilation or emulation, the runner matrix, Apple Silicon local builds and the native-build guard.
- [Documentation site guide](documentation-site-guide.md) — locale, route, navigation, and future site-generator contract.
- [Getting started](getting-started.md) — complete local stack and first Repository.
- [Product direction](../PRODUCT.md) — users, use cases, product boundary, and priorities.
- [Contributing](../CONTRIBUTING.md) — change workflow, documentation conventions, and required checks.
- [Format extension guide](format-extension-guide.md) — admission requirements for a new package ecosystem.

## Architecture and design

- [Architecture](../ARCHITECTURE.md) — runtime, storage, consistency, and code ownership.
- [Architecture diagrams](architecture-diagrams.md) — system, deployment, publication, and Worker flows.
- [Design system](../DESIGN.md) — Console visual language, components, layout, and interaction rules.
- [Domain language](../CONTEXT.md) — canonical Repository, Artifact, lifecycle, and distribution terminology.
- [Object storage configuration](object-storage.md) — drivers, `NOTARX_OBJECT_STORE_*` keys, checksum modes, and OCI settings.
- [PostgreSQL capabilities](postgresql-capabilities.md) — locks, leases, queues, notifications, JSONB, search, and observability.
- [Native Hosted contract](native-hosted-contract.md) — metadata authority, object lifecycle, transactions, and idempotency.
- [V2 contract](v2-contract.md) — historical Raw, Conan 2, anonymous-policy, and migration context.
- [Artifact lifecycle contract](artifact-lifecycle-contract.md) — visibility, Tombstone, restore, collection, promotion, and replication.
- [Repository deletion contract](repository-deletion-contract.md) — safe logical deletion and recovery semantics.
- [Distributed deployment](distributed-deployment.md) — API, Scheduler, Worker, PostgreSQL, and object-store topology.
- [ADR 0001: full artifact repository](adr/0001-full-artifact-repository.md)
- [ADR 0002: promotion snapshots](adr/0002-promotion-snapshots.md)
- [ADR 0003: protocol-only formats](adr/0003-protocol-only-formats.md)
- [ADR 0004: Go Hosted publication](adr/0004-go-hosted-publication.md)
- [ADR 0005: Console semantic theme system](adr/0005-console-semantic-theme-system.md)
- [ADR 0006: account levels and per-repository authority](adr/0006-account-levels-and-repository-grants.md)
- [ADR 0007: notarxctl CLI shape and cross-cutting contract](adr/0007-agctl-cli-shape.md)
- [ADR 0008: Go module path follows the source repository](adr/0008-go-module-path.md)
- [ADR 0009: Native management architecture](adr/0009-native-architecture.md)
- [ADR 0010: Memory store contracts](adr/0010-memory-store-contracts.md)
- [ADR 0011: Explicit format modules and Raw tracer](adr/0011-format-modules.md)
- [ADR 0012: Management API domain composition](adr/0012-management-domain-composition.md)
- [ADR 0013: notarx Fork Charter](adr/0013-notarx-fork-charter.md)
- [ADR 0014: Locale Policy: en-CA Reference, fr-CA First-Class, zh-CN Secondary](adr/0014-locale-policy.md)
- [ADR 0015: Single Baseline Migration](adr/0015-single-baseline-migration.md)
- [ADR 0016: Native Per-Architecture Builds](adr/0016-native-architecture-builds.md)
- [ADR 0017: Runtime Backends and the Oracle Cloud Free Tier Reference Deployment](adr/0017-runtime-backends-and-oci-free-tier.md)
- [ADR 0018: Human Identity Providers: OIDC and SAML Registry, SCIM Provisioning, Groups](adr/0018-identity-providers-saml-scim.md)
- [ADR 0019: Workload Identity Federation (Inbound)](adr/0019-workload-identity-federation.md)
- [ADR 0020: notarx as an OIDC Token Issuer](adr/0020-notarx-oidc-issuer.md)
- [ADR 0021: Rule-Based Promotion and Repository Chains](adr/0021-promotion-rules-and-repository-chains.md)
- [ADR 0022: Database Portability: MySQL 8.4 (HeatWave) as Second Backend](adr/0022-database-portability-mysql-heatwave.md)
- [ADR 0023: Data Model Scalability: Write Decoupling, Partitioning, Retention, Counters and Caches](adr/0023-data-model-scalability.md)
- [ADR 0024: Release Versioning, Conventional Commits and Nightly-to-Stable Promotion](adr/0024-release-versioning-and-conventional-commits.md)
- [ADR 0025: Distribution Channels: Images, Archives, Native Packages and Signed Repositories](adr/0025-distribution-channels.md)
- [ADR 0026: Debian and RPM Repositories](adr/0026-debian-and-rpm-repositories.md)
- [ADR 0027: Hugging Face Model and Dataset Repositories](adr/0027-hugging-face-repositories.md)
- [ADR 0028: Generic Versioned Packages on Raw](adr/0028-generic-versioned-packages.md)
- [ADR 0029: Repository Visibility: Public and Private Feeds](adr/0029-repository-visibility-public-private-feeds.md)
- [ADR 0030: SBOM Generation and Vulnerability Scanning](adr/0030-sbom-generation-and-vulnerability-scanning.md)
- [Upstream fork point and cherry-pick record](upstream.md) — fork origin, cherry-pick procedure, and picked/declined upstream commits.
- [notarx brand assets](brand-assets.md) — mark, lockups, icons, colour tokens.

## Protocols and formats

- [Protocol compatibility](protocol-compatibility.md) — implemented behaviour and explicit limits by format.
- [OCI Catalog and Referrers](oci-discovery.md) — authorized enumeration, coverage, verified closures and stable source bindings.
- [Conan 2 native publication](conan-native-publication.md) — canonical remote, manifest visibility and client acceptance.
- [PyPI file yanking](pypi-yank.md) — file selection, atomic management, pip and recovery evidence.
- [Raw HTTP ranges](raw-http-ranges.md) — bounded multipart reads and immutable validators.
- [Maven Hosted publication](maven-hosted-publication.md) — Nexus-compatible direct mode and strict opt-in commit.
- [npm version governance](npm-version-governance.md) — native deprecation, unpublish, authority barriers, recovery and immutable bytes.
- [APT Proxy and Group](apt-proxy.md) — byte-preserving reads, caching, authorization, and limits.
- [Package signing key ceremony and environments](release/packages-signing.md) — key layout, custody, S2 certification, rotation, revocation, and the protected environments for `packages.mlops.ca`.
- [APT Hosted signing](apt-hosted-signing.md) — H2 preview, external signer, H3 rotation, and production boundary.
- [APT first publication](apt-first-publication.md) — explicit package staging, identity review and retryable signing for an empty suite.
- [APT snapshot archives](apt-snapshot-archive.md) — signed-byte export, offline integrity checks, trusted same-repository restore, and staged legacy migration.
- [APT Hosted lifecycle](apt-hosted-lifecycle.md) — signed deletion, retention, recovery windows and reference-safe pruning.
- [APT Hosted distribution](apt-hosted-distribution.md) — target-signed promotion, verified replication and Worker recovery.
- [APT Hosted roadmap](apt-hosted-roadmap.md) — ordered H1-H4 acceptance gates.
- [Cargo client setup](cargo-usage.md) — Hosted, Proxy, and Group registry configuration and commands.
- [Cargo repository research](cargo-repository-research.md) — sparse registry decisions and C0-C4 implementation record.
- [Helm HTTP preview](helm-hosted-preview.md) — default-off Hosted publication, Proxy caching, Group source binding and verified cold consumption.
- [NuGet Hosted preview](nuget-hosted-preview.md) — default-off native publication and verified restore.
- [NuGet Proxy and Group preview](nuget-proxy-group-preview.md) — V3 source discovery, persistent authority and private verified caches.
- [NuGet roadmap](nuget-roadmap.md) — preview acceptance and remaining lifecycle/admission work.
- [NuGet durable replication preview](nuget-replication-preview.md) — fixed Hosted plans, fenced workers, verified target-owned packages and exact item retry.
- [RubyGems Hosted preview](rubygems-hosted-preview.md) — default-off native publication, compact/legacy dependency indexes and verified original gems.
- [RubyGems Hosted replication preview](rubygems-replication-preview.md) — default-off durable plans, immutable variants, fenced publication and cold client verification.
- [Legacy Group migration](legacy-group-migration.md) — compatibility behaviour and migration guidance.
- [Gitea backend reference](gitea-backend-reference.md) — preflight and evidence-loop design reference.

## Operations and security

- [Administrator test email delivery](email-notification-delivery.md) — opt-in TLS SMTP, encrypted targets, durable synthetic tests and delivery limits.
- [Repository logical quota alerts](repository-quota-alerts.md) — explicit sustained thresholds, durable evidence and delivery through the existing email worker.
- [Operational signal sources](operational-signals.md) — existing signals and opt-in administrator local-capacity diagnostics with namespace, freshness and failure boundaries.
- [Runtime log product design](runtime-log-product-design.md) — proposal for API/Worker log classification, search, durable storage, and deployment acceptance.
- [Kubernetes deployment](kubernetes-deployment.md) — local executable baseline and production requirements.
- [Recovery runbook](recovery-runbook.md) — backup, restore, RPO/RTO evidence, and rollback.
- [Historical Maven SNAPSHOT import](maven-snapshot-import.md) — controlled frozen-bundle admission, exact historical identities and source current selectors, capacity and checkpoint recovery.
- [Migration capacity preflight](migration-capacity-preflight.md) — offline logical and physical budget calculation for fixed inventory evidence.
- [Offline backup manifest verification](backup-manifest-verification.md) — private local byte verification and explicitly unproven snapshot consistency.
- [Anonymous access operations](anonymous-access-operations.md) — default-deny global, Group, and Repository gates.
- [Local user governance](user-governance.md) — accounts, sessions, lockout, OIDC linkage, and audit.
- [OIDC browser SSO](oidc-sso.md) — Code + PKCE, runtime configuration, role mapping, and session behaviour.
- [Keycloak on Kubernetes](oidc-keycloak-k8s.md) — real browser callback acceptance.
- [Repository Grant authorization](repository-grant-authorization-plan.md) — scoped runtime decisions and rollout.
- [Service Account operations](service-account-operations.md) — stable machine principals and zero-downtime rotation.
- [notarxctl CLI](cli.md) — client contract for CI, platform management, and agents: configuration precedence, exit codes, JSON envelope, and the current introspection commands.
- [notarxctl command map](cli-command-map.md) — every management and native protocol operation classified as first-release, later, or explicitly not applicable, with permissions and acceptance codes; guarded by `make cli-contract-check`.
- [Security admission policy](security-admission-policy.md) — promotion evidence, Quarantine, and optional read enforcement.
- [Artifact scanner contract](artifact-scanner-contract.md) — bounded external scanning, health, evidence, and reconciliation.
- [Proxy egress design](proxy-egress-design.md) — direct, environment, HTTP, and SOCKS5 per-Repository routing.
- [Webhook delivery contract](webhook-delivery-contract.md) — transactional events, HMAC, retries, and replay.
- [Security policy](../SECURITY.md) — private reporting and deployment baseline.

## Quality, performance, and release

- [Performance baseline](performance-baseline.md) — binary/image size, quiet memory, concurrency, and large-object evidence.
- [Artifact contains search evidence](reports/artifact-contains-search-2026-10-12.md) — isolated PostgreSQL query plans, short-fragment cost and pagination budgets.
- [Project quality assessment](project-quality-assessment.md) — strengths, risks, and improvement priorities.
- [Backend completion checklist](backend-completion-checklist.md) — V1 implementation status and next slices.
- [Release readiness](release-readiness.md) — executable protocol, integration, upgrade, and recovery gates.
- [Release record template](release-record-template.md) — evidence, approval, and rollback fields.
- [Preparation record: 2026-08-11](release-records/2026-08-11-d738d4ed.md) — historical local candidate evidence, not production approval.
- [Candidate record: 2026-09-08](release-records/2026-09-08-cb59b227.md) — fixed Group candidate, distribution upgrade/restore evidence, and remaining registry access gate.
- [Changelog](../CHANGELOG.md) — stable releases, generated from conventional commits.

## Strategy and reference

- [Full repository goal](full-artifact-repository-goal.md) — V1 definition of done.
- [Full repository roadmap](full-artifact-repository-roadmap.md) — architecture sequence and current status.
- [Nexus gap analysis](nexus-gap-analysis.md) — current cross-product capability and experience comparison.
- [Nexus gap review, 2026-08](nexus-gap-review-2026-08.md) — historical point-in-time review.
- [Nexus Maven publication research](nexus-maven-publication-research.md) — primary-source direct versus staging behaviour.
- [Repository Console roadmap](repository-console-experience-roadmap.md) — browse, capacity, Proxy operations, and policy experience.
- [OpenAPI governance](openapi-governance-plan.md) — source, generation, runtime, and review boundaries.

## Documentation rules

- English uses unsuffixed `.md`; Simplified Chinese uses `.zh-CN.md`.
- Every page pair links both ways and appears once in `site-map.json` with localized titles.
- Both locales preserve commands, routes, status codes, compatibility limits, security boundaries, and delivery status.
- Research, preview, historical evidence, and roadmap work stay explicitly labelled.
- Run `make docs-check` before submitting documentation changes.
