# Generic Versioned Packages on Raw

[Documentation index](../README.md)

Status: accepted (2026-11-17)

Management paths in this ADR are relative to `/api/v2` (the `servers` entry of
`api/openapi/management.yaml`). Root-mounted protocol endpoints are specified in
`api/openapi/protocols/*.yaml`.

## Context

Raw is a path store, not a package store.

- **Model.** `native_raw_assets` is `(repository_id, path) -> digest`
  (`migrations/000024_native_raw_hosted.sql:9-17`); bytes live once per digest in
  `native_raw_objects`. A path is a mutable pointer: `PUT /raw/{repository}/{path}`
  overwrites by default, and create-only is an opt-in per request through
  `If-None-Match: *` (`internal/format/raw/condition.go:41-47`, store flag
  `RawWriteCondition.RequireAbsent`, `internal/repository/raw_condition.go:17`).
- **Publication coordinator.** `internal/format/raw/publication.go` validates one
  asset per publication (`ValidatePublication`, line 15: `len(assets) != 1`),
  stages bytes, then `PublishPublication` calls `PutRawAssetIf` with
  `RequireAbsent: true` (line 50). The coordinator already gives create-only
  semantics; the HTTP route does not use it for plain PUT.
- **Governance.** The coordinate of a Raw asset is its path
  (`internal/format/raw/projection.go:50`, `ValidCoordinate` = valid non-empty
  prefix). `Tombstone`/`Restore` and `PlanRetention`
  (`internal/format/raw/services.go:45-110`) operate per file; retention is
  age-only per asset: `RepositoryRetentionPolicy.MinimumVersions` and
  `MaximumVersions` (`internal/repository/model.go:297-307`) exist but Raw ignores
  them.
- **Listing.** Prefix listing only (`QueryStore.ListRawAssets`,
  `internal/format/raw/module.go`). There is no "versions of a package" query, and
  the prefix index `native_raw_assets_repository_path_pattern_idx`
  (`migrations/000069`) serves only prefix scans.
- **Promotion and replication.** Raw promotion and replication move one asset
  per job: `Replication.publish` rejects `len(checkpoints) != 1`
  (`internal/format/raw/replication.go`). There is no way to promote "release
  1.4.2 of tool X" as a unit. ADR 0021 asks for exactly this in its open
  question about a "release set" for `scope: release`, and asks whether a
  generic property store should exist.
- **Scanning.** `format.Scanner.ResolveArtifactScan` returns a
  `scanning.Artifact` with a list of `Assets`
  (`internal/format/raw/services.go:40`), so a multi-file unit is already
  representable.
- **CLI and competitors.** `publish raw` uploads files or trees (`internal/agctl/publish_commands.go:229`, per-file cap 1 GiB, `maxRawObjectBytes`). GitLab generic packages use `/packages/generic/<name>/<version>/<file>`; Artifactory uses free paths plus properties; Nexus Raw is path-only.
- **Rules.** ADR 0021 rules match the canonical coordinate and never on mutable
  data; ADR 0021 defers properties. `domain_events` carries `coordinate` and
  `digest`.

Release scope: this ADR is milestone Formats, after v0.1.0 (ADR 0013 ordering),
so schema changes are numbered migrations (`000002+`, ADR 0015). If Formats
work is pulled in before ADR 0015 slice 7 (re-squash), the same SQL folds into
the baseline unchanged.

## Considered designs

1. **Keep Raw as is; document a convention.** No code. Nothing enforces
   immutability, nothing lists versions, promotion stays per file, and retention
   by version count is impossible. Rejected as the only answer; the docs
   slice still states the convention.
2. **A new format `generic`.** Own tables, routes and profile. Clean, but
   duplicates Raw's upload, resumable, range, group and replication code, adds
   another core format to the format registry (ADR 0011), and splits users of
   `curl --upload-file` across two URL spaces. Rejected.
3. **A layout mode on Raw repositories (chosen).** The data plane stays
   `/raw/{repository}/...` and `native_raw_assets`. A repository created with
   `layout: versioned` enforces `<name>/<version>/<file>`, create-only files,
   and gains a small `native_raw_versions` index table that makes a version a
   first-class, sealable, promotable unit. Costs one table, one column and a
   branch in the Raw module; reuses everything else.
