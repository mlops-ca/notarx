# Repository Visibility: Public and Private Feeds

[Documentation index](../README.md)

Status: accepted (2026-11-17)

## Context

notarx is meant to host both private feeds (the default) and public feeds that
anyone can pull from, including a small public instance on the OCI Always Free
reference deployment (ADR 0017). Today the model cannot do that safely.

**Policy model.** Anonymous access has three gates, documented in
`docs/anonymous-access-operations.md`: the singleton `anonymous_access_policy`
row (migration `000055`), a boolean `anonymous_read` on `hosted_repositories` and
`hosted_groups` (migration `000054`; `internal/repository/model.go`,
`postgres_hosted.go`), and, for Groups, a stored per-member copy
`Member.Anonymous` set from the repository flag in
`internal/app/hosted_group_resolver.go`. There is no "any logged-in user" tier:
a user without a grant is denied on a repository meant for the whole
organisation (ADR 0006 grants are a separate axis).

**No choke point.** The decision is re-implemented per handler
(`anonymousHostedRepositoryReadAllowed`, `anonymousHostedGroupReadAllowed`,
`anonymousHostedGroupMembers`, `anonymousOCIAllowed`, `anonymousMavenAllowed`,
`anonymousConanAllowed` in `internal/app/anonymous_policy.go` and per-format
`hosted_group_*.go`). `authorizeMembers` returns members unfiltered for an anonymous
principal, so safety depends on an earlier layer having filtered; nothing enforces that
every format behaves the same.

**Cost and abuse surface.**
- `GET /api/v2/public/repositories` (`internal/app/public_catalog.go`) scans the catalog
  (200 per page) on every unauthenticated request, with no cache and no `Cache-Control`.
- No per-IP, per-token or per-repository rate limit exists on the data path.
- Every anonymous download runs policy and member lookups and a transactional audit and
  usage write; ADR 0023 warns that anonymous volume is attacker-controlled.
- Bytes always proxy through the server (no presigned URLs); blob responses carry a strong
  `ETag` and `Range` support but no `Cache-Control` and no `Vary`.

**Related decisions.** ADR 0021 provides promotion, approvals and `domain_events`
(transactional outbox); ADR 0023 fixes key types and retention rules; ADR 0022 requires
MySQL-neutral DDL. ADR 0015 slice 7 re-squashes the baseline before `v0.1.0`, so shape
changes landing before it (slices 1 to 4 here, Milestone Foundation) cost no upgrade path.
Consumers of public visibility: ADR 0026 Part D (`feeds.mlops.ca`, the project's own
public deb and rpm feeds) and the anonymous legs of the formats in "Formats added after
Foundation" below.

## Considered designs

1. **Keep the boolean, add a second flag `authenticated_read`.** Smallest diff.
   Two independent booleans allow the nonsensical state "anonymous yes,
   authenticated no", every handler keeps its own check, and the group rules
   stay implicit. Rejected.
2. **Visibility enum plus one enforcement function (chosen).** `visibility` is
   an ordered enum (`private` < `authenticated` < `public`) on repositories and
   groups; one package decides read access for every format and also derives
   response caching class, rate-limit class and audit class from the same
   decision. Cost: a cross-cutting refactor of about ten handlers, mitigated by
   a table-driven conformance test.
3. **Public feeds as a separate read-only deployment.** Strong isolation, but it
   needs replication tooling per feed; ADR 0021 promotion to a dedicated public
   instance remains an operating pattern (ADR 0026 Part D uses one), not the only way.
4. **External CDN or reverse proxy for access control.** Leaks policy out of the data
   model, untestable in CI, fails open. Rejected; the CDN is only a cache.

## Decision

Option 2, with the rules below. The global gate stays as an instance kill
switch.

### Data model

```
hosted_repositories.visibility  VARCHAR(16) NOT NULL DEFAULT 'private'
  CHECK (visibility IN ('private','authenticated','public'))   -- short text, indexable on MySQL
hosted_groups.visibility        (same)
-- anonymous_read columns are dropped; no alias in API, store or CLI
-- index: (visibility, name) on both tables (plain composite, no partial index:
--        MySQL has none, ADR 0022)
-- anonymous_access_policy renamed public_access_policy (singleton, enabled=false)
-- cdn_purge_requests(id UUID (UUIDv7), repository_id UUID, scope VARCHAR(16),
--   prefixes JSON (jsonb on PostgreSQL), state VARCHAR(16), attempt INT,
--   last_error TEXT, created_at, updated_at)  -- no FK; index (state, created_at);
--   claimed with FOR UPDATE SKIP LOCKED (valid on MySQL 8.0, ADR 0022);
--   retention 7d via the RetentionJanitor (ADR 0023)
```

