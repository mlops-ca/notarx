# notarx Fork Charter

[Documentation index](../README.md)

Status: accepted (2026-11-17)

Supersedes: [ADR 0008](0008-go-module-path.md)

Amends: [ADR 0007](0007-agctl-cli-shape.md) (CLI binary name `agctl` becomes `notarxctl`; the
CLI shape and cross-cutting contract are unchanged)

## Context

This repository is a hard fork of `ccsert/artifact-gateway` at version `0.9.0`
(`VERSION`). It is renamed **notarx** and owned by mlops.ca, a sub-brand of
Kharkevich Engineering Lab (Canada). The identity of the upstream project is
embedded in many places:

- `go.mod` declares `module github.com/ccsert/artifact-gateway` (ADR 0008).
  923 tracked files contain the string `ccsert`: Go imports, `Makefile:15`
  (`CLI_LDFLAGS` with `-X .../internal/buildinfo.injectedVersion`), the
  `Dockerfile` and `Dockerfile.agctl` ldflags, 28 references to `ghcr.io/ccsert`
  in compose files, docs and deploy manifests, `README*`, `CHANGELOG*`.
- Binaries: `cmd/gateway` (server), `cmd/agctl` (CLI); release archives are
  `artifact-gateway_<v>_<os>_<arch>` and `artifact-gateway-agctl_<v>_...`
  (`scripts/build-release-artifacts.sh:138,188`).
- `scripts/release-version-check.sh:32-56` couples the module path, the
  documented `go install <module>/cmd/agctl@` command and the source repository.
- Configuration uses the `GATEWAY_` prefix in 249 files.
- The console title is "Artifact Gateway Console" (`console/index.html:26`); its
  default themes are `gateway-dark/light` and `aerok-dark/light`
  (`console/src/lib/theme/defaultConsoleThemes.ts`), on the semantic theme system
  of [ADR 0005](0005-console-semantic-theme-system.md); a root `themes/`
  directory currently holds only the theme JSON schema and a README (no theme
  files). `docs/assets/` holds three upstream-branded PNGs.
- `LICENSE` is MIT, "Copyright (c) 2026 ccsert and Artifact Gateway contributors".
- Contribution rules require Chinese issue text
  (`.agents/skills/issue-authoring/SKILL.md:11`), Chinese area words in titles
  (`CONTRIBUTING.md:109`) and a Chinese twin for every Markdown page
  (`CONTRIBUTING.md:298-300`). `scripts/docs-i18n-check.mjs` has already been
  changed in the working tree to make `.zh-CN.md` companions optional (ADR 0014
  records that step).
- The mlops.ca brand (maintained in the separate mlops.ca website repository,
  not citable from this repository, so the tokens below are copied here and this
  ADR is their source for notarx): ink `#0b0c0a`, signal-lime `#c8f135` (fills only; `--signal-text` `#3f5c00` on
  light), Bricolage Grotesque and JetBrains Mono, 2px radius, 1px hairlines.

Later ADRs ([0014](0014-locale-policy.md) locales, [0015](0015-single-baseline-migration.md)
baseline, [0016](0016-native-architecture-builds.md) builds,
[0017](0017-runtime-backends-and-oci-free-tier.md) backends,
[0018](0018-identity-providers-saml-scim.md) identity providers,
[0019](0019-workload-identity-federation.md) workload identity,
[0020](0020-notarx-oidc-issuer.md) issuer,
[0021](0021-promotion-rules-and-repository-chains.md) promotion) build on the
identity chosen here. See also the format and platform ADRs of the roadmap:
[0022](0022-database-portability-mysql-heatwave.md) MySQL, [0023](0023-data-model-scalability.md) scale,
[0024](0024-release-versioning-and-conventional-commits.md) releases, [0025](0025-distribution-channels.md) channels,
[0026](0026-debian-and-rpm-repositories.md) APT and RPM, [0027](0027-hugging-face-repositories.md) Hugging Face,
[0028](0028-generic-versioned-packages.md) versioned generic packages,
[0029](0029-repository-visibility-public-private-feeds.md) public visibility and
[0030](0030-sbom-generation-and-vulnerability-scanning.md) SBOM and scanning. This ADR owns identity, upstream relationship, branding,
rename mechanics, the shared public-URL setting, the v0.1.0 release scope and the
global slice ordering.

## Considered designs

1. **Soft fork, keep tracking upstream.** Keep the module path and names, merge
   upstream regularly. Lowest rename cost, but the roadmap (multi-IdP, workload
   identity, issuer, per-arch builds, new baseline) diverges structurally from
   upstream within weeks; merges would conflict on every squashed migration and
   renamed env var, and the product could not carry its own name or registry.