4. **Generic artifact property store across all formats (Artifactory style).**
   Most general, but every format's publish transaction changes (ADR 0021 open
   question) and mutable properties conflict with digest-pinned rule
   evaluation. Rejected now; a bounded, frozen-at-seal map is kept for
   versioned generic only (below).

## Decision

Choose design 3. Format identity stays `raw`: no new format, no change to
`hosted_repositories.format` CHECK, no new permission or grant model (ADR 0006
roles apply unchanged).

### Repository layout

`hosted_repositories.raw_layout TEXT NOT NULL DEFAULT 'path'` with
`CHECK (raw_layout IN ('path','versioned'))` and `seal_mode TEXT NOT NULL DEFAULT
'explicit'` with `CHECK (seal_mode IN ('explicit','idle'))`. Both apply to
`format = 'raw'`, `repo_type = 'hosted'` only (proxy and non-Raw repositories
keep the defaults; enforced by a shape CHECK like
`hosted_repositories_repo_shape_check`, `migrations/000051`). They are chosen at
`POST /repositories` and are immutable afterwards; changing a non-empty
`path` repository to `versioned` is not offered (`409 layout_immutable`).
`GET /repositories/{id}/capabilities` reports `layout` and `versions: true`.
Groups may contain versioned and path members; group reads resolve by path
exactly as today and stay read-only.

### Coordinates and paths

Layout `<name>/<version>/<file>` inside the Raw path; the coordinate is
`name@version` (what `Governance.ValidCoordinate`, retention, scanning,
promotion and events use). Validation is ASCII-only, applied after
`v2contract.CanonicalRawPath`:

- `name`: `^[a-z0-9][a-z0-9._-]{0,127}$` (lower case, so names cannot collide
  by case or confuse visually);
- `version`: `^[A-Za-z0-9][A-Za-z0-9._+~-]{0,127}$`; semver is not required;
- `file`: one to eight further segments (a release tree is allowed), each valid
  per `CanonicalRawPath`; the first segment must not be `.notarx`
  (reserved). Paths with fewer than three segments are rejected in versioned
  repositories (`422 invalid_generic_path`).

### Version lifecycle

`native_raw_versions.state`: `open -> sealed -> tombstoned` (and `tombstoned ->
sealed` through restore).

- The first successful file `PUT` for an unknown `name@version` creates the row
  `open` in the same transaction that publishes the file.
- **Files are create-only.** A `PUT` to an existing path with the same digest is
  an idempotent `200` (safe CI retry); with a different digest it is
  `409 version_file_exists` and never mutates. `If-None-Match` is not needed and
  `If-Match` is rejected. Implementation: the publication coordinator
  (`PutRawAssetIf` + `RequireAbsent`) then, on `ErrPreconditionFailed`, a digest
  comparison under the version row lock.
- While `open`, a single file may be `DELETE`d (to repair a failed upload) and
  re-uploaded; this is the only mutation and it is refused once sealed
  (`409 version_sealed`). Reads work in every state except `tombstoned`.
- **Sealing** (`POST /repositories/{id}/packages/{name}/versions/{version}:seal`)
  takes the version row lock, rejects an empty version, optionally verifies an
  expected file list (`files: [{path, sha256}]`, exact set equality) and an
  uploaded checksums file (`checksumsFile: "SHA256SUMS"`, parsed as `sha256sum`
  output, every listed file must exist with that digest; mismatch is
  `422 checksum_mismatch`), computes the manifest digest, freezes properties, and
  sets `sealed`. Sealing twice is idempotent and returns the same digest. No file
  may be added or removed after seal; the row lock serialises seal against
  concurrent uploads.
- `seal_mode = idle`: a sweep seals open versions with no write for
  `NOTARX_GENERIC_AUTOSEAL_IDLE` (default 300s, minimum 60s). It exists for
  GitLab-style and plain `curl` publishers that never call seal.
- **Tombstone** hides the version (hosted reads and listings return 404, like a
  Raw asset tombstone) and burns the identifier: the same `name@version` cannot
  be re-published (`409 version_tombstoned`) until restored. Garbage collection removes
  the tombstoned files' unreferenced objects but keeps the version row, so the identifier stays
  burned permanently; only an administrator may delete the row (audited, `DELETE ...?purge=true`).
  Restore is allowed while file rows exist (ADR 0002 lifecycle).