Semantics, evaluated per request for a principal class
(`anonymous`, `authenticated`, `granted`):

- `private`: only principals with a grant (ADR 0006) or admin authority.
- `authenticated`: any valid non-anonymous identity (user, service account,
  workload-identity token from ADR 0019) may read. Writes always require a grant.
- `public`: anyone may read, subject to the global gate. When the gate
  `public_access_policy.enabled` is false, `public` behaves as `authenticated`
  (effective visibility is capped; the gate never widens access).
- Only GET and HEAD are ever anonymous; publication, deletion, promotion, cache
  mutation and management writes always require authentication (unchanged).
- Data migration of existing rows: `anonymous_read=true` becomes `public`,
  `false` becomes `private`. There are no upstream installations to carry, so
  this is for development databases only.

Management API (relative to `/api/v2`, OpenAPI first in
`api/openapi/management.yaml`): repository and group create/update/read bodies
carry `visibility`; `GET|PUT /public-access-policy` replaces
`/anonymous-access-policy` (same `If-Match` semantics); `GET
/public/repositories` takes no auth and lists only `public` repositories (empty when the gate is off);
`GET /repositories` gains a `visibility` filter. Protocol specs in
`api/openapi/protocols/` gain `401`/`404`/`429` documented responses and the
cache headers below.

Changing visibility is a security-relevant state change: audited in the caller's
transaction (`sync` class, ADR 0023). Raising to `public` requires the
`promote:public` capability on the repository (defined below), raising to
`authenticated` requires repository admin, lowering requires repository admin and
enqueues a CDN purge (below).

### Single choke point: `internal/access`

```go
type Class int // Anonymous, Authenticated, Granted
type Decision struct {
    Allow       bool
    Deny        DenyKind // Challenge401, NotFound404, Forbidden403
    Visibility  Visibility // effective, after gate cap
    Cacheable   CacheClass // Immutable, Metadata, Dynamic, NoStore
    RateClass   string     // "anon", "token", "none"
    AuditClass  string     // ADR 0023 durability class
}
func Decide(ctx, req ReadRequest) (Decision, ResolvedMembers, error)
```

- All repository and group read paths (protocol handlers, browse, search,
  download, effective-access) call `Decide`; format modules (ADR 0011) supply the
  path-to-repository mapping and `CacheClass(request)` only. The per-handler
  copies listed in Context are deleted.
- `architecture-check` gains a rule: outside `internal/access` and
  `internal/repository`, no code may read `Visibility` fields or the
  public-access policy.
- Decisions for anonymous requests are computed from one repository/group row
  read; ADR 0023 slice 12 later puts both behind the read cache with
  invalidation. Until then the cost is one indexed read per request, replacing
  the current policy-plus-per-member reads.
- `GET /repositories/{id}/effective-access` is reimplemented on `Decide`, so the
  simulator cannot drift from enforcement.

**Deny shaping (no existence leaks).** For an anonymous caller, a non-public
repository or group and a name that does not exist produce the identical
response: `401` with the per-format `WWW-Authenticate` challenge (so
credential-capable clients prompt), same body, same headers, same code path
length (members are filtered before any lookup, never after). For an
authenticated caller without read, the response is `404`, identical to a missing
resource. Within a visible group, an anonymous request for a package that exists
only in a non-visible member is the ordinary `404` of that format.

### Group invariants

- The group's `visibility` is a ceiling. For a caller class, the resolved member
  list is computed at request time as the members whose own effective
  visibility admits that class. `Member.Anonymous` is removed (it was a stored
  copy that could drift); nothing derived is persisted.
- **Strict mode** (default, `NOTARX_GROUP_VISIBILITY_MODE=strict`): a group may
  not be more visible than its least visible member. Writes that would violate
  it fail with `409 visibility_conflict` (body lists the offending members, only
  those the caller may administer). Lowering a repository below a group that
  contains it fails the same way. `lenient` mode keeps the computed filtering
  (members above the caller class are silently invisible) and shows a console
  warning; it exists for migration of existing layouts, not as a recommended
  state.