2. **Hard fork with full rename and no compatibility layer.** New name, module
   path, images, env prefix, version line; upstream imported only by
   cherry-pick. Highest one-time cost, but no dual-name surface to maintain and
   no deceptive resemblance to the upstream distribution.
3. **Hard fork with compatibility aliases.** As 2, but accept `GATEWAY_*` env
   vars and ship an `agctl` symlink. Smooths migration for upstream users, but
   there are none: the baseline migration (ADR 0015) already removes any upgrade
   path, so aliases would be permanent debt with no consumer.
4. **Rename only the brand, keep Go identifiers.** Cosmetic rename of UI and
   docs, leave `GATEWAY_*` and `internal/` names. Cheap, but leaves two
   vocabularies in logs, config and metrics, which is the worst long-term state.

## Decision

Option 2. The fork is a distinct product; nothing of the upstream identity is
retained except attribution required by the licence.

### Identity

| Item | Upstream | notarx |
| --- | --- | --- |
| Product name | Artifact Gateway | `notarx` (lower case in prose and UI) |
| Go module | `github.com/ccsert/artifact-gateway` | `github.com/mlops-ca/notarx` |
| Server binary / `cmd/` dir | `gateway` / `cmd/gateway` | `notarx` / `cmd/notarx` |
| CLI binary / `cmd/` dir | `agctl` / `cmd/agctl` | `notarxctl` / `cmd/notarxctl` |
| Images | `ghcr.io/ccsert/artifact-gateway`, `artifact-gateway-console` | `ghcr.io/mlops-ca/notarx`, `ghcr.io/mlops-ca/notarx-console`, `ghcr.io/mlops-ca/notarx-scanner` (ADR 0030, optional in every profile); these are the only three published images |
| Release archives | `artifact-gateway_<v>_<os>_<arch>` | `notarx_<v>_<os>_<arch>`, `notarxctl_<v>_<os>_<arch>`, and `notarx-signer_<v>_<os>_<arch>` (ADR 0026; `cmd/notarx-signer`, new; a release archive, not an image; the `pkcs11` build tag is not in the default archive) |
| Scanner | `cmd/reference-scanner`, `Dockerfile.scanner`, make target `reference-scanner-smoke`, volumes `gateway-scanner-sboms` and `gateway-trivy-cache` | `cmd/notarx-scanner`, `Dockerfile.notarx-scanner`, the smoke target renamed to match, volume `notarx-trivy-cache` (the SBOM volume goes away); ADR 0030 slice 1b |
| Env prefix | `GATEWAY_` | `NOTARX_`, **no aliases, no fallback reads** |
| Versions | `0.9.0` | `VERSION` carries the development value `0.0.0-dev`; the first stable tag is `v0.1.0` and versions are stamped at build time (ADR 0024); `CHANGELOG.md` restarts with a "forked from upstream 0.9.0 (commit <sha>)" preamble |
| Buildinfo | `internal/buildinfo` | same package, new import path in `-X` flags |
| CLI internals | `internal/agctl`, `.agents/skills/agctl` | `internal/notarxctl`, `.agents/skills/notarxctl` |
| Metrics, ledger, metadata | `gateway_*` metric prefix, `artifact_gateway_*` tables, `artifact-gateway-sha256` metadata key | `notarx_*`, `notarx_*`, `notarx-sha256`; tables are renamed by ADR 0015, metrics and metadata keys by slice 6 |

Two images are published today (`ci.yml:253-255`, `release.yml:78-80`;
`release.yml:36` requires exactly two `mainline-images` runs). ADR 0030 slice 1b adds a
third and last one, `notarx-scanner`, and `release.yml` then requires three runs (ADR 0016 and 0024
own the workflow change). There is no CLI image: `notarxctl` is distributed as release archives and
through `go install github.com/mlops-ca/notarx/cmd/notarxctl@<version>`.
`Dockerfile.agctl` and the apt reference signer (an unpublished test fixture) are not published; adding
another image is a separate decision with its own ADR. Helper binaries (`cmd/healthcheck`,
`cmd/reference-*` other than the scanner, `cmd/native-*-fixture`) keep their names; only their imports
change.

The organization `mlops-ca`, the repository `github.com/mlops-ca/notarx` and the GHCR namespace
`ghcr.io/mlops-ca` are final and provisioned (decided 2026-11-17); the module path and image names
are not reopened after `v0.1.0`.

