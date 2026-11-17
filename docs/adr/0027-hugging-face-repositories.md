# Hugging Face Model and Dataset Repositories

[Documentation index](../README.md)

Status: accepted (2026-11-17)

## Context

ML teams pull multi-gigabyte model weights and datasets from the Hugging Face Hub
through `huggingface_hub` (`hf_hub_download`, `snapshot_download`,
`from_pretrained`) with `HF_ENDPOINT` and `HF_TOKEN`. Air-gapped and regulated
sites need a controlled mirror, and teams that fine-tune need a private place to
push. notarx has no Hugging Face (HF) format. This ADR adds one, milestone Formats.

Facts about the current tree that shape the design:

- **Format modules.** `internal/format/capabilities.go` defines optional
  capabilities (`Profile`, `Scanner`, `Retention`, `Governance`, `Promotion`,
  `Replication`, `Browse`, `Usage`, `CacheMaintenance`, `Background`). Only
  `raw`, `helm`, `nuget` and `rubygems` live under `internal/format/`; ADR 0011
  owns the module contract. ADR 0021 adds `format.Tagged` (mutable refs such as
  OCI tags) and `Importer` for promotion.
- **Protocol specs.** `api/openapi/protocols/` holds one spec per format (`raw.yaml`,
  `helm.yaml`, ...). Management API paths are relative to `/api/v2`.
- **Raw proxy cannot serve HF as is.** `internal/format/raw/upstream.go:65-67` (`CheckRedirect` returns `http.ErrUseLastResponse`)
  refuses every upstream redirect; HF resolves a file with a 302 to a CDN, so one
  controlled hop is needed. A cold GET fetches and validates the whole object
  before applying ranges (`docs/raw-http-ranges.md`). The cache object limit is
  `defaultMaxObjectBytes = 1 << 30` (`internal/protocol/raw/cache.go`), set from
  `RawCacheMaxObjectBytes` (`internal/config/config.go:121,208`), and raw PUT is
  capped at 1 GiB (`internal/format/raw/native.go:113`). Multi-shard models have
  5-50 GB shards.
- **Egress rules.** `docs/proxy-egress-design.md` already requires that
  cross-origin redirects drop `Authorization`, `Cookie` and `Proxy-Authorization`
  and that the allowlist is re-evaluated per hop.
- **Auth.** Raw uses a Basic realm. ADR 0019 adds a bearer branch and
  JWT-as-Basic-password (`AuthenticateBasicContext`), so workload tokens work
  wherever a token is accepted. HF clients send `Authorization: Bearer <HF_TOKEN>`.
- **Storage.** Objects are stored by digest (`NativeObjectKey(digest)`,
  `internal/format/raw/native.go:139`), so identical LFS blobs dedupe. ADR 0023
  owns quota counters (`repository_usage_counters`), UUIDv7 ids and retention.
- **Protocol behaviour (HF Hub, verified against the public OpenAPI at
  `https://huggingface.co/.well-known/openapi.json` and the `huggingface_hub`
  sources).** The client issues `HEAD /{repo}/resolve/{rev}/{file}` without
  following redirects and reads `X-Repo-Commit`, `ETag`, `X-Linked-Etag`,
  `X-Linked-Size` and `Location`; then GETs the file. Other calls:
  `GET /api/{models|datasets|spaces}/{id}`, `.../revision/{rev}`,
  `.../tree/{rev}[/path]` (paginated by `Link`). Large files use HTTP Range with
  resume. Hub storage is now Xet (chunk-level CAS); a client discovers it from an
  `X-Xet-Hash` header on the resolve redirect and, if absent, uses plain HTTP.
  Upload is `POST /api/{type}s/{id}/preupload/{rev}`, LFS batch at
  `POST /{id}.git/info/lfs/objects/batch`, then `POST /api/{type}s/{id}/commit/{rev}`
  with NDJSON.
- **Prior art.** Artifactory (local, remote, virtual; Spaces unsupported; 409
  without overwrite permission; virtual repos serve `snapshot_download` only; a
  pinned client range), Nexus (proxy only, bearer auth), and the `nexspence` fork
  (proxy by relaying the 302 metadata as 200, strips `X-Xet-Hash`; its HEAD always
  goes upstream, so a cached model cannot be used offline).