- Proxy members: a proxy repository's visibility is its own; a public group with
  a proxy member and a private hosted member can neither be created in strict
  mode nor probed in lenient mode (private-name existence is never observable,
  per deny shaping). Dependency-confusion exposure is therefore limited to what
  group ordering already controls (ADR 0003/0011), not widened by visibility.
- Group `visibility` is independent of repository `visibility` for direct
  access to a member repository by name: reading a repository directly uses the
  repository's own visibility only.

### Public catalog and search

- `GET /public/repositories` is served from `WHERE visibility='public'` (index
  above) behind a per-node TTL cache (`NOTARX_PUBLIC_CATALOG_TTL`, default 30s),
  with `Cache-Control: public, max-age=60` and `ETag`. Page size capped;
  keyset pagination (ADR 0023 section 8).
- Search and listing show: anonymous, `public` only; authenticated, `public` plus
  `authenticated` plus granted `private`. Responses to authenticated callers are
  `private, no-store` with `Vary: Authorization, Cookie`, so a shared cache can
  never store them.

### Anonymous rate limiting: `internal/ratelimit`

Middleware that runs before authentication lookups and again after `Decide` (to
know the class). Per node, in memory, bounded:

| Key | Mechanism | Defaults |
| --- | --- | --- |
| anonymous, client IP (IPv6 by /64) | token bucket requests | `NOTARX_ANON_RATE_RPS=20`, `NOTARX_ANON_RATE_BURST=40` |
| anonymous, client IP | bytes/s limiter wrapping the response writer | `NOTARX_ANON_RATE_BYTES_PER_SEC=33554432` |
| anonymous, client IP | concurrent downloads | `NOTARX_ANON_CONCURRENCY_PER_IP=8` |
| anonymous, node | concurrent downloads semaphore | `NOTARX_ANON_CONCURRENCY_NODE=64` |
| anonymous, node | monthly egress budget, hard stop | `NOTARX_PUBLIC_EGRESS_MONTHLY_BYTES=0` (off); soft alert at 80%; seeded at start-up from the current month's usage rollups (ADR 0023 slice 6) so a restart does not reset it |
| authenticated, token id | same bucket types, higher limits | `NOTARX_TOKEN_RATE_*` (off by default) |

- Over limit: `429` with `Retry-After`, `Cache-Control: no-store`, no body detail.
- Client IP: `NOTARX_TRUSTED_PROXIES` (CIDR list, default empty) and
  `NOTARX_CLIENT_IP_HEADER` (`X-Forwarded-For` taking the right-most untrusted
  hop, or `CF-Connecting-IP`). With an empty list only `RemoteAddr` is used and
  forwarded headers are ignored, so a spoofed header cannot choose another
  client's bucket. Behind a CDN the operator must set both, or every client
  shares one bucket.
- The bucket table is a bounded LRU (`NOTARX_ANON_RATE_MAX_KEYS`, default
  100000) with idle eviction, so an attacker cannot grow memory by rotating
  sources.
- Metrics: `notarx_ratelimited_total{class,limit}`,
  `notarx_anonymous_egress_bytes_total`, `notarx_anonymous_inflight`. Per-node
  limits are the v1 contract; a shared limiter in PostgreSQL is out of scope.

### Cache headers by visibility

Set only by `internal/access` from the decision, never by handlers:

| Response | Header |
| --- | --- |
| Anonymous request, effective `public`, digest-addressed or version-immutable (OCI blob and manifest by digest, npm tarball, PyPI file, crate, Maven release file with overwrite disabled, Conan revision files) | `Cache-Control: public, max-age=31536000, immutable` when `NOTARX_CDN_PURGE_PROVIDER` is configured, otherwise `max-age=86400` |
| Anonymous request, `public`, mutable metadata (OCI tags, npm packument, PyPI simple index, `maven-metadata.xml`, Raw, repository catalog) | `Cache-Control: public, max-age=60, stale-while-revalidate=30, stale-if-error=300` (`NOTARX_PUBLIC_METADATA_MAX_AGE`) plus the existing `ETag` |
| Any request carrying `Authorization` or a session cookie, or effective visibility below `public` | `Cache-Control: private, no-store` |
| 401, 403, 404, 429, 5xx | `no-store` |
| All repository data routes | `Vary: Authorization, Cookie` |

