# Runtime Backends and the Oracle Cloud Free Tier Reference Deployment

[Documentation index](../README.md)

Status: proposed (2026-11-17)

## Context

notarx keeps the two-plane design inherited from upstream: PostgreSQL holds the
control plane and coordination state, an S3-compatible service holds verified
immutable bytes. The fork charter (ADR 0013) asks for a low-cost, self-hostable
reference deployment; the first target is Oracle Cloud Infrastructure (OCI)
Always Free on Ampere A1 (arm64), built on native arm64 images (ADR 0016).

**Database.** `docs/postgresql-capabilities.md` declares PostgreSQL 16 the only
database. The code is bound to it far beyond SQL syntax. Approximate counts from
the tree at the time of writing (grep, not a guarantee):

- advisory locks (`pg_advisory_xact_lock`, `pg_try_advisory_*`, session locks):
  about 40 non-test lines under `internal/`, wrapped in
  `internal/repository/postgres_advisory_lock.go` and used by
  `internal/cache/coordinator.go`;
- `FOR UPDATE SKIP LOCKED` work-queue claims: about 27 sites (lifecycle,
  replication, cache, webhook, audit, upload expiry, e-mail delivery in
  `internal/repository/postgres_email.go`, raw uploads in `postgres_raw.go`);
- `LISTEN/NOTIFY`: `internal/database/notification.go` (listener),
  `internal/app/postgres_cache_task_queue.go` (`pg_notify`), and notify triggers
  from `migrations/000066_postgres_notification_channels.sql`;
- 14 `CREATE TRIGGER` statements in 5 migration files (for example capacity
  enforcement in `000046_repository_capacity_enforcement.sql`);
- `pg_trgm` GIN indexes and `pg_stat_statements` (`000064_postgres_observability_indexes.sql`),
  `TEXT[]` columns, `jsonb`, `ON CONFLICT` (about 112), `RETURNING` (about 146),
  `::` casts (about 930), `DISTINCT ON`, `ILIKE`.

There is no dialect layer; the `*Store` interfaces in
`internal/repository/store.go` are a seam but every implementation is raw
PostgreSQL SQL. The migrations (173 files, to be squashed by ADR 0015) are
applied by `scripts/run-migrations.sh`. `pg_stat_statements` is only preloaded
(`shared_preload_libraries` in `compose.yml:199` and `internal/backupops/docker.go:222`);
no Go code reads it.

**Object storage.** `objectstore.Store` (`internal/objectstore/store.go:31`) is
the port (Get, Put, Stat, Open, OpenRange, PutReader, PutVerifiedReader,
SetVerifiedDigest, Delete, List). Implementations are `MemoryStore` (tests) and
`RustFSStore` (`store.go:150-388`), which is really a generic S3 client:

- `NewRustFSStore(endpoint, accessKey, secretKey, bucket)` hard-codes
  `Region: "us-east-1"` and `UsePathStyle = true` (`store.go:162-170`);
  `internal/backupops/s3.go:36` repeats both for a second client. OCI verifies
  the SigV4 region; per Oracle, `us-east-1` or a blank region works only in the
  tenancy's home region, so it fails everywhere else.
- No checksum configuration exists. The SDK (`aws-sdk-go-v2/service/s3`, see
  `go.mod`) adds CRC checksum headers or trailers to `PutObject` by default and
  validates them on reads; several S3-compatible services reject or mishandle
  this.
- `SetVerifiedDigest` (`store.go:324-344`) rewrites metadata with `CopyObject`
  and `MetadataDirective: REPLACE`; the digest lives in user metadata
  `artifact-gateway-sha256`. `EnsureBucket` calls `CreateBucket` (`store.go:173`).
- Non-seekable bodies are spooled to `os.CreateTemp` (`store.go:293`), so the
  container needs a writable, sized temp directory.
- Configuration is `GATEWAY_RUSTFS_{ENDPOINT,BUCKET,ACCESS_KEY,SECRET_KEY}`
  (`internal/config/config.go`, `compose.yml:58-61`); integration tests read
  `TEST_RUSTFS_*` in 76 files; `internal/objectstore/s3_integration_test.go`
  is the only store-level test and is S3-only.
- The RustFS image in `compose.yml:227` is pinned by digest; whether that digest
  has an arm64 manifest is unverified.