A grep gate enforces the rename permanently: `scripts/identity-check.sh` fails
`make lint` when `ccsert`, `artifact-gateway`, `artifact_gateway`, `GATEWAY_`,
`agctl` or the `gateway_` metric prefix appear in tracked files. The allow-list
is exactly: `LICENSE`, `NOTICE`, `docs/upstream.md`, `CHANGELOG*`, `docs/adr/**`
(ADRs quote history and old names, including ADR 0007's filename), and
`*.zh-CN.md` (legacy twins, not a gate per ADR 0014). The wire header spellings `X-Artifact-Signer-*`
and `X-Artifact-*` of the signer contract (`api/openapi/protocols/signer.yaml`, `internal/signing`,
`internal/refaptsigner`; ADR 0026) are protocol names, not identity strings, and are not flagged by
the patterns above. ADR 0030 slice 1a extends the gate to fail on `aquasec/trivy:` references without
`@sha256:`. The gate is introduced
last (slice 9), after every rewrite it checks has landed.

### Relationship with upstream

- Upstream is **not merged**: no `git merge`, no standing `upstream` remote
  requirement. The fork keeps upstream history up to `0.9.0`, so `git blame` and
  licence provenance stay intact.
- **Selective cherry-pick only.** A change is cherry-picked when it is a security
  fix, or a bug fix in a code path that still exists unchanged in notarx. Each
  pick is a commit made with `git cherry-pick -x`, followed by the mechanical
  identity rewrite in the same commit, and carries a trailer
  `Upstream: ccsert/artifact-gateway@<sha>` plus the original
  `Co-authored-by:` of the author. Features are re-implemented, not picked,
  because the migration baseline and auth model differ.
- `docs/upstream.md` records the fork point (upstream `0.9.0`, commit hash) and a
  table of picked/declined upstream commits. Upstream security advisories are
  reviewed at each notarx minor release.
- **Licence.** The MIT licence stays. `LICENSE` keeps the upstream line and adds
  `Copyright (c) 2026 mlops.ca (Kharkevich Engineering Lab) and notarx
  contributors`. A `NOTICE` file states the fork origin and that "Artifact
  Gateway" and "ccsert" are not endorsements. Per-file headers are not added.
- notarx does not claim to be, or interoperate with the release channel of,
  upstream; its documentation never links to upstream installers.

### Branding

- Brand source is mlops.ca. A short `TRADEMARKS.md` (added before `v0.1.0`; registration is deferred) states
  that nominative use of the notarx name and mlops.ca marks is allowed and that modified builds and forks
  must not use the name or logo; `LICENSE-ASSETS` links to it. Assets live in `docs/assets/brand/`: `notarx-mark.svg`,
  `notarx-lockup-dark.svg`, `notarx-lockup-light.svg`, `favicon.svg`,
  `favicon-32.png`, `apple-touch-icon.png`, `og-image.png` (1200x630), and a
  `README.md`-free `LICENSE-ASSETS` line stating the assets are MIT with the
  repository except that the mlops.ca marks are not licensed for use as another
  project's identity. The old `docs/assets/artifact-gateway-*.png` are replaced
  with regenerated notarx diagrams.
- Tokens: ink `#0b0c0a`, surface `#161812`, text `#ecebe3`, muted `#9ca091`;
  light `#edece3` / `#f6f5ee` / `#12140e`; signal-lime `#c8f135` used for fills
  only with ink text on top, and `#3f5c00` as accent text on light. Danger
  `#ff7a52` dark / `#b0310f` light. Fonts: Bricolage Grotesque (headings,
  700-800) and JetBrains Mono (labels, numbers, code), both self-hosted from the
  console bundle (no third-party font CDN, preserving air-gapped operation).
  Radius 2px; hairline 1px borders; no shadows except overlays.
- The console default theme becomes `notarx-dark` and `notarx-light` through the
  existing ADR 0005 theme system, defined in
  `console/src/lib/theme/defaultConsoleThemes.ts` and the semantic role mapping in
  `builtInThemeRoles.ts`; the `gateway-*` and `aerok-*` built-ins are removed (no
  stored-preference migration, since there are no upgraders). Contrast
  (WCAG AA) of every role pair is asserted in
  `defaultConsoleThemes.test.ts`. Default is `prefers-color-scheme`, dark if
  unspecified.
- User-visible strings use the product name through a single message variable
  (`productName`), not literals, so that i18n catalogs (ADR 0014) never embed it.

### Contribution rules

- Issues, PRs, commit messages, code comments and docs are **English**
  (en-CA spelling, per ADR 0014). `.agents/skills/issue-authoring/SKILL.md:11`
  and the `查重` step headings are rewritten in English; `CONTRIBUTING.md:109`
  switches the Chinese area words to `Auth:`, `Audit:`, `Platform:`, `Ops:`.
  Labels are unchanged. Issue templates in `.github/ISSUE_TEMPLATE/` are
  translated.
- The mandatory Chinese twin rule (`CONTRIBUTING.md:298-300`) is replaced by the
  locale policy of ADR 0014; new docs need no `.zh-CN.md` twin.
- Conventional Commits stay. A commit that cherry-picks upstream carries the
  trailers above.

### Environment variables

Every `GATEWAY_*` setting is renamed 1:1 to `NOTARX_*` (slice 4). This ADR owns
the cross-ADR settings below; the others are defined by the ADR that introduces them.

| Key | Meaning | Used by |
| --- | --- | --- |
| `NOTARX_PUBLIC_URL` | Externally visible base URL of this instance (absolute `https://` URL, no trailing slash; `http://` only for loopback). Empty means features that need it are disabled or refuse to start. | ADR 0018 (SAML ACS and metadata URLs, SCIM base), ADR 0019 (default token audience), ADR 0020 (`NOTARX_ISSUER_URL` defaults to it) |
| `NOTARX_INSTANCE_ID` | Renamed from `GATEWAY_INSTANCE_ID` (`internal/config/config.go:172`). | ADR 0017, ADR 0020 |
| `NOTARX_SETTINGS_ENCRYPTION_KEY` | Renamed from `GATEWAY_SETTINGS_ENCRYPTION_KEY`; read through `internal/secrets` only. Key-ring support is owned by ADR 0018. | ADR 0018, ADR 0020 |
| `NOTARX_EMAIL_DEFAULT_LOCALE` | Default locale tag for new email targets (`en-CA`, `fr-CA` or `zh-CN`; default `en-CA`). Defined by ADR 0014 slice 7; registered here so the table stays the one list of cross-ADR keys. | ADR 0014 |
| `NOTARX_TRUSTED_PROXIES`, `NOTARX_CLIENT_IP_HEADER` | Trusted reverse-proxy CIDRs and the header carrying the client IP; one resolution of the client address for rate limiting and audit. Defined by ADR 0029 slice 6. | ADR 0029, ADR 0019 (IP in audit rows) |
| `NOTARX_APT_SIGNER_*` (renamed from `GATEWAY_APT_SIGNER_*`), `NOTARX_RPM_SIGNER_*`, `NOTARX_SIGNER_*`, `NOTARX_RPM_*` | Signer client and `notarx-signer` settings and RPM format settings. | ADR 0026 |
| `NOTARX_HF_*` | Hugging Face format settings. | ADR 0027 |
| `NOTARX_SCANNER_*` (renamed from `GATEWAY_SCANNER_*`), `NOTARX_RESCAN_*` | Scanner, Trivy database mirror and re-scan settings. | ADR 0030 |

Metric names registered by later ADRs keep the `notarx_` prefix: ADR 0029 adds
`notarx_ratelimited_total`, `notarx_anonymous_egress_bytes_total`, `notarx_anonymous_inflight`,
`notarx_cdn_purge_pending` and `notarx_cdn_purge_failures_total`.

No other ADR may introduce a second public-URL key (`NOTARX_EXTERNAL_URL`,
`NOTARX_PUBLIC_BASE_URL`) or a second encryption key. There is no `NOTARX_ENV`.

Management API paths in every ADR are relative to `/api/v2` (the `servers` entry of
`api/openapi/management.yaml`). Root-mounted protocol endpoints (OIDC discovery,
JWKS, SAML, SCIM, token exchange) are specified under `api/openapi/protocols/`.

### Release scope and global ordering

**v0.1.0 is the foundation release: ADRs 0013, 0014, 0015, 0016 and 0017, plus the
schema-shape slices of ADR 0023 (data-model scalability) and ADR 0029 slices 1-4 and 14 (visibility
enum replacing `anonymous_read`, `internal/access` choke point in three waves, group invariants).** ADRs
0018-0022 and 0026-0030 ship in 0.2.x and later as additive numbered migrations (`000002+`, per
ADR 0015). Other ADRs depend on "ADR 0015 slice <cut-over>", never on ADR 0015 as a
whole. `release.yml` is owned by ADR 0024 (orchestration) and its reusable build workflows by
ADR 0016; slices in this ADR that touch it depend on those slices instead of editing it independently.

| Step | Slices | Notes |
| --- | --- | --- |
| 1 | ADR 0013 slices 1-4 | licence, module, binary, env renames |
| 2 | ADR 0015 cut-over slice (slice 4) | single baseline `000001_baseline.sql`; schema changes after it are `000002+` |
| 3 | ADR 0013 slices 5-8, 11 | deploy/compose/docs references, metrics, brand, contribution rules, `NOTARX_PUBLIC_URL` |
| 4 | ADR 0014, ADR 0016, ADR 0017 | in parallel; ADR 0016 slice 3 edits `release.yml` and the publish jobs of `ci.yml` only to call its reusable workflows until ADR 0024 slice 4 replaces `release.yml` |
| 5 | ADR 0013 slices 9-10 | identity gate and development version (`0.0.0-dev`) with stamping; both depend on ADR 0016 slice 3 so the workflows are already renamed |
| 5a | ADR 0024 slices 1-8 and ADR 0025 | milestone Foundation, before the first release: commit policy, stamping, plan script, nightly and stable pipeline, governance; ADR 0024 slices 4-5 follow ADR 0016 slices 3-4; ADR 0024 slice 2 replaces ADR 0013 slice 10 |
| 5b | ADR 0023 slices 1-10 (milestone Foundation) | schema-shape and write-path work (typed keys, partitioned audit, async audit writer, RetentionJanitor, quota counters, index hygiene, payload shape) lands as numbered migrations after the cut-over and before the re-squash, so it is folded into the final baseline at no upgrade cost; ADR 0023 slices 1-4 may start right after step 2, slices 5-10 after step 4 |
| 5c | ADR 0029 slices 1-4 and 14 (milestone Foundation) | visibility enum (replaces `anonymous_read`), `internal/access` choke point in three waves (slice 14 is the browse, search and lint wave), group invariants; land after the ADR 0015 cut-over and before the re-squash (ADR 0015 slice 7), which depends on them; slice 1 needs ADR 0023 slice 1 |
| 6 | ADR 0015 re-squash (slice 7), then the manual stable dispatch of ADR 0024 slice 9 creates `v0.1.0` | fresh baseline from the same generator, equivalence proof re-run |
| 6b | ADR 0023 slices 11-15 and 17 (milestone Data scale) | runs immediately after the `v0.1.0` tag and before any of ADRs 0018-0022: externalized payloads, read caches (slice 12), queue claiming, keyset and catalogue search, load harness. Slice 12 gates the cache consumers of ADR 0018 slice 3 and ADR 0019 slice 4 (hard dependency, no local-map fallback); slice 17 may start as soon as slice 9 is merged and gates slice 15; slices 11 to 14 are independent of ADRs 0018-0022 and may run in parallel with them once slice 12 has merged |
| 7 | ADR 0019 slice 1 | `internal/jose` |
| 8 | ADR 0018 | secrets key-ring slice first |
| 9 | ADR 0019 remaining slices | |
| 10 | ADR 0020 | slice 1 depends on ADR 0019 slice 1 and the ADR 0018 key-ring; slice 9 (two-instance e2e) trails ADR 0021 slice 8 |
| 11 | ADR 0021 | owns the peer client for notarx-to-notarx push |
| 12 | ADR 0022 | MySQL 8.4 (HeatWave) backend, milestone MySQL HeatWave; ADR 0023 slice 16 (pool sizing and lease rows) follows ADR 0022 slice 6; parallel to ADRs 0018-0021; depends on ADR 0015 slice 4 (cut-over) and ADR 0017 slice 1; its baseline slice waits for ADR 0015 slice 7 (re-squash) |
| 13 | ADR 0028 (milestone Formats) | after `v0.1.0`; generic versioned packages on Raw; slices 1-5, 7 and 8 need only ADR 0015 slice 4 and ADR 0023 slice 1; slice 6 needs ADR 0021 slices 1b and 2b, slice 12 (rule matching) needs ADR 0021 slice 3a; slice 9 needs ADR 0021 slices 7b and 8; slice 11 (anonymous reads through public groups) is milestone Public feeds and needs ADR 0029 slices 2, 5, 7 and 14 |
| 14 | ADR 0027 (milestone Formats) | after `v0.1.0`; Hugging Face repositories; needs ADR 0023 usage-counter slices; the workload-JWT case of slice 6 is gated on ADR 0019 slice 4; slice 12 needs ADR 0021 |
| 15 | ADR 0026 slices 1-14 and 17-21 (milestone Formats) | after `v0.1.0`; signing package and `notarx-signer` (1-3), APT metadata and retention (4-5; slice 5 needs ADR 0023 slice 7), APT GA (6, 17 and 18; needs ADR 0029 slices 2 and 3, which are within the Foundation set, and ADR 0021 slices 2b and 7b; it keeps only the `format.Scanner` hook and does not depend on ADR 0030), RPM (7-13, 19-21) and the conformance matrix (14); slice 3 needs ADR 0025 slice 4 |
| 16 | ADR 0029 slices 5-12 (milestone Public feeds; slice 13, the optional Trivy database feed, is Supply-chain security) | public catalog, rate limiting, cache headers, CDN purge, anonymous audit, presigned redirect, promotion to public, console and docs; after ADR 0023 slices 6 and 14 and ADR 0021 slices 1a, 3a and 5 |
| 17 | ADR 0026 slices 15, 16 and 22 (milestone Public feeds) | dogfooding: notarx publishes its own deb and rpm packages to public feeds on the dedicated instance `feeds.mlops.ca`, nightly first and stable after the ADR 0026 promotion criteria; needs ADR 0026 slices 3, 6, 9 and 14, ADR 0029 slices 1-7 and 14 (public visibility, catalog, rate limit, cache headers), ADR 0019 slices 2-5 (GitHub issuer), ADR 0025 slices 6 and 8 and ADR 0024 slice 4; `packages.mlops.ca` (ADR 0025) stays the bootstrap and fallback source |
| 18 | ADR 0030 (milestone Supply-chain security) | `notarx-scanner` image, SBOM and findings model, Trivy database mirror through the OCI proxy, re-scan, VEX, policy automation; the `.deb` and `.rpm` scanning adapters (slices 9a and 9b) depend on ADR 0026, never the reverse; slice 11 (ML-BOM) depends on ADR 0027; slice 6 needs ADR 0021 slice 1a and slice 8 the ADR 0021 evaluator |

ADR 0013 slices that would edit `release.yml` do not do so; the image and archive
names there change inside ADR 0016 slice 3.

Follow-ups outside this change (ADRs 0006 and 0011 are not edited here): ADR 0006 or its successor defines
`promote:public` as a per-repository grant capability held by the global admin, and the semantics of the
`authenticated` visibility tier (any non-anonymous identity, including ADR 0019 workload tokens; never
grants write); ADR 0011 gets a note that format modules supply `CacheClass(request)` and
`PublicPathPrefixes(repo)` and own no access decisions (ADR 0029); `docs/artifact-scanner-contract.md`
moves to schema v2 in ADR 0030 slice 2b.

### Rename mechanics and order

The rename is a mechanical, scripted rewrite committed as a sequence of small,
individually green PRs before any feature ADR lands, because every later slice
touches files in the same paths:

1. Licence/NOTICE/`docs/upstream.md` (no code).
2. Module path rewrite: `go.mod`, all imports (`gofmt -r`/`sed` through
   `scripts/rename-module.sh`, idempotent), `Makefile:15`, both Dockerfile
   ldflags, and the module-path checks in `scripts/release-version-check.sh`.
3. Binary and directory renames: `git mv cmd/gateway cmd/notarx`,
   `cmd/agctl cmd/notarxctl`, `internal/agctl internal/notarxctl`; Makefile
   targets, Dockerfiles, release script archive names,
   `scripts/release-version-check.sh` (`test -d cmd/agctl`, archive-name grep,
   `go install` check) and `docs/cli*.md`, in the same PR so
   `make release-version-check` stays green.
4. `GATEWAY_` -> `NOTARX_` across Go, compose, deploy, docs and tests. The loader
   adds a startup refusal for a closed list of known legacy names (see Security
   considerations); it does not read them.
5. Images, registry, metric prefix and metadata key, CI/CD references
   (`ghcr.io/mlops-ca/...`, `release.yml` via ADR 0016).
6. Branding: assets, console title/favicon/themes, doc images.
7. Docs and contribution rules.
8. `scripts/identity-check.sh` turned on.
9. `VERSION` -> `0.0.0-dev` and stamping (ADR 0024 slice 2), changelog reset; `0.1.0` is produced by the first stable release, not committed.

`scripts/release-version-check.sh` keeps ADR 0008's invariant (documented
`go install <module>/cmd/notarxctl@` equals the `go.mod` module and, when known,
the source repository `github.com/mlops-ca/notarx`), now against the new names.