## Considered designs

1. **Reuse Raw with path conventions** (`/raw/hf/<org>/<name>/<rev>/<file>`).
   Zero new code for storage, but clients speak the HF API, not Raw. A revision is
   a mutable ref over an immutable commit and Raw maps path to a mutable digest,
   so commit pinning, the tree API and offline HEAD cannot be expressed. Rejected.
2. **Dedicated `huggingface` format module, proxy first, hosted second, no Xet
   (chosen).** New coordinate model (`type/org/name@commit`), commit-keyed cache,
   plain HTTP bytes from the existing digest store. Bounded scope and the whole
   `huggingface_hub` read and write path works with `HF_HUB_DISABLE_XET=1`, or
   automatically when the server never advertises Xet. Cost: a new module and a
   conformance matrix against client versions.
3. **Implement the Xet CAS protocol** (chunking, xorbs, shards, global dedupe).
   Best bandwidth for large fine-tunes, but a quarter-scale project with its own
   storage layout and a moving upstream spec. Deferred, not rejected forever.
4. **Git smart-HTTP plus git-lfs server** (`git clone`/`git push`). Needs a git
   object store, which notarx does not have. Rejected for this ADR.

## Decision

Adopt option 2. A format module `internal/format/huggingface` registers format
`huggingface` and serves the HF Hub protocol under `/hf/<repository>/...`.
`HF_ENDPOINT=https://<host>/hf/<repository>` selects a notarx repository; the HF
repo id (`org/name`) follows in the path exactly as on the Hub.

### Repository types

Hosted, Proxy and Group, like other formats. A repository holds models and
datasets (`repo_type` `model` or `dataset`, selected by the HF path prefix
`/datasets/`). **Spaces are not supported**: `/api/spaces/...` returns 404
`unsupported_repo_type`. Proxy upstream default is `https://huggingface.co`; the
upstream URL is configurable for private mirrors and subject to the existing
proxy egress policy.

### Coordinate and identity

The coordinate is `<repo_type>/<org>/<name>@<commit_sha>` (40 hex chars). A commit
is immutable. Branches, tags and `main` are mutable refs (`format.Tagged`), with
resolution `ref -> commit_sha`. `format.Governance.ValidCoordinate` accepts only
the commit form. File identity is `(commit, path) -> sha256 digest`; bytes live in
the digest-addressed object store, so shared blobs dedupe across commits and repos.

### Data model (numbered migration after the ADR 0015 cut-over)

Ids are UUIDv7 (`internal/ids`, created by ADR 0023 slice 1), no foreign key points into a partitioned table,
and all tables use the ADR 0023 typed-key conventions and are MySQL-compatible
(ADR 0022: no PostgreSQL-only types except JSONB, which maps to JSON).

```
hf_repos(id UUID PK, repository_id UUID, repo_type VARCHAR(16), repo_name VARCHAR(255),
         created_at, UNIQUE(repository_id, repo_type, repo_name))
hf_commits(id UUID PK, hf_repo_id UUID REFERENCES hf_repos ON DELETE CASCADE,
           commit_sha CHAR(40), parent_sha CHAR(40) NULL, message TEXT NULL,
           author TEXT NULL, committed_at TIMESTAMPTZ, source VARCHAR(16), -- upstream|hosted|promoted
           file_count INT, total_bytes BIGINT, last_read_at TIMESTAMPTZ, UNIQUE(hf_repo_id, commit_sha))
hf_files(commit_id UUID REFERENCES hf_commits ON DELETE CASCADE, path VARCHAR(512),
         size BIGINT, sha256 CHAR(64), lfs BOOL, git_blob_id CHAR(40),
         pickle_flag BOOL, cached BOOL, PRIMARY KEY(commit_id, path), INDEX(sha256))
hf_blobs(hf_repo_id UUID, sha256 CHAR(64), size BIGINT, created_at,
         PRIMARY KEY(hf_repo_id, sha256))
hf_refs(hf_repo_id UUID, ref_kind VARCHAR(8), -- branch|tag
        ref_name VARCHAR(255), commit_sha CHAR(40), checked_at, expires_at,
        PRIMARY KEY(hf_repo_id, ref_kind, ref_name))
```