**OCI Always Free facts this ADR relies on** (checked against docs.oracle.com on
2026-11-17; provider terms change, so the reference deployment documentation
must restate and date them): Ampere A1 is a monthly allowance of 1,500 OCPU-hours
and 9,000 GB-hours, that is about 2 OCPU and 12 GB in total (one instance, or
two of 1 OCPU); 200 GB of block storage in total, boot volumes included; Object
Storage of 20 GB and 50,000 API requests per month after the Free Trial ends
(objects above 20 GB are deleted at trial end); an egress allowance; and
reclamation of idle Always Free instances (CPU p95, network and, for A1, memory
all below 20% over seven days). A1 capacity in a home region is sometimes
unavailable at creation time. OCI Database with PostgreSQL is a paid service, so
a free deployment must self-host PostgreSQL. Consequence: the free tier cannot
host the primary byte store in Object Storage, and two A1 VMs need Pay As You Go.

## Considered designs

Database:

1. **PostgreSQL only (status quo).** Zero new code; every guarantee in the
   Context list keeps working. Cost: users on Oracle/MySQL estates must run a
   PostgreSQL somewhere.
2. **Add SQLite for single node.** Attractive for demos. SQLite has no row locks
   or `SKIP LOCKED`, no advisory locks, no `LISTEN/NOTIFY`, no arrays or PL/pgSQL;
   all of the Context list would need a second implementation and a second test
   matrix. Rejected: it would roughly double the store layer for a deployment
   mode the filesystem object backend plus a small PostgreSQL already serves.
3. **Add Oracle Autonomous Database or MySQL.** Same rewrite as option 2 plus
   `ON CONFLICT`/`MERGE`, `RETURNING INTO`, no `ILIKE`, different boolean and
   array semantics; weeks to months and a permanent dual-dialect burden.

Object storage:

1. **Keep a single S3 client with fixed parameters.** Works for RustFS/MinIO
   only; blocks OCI and any real-region AWS bucket.
2. **Generalise the S3 client and add a filesystem driver behind the existing
   port.** Small surface (the port has ten methods), keeps one wire protocol for
   remote stores, gives single-node installs a dependency-free backend.
3. **Use OCI native Object Storage SDK for OCI.** Avoids compat quirks but adds
   a second cloud SDK and a driver nobody else benefits from. Deferred; the
   compat API is sufficient if the contract suite passes.
4. **Add Azure Blob and GCS drivers now.** Not needed by the reference target;
   the driver registry below leaves room.

Reference deployment:

1. **Docker Compose on one A1 VM.** Matches `compose.yml` practice, minimal
   overhead, easy to document and test.
2. **k3s on A1.** Reuses `deploy/kubernetes`, but costs memory and operational
   surface on a 12 GB budget and adds nothing for one or two VMs.
3. **Two A1 VMs (data and app).** Better isolation, but each VM is 1 OCPU /
   6 GB inside the free allowance, it forces a networked object backend (the
   filesystem driver is single node) and cross-VM networking, and it needs Pay As
   You Go to be reliable. Not the default.

## Decision

### Database

PostgreSQL 16 or later is the reference database and the only one supported in
v0.1.0. MySQL 8.4 (MySQL HeatWave) is added as a second backend by ADR 0022 so the
free tier can use its managed database; Oracle Autonomous Database and NoSQL are
rejected there. Managed PostgreSQL is
supported through `NOTARX_DATABASE_URL` as today when it meets the matrix below;
the OCI free deployment self-hosts `postgres:16-alpine` (multi-arch, includes
arm64) on the block volume and starts it with
`-c shared_preload_libraries=pg_stat_statements` as `compose.yml:199` does.

**Managed-PostgreSQL support matrix** (this is the matrix ADR 0015 delegates to
this ADR; it lists requirements, and slice 8 records the verified result per
provider in `docs/postgresql-capabilities.md`, so no provider is claimed
supported before it is tested):

| Requirement | Why | Failure mode |
| --- | --- | --- |
| PostgreSQL 16 or later | only tested version | unsupported |
| `CREATE EXTENSION pg_trgm` allowed | GIN trigram indexes in the baseline | baseline fails; unsupported |
| `pg_stat_statements` (optional) | created in a guarded block by the baseline (ADR 0015); no Go code reads it | statement statistics unavailable; migration still succeeds |
| Session-level connections (direct, or pooler in session mode) | session advisory locks and `LISTEN/NOTIFY` | lost locks and notifications behind a transaction-mode pooler; unsupported |
| Row triggers and PL/pgSQL | 14 triggers incl. capacity enforcement | unsupported |
| Role may `CREATE` in the target schema | migration runner | unsupported |