## Security considerations

- Dropping aliases means a mis-named variable silently falls back to a default.
  Mitigation: the server refuses to start, naming the new key ("renamed to
  NOTARX_..."), when any process env var is in a **closed list of known legacy
  `GATEWAY_*` names**, derived by a script from the pre-rename config loader
  and committed as `internal/config/legacy_env.go`. It is not a prefix match, so an
  unrelated `GATEWAY_*` variable in the environment is harmless, and it is a refusal
  check, not a compatibility reader.
- Kubernetes injects `<SERVICE>_SERVICE_HOST` and `<SERVICE>_PORT` for a Service
  named `notarx`, which would land in the `NOTARX_` namespace. The pod specs set
  `enableServiceLinks: false` (slice 5), and the loader ignores those two names.
- Image and module provenance: images are published only from `mlops-ca/notarx`;
  signing and attestation are designed and implemented by ADR 0016 (no cosign
  step exists in `.github/workflows` today, so this ADR makes no signing claim).
  Documentation must not reference `ghcr.io/ccsert`, whose content the project does
  not control (typosquat/substitution risk). `identity-check.sh` enforces this.
- Cherry-picks are security-relevant code; each is reviewed as new code, not
  trusted for coming from upstream.
- Trademarks: the notarx name and mlops.ca marks are reserved; the code licence
  does not grant brand use. `TRADEMARKS.md` (slice 7) is the policy of record.

## Consequences

- One coherent vocabulary in config, logs, metrics, images and docs; a single
  clean repository for release automation.
- Large but mechanical diff; open upstream branches and PRs become unusable.
  Doing the rename first keeps conflicts to one wave.
- Existing upstream deployments cannot upgrade in place (also ADR 0015) and must
  re-install and re-ingest.
- Upstream security fixes require active monitoring, not automatic inheritance.
- ADR 0008 is superseded: its "follow the source repository" rule stands with
  the new path; its status line is changed to "superseded by ADR 0013" in slice 1.
  ADR 0007 is amended (CLI name) and gets an "amended by ADR 0013" note in slice 3.
- Existing zh-CN docs remain but stop being a gate (ADR 0014).
- v0.1.0 deliberately excludes identity, issuer and promotion work (ADRs 0018-0021).

## Implementation slices

1. **Licence, NOTICE and upstream record.**
   Scope: `LICENSE`, `NOTICE`, `docs/upstream.md`, `docs/adr/0008-go-module-path.md`
   (status line only), `docs/README.md` index entry.
   - [ ] `LICENSE` retains the upstream copyright line and adds the mlops.ca line
   - [ ] `docs/upstream.md` records the fork commit and the cherry-pick procedure
   - [ ] `make docs-check` passes
   Dependencies: none.

2. **Module path rewrite.**
   Scope: `go.mod`, all `*.go` imports, `Makefile`, `Dockerfile*`,
   `scripts/rename-module.sh`, the module checks in
   `scripts/release-version-check.sh`, generated OpenAPI server/client packages.
   - [ ] `grep -r github.com/ccsert` finds nothing outside the allow-list
   - [ ] `make openapi-check`, `make lint`, `make test`, `make architecture-check` pass
   - [ ] `make release-version-check` passes with the new module path
   Entry checklist (the slice does not start until every item is ticked in the PR description): the
   `mlops-ca` organization exists, `github.com/mlops-ca/notarx` is created, GitHub Actions can write
   packages to `ghcr.io/mlops-ca`, and package visibility is public.
   Dependencies: ADR 0013 slice 1.

3. **Binary, directory and CLI package renames.**
   Scope: `cmd/gateway` -> `cmd/notarx`, `cmd/agctl` -> `cmd/notarxctl`,
   `internal/agctl` -> `internal/notarxctl`, `.agents/skills/agctl` -> `notarxctl`,
   `Makefile`, `Dockerfile`, `Dockerfile.agctl`, `scripts/build-release-artifacts.sh`,
   `scripts/release-version-check.sh` (`test -d`, archive-name grep, `go install`
   check), `docs/cli.md`, `docs/cli.zh-CN.md`, ADR 0007 status note, CLI help/version strings.
   - [ ] `notarx version` and `notarxctl version` print injected version and revision
   - [ ] release archives are named `notarx_*` and `notarxctl_*`
   - [ ] `make test` and `make release-version-check` pass
   - [ ] CLI contract tests under `contracts/` updated and green
   Dependencies: ADR 0013 slice 2.

4. **Environment prefix and legacy refusal.**
   Scope: config loading in `internal/` and `cmd/notarx`, `internal/config/legacy_env.go`
   plus the script that derives it, compose files, `deploy/`, `examples/`, docs, tests.
   - [ ] no `GATEWAY_` outside the allow-list
   - [ ] startup refuses each name in the closed legacy list and ignores unrelated `GATEWAY_*` variables (unit tests)
   - [ ] `make test`, `make lint` and the integration compose lane pass
   Dependencies: ADR 0013 slice 3.

5. **Deployment and documentation image references.**
   Scope: `compose*.yml`, `deploy/` (Service renamed `notarx`,
   `enableServiceLinks: false`), docs references to `ghcr.io/ccsert`, workflows other
   than `release.yml` and the publish jobs of `ci.yml`.
   - [ ] zero `ghcr.io/ccsert` references outside the allow-list and outside the workflows owned by ADR 0016
   - [ ] `make docs-check` and the compose and kubernetes manifest checks pass
   Dependencies: ADR 0013 slice 4. `release.yml` and `ci.yml` publish names change in ADR 0016 slice 3.

6. **Metrics prefix and metadata key.**
   Scope: `gateway_*` metric names, the `artifact-gateway-sha256` metadata key, dashboards
   and alert rules in `deploy/` and docs. Ledger and table names (`artifact_gateway_*`)
   are renamed by ADR 0015, not here.
   - [ ] `gateway_` and `artifact-gateway-sha256` find nothing outside the allow-list
   - [ ] `make test` passes; metrics documentation lists the new names
   Dependencies: ADR 0013 slice 4.

7. **Brand assets and console theme.**
   Scope: `docs/assets/brand/*`, `TRADEMARKS.md`, `console/index.html`, `console/public`,
   `console/src/lib/theme/*`, self-hosted fonts, README hero image.
   - [ ] `notarx-dark`/`notarx-light` registered, set as defaults, AA contrast tests in `defaultConsoleThemes.test.ts`
   - [ ] `make console-test` and `make console-build` pass; page title and favicon are notarx
   - [ ] no external font or image URL in the built console
   - [ ] `LICENSE-ASSETS` states the assets are MIT with the repository except that the mlops.ca marks are not licensed as another project's identity, and links to `TRADEMARKS.md`
   - [ ] `TRADEMARKS.md` exists (nominative use allowed; modified builds and forks must not use the name or logo; no registration claimed) and is linked from `README.md`; `make docs-check` passes
   Dependencies: ADR 0013 slice 3 (names); ADR 0005. Scope also includes `TRADEMARKS.md`.

8. **Contribution rules.**
   Scope: `CONTRIBUTING.md`, `.agents/skills/issue-authoring/SKILL.md`,
   `.github/ISSUE_TEMPLATE/*`.
   - [ ] rules state English-only; area words are English
   - [ ] `make docs-check` passes
   Dependencies: ADR 0013 slice 3; coordinates with ADR 0014 (locale policy text).

9. **Identity gate.**
   Scope: `scripts/identity-check.sh`, `Makefile` lint target, allow-list fixture test.
   - [ ] `identity-check.sh` runs inside `make lint` and passes on the tree
   - [ ] fixture test proves the allow-list is exactly the one in the Decision section
   Dependencies: ADR 0013 slices 2-8, ADR 0015 cut-over slice, ADR 0016 slice 3.

10. **Version reset (development value and stamping).**
    Implemented by ADR 0024 slice 2: `VERSION`, `console/package.json`, `tools/openapi/package.json`,
    lockfiles and OpenAPI `info.version` carry `0.0.0-dev`; releases stamp versions at build
    time and `0.1.0` is created by the first stable dispatch (ADR 0024 slice 9).
    - [ ] every file above carries `0.0.0-dev`; `make release-version-check` passes
    - [ ] changelog preamble names the upstream fork point
    Dependencies: ADR 0013 slices 1-9, ADR 0024 slice 2. The tag `v0.1.0` is created only after ADR 0015's re-squash slice.

11. **`NOTARX_PUBLIC_URL` setting.**
    Scope: `internal/config`, config reference docs.
    - [ ] value validated (absolute `https://` URL, loopback `http://` allowed, no trailing slash); empty is allowed
    - [ ] exposed to consumers through one typed config accessor; unit tests cover rejection cases
    Dependencies: ADR 0013 slice 4.

## Resolved questions

**Q1 — Is `github.com/mlops-ca/notarx` the final name and is `ghcr.io/mlops-ca` provisioned.** Decision: confirmed final; the GHCR namespace is provisioned now and its readiness is the slice 2 entry checklist. Module path and image names are very hard to change after v0.1.0.

**Q2 — Separate trademark policy or only the `LICENSE-ASSETS` note.** Decision: add a short `TRADEMARKS.md` before v0.1.0 (nominative use allowed, modified builds must not use the name or logo); defer registration. The code licence grants no marks and a one-page policy is cheap insurance for a name-bearing fork.