A shared cache therefore never holds anything produced for an authenticated
request or a non-public repository. A format whose repository allows overwrite
(Maven redeploy, Raw) is demoted from `Immutable` to `Metadata` by its
`CacheClass`.

### CDN guidance and purge

A new `docs/public-feeds-cdn.md` documents Cloudflare (and the same rules for
other CDNs): a proxied read-only hostname for pulls (cache GET/HEAD, bypass on
`Authorization` header and session cookie, bypass `/api/` and `/v2/token`, block
non-GET/HEAD) and an unproxied hostname for publication and management. The
cache key is path plus `Accept` for OCI. Origin headers above are authoritative;
the docs do not require operators to override TTLs.

On visibility lowering, repository or group deletion, and quarantine of an
artifact in a `public` repository, the same transaction inserts a
`cdn_purge_requests` row and emits `repository.visibility_changed` (new
`domain_events` type, payload `{repositoryId, from, to, groups}`; ADR 0021).
A `cdnpurge` worker (`NOTARX_CDN_PURGE_PROVIDER=none|cloudflare`,
`NOTARX_CDN_PURGE_ZONE`, token via `internal/secrets`) drains the table with
backoff, purging by per-repository path prefix computed by format modules
(`PublicPathPrefixes(repo)`), falling back to a URL list or purge-everything
when prefix purge fails. Cloudflare has offered purge by prefix, tag and hostname on all plans since April 2025 (changelog 2025-04-01); the Free plan allows 5 purge requests per minute (bucket 25), so the worker batches rows per repository and respects `Retry-After`. Failure is visible: `notarx_cdn_purge_pending`,
`notarx_cdn_purge_failures_total`, and console banner on the repository. Until a
purge succeeds the exposure is bounded by `max-age` (86400 s for immutable class
without a configured provider, 60 s for metadata); this bound is stated in the
console warning shown when lowering visibility.

### Optional presigned redirect for large blobs

`NOTARX_BLOB_REDIRECT_MODE=off|oci|all` (default `off`: the object-store endpoint is often internal; enabling requires `NOTARX_BLOB_REDIRECT_PUBLIC_ENDPOINT`, a client-reachable endpoint that is operator-configured, never derived from a request),
`NOTARX_BLOB_REDIRECT_MIN_BYTES=8388608`, `NOTARX_BLOB_REDIRECT_TTL=120s`.
The optional `Presigner` capability of `internal/objectstore` is defined by ADR 0017
(S3-compatible drivers); the filesystem backend lacks it and the feature is silently off there.

- Only after `Decide` allows, after lifecycle checks (quarantine, deletion), and
  after rate-limit accounting.
- Response `307` with `Cache-Control: no-store`; the presigned URL is single
  purpose (GET of one content-addressed key with a pinned
  `response-content-type`), 60 to 300 s.
- Audit and usage count at redirect time with `bytes_mode="redirected"`; byte
  counts are therefore not exact for redirected blobs, and the egress budget
  counts the object size at redirect.
- `all` is unsafe for clients that forward `Authorization` across origins; the OCI mode is
  the recommended one (Docker and ORAS follow redirects without forwarding credentials). Private
  repositories may redirect too; the URL is the credential, never logged.

### Anonymous audit and statistics

Anonymous reads use ADR 0023's async writer and aggregation: usage counters are
batched in memory (exact up to loss on crash), raw audit rows are sampled
(`NOTARX_AUDIT_ANON_ALLOW_SAMPLE`, default `0.01`; denials and `429`s are kept
as the first `NOTARX_AUDIT_ANON_DENY_BURST` (default 20) per IP per minute and
then sampled at the same rate, with exact counters). Audit rows gain
`visibility`, `cache_disposition` (`origin`, `revalidated`, `redirected`) and
`rate_limited`; the ADR 0023 audit schema change lands with ADR 0023 slice 6.
`NOTARX_AUDIT_READ_MODE=sync` still forces complete read audit. A CDN hides cache
hits from the origin, so download counts for public feeds are lower bounds; the
CDN docs point to CDN analytics for totals.