Providers that cannot meet every row are unsupported. Cloud-specific claims
(for example RDS or OCI Database) stay out of this ADR until slice 8 verifies
them.

Criteria for revisiting (all must hold before an ADR proposing a second
database is accepted): (a) a concrete, funded user population that cannot run
PostgreSQL; (b) a dialect abstraction design that keeps advisory-lock, queue
claim and notify semantics provably equivalent, with the existing PostgreSQL
integration suite re-run against it; (c) a named maintainer for the extra matrix.
ADR 0022 meets them for MySQL only (funded population: the OCI free tier). Absent
these, SQLite/Oracle requests are declined.

### Object storage configuration

`GATEWAY_RUSTFS_*` is replaced (no aliases, per ADR 0013) by:

| Key | Default | Meaning |
| --- | --- | --- |
| `NOTARX_OBJECT_STORE_DRIVER` | `s3` | `s3` or `filesystem` (registry; unknown value fails startup) |
| `NOTARX_OBJECT_STORE_ENDPOINT` | required for `s3` | base URL, scheme http/https, no path |
| `NOTARX_OBJECT_STORE_BUCKET` | required for `s3` | bucket name |
| `NOTARX_OBJECT_STORE_REGION` | `us-east-1` | SigV4 region; set to the real region for OCI/AWS (compat API accepts `us-east-1` only in the tenancy home region) |
| `NOTARX_OBJECT_STORE_ACCESS_KEY` / `_SECRET_KEY` | required for `s3` | static credentials (OCI customer secret key pair) |
| `NOTARX_OBJECT_STORE_PATH_STYLE` | `true` | path-style addressing; `false` for virtual-hosted services |
| `NOTARX_OBJECT_STORE_CHECKSUM_MODE` | `when_required` | `when_required` or `when_supported`; maps to the SDK `RequestChecksumCalculation` and `ResponseChecksumValidation`, set in `newS3Client` so both the store and the backup client get it |
| `NOTARX_OBJECT_STORE_AUTO_CREATE_BUCKET` | `true` for compose, `false` in production docs | gates `EnsureBucket`; otherwise only `CheckBucket` runs |
| `NOTARX_OBJECT_STORE_FS_ROOT` | required for `filesystem` | absolute directory |
| `NOTARX_OBJECT_STORE_FS_FSYNC` | `true` | fsync file and parent directory on commit |

Code shape: `RustFSStore` becomes `S3Store`; a single `objectstore.Config` and
`objectstore.Open(ctx, cfg) (Store, error)` build either driver. A shared
`newS3Client(cfg)` is used by `S3Store` and by `internal/backupops/s3.go`, so
the two hard-coded copies disappear and `backupops.S3Settings` gains `Region`,
`PathStyle`, `ChecksumMode`. Integration test variables become
`TEST_OBJECT_STORE_*`. The digest metadata key changes from
`artifact-gateway-sha256` to `notarx-sha256` (fresh start, ADR 0015; no reader
of old keys is kept). Error mapping (`rustFSNotFound` becomes `s3NotFound`) is
unchanged, including the 404 fallback.

### OCI Object Storage specifics (S3 compatibility API)

- Endpoint `https://<namespace>.compat.objectstorage.<region>.oci.customer-oci.com`
  (the older `...oraclecloud.com` form is legacy),
  `REGION=<region identifier>` (for example `ca-toronto-1`), `PATH_STYLE=true`,
  `CHECKSUM_MODE=when_required`.
- Credentials are Customer Secret Keys of an IAM user (not API signing keys). A
  dedicated IAM user with a policy limited to one compartment and bucket is the
  documented pattern; keys are rotated by creating the second key first.
- Buckets created through the compat API land in the tenancy's *S3 Compartment
  designation*; production guidance pre-creates the bucket (OpenTofu slice) and
  sets `AUTO_CREATE_BUCKET=false`.
- `SetVerifiedDigest` depends on `CopyObject` with metadata REPLACE, and the copy
  source is built with `url.PathEscape(bucket + "/" + key)` (`store.go:324-344`),
  which encodes `/` as `%2F`; the live test must cover keys with slashes. Support in
  the compat API is to be proven by the live test. If the capability probe fails,
  the driver falls back to streaming the object through `PutVerifiedReader` (read
  bytes, re-verify digest, overwrite), accepting the extra egress; the chosen
  mode is logged once at startup.