- **Store-level guard.** The layout rule is enforced in `replaceRawAssetIf` (`internal/repository/postgres_raw.go:210`, shared by `PutRawAssetIf` and `CompleteRawUploadIf`, so resumable uploads are covered) and its memory/MySQL equivalents, not only in the HTTP handler: any write, delete or restore of a `native_raw_assets` row in a versioned repository that does not come through the versioned store methods is refused. Nexus migration, snapshot import and backup restore therefore cannot bypass create-only or seal.
- Only `sealed` versions are eligible for events, promotion, replication,
  retention and rule matching. Publishing events on individual files in a
  versioned repository is suppressed.

### Data model

Conventions follow ADR 0023 (UUIDv7 through `internal/ids`, typed keys, no
partitioning because the tables are bounded and updated, FKs only to
non-partitioned tables, MySQL-portable per ADR 0022: bounded `VARCHAR(128)` for
name and version, regex validated in Go and not by a PostgreSQL-only `~` CHECK; MySQL columns use the
`utf8mb4_0900_bin` collation of ADR 0022 so `1.0.0-RC1` and `1.0.0-rc1` stay distinct
versions, and column names avoid MySQL reserved words such as `key`):

```
native_raw_versions(
  id UUID PK,                      -- ids.New()
  repository_id UUID NOT NULL REFERENCES hosted_repositories(id),
  name VARCHAR(128), version VARCHAR(128),
  state TEXT CHECK (state IN ('open','sealed','tombstoned')),
  file_count INT, size_bytes BIGINT,       -- maintained in the publish txn
  manifest_digest TEXT NULL,               -- set at seal, 'sha256:<hex>'
  created_by TEXT, created_at, sealed_at NULL, updated_at,
  UNIQUE (repository_id, name, version),
  INDEX (repository_id, name, sealed_at DESC, id),      -- version listing
  INDEX (repository_id, state, sealed_at) )             -- retention, idle sweep
native_raw_version_properties(
  version_id UUID NOT NULL REFERENCES native_raw_versions(id) ON DELETE CASCADE,
  repository_id UUID NOT NULL,             -- denormalised for the index
  prop_key VARCHAR(64), prop_value VARCHAR(256),   -- `key` is reserved in MySQL
  PRIMARY KEY (version_id, prop_key),
  INDEX (repository_id, prop_key, prop_value) )
```

The files of a version are the `native_raw_assets` rows in the half-open path
range `[name/version/, name/version0)` (`/` + 1 is `0`), a byte-ordered range
scan on the existing `native_raw_assets_repository_path_pattern_idx`
(`migrations/000069`, `text_pattern_ops`, so the scan is independent of the database
locale) on PostgreSQL and the binary-collation primary key on MySQL (ADR 0022 slice 7). No
`LIKE`, so `_` in names needs no escaping. Repository quota continues to
count assets (ADR 0023 section 3); `file_count`/`size_bytes` are display and
retention aids, updated under the version row lock (bounded contention: at most
`NOTARX_GENERIC_MAX_FILES_PER_VERSION` writers per row; the lock is held only for the
publish transaction, never while bytes stream, because bytes are staged first).

### Manifest, checksums and digest

Every sealed version has a derived manifest, never stored as an object:

```
{"name":..,"version":..,"files":[{"path":..,"sha256":..,"size":..}...sorted by path],
 "properties":{...sorted keys}}
```

serialised canonically (sorted keys, no insignificant whitespace, UTF-8). The
version digest is `sha256` of that byte string and is the `digest` used by
promotion pinning, events and provenance (ADR 0021): a release set is identified
by `(name@version, manifest_digest)`, so any file or property difference yields a
different digest. `GET .../versions/{version}/manifest` returns the JSON,
`GET .../versions/{version}/checksums` returns `sha256sum -c`-compatible text
(`<hex>  <path>`, sorted). A publisher-supplied `SHA256SUMS` is just a file and
is verified at seal only if `checksumsFile` is named. Cap:
`NOTARX_GENERIC_MAX_FILES_PER_VERSION` (default 1000) keeps the manifest under
256 KiB so nothing large enters the control plane (ADR 0023 section 9).

### Properties (included, narrowly)

Decision: include a per-version property map for versioned repositories only.
It is not a cross-format property store and does not touch other formats'
publish APIs.

- Bounded: at most 32 keys, key `^[a-z][a-z0-9._-]{0,63}$`, value at most 256
  bytes of UTF-8 without control or bidi characters (the `invalidRawPathRune`
  set), 8 KiB in total.