### Promotion to public

Amends ADR 0021 (listed in the amendments for the integrator):
- New capability `promote:public`, held per repository on a grant or by global
  admin, required to set a repository `public`, to create or edit a rule whose
  target is public, and by the rule's run-as service account at run time.
- A rule or manual promotion whose target is `public` at evaluation time always
  goes through approval (`approval.required >= 1`) unless the rule carries
  `publishing: true` set by a principal holding `promote:public` and
  `NOTARX_PROMOTE_PUBLIC_REQUIRE_APPROVAL=false`. Visibility is checked at run
  time, not only at save time: a target raised to `public` after the rule was
  saved pauses unflagged rules (`paused: target_became_public`).
- A promotion that completes into a public target records the target
  visibility in `artifact_promotions` and the audit entry. Remote push to another
  notarx instance never sets visibility; the receiver's own rules apply.

### Formats added after Foundation

`internal/access` is the only place a format learns whether an anonymous read is allowed;
a format module supplies `CacheClass(request)` and `PublicPathPrefixes(repo)` and nothing
else. Anonymous reads of the later formats are delivered by their own ADRs against
slices 2 and 3 of this ADR and the cache headers of slice 7:
- APT and RPM Hosted: ADR 0026 slices 17 (APT anonymous conformance), 6 (capability flip) and 9 (RPM reads);
  `repodata/*`, `Release`, `InRelease` and by-hash files are metadata or immutable
  classes by name, pool files immutable.
- Hugging Face: ADR 0027 slice 6; commit-addressed files immutable, refs and tree listings
  metadata.
- Generic versioned packages: ADR 0028 slice 11; sealed version files immutable.
Each registers in the conformance harness before its capability is advertised.

### Optional feed: Trivy database mirror

ADR 0030 mirrors the Trivy databases through an OCI proxy repository (no new format).
An operator may mark that repository `public` so unauthenticated Trivy clients can use it.
It is **off by default**, read-only, subject to the anonymous rate limits and egress
budget above, and publishes nothing beyond the mirrored OCI artifacts. Whether upstream
terms permit redistribution is an open question (below) and gates enabling it on any
instance the project operates (slice 13).

### Public instance operations

`docs/public-feeds-operations.md` (extends `docs/anonymous-access-operations.md`):
- Abuse: enable the rate limits above before opening the gate; keep the gate as
  the incident kill switch; block by IP at the CDN; publish an abuse contact and
  takedown procedure (quarantine plus purge).
- Dogfooding: `feeds.mlops.ca` (ADR 0026 Part D) is a dedicated public instance that
  depends on slices 1 to 7 here (and 14); `packages.mlops.ca` on R2 (ADR 0025) stays the
  bootstrap and fallback.
- Egress on the OCI Always Free reference deployment (ADR 0017): Oracle documents a 10 TB
  per month outbound allowance (ADR 0017 requires the docs to restate and date it; not
  re-verified here). A saturated 1 Gbit/s link exhausts it in about 22 hours, hence
  `NOTARX_PUBLIC_EGRESS_MONTHLY_BYTES`, set below the allowance in reference values. The
  free tier cannot hold large public blobs in Object Storage, so a public instance there
  serves from the local filesystem behind a CDN.

## Security considerations

- Fail closed: unknown visibility value, missing row or gate read error yields
  deny. The conformance suite asserts it.
- Existence oracles: deny shaping above, plus members filtered before lookup so
  timing does not vary with private-member content; tested with identical
  response comparison (status, headers, body) between "private" and "absent".
- Cache poisoning and cross-user leakage: `public` cache class only for
  anonymous requests; `Vary` and `no-store` rules; CDN cache key ignores
  unrelated query strings, bypasses on credentials.
- Spoofed client IP: forwarded headers honoured only from `NOTARX_TRUSTED_PROXIES`.
- Visibility changes and public promotion are audited `sync`, require distinct
  capability, and are rejected on `Idempotency` replays that would widen access
  without the capability.
- Presigned URLs: short TTL, post-authorization, never logged, `no-store`.
- Public content is untrusted-by-origin: quarantine and admission policy (ADR
  0021 security) apply before visibility, never after.

## Consequences

- One enforcement path; adding a format means implementing its path mapping and
  `CacheClass`, and automatically inherits the conformance suite.