MySQL: `path` is `VARCHAR(512)` (2048 bytes in `utf8mb4_0900_bin`, inside the 3072-byte
index key with the 16-byte id; ADR 0022 forbids `TEXT` keys). Longer paths are
rejected (hosted 400 `path_too_long`; proxy 502 `upstream_path_too_long`). Tree
listings are primary-key range scans, so keyset pagination needs no extra index.
`hf_blobs` records the distinct digests a repository holds; a digest is counted
once per repository in `repository_usage_counters` (`capacity.Reserve`, ADR 0023
section 3) when its `hf_blobs` row is first inserted, and released when blob GC
deletes it.

**Row growth.** `hf_files` has one row per file per commit, so a 100k-file dataset
adds 100k rows per upstream commit. Bounds: `NOTARX_HF_MAX_FILES_PER_COMMIT`
(default 200000; larger trees are refused with 502 `upstream_tree_too_large`);
file rows are created only when a commit's tree is first requested, not on ref
revalidation; commits that no ref points to and that were not read for
`NOTARX_RETENTION_HF_COMMITS` (default 30d, per `last_read_at`) are deleted in
batches by the retention janitor, keeping at least the last 3 commits per ref.

`hf_files` and `hf_commits` are not append-only and are not partitioned (their volume is bounded by the rules above, not by partitioning). A proxy
commit row stores the tree metadata captured from upstream (`tree` and model-info
JSON are rendered from rows, not stored verbatim). `cached=false` rows are
metadata only; bytes are fetched on first GET. Unreferenced commits and orphaned
blobs are collected by the format's `CacheMaintenance` and the retention janitor
(ADR 0023 section 2; blob GC deletes a digest only when no `hf_files` row in the
repository references it); `hf_refs` rows past `expires_at + keep` are deleted in
batches (`NOTARX_RETENTION_HF_REFS`).

### Read protocol (proxy, hosted, group)

Endpoints (all under `/hf/{repository}`, specified in
`api/openapi/protocols/huggingface.yaml`):

| Request | Behaviour |
| --- | --- |
| `GET/HEAD /{id}/resolve/{rev}/{path}` | resolve `rev` to a commit, return `X-Repo-Commit`, `ETag` (`"<sha256>"` for LFS, git blob id otherwise), `X-Linked-Etag`, `X-Linked-Size` for LFS files, `Content-Length`, `Accept-Ranges: bytes`; GET streams bytes with single and multi Range and `If-Range` (reusing `internal/protocol/raw/range.go` logic, extracted to a shared package) |
| `GET /api/{models,datasets}/{id}[/revision/{rev}]` | model info: `id`, `sha`, `siblings[]`, `tags`, `lastModified`, `private`, `gated=false` |
| `GET /api/{type}/{id}/tree/{rev}[/{path}]` | tree entries with `Link: rel="next"` pagination, `recursive`, `expand` honoured |
| `POST /api/{type}/{id}/paths-info/{rev}` | per-path info used by `snapshot_download` |
| `GET /api/whoami-v2` | caller identity for `huggingface_hub` login checks |

No other Hub endpoint is implemented; unknown routes return 404 with a JSON
`{"error": ...}` body (the Hub error shape, with `X-Error-Code`).

**Pinning.** `resolve` always answers with the commit sha. The client's follow-up
GET carries the revision it resolved; the server serves the object for the
commit in `X-Repo-Commit` only (it never re-resolves a mutable ref between HEAD
and GET). Pinning is enforced by the server, so it does not depend on the client passing the sha.

**Commit-keyed cache and ref TTL.** A request by 40-hex commit sha is served from
the cache forever once complete. A branch or tag is revalidated against upstream
when `expires_at` has passed (`NOTARX_HF_REF_TTL`, default `5m`; `main` may be
overridden per repository). If upstream is unreachable and a prior resolution
exists, the stale ref is served (`NOTARX_HF_SERVE_STALE`, default `true`) with an
`X-Notarx-Stale: true` header, and `HEAD`, tree and model-info are answered from
`hf_commits`/`hf_files`, so a model already cached works offline. A file that
was never fetched fails with 504 `upstream_unavailable`; a cache miss never
fabricates metadata.