- Set only while `open` (`PUT .../versions/{version}/properties`, full replace,
  or `properties` in the seal body); **frozen at seal and part of the manifest
  digest**. This satisfies ADR 0021's rule that matches never use data that can
  change after the decision.
- Stored in the side table, not a JSON column, so lookup is a plain B-tree
  `(repository_id, prop_key, prop_value)` on both PostgreSQL and MySQL; no GIN index.
- Query: `GET .../versions?property=key:value` (repeatable, AND). Promotion
  copies them with the version. Typical keys: `git.sha`, `build.number`,
  `channel`. Values are untrusted text (escaped by the console, never logged).

If a cross-format property store is later wanted, this table is its
migration source; nothing here blocks it.

### Management API (OpenAPI-first, `api/openapi/management.yaml`)

| Operation | Permission |
| --- | --- |
| `GET /repositories/{id}/packages` (names, latest sealed version, counts; keyset pagination, ADR 0023 section 8) | read |
| `GET /repositories/{id}/packages/{name}/versions` (`state`, `property`, `cursor`, `limit`) | read |
| `GET .../versions/{version}` (state, files with digest/size/content type, `manifestDigest`, properties) | read |
| `PUT .../versions/{version}` (create `open` explicitly with properties; idempotent) | write |
| `PUT .../versions/{version}/properties` | write, `open` only |
| `POST .../versions/{version}:seal` | write |
| `GET .../versions/{version}/manifest`, `.../checksums` | read |
| `DELETE .../versions/{version}`, `POST .../versions/{version}:restore` | the existing tombstone/restore permission |

`latest` means the newest sealed version by `sealed_at`, not the highest semver
(see Consequences). Errors use the existing problem format with codes
`version_file_exists`, `version_sealed`, `version_tombstoned`,
`invalid_generic_path`, `checksum_mismatch`, `layout_mismatch`,
`layout_immutable`. Seal, tombstone and restore write audit rows with the
actor and manifest digest. The data plane is unchanged
(`api/openapi/protocols/raw.yaml`), with the versioned-layout behaviours added
to its operation descriptions.

### Retention by version count

`Raw.PlanRetention` branches on layout. For versioned repositories candidates
are sealed versions, `Coordinate = name@version`, `Digest = manifest_digest`,
`VersionType = "release"`. Per name, ordered by `sealed_at` descending: the
newest `MinimumVersions` are always kept; versions beyond `MaximumVersions` are
candidates (reason `count`); the existing `KeepDays` rule applies to the rest
(reason `age`). `CoordinatePatterns` and `ProtectedPatterns` match
`name@version`. `Tombstone` on a versioned coordinate tombstones the version and
its files. Open versions are never retention candidates (an abandoned open
version is visible in the console and cleaned by an operator or `idle`).

### Promotion, replication and events (answers ADR 0021 open question)

The release set is the version. In a versioned repository the unit of promotion,
replication, import and rule matching is `(name@version, manifest_digest)`.