- Breaking API and CLI change: `anonymousRead` is removed with no alias (pre-`v0.1.0`).
- Strict group mode may reject layouts upstream allowed; `lenient` exists.
- CDN-served traffic is under-counted at the origin; presigned redirects trade audit
  exactness for egress offload; per-node rate limits multiply on multi-node deployments.

## Implementation slices

Slices 1 to 4 and 14 are shape or enforcement changes (Milestone Foundation) that must merge before ADR 0015
slice 7 (re-squash). Slice 14 completes "wave 2" of the choke point; references elsewhere to "slices 2 and 3" mean the choke point complete through slice 14. Every slice follows OpenAPI-first (`api/openapi`,
regenerated server and client), coverage floors and `architecture-check`.

1. **Visibility enum schema, API and store.** Milestone: Foundation.
   - Scope: migration adding `visibility` and indexes, dropping `anonymous_read`,
     renaming the policy table; `internal/repository` (model, postgres, memory
     stores, `format_profiles.go`), `api/openapi/management.yaml` and regenerated
     code, `internal/notarxctl` flags, minimal console rename in
     `console/src/features` (selector with three options).
   - [ ] `anonymousRead` absent from OpenAPI, Go, console and CLI (grep gate in CI)
   - [ ] backfill test: true to `public`, false to `private`
   - [ ] `make test`, `make lint`, `make openapi-check`, `make console-test`
   - Depends on: ADR 0015 slice 4; typed-key conventions of ADR 0023 slice 1.
2. **`internal/access` and conformance harness, wave 1.** Milestone: Foundation.
   - Scope: new package, table-driven conformance test iterating
     `{format} x {visibility} x {gate on/off} x {anonymous, authenticated, granted}
     x {GET, HEAD, write}`; migrate OCI, Maven, Conan and
     `hosted_group_resolver.go`.
   - [ ] deny shaping asserted byte-identical for private versus absent
   - [ ] deleted: `anonymousOCIAllowed`, `anonymousMavenAllowed`, `anonymousConanAllowed`
   - [ ] `make test`, `make lint`, `make architecture-check`
   - Depends on: slice 1.
3. **Choke point wave 2: npm, PyPI, Go, Cargo, APT.** Milestone: Foundation.
   - Scope: migrate those handlers and their group resolvers to `Decide`; harness rows for them.
   - [ ] per-handler anonymous helpers deleted for these formats; deny shaping identical
   - [ ] `make test`, `make lint`, `make architecture-check`
   - Depends on: slice 2.
4. **Group invariants.** Milestone: Foundation.
   - Scope: computed member visibility (remove `Member.Anonymous`), strict mode
     write validation and `409 visibility_conflict`, `NOTARX_GROUP_VISIBILITY_MODE`
     in `internal/config`, console conflict message.
   - [ ] strict and lenient tests including proxy members and lowering a member
   - [ ] no stored derived visibility (schema test)
   - [ ] `make test`, `make lint`, `make openapi-check`, `make console-test`
   - Depends on: slice 14.
5. **Public catalog and search tiers.** Milestone: Public feeds.
   - Scope: `internal/app/public_catalog.go`, keyset query, TTL cache, headers,
     `visibility` filter, authenticated search tiers.
   - [ ] `EXPLAIN` shows index use; cache and `no-store` header tests
   - [ ] `make test`, `make openapi-check`
   - Depends on: slices 4 and 14; ADR 0023 slice 14 for keyset helpers.
6. **Anonymous rate limiter.** Milestone: Public feeds.
   - Scope: `internal/ratelimit`, trusted-proxy parser, writer wrapper,
     config keys, metrics, `docs/operational-signals.md`.
   - [ ] spoofed `X-Forwarded-For` from untrusted peer ignored
   - [ ] bounded key table under rotating-source test; `429` with `Retry-After`
   - [ ] `make test`, `make lint`
   - Depends on: slice 14.
7. **Cache headers by visibility.** Milestone: Public feeds.
   - Scope: `CacheClass` per format module, header emission in `internal/access`,
     `internal/artifacthttp/object.go`, protocol OpenAPI response headers.
   - [ ] header table test over all formats and classes; `Vary` always present
   - [ ] `make test`, `make openapi-check`
   - Depends on: slice 14.