**Upstream fetch.** The proxy performs `HEAD` upstream with redirects disabled
to capture metadata from the first response, then follows exactly one redirect
for the byte fetch. The hop is permitted only if the `Location` host matches
`NOTARX_HF_CDN_ALLOWLIST` (default `cdn-lfs.huggingface.co`, `cdn-lfs-us-1.hf.co`,
`*.hf.co`, `*.xethub.hf.co`), uses `https`, and passes the existing egress policy
(private/loopback/link-local addresses refused after DNS resolution and again at dial time, so DNS rebinding cannot bypass the check). The
credential (`Authorization`) is never sent to the CDN host. A second redirect is an
error (`upstream_redirect_rejected`). The allowlist default applies only to the default upstream; when the upstream URL is overridden the allowlist is empty (no redirect followed) until the operator sets `NOTARX_HF_CDN_ALLOWLIST` explicitly. This is a new primitive in the egress layer
(`internal/egress`: `FollowOnce(allowlist)`), not a relaxation of Raw.

**No Xet.** The server strips `X-Xet-Hash` and any Xet link header from upstream
responses and never serves `/api/{type}s/{id}/xet-read-token/{rev}` (404). The Hub documents Git LFS as a supported legacy path (Xet docs, "Backwards Compatibility & Legacy"); for files whose resolve response carries Xet headers, the proxy uses the plain `resolve` redirect and ignores the Xet headers. If
upstream stops providing it, those files fail with 502 `upstream_xet_only`; this is
documented. Client guidance in `docs/formats/huggingface.md`: set
`HF_HUB_DISABLE_XET=1` (the standard 0.x and 1.x client falls back to HTTP anyway
when no Xet header is present), and raise `HF_HUB_ETAG_TIMEOUT` and
`HF_HUB_DOWNLOAD_TIMEOUT` for cold multi-GB fetches.

**Large objects.** `NOTARX_HF_MAX_OBJECT_BYTES` (default 200 GiB, hard maximum
1 TiB) replaces the 1 GiB cap for HF only; the Raw default is unchanged. Each
repository may set a lower `max_object_bytes`. Quota follows ADR 0023 section 3: at fetch start the counter is read and a fetch whose `Content-Length` would exceed the remaining quota fails early with the standard quota error; the authoritative `capacity.Reserve` runs in the transaction that inserts `hf_blobs` and sets `cached` (last statement before commit), so a failed fetch holds no reservation. Cold fetches stream through a **tee**: the
object is written to the staging area and to the first client simultaneously, the
digest is verified at end of stream (sha256 equals the LFS `ETag`), and only then
is the object committed and the `cached` flag set. A Range request on a cold object
waits on the same in-flight fetch (single-flight per digest, using the existing
distributed lock in `internal/protocol/raw/cache.go`) and is served from staging
as bytes become available; once cached, ranges are served from the store with
`hf_transfer`-style parallel reads supported. Concurrency is bounded by
`NOTARX_HF_MAX_CONCURRENT_FETCHES` (default 4), following
`defaultMaxConcurrentSpools`. A digest mismatch discards staging and fails the
request; it never reaches the cache.

**Datasets and external locations.** Dataset repositories use the same module with
`repo_type=dataset`. A dataset whose tree references external locations (loading
scripts pulling from arbitrary URLs, `dataset_infos` pointing off-Hub) is not
rewritten: the proxy serves only files that are in the Hub git tree and refuses
requests to external URLs. Loading scripts (`*.py` in a dataset repo) are cached
as ordinary files but flagged, and `trust_remote_code` remains the client's
decision; notarx does not execute them.

### Authentication