- `Promotion.EnqueuePromotion` accepts the coordinate `name@version`. It
  requires the source `sealed`, a `versioned` target (`422 layout_mismatch`),
  the existing quarantine and admission-policy checks over all files
  (`ResolveArtifactScan` returns every file as an `Asset`; quarantine of any
  file's digest blocks the version).
- The job copies the version atomically in one transaction: the version row
  (`sealed`, same `manifest_digest`, `created_by` = promoter), every asset row
  (objects reused, ADR 0002) and the properties. It recomputes the manifest at
  the target and fails if the digest differs. Target already holds the same
  `manifest_digest`: success no-op. Different digest at the same
  `name@version`: `conflict` (immutability). No partial version is ever visible.
- `artifact.published` (ADR 0021) is emitted by the seal transaction with
  `coordinate = name@version` and `digest = manifest_digest`; dedupe key per
  ADR 0021 needs nothing new. Promotion to the target emits the same event
  there (target version is created `sealed`).
- Rules: `match.coordinate` globs `name@version`, `match.version` evaluates
  semver ranges only when `version` parses as semver (otherwise the rule does
  not match, never errors), and `match.properties: {key: glob}` is allowed only
  for sources that are versioned generic. This is the one extension to
  ADR 0021 matching; properties are frozen so the evaluation stays pinned.
- Replication plans and remote push (ADR 0021 7b/8) treat the version as the
  unit: checkpoints are per file, and the destination publishes the version
  `sealed` only after every file is verified. `Exporter` returns the manifest
  JSON plus file blob descriptors; the `Importer` recomputes the manifest from
  verified bytes and rejects a claimed coordinate or digest that differs.

### CLI (`notarxctl`)

- `generic push --repo ID --name N --version V [--property k=v]... [--checksums]
  [--no-seal] [--parallel N] [--resumable] PATH...`: uploads files through the
  Raw data plane (reusing the `publish raw` upload and resumable code), then
  seals with the expected file list; `--checksums` writes and verifies a
  `SHA256SUMS` file. Exit code distinguishes `409 version_file_exists` with a
  different digest (conflict) from an idempotent replay (success).
- `generic pull --repo ID --name N --version V [--file F]... [--to DIR]`: reads
  the version detail, downloads each file, verifies size and sha256 per file and
  the manifest digest; refuses `open` versions unless `--allow-open`.
- `generic list|show|seal|delete|restore` map 1:1 to the management API;
  `generic show --manifest|--checksums` prints the derived documents.
- `publish raw` remains for `path` repositories and fails with a hint on a
  versioned repository. Every new command is classified in
  `docs/cli-command-map.md` (`make cli-contract-check`).

### GitLab generic-style URLs

Not included: the wire shape `PUT /raw/{repository}/{name}/{version}/{file}` already matches, and a GitLab alias needs a project-id mapping and `JOB-TOKEN` header auth for little gain. `docs/generic-packages.md` documents the `curl` equivalents.

### Configuration

`NOTARX_GENERIC_MAX_FILES_PER_VERSION` (1000),
`NOTARX_GENERIC_AUTOSEAL_IDLE` (300s, min 60s),
`NOTARX_GENERIC_AUTOSEAL_INTERVAL` (60s sweep period, lease-elected through
`scheduled_tasks` per ADR 0023 section 2). The per-file size cap is inherited
from Raw (1 GiB, `internal/format/raw/native.go:113`) and is unchanged by ADR 0027, whose larger
cap applies to Hugging Face only.

### Anonymous reads

Versioned and path Raw repositories follow ADR 0029 repository visibility; the Raw module supplies only `PublicPathPrefixes` and `CacheClass` (slice 11) and makes no access decisions. Only sealed versions are served anonymously, also through public groups: group resolution of a versioned member skips `open` and `tombstoned` versions for anonymous callers.

## Security considerations

- **Immutability is the control.** Create-only files, sealed versions with a
  content-derived digest, burned identifiers after tombstone, and a seal that
  holds the row lock prevent a published release being silently replaced by
  overwrite, delete-and-reupload (not possible after seal) or concurrent upload.
  Overwrite attempts are audited with actor and path.
- **Path abuse.** All paths pass `CanonicalRawPath` (no `..`, encoded slashes,
  control or bidi characters); `name` is lower-case ASCII; `.notarx/` is reserved
  so future metadata cannot be spoofed by an upload.
- **Digest trust.** `manifest_digest` is always recomputed server-side from the
  stored file digests, never taken from a client; an expected list or checksums
  file only adds a check.
- **No cross-version leakage.** Promotion and import authorise on source read and
  target write per ADR 0021; the missing-blob rule of the import API applies per
  file.
- **Properties are untrusted.** Bounded size and charset, escaped in the console,
  excluded from logs and webhook payload secrets scan, and never executed or
  interpolated into paths or SQL (bound parameters only).
- **Resource abuse.** File count per version, property count and bytes, and the
  open-version row lock bound work per request; abandoned open versions are
  visible and count toward repository quota.
- **Quarantine fencing** is checked at promotion and replication publish for every
  file of a version, as for single Raw assets.

## Consequences

- Raw gains a real release-set concept without a new format, route, permission
  or storage path; existing `path` repositories are untouched, and uploads with
  plain `curl` keep working.
- Users of `explicit` sealing must call seal (the CLI does); `idle` mode trades
  determinism for convenience. A version that is never sealed is readable but
  cannot be promoted, replicated or matched by a rule.
- `latest` is by seal time, not semver, because a stored sortable semver key
  would need a normalised column and breaks for non-semver versions; clients may
  sort the listing. Revisit if requested.
- ADR 0021 gains a concrete `scope: release` definition for Raw versioned
  repositories and a narrow property matcher; other formats still promote per
  coordinate.
- Versioned repositories add per-file rows to the artifact catalogue
  (ADR 0023 section 8) like any Raw repository; the version API, not the
  catalogue, is the browse surface for versions.
- The Raw module carries a layout branch in publish, delete, retention,
  promotion and replication; the cost is contained in `internal/format/raw`
  (`versioned.go`) and covered by the existing Raw contract suites run per
  layout.

## Implementation slices

Every slice is one PR and passes `make test`, `make lint` (includes `architecture-check`) and `make coverage`. API slices edit `api/openapi/*` first and pass `make openapi-check`; CLI mappings pass `make cli-contract-check`. All depend on ADR 0015 slice 4 and use numbered migrations (`000002+`) unless folded before ADR 0015 slice 7. This ADR owns generic versioned packages; the Hugging Face ADR (0027) has no generic slice.

1. **Layout, version tables and store contract.** Milestone: Formats.
   Scope: migration for `raw_layout`, `seal_mode`, `native_raw_versions`, `native_raw_version_properties`; `POST /repositories` and capabilities fields in `management.yaml`; `NativeRawVersionStore` in `internal/repository` (memory and PostgreSQL; MySQL by ADR 0022 slice 7); contract suite in the style of `internal/repository/raw_store_contract_test.go`.
   - [ ] Contract tests pass on memory and PostgreSQL (and MySQL once ADR 0022 stores exist); shape CHECK rejects layout on proxy and non-Raw repositories; `layout_immutable` test.
   - [ ] Version file range query is index-backed (EXPLAIN test, ADR 0023 section 11); column names avoid MySQL reserved words.
   - [ ] `make migrate`, `make integration-test`, `make openapi-check`, `make test`, `make lint`
   - Depends on: ADR 0015 slice 4; ADR 0023 slice 1 (`internal/ids`).
2. **Versioned write path.** Milestone: Formats.
   Scope: `internal/format/raw` (`versioned.go`, `publication.go`, delete), `internal/protocol/raw`, `internal/repository/postgres_raw.go` store-level guard, `api/openapi/protocols/raw.yaml`.
   - [ ] Path validation table tests (name/version/file, reserved `.notarx`, bidi, traversal); same-digest replay is 200, different digest is 409 and never mutates.
   - [ ] Race test: N concurrent PUTs of one path yield one winner and consistent `file_count`; delete allowed only while open; tombstoned version refuses re-publish; files-per-version cap.
   - [ ] Resumable upload completion and a direct store call on a versioned repository obey the same create-only rule (store-level guard test).
   - [ ] `make raw-e2e` stays green for `path` repositories; `make openapi-check`, `make test`, `make lint`
   - Depends on: slice 1.
3a. **Version read API and manifest.** Milestone: Formats.
   Scope: handlers composed per ADR 0012 for packages list, versions list (state, property filter, keyset cursor), version detail, `manifest`, `checksums`; canonical manifest builder; OpenAPI first.
   - [ ] List, detail and keyset pagination tests; manifest digest golden test (canonical bytes pinned); `checksums` output verified with `sha256sum -c`.
   - [ ] Permission tests per role (read); tombstoned versions return 404.
   - [ ] `make openapi-check`, `make test`, `make lint`
   - Depends on: slice 1.
3b. **Version write API: create, properties, seal.** Milestone: Formats.
   Scope: `PUT .../versions/{version}`, `PUT .../properties`, `POST ...:seal` (expected file list, `checksumsFile`, idempotency), audit rows, properties validation.
   - [ ] `checksumsFile` mismatch is 422; seal twice returns the same digest; seal freezes properties, limits enforced.
   - [ ] Seal serialises against concurrent upload (race test); permission tests per role (write).
   - [ ] `make openapi-check`, `make cli-contract-check`, `make test`, `make lint`
   - Depends on: slices 2, 3a.
4. **Governance, scanning, tombstone and retention by version.** Milestone: Formats.
   Scope: `Governance`, `Retention` and `Scanner` for versioned coordinates in `internal/format/raw/services.go`; `DELETE`/`:restore` version operations and admin-only purge in `management.yaml`.
   - [ ] `ValidCoordinate` accepts `name@version` only in versioned repositories; retention table tests (min/max versions, age, protected patterns, open versions excluded).
   - [ ] Scan covers every file and quarantine of one file blocks the version; tombstone and restore round trip; GC keeps the burned version row.
   - [ ] `make openapi-check`, `make test`, `make integration-test`, `make lint`
   - Depends on: slice 3b.
5. **Idle auto-seal.** Milestone: Formats.
   Scope: sweep job, config keys, `scheduled_tasks` lease.
   - [ ] Injected-clock test; minimum interval enforced; only one node sweeps (lease test).
   - [ ] `make test`, `make integration-test`, `make lint`
   - Depends on: slice 3b.
6. **Promotion of versions and published event.** Milestone: Formats.
   Scope: Raw `Promotion` for the versioned layout, `artifact.published` emitted in the seal transaction, `layout_mismatch`, property copy.
   - [ ] Atomic copy test (fault injected mid-copy leaves nothing visible); same digest is a no-op, different digest is `conflict`; unsealed source refused; manifest digest and properties preserved.
   - [ ] Seal emits one event with `coordinate = name@version` and `digest = manifest_digest`; promotion emits at the target.
   - [ ] `make test`, `make integration-test`, `make lint`
   - Depends on: slices 3b, 4; ADR 0021 slices 1b and 2b (manual promotion lands without the rule parts).
7. **`notarxctl generic` commands.** Milestone: Formats.
   Scope: `internal/notarxctl` (package named by ADR 0013 slice 3), `docs/cli-command-map.md`, `.agents/skills/notarxctl`.
   - [ ] Push, pull, verification failure and conflict exit codes tested against a fake server; `--checksums` round trip.
   - [ ] `make cli-test`, `make cli-contract-check`, `make lint`
   - Depends on: slice 3b; ADR 0013 slice 3.
8. **Console versions view.** Milestone: Formats.
   Scope: `console/` package list, version detail (files, digests, properties, seal state, checksums copy), seal, tombstone and promote actions, i18n keys en-CA and fr-CA (ADR 0014), brand tokens (ADR 0013).
   - [ ] Properties rendered escaped; actions keyboard-operable.
   - [ ] `make console-test`, `make console-check`, `make console-api-check`, `make console-visual`
   - Depends on: slices 3b, 4.
9. **Remote import and push of versions.** Milestone: Formats.
   Scope: `Exporter` and `Importer` for versioned Raw (ADR 0021 capability registry); per-file checkpoints with the version published `sealed` only after every file is verified.
   - [ ] Round-trip manifest digest equality between two instances; importer rejects a mismatched claimed digest; partial transfer leaves no visible version.
   - [ ] `make promotion-remote-e2e` (extended), `make test`, `make lint`
   - Depends on: slice 6; ADR 0021 slices 7b and 8.
10. **E2E and docs.** Milestone: Formats.
   Scope: `make generic-e2e` (extends `raw-e2e` fixtures: push, seal, pull, promote, retention), new `docs/generic-packages.md` (convention, `curl` recipes including GitLab-style equivalents, sealing, retention), updates to `docs/nexus-gap-analysis.md` and `docs/security-admission-policy.md`.
   - [ ] `make generic-e2e`, `make docs-check`, `make lint`
   - Depends on: slices 6, 7.
11. **Anonymous reads of public versioned repositories.** Milestone: Public feeds.
   Scope: versioned-Raw `PublicPathPrefixes` and `CacheClass` (sealed version files immutable, listings short-lived) under ADR 0029; no access decisions in the module.
   - [ ] Anonymous `GET`, `HEAD` and `Range` of a sealed version file in a public repository succeed; open versions, tombstoned versions and `.notarx/` are not reachable, including through a public group; private repositories deny.
   - [ ] `make generic-e2e`, `make test`, `make lint`
   - Depends on: slice 6; ADR 0029 slices 2, 5, 7 and 14.
12. **Promotion rule matching for versions.** Milestone: Formats.
   Scope: register versioned Raw as a source for `match.coordinate` (`name@version`), `match.version` (semver only when parseable) and `match.properties` in the ADR 0021 matcher.
   - [ ] Non-semver version never matches a range and never errors; `match.properties` accepted only for versioned sources (`422` otherwise); evaluation pinned to frozen properties.
   - [ ] `make test`, `make integration-test`, `make lint`
   - Depends on: slice 6; ADR 0021 slice 3a.

## Open questions

- Should `latest` and listing order support a semver sort key (normalised
  column) for repositories whose versions are all semver, or stay seal-time only?
- Should an open version older than a TTL be auto-tombstoned or only reported?
- Is a per-file `DELETE` in an open version acceptable to compliance users, or
  should `layout: versioned` offer a strict mode where even open files are
  create-only and a failed upload requires a new version?