8. **CDN purge outbox and CDN docs.** Milestone: Public feeds.
   - Scope: `cdn_purge_requests` migration, `repository.visibility_changed`
     event, `internal/cdnpurge` worker with Cloudflare provider, secrets,
     `PublicPathPrefixes`, `docs/public-feeds-cdn.md`.
   - [ ] purge enqueued in the same transaction (rollback test), retry/backoff
   - [ ] provider tested against an HTTP fake; unconfigured provider caps TTL
   - [ ] `make test`, `make lint`
   - Depends on: slice 7; ADR 0021 slice 1a (events).
9. **Anonymous audit and usage aggregation.** Milestone: Public feeds.
   - Scope: audit fields, sampling and burst logic over `internal/auditwriter`.
   - [ ] sampling and drop-metric tests; counters exact under sampling
   - [ ] `make test`
   - Depends on: ADR 0023 slice 6; slice 6.
10. **Presigned redirect (OCI).** Milestone: Public feeds.
    - Scope: use of the ADR 0017 `Presigner` capability, OCI blob
      handler, config, audit `redirected` mode.
    - [ ] redirect only after allow; never for quarantined; Docker/ORAS-style test
    - [ ] filesystem backend falls back to proxying
    - [ ] `make test`, `make lint`
    - Depends on: slice 7; ADR 0017 slices 1 and 2 (S3 driver, object store contract suite).
11. **Promotion to public.** Milestone: Public feeds.
    - Scope: `promote:public` capability, rule `publishing` flag, run-time
      visibility check and `paused`, approval forcing, audit.
    - [ ] approval required for public target; pause on visibility raise
    - [ ] `make test`, `make openapi-check`
    - Depends on: slice 4; ADR 0021 slices 3a and 5 (rules, approvals).
   - Note: ADR 0021 text (section on public targets) already matches this contract.
12. **Console, CLI and operations docs.** Milestone: Public feeds.
    - Scope: console visibility UX (lowering warning with TTL bound, purge status),
      `notarxctl` commands, `docs/public-feeds-operations.md`.
    - [ ] `make console-test`, `make test`
    - Depends on: slices 8, 10, 11.
13. **Optional Trivy database public feed.** Milestone: Supply-chain security.
   - Scope: documented opt-in to mark the ADR 0030 mirror repository `public`, default-off configuration, rate-limit and egress class for the feed, `docs/public-feeds-operations.md` section with the redistribution-terms finding.
   - [ ] Default-off proven: a fresh install exposes no Trivy mirror anonymously; enabling serves `trivy-db` pulls anonymously with the limits applied.
   - [ ] Unauthenticated Trivy client run against the public mirror succeeds in the ADR 0030 e2e.
   - [ ] `make test`, `make native-oci-e2e`, `make docs-check`, `make lint`.
   - Depends on: slices 5, 6, 7; ADR 0030 slice 4a (database mirror).
14. **Choke point wave 3, browse/search and enforcement lint.** Milestone: Foundation.
   - Scope: Raw, Helm, NuGet, RubyGems handlers, browse and search paths; `architecture-check` rule forbidding visibility reads outside `internal/access`; harness covers all formats.
   - [ ] every format in `docs/protocol-compatibility.md` appears in the harness
   - [ ] fail-closed rows (unknown visibility, policy read error) pass
   - [ ] `make test`, `make lint`, `make architecture-check`
   - Depends on: slice 3.

## Open questions

1. Should anonymous OCI pulls of public repositories skip the token round trip
   (serve `/v2/` without a challenge), or keep the standard anonymous-token flow
   so clients behave uniformly? Slice 2 chooses the token flow unless a client
   incompatibility is found.
2. Does the Cloudflare free plan's terms on bulk non-HTML content allow caching OCI layers? Not verified; legal review precedes recommending it in the CDN docs. (Prefix purge on all plans: verified, see CDN guidance.)
3. Is a single `authenticated` tier enough, or do operators need "authenticated
   via IdP X"? Today that is `private` with a group grant (ADR 0018).
4. May the project publish a public Trivy database mirror (slice 13) given the upstream database licence and ghcr.io terms? Resolved before any project-operated instance enables it.
5. Is a shared (PostgreSQL) rate limiter needed for multi-node public
   instances, or is per-node plus CDN limiting sufficient?
