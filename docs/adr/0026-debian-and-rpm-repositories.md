# Debian and RPM Repositories

[Documentation index](../README.md)

Status: accepted (2026-11-17)

## Context

notarx ships `.deb` and `.rpm` packages (ADR 0025) and must host Debian and RPM
repositories for its users. The state of the tree:

- **APT Hosted is a complete but unadvertised operator preview.** H1 (sessions,
  streaming `.deb` parser, content-addressed staging) and H2 (signed atomic
  snapshots) are done; H3 (production signing) and H4 (lifecycle) have open gates
  (`docs/apt-hosted-roadmap.md`). The advertised profile lists APT as Proxy plus
  Group reads only (`internal/repository/format_profiles.go:138-148`), and Hosted is
  provisionable only through a special case (`format_profiles.go:251-258`).
- **Publication model.** `POST /api/v2/repositories/{id}/apt/publication-sessions`
  (idempotent, quota-reserving), `PUT .../{sid}/package`, then
  `POST .../apt/snapshots` which regenerates, signs and publishes one suite in one
  PostgreSQL transaction (`docs/apt-hosted-signing.md`). Tables are
  `native_apt_publication_sessions`, `native_apt_package_revisions`,
  `native_apt_repository_snapshots`, `native_apt_snapshot_packages`,
  `native_apt_snapshot_assets` (`migrations/000098`, `000100`, `000102`, `000116`, `000117`).
- **Metadata.** `internal/aptpublication/snapshot.go:465` emits only `Packages` and
  `Packages.gz` per component and architecture; `Release` hard-codes
  `Origin: Artifact Gateway` and `Acquire-By-Hash: yes` (`snapshot.go:477`). `go.mod`
  already carries `github.com/ulikunitz/xz` and `github.com/klauspost/compress`.
- **Signer boundary.** `HTTPSigner.SignRelease` (`internal/aptpublication/http_signer.go:362`)
  posts the `Release` bytes to `POST /v1/sign-release` and receives `InRelease` and
  `Release.gpg` as JSON; notarx re-verifies both against a pinned public keyring of one
  or two fingerprints (RSA, SHA-256, 2048 to 4096 bits; `http_signer.go:158-249`).
  The reference signer (`internal/refaptsigner/handler.go:47`, `cmd/reference-apt-signer`)
  keeps a local-volume key and is documented as a fixture.
  `make apt-signer-rotation-e2e` and `make native-apt-e2e` already prove rotation overlap
  and restore.
- **Lifecycle.** Deletion, recovery, retention preview/apply, pruning, snapshot export
  and restore, scanning, and target-signed promotion/replication exist as preview
  (`docs/apt-hosted-lifecycle.md`, `docs/apt-hosted-distribution.md`). The generic
  per-repository `RetentionPolicy` (`internal/repository/model.go:297`, API
  `/repositories/{id}/retention-policy`) has no APT scheduled executor. Promotion is
  gated by `supportsAPTDistributionPreview` (`internal/app/repository_distribution_api.go:27,612`),
  and ADR 0021 keeps that gate until APT leaves preview.
- **RPM does not exist.** A search finds only fixtures and `docs/nexus-gap-analysis.md`.
  ADR 0025 slice 13 defers an RPM hosted format "to its own ADR": this one.
- **Peers.** ADR 0011 format modules (`internal/format/{helm,nuget,raw,rubygems}`)
  are the pattern for a new format; `internal/format/rubygems` is the closest model
  for a publication-session format.

ADR 0025 chose static R2 repositories for the project's own packages and allowed a
notarx-hosted `nightly` suite later. This ADR makes both formats production-grade and
defines that dogfooding. ADR 0029 owns public visibility; ADR 0030 owns scanning and the `.deb` and `.rpm` scanner adapters. Dependency direction: this ADR only registers a scan hook; ADR 0030 slices 9a and 9b depend on this ADR, never the reverse.

## Considered designs

1. **Delegate to `createrepo_c` and `reprepro`/`aptly` subprocesses.** Mature and
   byte-compatible, but GPL tools inside a server image, state on local disk
   (conflicts with a stateless multi-node server), and two code paths for signing.
2. **Native generators inside notarx, reusing the APT snapshot model (chosen).**
   One more `native_rpm_*` store family and a ~600 line XML writer; metadata is
   generated from committed rows, signed through the existing signer boundary, made
   visible in one transaction. Cost: we own client-compatibility risk, mitigated by
   real `dnf`/`yum`/`apt` gates in CI and `createrepo_c` only as a test oracle.
3. **Make notarx a generic file store with a CI-generated tree.** Upload a tree
   built elsewhere; no format logic. Loses atomicity, quota, scanning and promotion
   per package, and cannot verify what clients see.
4. **Extract one shared "signed snapshot engine" first, then build RPM on it.**
   Cleaner in theory, but it refactors a working preview and its restore and rotation
   drills before the second consumer exists. Rejected for now; only the signer client
   is extracted (decision below). Duplication in store ports is accepted and is what
   RubyGems and Helm already do.

## Decision

Option 2. Management API paths are relative to `/api/v2`; protocol paths follow
`api/openapi/protocols/`.

### Part A. APT Hosted from preview to GA

APT Hosted leaves preview when all gates below are closed and `FormatProfile` for APT
lists `hosted` (with `HostedOperations` read, browse, publish, delete, reclaim, promote,
replicate), the special case in `FormatSupportsRepositoryProvisioning` is deleted, the
Console creation list includes it, and the promotion gate is removed (ADR 0021 slice 2a
keeps its behaviour until then; the lift is slice 18 here).