`Authorization: Bearer <token>` is accepted; the token is resolved by the
existing authentication path to a principal, exactly as for other formats:
service-account API keys and, once ADR 0019 slice 4 lands, workload JWTs through
the bearer branch (so a GitHub Actions or Kubernetes workload can run with
`HF_TOKEN=$OIDC_JWT`). Basic with the same secret as password is also accepted
for client variants that use it. Repository grants (ADR 0006) decide
read (`resolve`, `api`) versus write (`preupload`, `commit`, LFS). Anonymous read
follows ADR 0029 repository visibility (public repositories only; the format supplies
`PublicPathPrefixes` and owns no access decisions). Auth failures return the Hub
JSON error with 401 and `WWW-Authenticate: Bearer`; a token with only read grant is allowed on read routes and gets 403 on write routes.

### Group repositories

A Group resolves `resolve`, model-info and tree requests by member order: the
first member that has the repo id (and revision) answers, and its commit sha is
returned. Because mixing members mid-snapshot would produce an inconsistent tree,
resolution is committed per `(repo id, commit)` at the first request and cached
for the request flow; subsequent file GETs for that commit go to the same member.
Every file GET on a Group is authorized against the Group and the answering member under the ADR 0029 group invariants (a Group is never more visible than its least visible member, and a private member never answers an anonymous request), so a Group cannot leak a private repo. Group repositories are read-only. Like Artifactory, **only the read flow of
`snapshot_download` and `hf_hub_download` is supported on a Group**; uploads
return 405 with the member to use.

### Hosted repositories

Hosted write support is a second phase over the same schema:

| Request | Behaviour |
| --- | --- |
| `POST /api/{type}s/{id}/preupload/{rev}` | classify each file as `regular` or `lfs` by size (`NOTARX_HF_LFS_THRESHOLD`, default 10 MiB) and `.gitattributes`-style patterns; reports `shouldIgnore` for identical existing digests |
| `POST /{id}.git/info/lfs/objects/batch` | Git LFS batch API, `basic` transfer only; returns `upload` actions with a notarx PUT URL (`/hf/{repository}/lfs/upload/{oid}`, single-use signed token bound to oid, size, repository, principal and a 1 h expiry) and a `verify` action; `download` actions point at `resolve` |
| `PUT /lfs/upload/{oid}` | streams to staging, verifies sha256 and size against oid, commits the blob |
| `POST /api/{type}s/{id}/commit/{rev}` | NDJSON (`header`, `file`, `lfsFile`, `deletedFile`, `deletedFolder`, `copyFile`); validates that every `lfsFile` oid exists and every inline file is within `NOTARX_HF_MAX_INLINE_BYTES` (default 10 MiB); creates a new commit row and moves the branch ref in one transaction |
| `POST /api/repos/create`, `DELETE /api/repos/delete` | create or soft-delete an HF repo id (delete requires grant `delete`) |

**Immutability.** A commit is never edited; a new commit row is created with the
parent sha. `parent_commit` in the header gives optimistic concurrency: a
mismatch returns 409 `ref_moved`. Deleting a file only adds a commit without it.
Force-overwriting a tag, or rewriting history, returns 409 `immutable_ref`
unless the caller has the `overwrite` grant, matching Artifactory's behaviour.
`Xet`-style commits (file by Xet id) are rejected with 400 `xet_not_supported`;
no Xet token endpoint is served, so clients use the LFS path (verified by the slice 7 suite with default client settings). Branch creation and tag creation
(`POST /api/{type}s/{id}/branch/{rev}`, `tag/{rev}`) are supported.

### Promotion and replication (ADR 0021)

The promotable unit is `repo@commit`: `ValidCoordinate` accepts only
`<type>/<org>/<name>@<sha40>`. `format.Tagged` exposes branches and tags so rules
can match `refs: ["main", "v*"]`; evaluation pins `(coordinate, digest)` where
the digest is the sha256 of the canonical commit manifest (sorted
`path|size|sha256` lines). The `Importer` verifies the manifest and every blob
digest from the bytes before creating the target commit, preserving `commit_sha`
in the target (so downstream clients and caches agree on the sha) and recording
`source='promoted'`. Replication to a remote notarx instance uses the ADR 0021 peer
client; checkpoints are per `(hf_repo, commit)`. Blob transfer reuses digest
dedupe so only missing blobs travel.

### Pickle risk and scanning (ADR 0030)