- Today every write is one `PutObject` (`store.go:261-284`), so artifact size is
  bounded by the service's single-request limit. Multipart upload is introduced
  only above `NOTARX_OBJECT_STORE_MULTIPART_THRESHOLD` (default 256 MiB) using
  the SDK transfer manager; part-size and part-count limits are taken from the
  provider and verified by the live test rather than hard-coded here.
- The free Object Storage allowance (20 GB total, 50,000 API requests per month
  after the trial) is too small to be the primary byte store for a registry, and
  one request per blob or per `List` page would exhaust it. The reference
  deployment therefore uses the filesystem driver as the primary store and OCI
  Object Storage only for backups, written as few large objects (see Backups).
  `driver=s3` against OCI is for evaluation or paid tenancies.

### Filesystem driver

Purpose: single node, no extra service. It is not for multi-node and not for
shared network filesystems.

- Layout: `<root>/data/<key>` for bytes, `<root>/meta/<key>.sha256` for the
  verified digest, `<root>/tmp/` for in-flight writes, `<root>/.notarx-root`
  containing a random root ID. All must live on one filesystem so `rename(2)` is
  atomic.
- Keys are validated by the existing rules in `internal/objectstore/validation.go`
  plus: no `..`, no empty or `.`-leading segments, no segment `tmp` or `meta` at
  the top level, no symlink traversal (open with `O_NOFOLLOW`, verify the resolved
  path stays under root).
- Put: stream into `tmp/<uuid>`, hash while writing, compare size and (if given)
  expected SHA-256, `fsync` file, `rename` into `data/`, `fsync` parent directory,
  then write the digest file by the same tmp-and-rename. A crash leaves either the
  old object, the new object, or a stray `tmp/` file removed at startup (files
  older than one hour). A data file without a digest file reads as unverified,
  matching an S3 object lacking metadata, so ordering is safe.
- `Stat` returns size and digest from the sidecar; `SetVerifiedDigest` rewrites
  the sidecar only; `Open`/`OpenRange` use `os.File`; `Delete` removes digest then
  data and is idempotent; `List(prefix)` walks `data/`, returns keys in
  lexicographic order (the backup walker in `internal/backupops/s3.go:39-62`
  already requires ordered listings) and skips `tmp/` and `meta/`.
- Concurrency: same-key concurrent Puts last-writer-wins, as in S3; callers
  already serialise by advisory lock plus content digest.
- Single-node enforcement: a table `object_store_identity(singleton, driver,
  root_id)` is written at first boot; a gateway whose `.notarx-root` ID does not
  match (second node, restored volume, wrong mount) refuses to start. The gateway
  also holds a session advisory lock keyed on the root ID so two processes cannot
  share one root.
- Capacity: `statfs` free-space is exposed to readiness and metrics; writes fail
  with a typed insufficient-storage error under a configurable reserve.

### Contract suite

New package `internal/objectstore/storetest` exports `RunContract(t, factory)`,
covering: put/get round trip, empty object, binary content, size mismatch error,
digest mismatch rejection, `PutVerifiedReader` plus `Stat` digest, `SetVerifiedDigest`
on existing and missing keys, `OpenRange` bounds and error cases, `List` prefix
semantics and ordering past 1000 keys, `Delete` idempotency, not-found mapping to
`ErrNotFound`, keys with unusual but valid characters, concurrent Put of one key,
and context cancellation. It runs against `MemoryStore` and the filesystem driver
in `make test`, against MinIO or RustFS (S3 driver) under the integration build
tag, and optionally against OCI (below). `MemoryStore`'s own `Put` ignoring the
digest is brought into line so the suite is uniform.

### Reference deployment: `deploy/oci-free-tier`

Chosen topology (Compose, one A1 VM, 2 OCPU / 12 GB, Ubuntu arm64, the whole
Always Free A1 allowance):

- `compose.yml` with `postgres:16-alpine`, `migrate` (the existing psql runner),
  `notarx`, `notarx-console` (arm64 images from ghcr.io/mlops-ca, ADR 0013/0016),
  and `caddy` for automatic TLS on a user-supplied DNS name; only 80/443 are
  exposed in the OCI security list. Resource limits: PostgreSQL 3 GB, notarx 3 GB,
  console 256 MB, leaving about 5 GB for the OS and page cache; the scanner
  stays off. PostgreSQL starts with `shared_preload_libraries=pg_stat_statements`.