| Gate | Closure |
| --- | --- |
| G1 Production signer custody | Generalised signer contract and a KMS/HSM-backed `notarx-signer` adapter (below); key-recovery drill documented and run. |
| G2 Scheduled retention | Executor driven by the existing `RetentionPolicy` (below). |
| G3 Anonymous Hosted reads | Black-box conformance suite (below; slice 17). |
| G4 `Packages.xz` and truthful Release | `.xz` index, by-hash for it, configurable `Origin`/`Label`/`Description` (default `notarx`). |
| G5 Advertised capability | Profile, Console, CLI map and docs flipped in one PR. |
| G6 Promotion gate lift | `supportsAPTDistributionPreview` removed; APT registered in the ADR 0021 format registry (`PromotionPublisher`, `Exporter`, `Importer`; slice 18). |
| G7 Scan hook | APT is registered in the existing `format.Scanner` registry so `autoScanOnPublish` resolves a `.deb` artifact (adapter absent means `not_scanned`, never clean). The adapter itself is ADR 0030 slices 9a (.deb) and 9b (RPM) and does not gate GA. |
| G8 Alerts | Alert rules for signing outcome and latency ship as a deployable file under `deploy/` (documentation alone does not close it). |

APT Group aggregate signing stays out of scope: Groups remain ordered Proxy members
and independently signed upstream metadata is never merged (roadmap H4).

**Signer contract v2.** A new protocol spec `api/openapi/protocols/signer.yaml` (also
describing today's endpoint as deprecated) defines:

- `POST /v1/sign-release` unchanged (APT: raw `Release` in, `inRelease` plus `detached`
  out).
- `POST /v1/sign-detached`: body is the exact bytes to sign; headers carry
  `X-Artifact-Signer-Schema: v2`, `X-Artifact-Signer-Purpose` (`apt-release-detached`,
  `rpm-repomd`), `X-Artifact-Repository-Id`, `X-Artifact-Snapshot-Id` and
  `X-Artifact-Payload-Digest` (`sha256:...`, verified by the signer). Response JSON:
  `detached` (armored OpenPGP signature), `signerIdentity`, `keyFingerprint`,
  `algorithm`. Header names keep the existing `X-Artifact-` spelling because they are a wire
  contract, not product identity; the identity gate (ADR 0013) allow-lists them.
- `GET /v1/public-key`, `GET /healthz` unchanged.

notarx refuses a response unless the detached signature verifies against the pinned
keyring and the verified entity's primary fingerprint equals the reported one (existing
`signatureEnvelopeVerifiedByTrustedKey` logic, moved to `internal/signing` so APT and
RPM share it). The signing key may be a signing **subkey** of a pinned primary; the
verifier already accepts subkey signatures through the entity and a test pins this.

**`notarx-signer` (production adapter).** Shipped as a release archive (ADR 0016 slice 4), not a published image. New `cmd/notarx-signer` and `internal/signer`
(the existing `internal/refaptsigner` becomes the `file` backend and stays a fixture).
The adapter never holds key material: it hashes the payload, builds the OpenPGP
signature packet (V4, RSA, SHA-256) and sends only the digest to a backend that performs
the raw RSA PKCS#1 v1.5 private-key operation. Backends:

- `pkcs11` (HSM, SoftHSM2 in CI; cgo, build tag `pkcs11`, built natively per ADR 0016),
- `oci-vault` (OCI Vault `Sign` with `messageType=DIGEST` and `signingAlgorithm=SHA_256_RSA_PKCS1_V1_5`, fits ADR 0017's reference deployment). A community report says software-protected keys may return a signature without the PKCS#1 DigestInfo wrapper for DIGEST input, so the backend runs a signature self-test against the public key at startup and in `cert-init`, and a live-vault run is mandatory before S2 is trusted (slice 3),
- later: AWS KMS, Google Cloud KMS, Azure Key Vault (Backlog; same `Backend` interface).

Adapter configuration: `NOTARX_SIGNER_LISTEN` (default `127.0.0.1:18083`; a non-loopback address requires TLS), `_TOKEN_FILE`,
`_BACKEND`, `_CERT_FILE` (public OpenPGP certificate binding the KMS key, produced once by
`notarx-signer cert-init` in the key ceremony), `_ALLOWED_PURPOSES`, `_ALLOWED_SUITES`,
`_MAX_PAYLOAD_BYTES` (default and max 16 MiB) and TLS settings. Policy before any backend call: purpose allowed; the payload must parse as the purpose's document (`Release` for APT, `repomd.xml` for RPM) and is refused otherwise; `Release` `Suite` in the allow-list and `Date` (APT), or `<revision>` (RPM, keyed by repository id), not older than the last value seen (rollback guard, small local file; replacing it is a documented key-ceremony step); per-token rate limit;
every request logged with payload digest, purpose, repository and snapshot ids. The server
adds `NOTARX_RPM_SIGNER_*` keys (`ENDPOINT`, `TOKEN`, `TIMEOUT`, `TRUSTED_FINGERPRINTS`,
`TRUSTED_PUBLIC_KEYS_FILE`, `TLS_CA_FILE`), mirroring the renamed `NOTARX_APT_SIGNER_*`
keys so APT and RPM may use different keys or the same signer.

**Scheduled retention (G2).** A scheduled task (`scheduled_tasks` lease, ADR 0023
slice 5 style single runner) reads each Hosted APT repository's `RetentionPolicy`
(`enabled`, `keepDays`, `minimumVersions`, `maximumVersions`, `coordinatePatterns`,
`protectedPatterns`), computes the same plan as the lifecycle preview per suite
(grouped by component, package, architecture; age is original upload time), and applies
it as one new signed snapshot per suite with a deterministic idempotency key
(`retention:<repositoryId>:<suite>:<baseSequence>:<planDigest>`). A plan that is empty,
changed concurrently (409) or blocked by signer unavailability is retried at the next
tick and audited, never forced. Metrics: `notarx_apt_retention_runs_total{outcome}`.

**Anonymous Hosted reads (G3).** ADR 0029 defines how repository visibility becomes
public. APT Hosted must honour the same two-level rule as Proxy (the global gate, which stays as an instance kill switch, and repository visibility; `docs/apt-proxy.md:60-63` describes today's `anonymous_read` form). A black-box suite (`make
apt-anonymous-e2e`, Debian and Ubuntu containers, no credentials) proves: `apt-get
update` and `install`; `HEAD`, `Range`, `If-None-Match` and `If-Modified-Since` on
`InRelease`, `Release`, `Release.gpg`, `Packages*` and `pool/*.deb`; and negatives: staged
or session objects, retired snapshots, unpublished suites and quarantined packages are
not reachable; `/api/v2/**` demands credentials; no directory listings.

### Part B. RPM repositories

**Repository shape.** One RPM repository is one yum repository: one `repodata/` for
all architectures (as `createrepo_c` produces). Channels such as `stable` and `nightly`
are therefore separate repositories (also a natural ADR 0021 chain). Protocol root:
`/rpm/<repository>/`, with `repodata/repomd.xml`, `repodata/repomd.xml.asc`,
`repodata/repomd.xml.key`, hashed metadata files and packages at
`Packages/<sha256[0:2]>/<name>-<version>-<release>.<arch>.rpm`. NEVRA fields are validated against `[A-Za-z0-9._+~^-]` (no `/`, no `..`) at upload, and a second package with the same file-name tuple but a different SHA-256 (including a different epoch) is refused with 409, so a published path never changes bytes. Clients use plain
`baseurl`; `$releasever`/`$basearch` live in the client's `.repo` file, not the server.
dnf authenticates by HTTP Basic only, so ADR 0019's JWT-as-Basic-password is the token
path; anonymous reads follow ADR 0029 as in G3.

**Hosted data model.** Format module `internal/format/rpm` (ADR 0011), store family
`internal/rpmpublication` (Memory, PostgreSQL; MySQL twin by ADR 0022), tables created
under ADR 0023 conventions (UUIDv7 `ids.New()`, `uuid` typed keys, no `::text`
comparisons, no foreign keys into partitioned tables, nothing append-only so nothing
partitioned):

```
native_rpm_publication_sessions(id, repository_id, state, idempotency_key,
  declared_sha256, declared_size, name, epoch, version, release, arch,
  quota_reservation_id, created_at, expires_at)
native_rpm_package_revisions(id, repository_id, name, epoch, version, release, arch,
  sha256, size, object_key /* native/rpm/sha256/.. */, header_start, header_end,
  signature_state /* unsigned|valid|untrusted */, signature_key_id, publisher, created_at)
native_rpm_repository_snapshots(id, repository_id, sequence, state /* building|visible|retired */,
  repomd_digest, signer_identity, key_fingerprint, algorithm, created_at, retired_at, grace_until)
native_rpm_snapshot_packages(snapshot_id, revision_id, location)
native_rpm_snapshot_assets(snapshot_id, path, kind, digest, size, object_key, content_type)
native_rpm_trusted_keys(id, repository_id, fingerprint, armored_public_key, created_at)
```

plus idempotency, object-intent and pool-path tables copied from the APT shape (`native_apt_publication_idempotency`, `..._snapshot_object_intents`, `..._pool_paths`). MySQL rules (ADR 0022): `path` is bounded (512 characters, `VARCHAR` in keys), no partial or expression indexes, no `RETURNING`. `native_rpm_snapshot_packages` grows by one row per package per snapshot (ADR 0023 growth rule): the retention task also prunes retired snapshots after their grace period, keeping at most 10, so row count stays bounded by 11 times the 50000 cap. These land as numbered migrations after the ADR 0015 cut-over
(or folded into the baseline if they merge before ADR 0015 slice 7); never edited after
the tag.

**Upload and parsing.** Same three-step sequence as APT:
`POST /repositories/{id}/rpm/publication-sessions` (idempotent, reserves quota),
`PUT .../publication-sessions/{sid}/package` (streams, hashes, parses lead, signature and
main header; derives NEVRA, dependencies, file list; rejects identity mismatch), `POST
.../rpm/snapshots` (`{sequence, publicationSessionIds}`) builds, signs and publishes.
Parsing uses `github.com/sassoftware/go-rpmutils` (Apache-2.0, verifies signatures with
`ProtonMail/go-crypto`); `cavaliergopher/rpm` is the fallback. The payload is hashed, never
decompressed. Limits: `NOTARX_RPM_MAX_PACKAGE_BYTES` (2 GiB), header 64 MiB, 1,000,000 file
entries, `NOTARX_RPM_MAX_SNAPSHOT_PACKAGES` (50000). Ordering (`rpmvercmp`, `~`, `^`) lives in
`internal/format/rpm/evr.go` with rpm's own vectors and the ADR 0025 ordering
(`0.2.0-0.nightly.3 < 0.2.0-1`).

**Package signature policy.** Per repository `rpmSignaturePolicy`: `accept_unsigned`
(default), `require_valid` (signature must verify against some key in the repository's
`native_rpm_trusted_keys`, managed by `PUT/GET/DELETE /repositories/{id}/rpm/trusted-keys`;
public-only, bounded, RSA 2048 to 4096; system keyrings are never consulted). Outcome is
stored per revision and surfaced in the API, Console and scan evidence. notarx does not
sign packages in this ADR (nfpm signs at build time, ADR 0025); package bytes are
immutable and promotion preserves publisher signatures.

**Repodata writer.** `internal/format/rpm/repodata`: deterministic `primary.xml`,
`filelists.xml`, `other.xml` (changelog entries from the header, capped), `repomd.xml`
with `revision` = snapshot sequence and `timestamp`, open and closed checksums (SHA-256),
and hashed file names (`<sha256>-primary.xml.gz`) so a new generation only adds files
and clients never see a mixed state. Compression: `gz` by default for EL8 compatibility;
`zstd` per repository (`repodata.compression`) via `klauspost/compress`. No sqlite
databases and no `modules.yaml`, `updateinfo` or delta RPMs (Deferred). `comps.xml` is
not generated. XML is produced with an escaping encoder and control characters are
stripped. Signing: `repomd.xml` goes through `/v1/sign-detached` purpose `rpm-repomd`;
`repomd.xml.asc` and the public key (`repomd.xml.key`, from the trusted signer keyring)
are written in the same visibility transaction as `repomd.xml`. With no signer
configured, RPM publication is refused (same fail-closed rule as APT).

**Proxy.** `upstream.kind` is `baseurl`, `metalink` or `mirrorlist`. `baseurl`: byte-for-byte
cache as APT Proxy; conditional `repomd.xml` revalidation; hashed `repodata/*` and packages
immutable; signed metadata never rewritten. `metalink` (Fedora): fetch over HTTPS (1 MiB cap, `internal/egress` client; the metalink host is allow-listed, mirror hosts cannot be, so mirror connections use the same client with its pinned-DNS private-address refusal and redirect re-validation), choose mirrors in preference order, verify
`repomd.xml` size and hash against the metalink, pin that mirror
(`NOTARX_RPM_PROXY_MIRROR_PIN_TTL`, 1h) and fall back down the list; clients see a plain
`baseurl`. `mirrorlist`: same without hashes (trust is TLS plus `repo_gpgcheck`). Every
other metadata file is verified against the verified `repomd.xml`. One proxy repository
per release and architecture.

**Group.** Read-only ordered failover: a path is served from the first member that can
serve it, and `repodata/repomd.xml` comes from the first healthy member as a whole
(never mixed across members). Merged aggregate repodata needs a Group snapshot owner and
is out of scope, as for APT. The Console states that package visibility is that of the
serving member.

**Lifecycle.** Package deletion and recovery, retention (same executor as APT, per
repository policy, run per repository), snapshot export/restore and pruning mirror the
APT shape. Scanning: RPM registers the `format.Scanner` hook; the extractor, SBOM and adapter are ADR 0030 slice 9b. Promotion and
replication register `PromotionPublisher`, `Exporter` and `Importer` in the ADR 0021
registry: the unit is one package revision (NEVRA plus SHA-256); the target regenerates
and signs its own repodata, never copying the source `repomd.xml`; imported packages
coalesce into one target snapshot per window (`NOTARX_RPM_SNAPSHOT_COALESCE`, default
`30s`) so a rule that promotes 200 packages does not publish 200 snapshots.

### Part C. Client conformance gates

`make pkg-clients-e2e IMAGE=<image>` starts notarx with PostgreSQL and the reference
signer, publishes fixture packages, and drives the real client. The CI matrix runs every
image on both native runners (`ubuntu-24.04`, `ubuntu-24.04-arm`; ADR 0016, no QEMU):

| Format | Images (each in `ci.yml`, both architectures) |
| --- | --- |
| apt | `debian:12`, `debian:13`, `ubuntu:24.04`, `ubuntu:26.04` |
| dnf/yum | `fedora:latest` (dnf5), `rockylinux:9`, `almalinux:9`, `rockylinux:10`, `rockylinux:8` (oldest dnf4 and libsolv) |

Each run (reference signer fixture, never published) asserts `apt-get update` / `dnf makecache` with signature enforcement
(`Signed-By`; `gpgcheck=1`, `repo_gpgcheck=1`), install, upgrade across the ADR 0025
version ordering, tamper rejection, anonymous reads (G3), and the same install with the
signer stopped. RPM adds an oracle test: `createrepo_c` and the notarx writer run over
the same package set and `dnf repoquery --qf` (NEVRA, requires, provides, files) must
match. Failures of `rockylinux:8` block merge like the others; a newly released image is
added in its own PR and may be `continue-on-error` for one week.

### Part D. Dogfooding: notarx publishes its own feeds

Governed by ADR 0025: **R2 at `packages.mlops.ca` remains the bootstrap and the
guaranteed fallback**; nothing needed to install or upgrade notarx ever depends on a
notarx. The hosted feed is a second, equal-trust mirror used first for nightly.

- **Instance.** A dedicated production notarx (not the dev or e2e stack; Milestone Public
  feeds) on the ADR 0017 Oracle Always Free A1 arm64 reference deployment at
  `https://feeds.mlops.ca`, running
  the newest stable release, upgraded from GitHub Release assets or GHCR images, never
  from its own feed. Object Storage via OCI S3-compat; single VM, no HA claim, optional
  CDN in front with cache rules for immutable pool and by-hash files (indices
  `max-age=60`, as ADR 0025). Both feeds are `public` through ADR 0029 (anonymous read of the current snapshot only, no listings); this is why Part D needs ADR 0029 slices 1 to 7.
- **Repositories.** APT Hosted `notarx` (suites `stable` and `nightly`, component `main`,
  architectures `amd64`, `arm64`, `all`), URL `https://feeds.mlops.ca/apt/notarx`; RPM
  Hosted `notarx-stable` and `notarx-nightly`, URLs `https://feeds.mlops.ca/rpm/notarx-stable/`
  and `.../notarx-nightly/`. Nightly retention: `minimumVersions` 14 per package and
  architecture; stable keeps all (ADR 0025). Both RPM repositories set `rpmSignaturePolicy: require_valid` with the project certificate as the trusted key (ADR 0025 signs RPMs with S1), so a compromised publisher token cannot push unsigned packages.
- **Keys.** One certificate `notarx packages <packages@mlops.ca>` (ADR 0025: RSA 4096
  primary offline) carries two signing subkeys: S1 in the CI `packages-signing`
  environment (R2 pipeline) and S2 held in OCI Vault behind `notarx-signer` (feeds).
  `gpg.key` publishes the certificate with both, so clients switch between R2 and
  the feeds without a trust change and either subkey can be revoked alone. notarx pins the
  primary fingerprint. This narrows ADR 0025's "distinct from the Oracle-hosted APT signer"
  sentence (see amendments): S2 is not the fixture key and not a customer signer.
- **Publishing.** A `publish-feeds` job follows `publish` in the ADR 0024 workflow
  (stable and nightly) in environment `packages-publish`. It authenticates with GitHub
  Actions workload identity (ADR 0019: trusted issuer plus a trust policy bound to
  repository, workflow, environment and ref) as service account `feeds-publisher`,
  which has write on exactly those repositories and no admin scope. Inputs are the same
  `gh attestation verify`-ed assets as `sync-repo`, never R2 or the feed. `notarxctl`
  uploads sessions, then publishes one snapshot with `Idempotency-Key =
  <run id>-<package-set digest>`. The CI job holds no signing key.
- **Residual risk (stated, not hidden).** An attacker controlling the feeds instance can
  ask the signer to sign arbitrary `Release` text. Mitigations: signer allow-lists and
  rollback guard, signer logs shipped off-host, and a `verify-feeds` job (slice 22) that checks
  every package hash in the published indices against the attested assets and fails the
  release on any mismatch; S2 is revocable without touching the R2 path.
- **Gates for the stable suite.** Nightly runs on the feeds from the day Part A and the
  RPM slices are done. Stable joins after 30 consecutive nightly publications with green
  `verify-feeds` on both architectures, one S2 rotation drill, one restore drill, and one
  drill where the feeds are stopped and clients install from R2.
- **Verification.** `verify-feeds` runs the install, upgrade and anonymous-read assertions of
  Part C (a subset: tamper and signer-stopped cases need the fixture stack and stay in
  `pkg-clients-e2e`) on the images ADR 0025 lists (`debian:12`, `ubuntu:24.04`,
  `rockylinux:9`, `fedora:latest`, all in the Part C matrix) against the live URLs on
  both native runners, and checks the package set equals the R2 set byte for byte.

## Security considerations

- Signer compromise is the central risk: no private key in notarx or CI, KMS/HSM
  custody, payload policy, rollback guard, per-purpose tokens, public-key pinning in
  notarx, revocable subkey, drilled recovery.
- Parsers are the attack surface for uploads: streaming, bounded, payload never
  decompressed, fuzz targets for the RPM header and for `Release` parsing in the signer.
- Generated metadata is built from typed rows, escaped, and every `location` is derived
  from the digest; uploaded names never become paths.
- Metalink and mirrorlist add SSRF and mirror-substitution risk: HTTPS only, egress
  allow-list, size caps, hash verification of `repomd.xml` (metalink) and of each
  metadata file against it.
- Replay and freeze: yum has no `Valid-Until`; `repomd.xml` carries `revision` and
  `timestamp` and clients bound staleness with `metadata_expire`. An APT `Valid-Until`
  option is an open question.
- `gpgcheck` (packages) and `repo_gpgcheck` (metadata) are independent; the docs and
  `.repo` examples enable both and state that metadata signing does not vouch for
  package provenance.
- Anonymous reads expose only the current visible snapshot and its pool objects.

## Consequences

- notarx can host Debian and RPM repositories for customers and for itself; the
  project's own feeds become a continuous soak test of both.
- The signer becomes a deployable component (`notarx-signer`), needing a release
  archive and docs; cgo only for the `pkcs11` build tag.
- RPM metadata compatibility with old `dnf` and `yum` is the largest technical risk;
  the EL8 gate is deliberate.
- Two store families with parallel shapes (APT, RPM) and no shared engine; a later
  extraction remains possible because the signer client is already shared.
- Snapshot cost is O(package count) per publication; the 50000 cap and coalescing bound
  it, incremental repodata is deferred.

## Implementation slices

All slices follow repository rules: OpenAPI edited first under `api/openapi` and
regenerated (`make openapi-check`), coverage floors (`make coverage`), `make test`,
`make lint` (includes `make architecture-check`; register new packages), `make
integration-test` and `make migrate` when the database is touched, `make console-test`
for Console changes, `docs/cli-command-map.md` classification (`make cli-contract-check`)
for CLI changes, MySQL twins by ADR 0022 store slices (each slice defines the
dialect-neutral port). Slices that add migrations depend on ADR 0015 slice 4. Slices 17 to 22 were split out of larger slices and keep their numbers so other ADRs' references to slices 1 to 16 stay valid.

1. **Signing package and contract v2.** Milestone: Formats.
   Scope: `internal/signing` (moved verification), `internal/aptpublication/http_signer.go`, `internal/refaptsigner`, `api/openapi/protocols/signer.yaml`, `internal/config`.
   - [ ] `/v1/sign-detached` with purposes `apt-release-detached` and `rpm-repomd` in the reference signer; `HTTPSigner.SignDetached`.
   - [ ] Subkey-signature acceptance test; unknown purpose, digest mismatch, wrong fingerprint all rejected.
   - [ ] `X-Artifact-Signer-*` headers allow-listed in the ADR 0013 identity gate.
   - [ ] `NOTARX_RPM_SIGNER_*` keys loaded and validated; APT behaviour unchanged (`make native-apt-e2e`, `make apt-signer-rotation-e2e`).
   - [ ] `make test`, `make lint`, `make openapi-check`.
   - Depends on: ADR 0013 slices 4 and 9.

2. **`notarx-signer` core and PKCS#11 backend.** Milestone: Formats.
   Scope: `cmd/notarx-signer`, `internal/signer`, `Makefile` target `signer-test`, release archive entry (a release archive, not an image).
   - [ ] `Backend` interface; `file` and `pkcs11` backends; OpenPGP packet assembly verified by `gpg --verify` and by notarx's verifier.
   - [ ] Policy engine: purpose, payload type, suite allow-list, `Date` and RPM `revision` rollback guards, rate limit, structured audit log; fuzz test for `Release` and `repomd.xml` parsing.
   - [ ] SoftHSM2 integration test on both architectures; `cert-init` creates a certificate binding the HSM key.
   - [ ] `make signer-test`, `make test`, `make lint`.
   - Depends on: Slice 1; ADR 0016 slice 4 (matrix release archives).

3. **OCI Vault backend and key ceremony.** Milestone: Formats.
   Scope: `internal/signer/ocivault`, `docs/release/feeds-signing.md`.
   - [ ] Backend uses `messageType=DIGEST` with `SHA_256_RSA_PKCS1_V1_5`; startup and `cert-init` self-test verifies a signature against the public key (guards the DigestInfo caveat).
   - [ ] Contract test against a recorded fake plus a live test gated by an env flag; one live run is recorded in the PR before S2 is trusted.
   - [ ] Runbook: S2 creation as subkey of the offline primary, `gpg.key` republish, recovery and revocation drill result recorded.
   - [ ] `make docs-check`, `make test`, `make lint`.
   - Depends on: Slice 2; ADR 0025 slice 4 (key ceremony).

4. **APT metadata completeness.** Milestone: Formats.
   Scope: `internal/aptpublication/snapshot.go` and tests.
   - [ ] Deterministic `Packages.xz` plus by-hash entry and `Release` listing; configurable `Origin`, `Label`, `Description` (default `notarx`).
   - [ ] Golden tests; Debian and Ubuntu containers install with xz-only index (`make native-apt-e2e`).
   - [ ] No migration; existing snapshots still restore (`make backup-drill`, `make restore-drill`).
   - [ ] `make test`, `make lint`.
   - Depends on: None.

5. **APT scheduled retention.** Milestone: Formats.
   Scope: `internal/aptpublication` retention runner, scheduled task, metrics, `deploy/` alert rules (G8).
   - [ ] Executor honours `RetentionPolicy` incl. `minimumVersions`, protected patterns; idempotent under double run and multi-node lease; empty and conflicting plans audited.
   - [ ] PostgreSQL integration test with two nodes; signer-down retry test.
   - [ ] Alert rule file linted; `make integration-test`, `make test`, `make lint`.
   - Depends on: ADR 0023 slice 7 (janitor and scheduling conventions).

6. **APT capability flip and scan hook.** Milestone: Formats.
   Scope: `internal/repository/format_profiles.go`, `internal/app/repository_distribution_api.go`, Console creation list, `docs/apt-*.md`, `docs/cli-command-map.md`.
   - [ ] G7 hook: `autoScanOnPublish` on a Hosted APT repository resolves through `format.Scanner` and reports `not_scanned` without an adapter.
   - [ ] APT profile lists Hosted; provisioning special case removed; G1 to G8 recorded closed in `docs/apt-hosted-roadmap.md` (H3 and H4).
   - [ ] Capability API, OpenAPI and Console tests updated.
   - [ ] `make openapi-check`, `make console-test`, `make cli-contract-check`, `make native-apt-e2e`, `make apt-anonymous-e2e`, `make test`, `make lint`.
   - Depends on: Slices 1 to 5 (production adapter available; the drill from slice 3 recorded), 17 (anonymous suite) and 18 (promotion gate lift).

7. **RPM store, migration and sessions.** Milestone: Formats.
   Scope: `internal/rpmpublication` (Memory, PostgreSQL), migration, OpenAPI session endpoints, `internal/repository` format constants.
   - [ ] Tables above with UUIDv7 and typed keys, bounded `VARCHAR` keys; sqllint ratchet unchanged; port and catalogue entry for the MySQL twin.
   - [ ] Upload validates NEVRA charset, derives locations from digests, refuses same file-name tuple with different digest (409).
   - [ ] Idempotency, quota reservation, interrupted-upload recovery, orphan GC parity with APT tests.
   - [ ] `make test`, `make integration-test`, `make migrate`, `make openapi-check`, `make lint`.
   - Depends on: Slice 21 (parser); ADR 0015 slice 4; ADR 0023 slice 1; ADR 0022 slice 3b (neutral schema catalogue).

8. **Repodata writer.** Milestone: Formats.
   Scope: `internal/format/rpm/repodata`.
   - [ ] Golden XML for primary, filelists, other, repomd; gz and zstd; hashed names; determinism test; escaping and control-character stripping tests.
   - [ ] Oracle test: `createrepo_c` equivalence via `dnf repoquery` in `rockylinux:8`, `rockylinux:9`, `fedora:latest`, both architectures.
   - [ ] 50000-package generation stays within a documented memory bound (benchmark).
   - [ ] `make test`, `make lint`.
   - Depends on: Slice 21 (domain records and parser).

9. **RPM publish, signing and protocol reads.** Milestone: Formats.
   Scope: Snapshot transaction, `api/openapi/protocols/rpm.yaml`, read handlers (GET, HEAD, ranges, conditional), signing-state endpoint, capability profile, `make native-rpm-e2e`, backup and restore checks.
   - [ ] `repomd.xml`, `.asc`, `.key` made visible in one transaction; failure injection at each stage keeps the previous snapshot readable; no signer means refusal.
   - [ ] `dnf` installs with `repo_gpgcheck=1`, with signer and object store down afterwards; tamper rejected.
   - [ ] Capability profile advertises RPM Hosted; anonymous leg honours visibility through `internal/access`.
   - [ ] `make native-rpm-e2e`, `make openapi-check`, `make test`, `make integration-test`, `make lint`.
   - Depends on: Slices 1, 7, 8; ADR 0029 slices 1 to 3 for the anonymous leg.

10. **Package signature policy.** Milestone: Formats.
   Scope: Trusted-keys API and store, upload verification, Console panel.
   - [ ] `require_valid` rejects unsigned and untrusted packages with stable problem codes; stored per-revision state; system keyrings never read.
   - [ ] go-rpmutils verification against subkeys and RSA only; bounded keyring parse.
   - [ ] `make test`, `make integration-test`, `make openapi-check`, `make console-test`, `make lint`.
   - Depends on: Slice 9.

11. **RPM Proxy (baseurl) and Group.** Milestone: Formats.
   Scope: Proxy handler, cache, ordered Group, egress allow-list integration.
   - [ ] Byte-exact cache, conditional revalidation, stale-on-error with `Warning: 110`, immutable hashed files; Group failover never mixes `repomd.xml` across members.
   - [ ] Client test against a local fixture upstream (`make native-rpm-proxy-e2e`).
   - [ ] `make test`, `make integration-test`, `make lint`.
   - Depends on: Slice 9.

12. **RPM Proxy metalink and mirrorlist.** Milestone: Formats.
   Scope: Resolver, verification chain, pinning.
   - [ ] Metalink parsing with size and hash checks, mirror pinning and fallback; metadata files verified against `repomd.xml`.
   - [ ] SSRF tests: private-address mirror, redirect to private or non-HTTPS, oversize metalink, DNS rebinding; mirror hosts connect only through `internal/egress`.
   - [ ] Fixture metalink server e2e; optional live Fedora smoke behind an env flag.
   - [ ] `make test`, `make lint`.
   - Depends on: Slice 11.

13. **RPM promotion, replication and scan hook.** Milestone: Formats.
   Scope: ADR 0021 registry entries (`PromotionPublisher`, `Exporter`, `Importer`), snapshot coalescing, `format.Scanner` registration.
   - [ ] Importer/Exporter round trip with digest equality; target regenerates and signs its own repodata; coalescing test (200 promoted packages produce at most a bounded number of snapshots).
   - [ ] RPM registered in the `format.Scanner` hook (adapter is ADR 0030 slice 9b, which depends on this slice and slice 21); `not_scanned` without adapter; native client install from the promotion target.
   - [ ] `make test`, `make integration-test`, `make openapi-check`, `make lint`.
   - Depends on: Slices 9, 19; ADR 0021 slices 2b and 7b.

14. **Client conformance matrix in CI.** Milestone: Formats.
   Scope: `Makefile` (`pkg-clients-e2e`), `.github/workflows/ci.yml` matrix, fixtures. Land as two PRs: apt legs, then dnf legs.
   - [ ] Part C matrix on both native runners; required checks on `main` after two green weeks.
   - [ ] `make lint`, `make release-readiness-check`.
   - Depends on: Slices 6, 9, 17 (apt legs can start earlier).

15. **Feeds instance and nightly publish pipeline.** Milestone: Public feeds.
   Scope: `deploy/oci-free-tier` feeds profile, `docs/release/feeds.md`, `release.yml` job `publish-feeds` (owned by ADR 0024, edited in coordination).
   - [ ] Instance provisioned per runbook with S2 signer; repositories created declaratively (RPM with `require_valid`); `feeds-publisher` trust policy bound to repo, workflow, environment and ref.
   - [ ] Nightly publication green on both architectures; `verify-feeds` (slice 22) passes including R2 byte equality.
   - [ ] Rotation, restore and R2-fallback drills recorded in the PR.
   - [ ] `make release-readiness-check`, `make docs-check`, `make lint`.
   - Depends on: Slices 3, 5, 6, 9, 14, 19, 20, 22; ADR 0017 slices 1 and 6; ADR 0019 slices 2 to 6; ADR 0025 slice 6; ADR 0029 slices 1 to 7; ADR 0024 slice 4.

16. **Stable suite on the feeds and install docs.** Milestone: Public feeds.
   Scope: `release.yml` (stable), `docs/install.md`.
   - [ ] Gate criteria (30 nightlies, drills) evidenced; stable suite and `notarx-stable` published; docs list R2 first and the feeds as equal-trust alternative, with key fingerprints.
   - [ ] `make docs-check`, `make release-readiness-check`.
   - Depends on: Slices 15, 22; ADR 0025 slice 8.

17. **APT anonymous conformance suite.** Milestone: Formats.
   Scope: `make apt-anonymous-e2e` (G3), Debian and Ubuntu container fixtures, `docs/apt-*.md` anonymous section.
   - [ ] Suite from G3 passes against a `public` Hosted repository on Debian and Ubuntu images, and negatives (private repository, staged objects, retired snapshots, `/api/v2/**`, listings) hold.
   - [ ] `make native-apt-e2e`, `make test`, `make lint`.
   - Depends on: ADR 0029 slices 1 to 3.

18. **APT promotion gate lift and registry.** Milestone: Formats.
   Scope: `internal/app/repository_distribution_api.go` (`supportsAPTDistributionPreview`), ADR 0021 format registry.
   - [ ] `supportsAPTDistributionPreview` removed; APT registers `PromotionPublisher`, `Exporter`, `Importer`; target regenerates and signs its own snapshot.
   - [ ] Promotion and replication e2e for APT; existing preview behaviour tests migrated.
   - [ ] `make test`, `make integration-test`, `make openapi-check`, `make native-apt-e2e`, `make lint`.
   - Depends on: ADR 0021 slices 2b and 7b.

19. **RPM lifecycle and retention.** Milestone: Formats.
   Scope: Package delete and recover, retention executor reuse per repository, retired-snapshot pruning, snapshot export/restore.
   - [ ] Delete, recover, retention, export/restore and prune parity tests with APT; retention executor runs for RPM; retired snapshots pruned after grace, at most 10 kept.
   - [ ] `make backup-drill` and `make restore-drill` cover RPM.
   - [ ] `make test`, `make integration-test`, `make openapi-check`, `make lint`.
   - Depends on: Slices 5, 9; ADR 0023 slice 7.

20. **RPM Console and notarxctl.** Milestone: Formats.
   Scope: Console creation and browse pages, `notarxctl rpm` commands, `docs/cli-command-map.md`.
   - [ ] en-CA and fr-CA message keys per ADR 0014; `notarxctl rpm` upload, publish and trusted-key commands classified.
   - [ ] `make console-test`, `make cli-contract-check`, `make openapi-check`, `make lint`.
   - Depends on: Slice 9; ADR 0014 slices 1 and 4.

21. **RPM header parser and version ordering.** Milestone: Formats.
   Scope: `internal/format/rpm` (domain, parser, `evr.go`), `go.mod` (`go-rpmutils`).
   - [ ] Parser tests with signed, unsigned, corrupt, truncated and oversized fixtures; fuzz target; payload never decompressed; limits enforced.
   - [ ] `evr.go` vectors from rpm plus the ADR 0025 ordering.
   - [ ] Open question 3 decided with evidence (use or fork go-rpmutils).
   - [ ] `make test`, `make lint`.
   - Depends on: None.

22. **`verify-feeds`.** Milestone: Public feeds.
   Scope: `scripts/verify-feeds.sh`, `release.yml` job `verify-feeds`.
   - [ ] Install, upgrade and anonymous-read assertions on `debian:12`, `ubuntu:24.04`, `rockylinux:9`, `fedora:latest`, both native runners, against a given base URL.
   - [ ] Every package hash in the published indices checked against attested assets; package set equals the R2 set byte for byte; fails the release on mismatch.
   - [ ] Tested against the fixture stack. `make docs-check`, `make release-readiness-check`, `make lint`.
   - Depends on: Slices 9, 14; ADR 0025 slice 6.

## Open questions

1. Does the OCI Always Free tier include a Vault able to hold an RSA 4096 software or HSM-protected key at no cost? If not, S2 falls back to a `pkcs11` SoftHSM2 backed by an encrypted volume with a weaker custody claim that the docs must state.
2. Should APT suites support `Valid-Until` with a scheduled re-sign job to limit freeze attacks, and should RPM do anything equivalent beyond `metadata_expire`?
3. Does `go-rpmutils` v0.4.0 (last release May 2024) meet maintenance and fuzz expectations, or should parsing be forked into `internal/format/rpm`? Slice 21 decides with evidence.
4. When EL10 and later require OpenPGP v6 package signatures, do we add v6 support in the signer and `require_valid`? Not needed while RSA v4 verifies on all matrix images.