At commit ingest (proxy fetch completion or hosted commit), files with extensions
`.bin`, `.pt`, `.pth`, `.ckpt`, `.pkl`, `.pickle` and `.joblib`, and zip
archives containing `data.pkl`, set `hf_files.pickle_flag`. This is a cheap
header sniff, never an unpickle. The `format.Scanner` capability exposes the flag
and the file list to the ADR 0030 pipeline, which (ADR 0030 slice 11) runs its pure-Go pickle opcode inspector
(no external `picklescan` or `modelscan` process) and writes findings with
`source = 'notarx-model-scan'` into the ML-BOM and the standard quarantine/admission hooks. `safetensors`, GGUF and ONNX are
not flagged. Repository setting `pickle_policy`: `allow` (default), `warn`
(response header `X-Notarx-Pickle: true`, Console badge), `block` (resolve of a
flagged file returns 403 `pickle_blocked`; evaluated at fetch start so no bytes are
stored).

### Console, CLI and observability

Console gets a browse view (repo list, commits, files, flagged files).
`notarxctl hf repo list|show|cache-status` and promotion commands follow
`docs/cli-command-map.md`. Metrics: `notarx_hf_requests_total{op,result}`,
`notarx_hf_upstream_bytes_total`, `notarx_hf_cache_hit_ratio`,
`notarx_hf_inflight_fetches`. Audit: sync for hosted commits and promotion,
async for reads (ADR 0023).

## Security considerations

- **SSRF and redirect abuse.** One hop, https, host allowlist, resolved-IP checks,
  no credentials forwarded; the `Location` is never echoed to clients.
- **Cache poisoning.** Only sha256-verified bytes are committed; ETag from
  upstream is checked against the computed digest for LFS files. Git blob ids for
  non-LFS files are recomputed (`sha1("blob <len>\0"+bytes)`) before commit.
- **Mutable ref confusion.** Clients always get the sha; file GETs are pinned.
- **Pickle execution.** The server never loads model files. Flagging and optional
  scanning only reduce client risk; `trust_remote_code` is out of notarx's control
  and documented.
- **Private repos.** Upstream tokens are repository-scoped secrets sealed through
  `internal/secrets`; never sent to the CDN or to clients.
- **Resource exhaustion.** Quota reservation at fetch start, bounded concurrency,
  per-token rate limits via the existing limiter, tree pagination capped (1000).
- **License and gating.** Gated models are not bypassed: upstream 401/403 are
  relayed; notarx does not store terms acceptance.

## Consequences

- Teams get an offline-capable, auditable mirror and private host for HF models
  and datasets with unmodified clients.
- Storage use grows quickly; per-repo quota and retention are mandatory from the
  first slice, and cache GC must understand unreferenced commits.
- The format needs a client conformance matrix; `huggingface_hub` moves fast and
  Xet may become the only upstream path, which would degrade proxying of new files.
  The risk is mitigated by the Hub's Git LFS bridge and by revisiting option 3.
- The egress layer gains a controlled redirect primitive that other formats
  (npm tarball CDNs) may reuse.
- Spaces and git push/clone are not available.

## Implementation slices

Every slice is one PR and passes `make test`, `make lint` (includes `make architecture-check`) and the coverage floors (`make coverage`); API changes edit OpenAPI first (`make openapi-check`). Slices that touch the schema depend on ADR 0015 slice 4 (cut-over) and use numbered migrations after it. Generic versioned packages are owned by ADR 0028, not by this ADR. Slice numbers 6, 10 and 12 are referenced by ADRs 0029, 0030 and 0021; letters keep them stable.

1. **Egress redirect primitive.** Milestone: Formats.
   Scope: `internal/egress` (`FollowOnce` with allowlist, header stripping, resolved-IP check at resolve and dial); Raw upstream untouched.
   - [ ] Table tests for second hop, http downgrade, allowlist miss, private IP, DNS rebinding at dial, credential stripping (`docs/proxy-egress-design.md` cases); Raw still refuses redirects.
   - [ ] `make test`, `make lint`.
   - Depends on: none.