- Data on one block volume (budget 200 GB total including the 50 GB minimum boot
  volume): `/srv/notarx/{postgres,objects,backups}`; the filesystem driver is the
  default, `NOTARX_OBJECT_STORE_FS_ROOT=/srv/notarx/objects`.
- Backups: nightly `pg_dump` plus the existing backup tooling
  (`scripts/backup-drill.sh`, `internal/backupops`) targeting an OCI Object
  Storage bucket through the compat endpoint. Filesystem objects are exported as
  the new manifest format `fs-bytes-v1`: tar segments (default 256 MiB) plus an
  inventory with per-object SHA-256, so the request count scales with size/256 MiB
  rather than object count and stays far below 50,000 per month; restore is exercised by
  `make restore-drill` semantics in the runbook.
- `cloud-init.yaml` installs Docker, formats and mounts the volume, installs the
  systemd unit that runs Compose, and enables unattended security upgrades.
- Optional `tofu/` (OpenTofu/Terraform, `oracle/oci` provider): VCN, subnet,
  security list, A1 instance, block volume and attachment, Object Storage bucket,
  IAM user, customer secret key (output marked sensitive), least-privilege policy.
  Applying it is optional; the runbook also lists the console steps.
- Distribution: the runbook installs from the digest-pinned `notarx-compose-<version>.tar.gz`
  release asset rendered from this directory (ADR 0025 slice 3); the `.deb` packages
  of ADR 0025 are the alternative for hosts that already run PostgreSQL.
- Variant C (ADR 0022, milestone MySQL HeatWave): the same A1 VM with a MySQL HeatWave Always
  Free DB system on a private endpoint instead of the PostgreSQL container.
- Variant B (documented, not default): a second VM runs PostgreSQL and a
  networked S3 backend, which requires `driver=s3`. Two VMs do not fit the free
  A1 allowance usefully (two 1-OCPU / 6 GB instances), so the variant needs Pay
  As You Go. Idle reclamation applies to Always Free instances when CPU, network
  and memory are all under 20% for seven days; the runbook says so and notes
  that a Pay As You Go account is not subject to it. The RustFS image has no
  verified arm64 manifest, so variant B names MinIO or another verified S3 server
  until slice 6 checks it.

## Security considerations

- Secrets (`ACCESS_KEY`, `SECRET_KEY`) come from env or files mounted outside
  Compose YAML; the OpenTofu state holds the customer secret key, so the module
  documents remote encrypted state and the output is `sensitive`.
- Filesystem driver: path traversal, symlink escape and tmp-file leaks are
  explicit test cases; the root directory is mode 0700 owned by the container
  user; digest verification on write preserves the "bytes match the recorded
  digest" invariant the S3 path has.
- `checksum_mode=when_required` removes client-side integrity trailers; integrity
  continues to rest on the SHA-256 the gateway computes and verifies itself.
- TLS: Caddy terminates TLS; PostgreSQL and the gateway listen only on the
  Compose network; the OCI security list opens 22 (restricted CIDR), 80, 443.

## Consequences

- One database to test and reason about; no portability tax. Users who cannot run
  PostgreSQL are not served, by design and with stated revisit criteria.
- Breaking, intentional renames: environment variables, `TEST_*` variables and
  the digest metadata key. No upgrade path (ADR 0013, 0015).
- The S3 client gains three knobs that every S3-compatible vendor needs; the
  filesystem driver removes the object service from small installs but cannot
  scale out, and backups must copy the root in addition to the database
  (`backupops` gets a filesystem walker).
- OCI free tier is a reference, not a guarantee; provider terms, capacity and
  reclamation can change and are documented as such.

## Implementation slices

1. **S3 driver generalisation and config rename.**
   Scope: `internal/objectstore` (`S3Store`, `Config`, `Open`, driver registry),
   `internal/config/config.go`, `internal/backupops/s3.go`, `compose.yml`,
   `deploy/kubernetes`, docs and `.env` samples, all `TEST_RUSTFS_*` readers.
   - [ ] region, path-style, checksum mode and auto-create flags with unit tests
         using a local HTTP stub asserting SigV4 region and absence of checksum
         headers in `when_required`
   - [ ] no occurrence of `GATEWAY_RUSTFS`, `RustFSStore`, `TEST_RUSTFS` remains
   - [ ] `make test`, `make lint`, `make integration-test`, `make docs-check` pass
   Depends on: ADR 0013 slice 4.