2a. **Extract shared range and spool code.** Milestone: Formats.
   Scope: move range parsing and the spool and distributed-lock helpers from `internal/protocol/raw/{cache,range}.go` to a shared package; no behaviour change.
   - [ ] Existing Raw tests pass unchanged; Raw 1 GiB default unchanged.
   - [ ] `make test`, `make architecture-check`, `make lint`.
   - Depends on: none.
2b. **Large-object tee and quota.** Milestone: Formats.
   Scope: tee streaming, single-flight per digest, range on an in-flight object, `NOTARX_HF_MAX_OBJECT_BYTES`, `NOTARX_HF_MAX_CONCURRENT_FETCHES`, per-repository limit, early quota check and `capacity.Reserve` at commit.
   - [ ] Tee serves the first client while staging; digest mismatch discards; range on an in-flight object is served; overflow at the quota edge rejected.
   - [ ] `make test`, `make race`, `make lint`.
   - Depends on: slice 2a; ADR 0023 slice 8 (`capacity.Reserve`).
3. **Module skeleton and schema.** Milestone: Formats.
   Scope: `internal/format/huggingface` (`Profile`, `Governance`, `Browse`, `Usage`), numbered migration with the five tables, store with MySQL-portable SQL, registry entry, repository create and profile in the management API.
   - [ ] Migration applies on PostgreSQL 16 and the MySQL harness; UUIDv7 ids; no `TEXT` keys; no foreign keys into partitioned tables.
   - [ ] `make migrate`, `make openapi-check`, `make architecture-check`, `make lint`.
   - Depends on: ADR 0015 slice 4; ADR 0011 (format modules contract); ADR 0023 slice 1.
4a. **Upstream client and metadata ingest.** Milestone: Formats.
   Scope: upstream HEAD and tree fetch with `FollowOnce`, commit and file row ingest, `hf_refs` TTL, `NOTARX_HF_REF_TTL`, `NOTARX_HF_MAX_FILES_PER_COMMIT`, retention registry entries (`NOTARX_RETENTION_HF_REFS`, `NOTARX_RETENTION_HF_COMMITS`) and blob GC.
   - [ ] Fake-Hub tests: ref moves upstream; oversized tree refused; janitor deletes only unreferenced, unread commits and keeps the last 3 per ref.
   - [ ] `make test`, `make lint`.
   - Depends on: slices 1, 3; ADR 0023 slice 7 (`RetentionJanitor`).
4b. **Proxy resolve and HEAD.** Milestone: Formats.
   Scope: `api/openapi/protocols/huggingface.yaml`, resolve and HEAD handlers, byte fetch through the tee, commit-keyed cache, stale serving, X-Xet stripping.
   - [ ] Cold, warm and offline HEAD tests; pinned GETs stay pinned when the ref moves; 5 GiB synthetic object streams.
   - [ ] Contract tests with a fake Hub.
   - [ ] `make test`, `make openapi-check`, `make lint`.
   - Depends on: slices 2b, 4a.
5. **Proxy API: model info, revision, tree, paths-info, whoami.** Milestone: Formats.
   Scope: Hub-shaped API handlers rendered from stored rows.
   - [ ] Pagination via `Link`; responses match recorded Hub fixtures; unknown routes return a Hub-shaped 404.
   - [ ] `make test`, `make openapi-check`, `make lint`.
   - Depends on: slice 4b.
6. **Bearer auth and anonymous reads.** Milestone: Formats.
   Scope: Bearer and Basic acceptance on `/hf`, grant mapping, Hub-shaped 401/403; anonymous reads through `internal/access`.
   - [ ] Service-account key works; workload JWT test is gated until ADR 0019 slice 4; no credential reaches the upstream CDN.
   - [ ] Anonymous read honours repository visibility (public reads allowed, private denied, including model-info `private` and tree routes); the format supplies only `PublicPathPrefixes`.
   - [ ] `make test`, `make integration-test`, `make lint`.
   - Depends on: slice 4b; ADR 0019 slice 4 (JWT case); ADR 0029 slice 2 (`internal/access`).
7. **Client conformance suite.** Milestone: Formats.
   Scope: `make native-hf-e2e` (modelled on `native-raw-e2e`, `Makefile:292`) with `huggingface_hub` versions pinned in `tests/e2e/huggingface/versions.txt` (oldest supported, current 0.x, current 1.x) in a container against a fake Hub; nightly job against the real Hub with a tiny public model.
   - [ ] `hf_hub_download`, `snapshot_download`, `from_pretrained` pass with `HF_HUB_DISABLE_XET=1` and with default client settings; offline mode runs from cache; supported matrix documented.
   - [ ] `make native-hf-e2e`, `make lint`.
   - Depends on: slices 5, 6.
8. **Datasets and external-location refusal.** Milestone: Formats.
   Scope: `/datasets/` routes, off-Hub URL refusal, script-file flagging.
   - [ ] Dataset routes pass the slice 7 suite; off-Hub URL requests are refused; script files flagged.
   - [ ] `make native-hf-e2e`, `make test`, `make lint`.
   - Depends on: slice 7.
9. **Group resolution.** Milestone: Formats.
   Scope: ordered Group resolution with a sticky member per commit; write rejection with 405; ADR 0029 group invariants.
   - [ ] Member order, sticky member and 405 covered; a Group with a private member never serves it anonymously; `snapshot_download` e2e on a Group.
   - [ ] `make native-hf-e2e`, `make test`, `make lint`.
   - Depends on: slice 7; ADR 0029 slice 4 (group invariants).
10. **Pickle flagging and policy.** Milestone: Formats.
    Scope: header sniffing, `hf_files.pickle_flag`, `pickle_policy`, `format.Scanner` data (flag plus file list). ADR 0030 slice 11 consumes this output; this slice does not depend on it.
    - [ ] Fixtures for each extension; `block` stores no bytes; no unpickling anywhere (test greps a deny-list).
    - [ ] `make test`, `make lint`.
    - Depends on: slice 4b.
11a. **Hosted repositories, preupload and LFS upload.** Milestone: Formats.
    Scope: `repos/create` and `delete`, preupload, LFS batch (`basic`), signed `PUT /lfs/upload/{oid}` with oid and size verification, `hf_blobs`, quota.
    - [ ] Signed URL rejected when reused, expired, or presented for another oid, repository or principal; digest mismatch stores nothing.
    - [ ] `make native-hf-e2e`, `make integration-test`, `make lint`.
    - Depends on: slices 2b, 5, 6.
11b. **Hosted commit, branches and tags.** Milestone: Formats.
    Scope: NDJSON commit endpoint, `parent_commit` concurrency, overwrite grant, branch and tag creation, pickle flagging on ingest.
    - [ ] `upload_file`, `upload_folder`, `create_commit` pass in the slice 7 suite; parent-commit conflict returns 409; `xet` commits rejected; immutable tag overwrite returns 409.
    - [ ] `make native-hf-e2e`, `make integration-test`, `make lint`.
    - Depends on: slices 10, 11a.
12. **Promotion and replication of `repo@commit`.** Milestone: Formats.
    Scope: `Tagged`, `Governance`, `Importer` and `Exporter` in the ADR 0021 registry; peer replication of missing blobs.
    - [ ] Commit sha preserved across promotion; peer replication transfers only missing blobs.
    - [ ] `make test`, `make integration-test`, `make lint`.
    - Depends on: slice 11b; ADR 0021 slices 1a, 2b, 7b and 8.
13a. **CLI and docs.** Milestone: Formats.
    Scope: `notarxctl hf`, `docs/formats/huggingface.md` (HF_ENDPOINT, HF_TOKEN, Xet, timeouts), `docs/cli-command-map.md`, metrics.
    - [ ] `make cli-contract-check`, `make docs-check`, `make lint`.
    - Depends on: slices 8, 9, 11b.
13b. **Console browse view.** Milestone: Formats.
    Scope: repo list, commits, files, flagged files.
    - [ ] `make console-test`, `make console-api-check`, `make lint`.
    - Depends on: slices 8, 10.

## Open questions

1. Will upstream keep serving the Git LFS path for Xet-backed files? If not,
   slice 4b needs either a minimal Xet reconstruction client (read-only) or an
   explicit unsupported list.
2. Non-HF model artifacts that need create-only per-version publication belong to ADR 0028 (generic versioned packages), not to this format.