2. **Object store contract suite.**
   Scope: new `internal/objectstore/storetest`, wiring in `store_test.go` and
   `s3_integration_test.go`, CI job running MinIO and RustFS service containers.
   - [ ] suite passes for `MemoryStore` and S3 driver (MinIO and RustFS)
   - [ ] suite is required by coverage floors for `internal/objectstore`
   - [ ] `make test`, `make lint`, `make integration-test`, `make coverage` pass
   Depends on: 1.
3. **Filesystem driver.**
   Scope: `internal/objectstore/filestore.go`, `object_store_identity` in a new
   numbered migration with a repository store, startup wiring, readiness
   and capacity metrics.
   - [ ] contract suite passes; crash-injection tests for each commit step;
         traversal, symlink and reserved-name tests
   - [ ] second-root and wrong-root startup refusal tests (integration)
   - [ ] `make test`, `make race`, `make lint`, `make architecture-check`,
         `make coverage` pass
   Depends on: 2, ADR 0015 slice 4. The `object_store_identity` table lands as a
   normal new numbered migration (000002 or later), not an edit of the baseline.
4. **Backup, restore and replication on both drivers.**
   Scope: `internal/backupops` (filesystem walker and transfer, S3 settings with
   region/path-style), `internal/backupmanifest` (`fs-bytes-v1` format accepted
   by `verify.go:227`, with tests), `scripts/backup-drill.sh`, restore drill.
   - [ ] `fs-bytes-v1` tar-segment export, inventory and restore verification, with
         an assertion that request count is bounded by size/segment size
   - [ ] backup-restore round trip with each driver in `make backup-transfer-test`
         and `make backup-restore-readiness`
   - [ ] `make test`, `make integration-test`, `make lint` pass
   Depends on: 1, 3.
5. **OCI compatibility probe and live test.**
   Scope: capability probe for `CopyObject` REPLACE with streaming fallback,
   multipart above threshold, `//go:build ocilive` test running the contract suite.
   - [ ] live job runs only when `OCI_*` repository secrets exist, never on forks
         or untrusted PRs, uses a throwaway prefix and cleans up
   - [ ] fallback path covered by unit tests with a stub that rejects `CopyObject`
   - [ ] live checklist includes keys containing `/` for `CopyObject`, and
         `aws-chunked` single-part and multipart uploads with both checksum modes
   - [ ] `make test`, `make lint` pass
   Depends on: 2.
6. **`deploy/oci-free-tier` Compose stack and runbook.**
   Scope: compose, Caddyfile, cloud-init, systemd unit, env template,
   `docs/deployment/oci-free-tier.md` (English; ADR 0014 governs optional locales).
   - [ ] `docker compose config` check in CI for arm64 service definitions; every
         image (postgres, caddy, notarx, console; RustFS only if used) has an arm64
         manifest (script asserting `docker buildx imagetools inspect`)
   - [ ] resource limits sum within 12 GB; compose starts PostgreSQL with
         `shared_preload_libraries=pg_stat_statements`
   - [ ] smoke script: push and pull an OCI image and a Raw file through Caddy
   - [ ] the directory layout is documented for `scripts/render-compose-bundle.sh` (ADR 0025 slice 3 renders and verifies the bundle)
   - [ ] `make docs-check` passes
   Depends on: 1, 3, 4, ADR 0016 slice 3.
7. **OpenTofu module (optional).**
   Scope: `deploy/oci-free-tier/tofu`.
   - [ ] `tofu validate` and `tflint` in CI; no secrets in state examples
   - [ ] runbook documents manual console equivalent
   Depends on: 6.
8. **Documentation alignment.**
   Scope: `docs/postgresql-capabilities.md` (counts, revisit criteria, managed
   PostgreSQL support matrix with verified providers),
   `ARCHITECTURE.md`, configuration reference.
   - [ ] `make docs-check` and `make openapi-check` pass (readiness fields, if
         any, added OpenAPI-first with regenerated server/client)
   Depends on: 3.

## Open questions

- Does the OCI S3 compat API honour `CopyObject` with `MetadataDirective: REPLACE`
  and what are its multipart part-size and part-count limits? Slice 5 answers
  this empirically; defaults here are provisional.
- Does the pinned RustFS digest (`compose.yml:227`) publish an arm64 manifest, or
  must the arm64 reference deployment use another S3 server for variant B?
- Should the filesystem driver support a second tier (hardlink-based dedup across
  repositories), or is dedup left to the database-level object table as today?
- Do we offer an in-tree OCI native-SDK driver if compat quirks prove too costly
  (design 3 above)?
