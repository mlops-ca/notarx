# Rule-Based Promotion and Repository Chains

[Documentation index](../README.md)

Status: accepted (2026-11-17)

See also: [ADR 0026](0026-debian-and-rpm-repositories.md) (APT GA, RPM), [ADR 0027](0027-hugging-face-repositories.md)
(Hugging Face), [ADR 0028](0028-generic-versioned-packages.md) (versioned generic packages),
[ADR 0029](0029-repository-visibility-public-private-feeds.md) (public visibility) and
[ADR 0030](0030-sbom-generation-and-vulnerability-scanning.md) (SBOM and scanning).

Management paths in this ADR are relative to `/api/v2` (the `servers` entry
of `api/openapi/management.yaml`). Root-mounted protocol endpoints are
specified in `api/openapi/protocols/*.yaml`.

## Context

Promotion and replication exist, but nothing runs them. Every run is an admin
call with an `Idempotency-Key`; there is no rule model, no trigger, no
schedule and no remote target.

- **Promotion.** `CreateRepositoryPromotion`
  (`internal/app/repository_distribution_api.go:25-106`) gates on
  `supportsAPTDistributionPreview` (an APT preview check), requires an
  active, same-format Hosted target (line 38), rechecks quarantine, evaluates
  the target's admission policy against the source's artifact intelligence
  (lines 45-64, `docs/security-admission-policy.md`), then enqueues a
  `lifecycle_jobs` row of kind `promotion`. Only Raw uses the
  `format.Promotion` registry; the other eight formats go through a
  hand-written `switch` (lines 66-92). Bytes are reused, not copied
  (ADR 0002; OCI `MountOCIBlobFrom` in
  `internal/protocol/oci/promotion.go`), which only works inside one
  installation and is authorized against a source repository.
- **Policy timing.** `EvaluateRepositorySecurityPolicy` runs at request
  time (`repository_distribution_api.go:57`) and in the policy dry run
  (`repository_policy_api.go:519`). The promotion worker does not
  re-evaluate it; only quarantine fencing is re-checked at publish. A queued
  or waiting job can therefore execute long after the policy decision.
- **Replication.** `replication_plans` (`migrations/000044`) point at
  local `hosted_repositories` only. `replication.Worker`
  (`internal/replication/worker.go`) has checkpoints, lease heartbeat,
  quarantine fencing and object locks, but every format wires
  `Source: r.objects, Destination: r.objects`
  (`cmd/gateway/background_runtime.go`). NuGet, Helm and RubyGems use
  separate `native_*_replication_jobs` tables and
  `/previews/*/replication-jobs` endpoints, and their docs exclude
  promotion, federation and Console/CLI
  (`docs/nuget-replication-preview.md:7-11`,
  `docs/helm-replication-preview.md:61`).
- **Triggers.** `publicationScanScheduler.Schedule`
  (`internal/app/publication_scan.go`) is the only "on publish" hook. It
  runs after the publication commits and only enqueues a scan job when
  `autoScanOnPublish` is set. Scan completion triggers nothing.
- **Webhooks.** `webhook_events`/`webhook_deliveries`
  (`migrations/000097`) insert event and deliveries in the state-change
  transaction. The event type is a CHECK over `artifact.quarantined`,
  `artifact.released` plus Cargo-only `cargo.promotion.*`/
  `cargo.replication.*` (`docs/webhook-delivery-contract.md`). There
  is no `artifact.published` event and no promotion event for the other
  formats.
- **Provenance.** Nothing links a promoted artifact to its origin beyond audit
  rows. Lifecycle states are `staged -> visible -> tombstoned` only
  (`internal/lifecycle/state.go`).
- **Metadata available for matching.** Coordinates are format-defined
  immutable units (`format.Governance.ValidCoordinate`,
  `internal/format/capabilities.go`); OCI tags and npm dist-tags are
  mutable refs. No cross-format artifact-property store exists (no migration
  defines one); ADR 0028 adds only a bounded per-version property map, frozen at seal,
  for versioned Raw repositories.
- **Permissions.** `ImportWrite` does not exist yet; this ADR introduces
  it as a new permission in the permission model.

Release scope: v0.1.0 contains only the foundation ADRs 0013-0017 (ADR 0013
global ordering table). This ADR ships in 0.2.x or later, so every schema
change below is a normal numbered migration (`000002+`, ADR 0015 rules
after the cut-over), not an edit of the baseline. ADR 0019 and 0020 provide
the machine identity used for remote targets; ADR 0018 provides group
principals. Slices that need them state a stub-auth fallback so local rules
can land first.

## Considered designs

1. **Rules as webhooks.** Ship the events and let CI call the existing
   promotion API. Smallest change, but gating, idempotency, retries, approval
   and provenance all move to user scripts, and every user re-implements the
   admission precheck. Rejected as the only answer; the events it needs are
   still part of this ADR.
2. **Polling rule engine.** Rules table plus a ticker that scans catalogs.
   Needs no event plumbing, but latency is the poll interval, scans of large
   repositories are expensive, and "after a clean scan" requires reading
   intelligence for every candidate each tick. Kept only as the schedule sweep
   (below), not the primary trigger.
3. **Event-driven rules with a transactional outbox (chosen).** Publication,
   scan completion and promotion write immutable domain events in their own
   transaction; the same transaction fans out pending rule evaluations,
   exactly as webhook deliveries already work. Adds one table family and
   touches each format's publish transaction.
4. **External workflow engine (Argo, Tekton).** Rich, but the repository is
   the only component that knows digests, quarantine and policy atomically; a
   second control plane contradicts the single-PostgreSQL deployment goal
   (ADR 0017). Rejected.

For remote targets: (a) **push** through a notarx-to-notarx import API, (b)
**pull** by creating a Proxy repository on the remote that points at the
source. Pull already works for reads and needs no code, but it carries no
gate, no provenance, no admission at the point of acceptance, and needs the
source reachable from the remote. Push is chosen; pull stays documented as a
read-through alternative.

## Decision

### Domain events

New table `domain_events` replaces `webhook_events` (migration in
slice 1). Shape follows ADR 0023 section 2 (partitioned tables cannot carry a
global unique key, and MySQL cannot partition at all):

```
domain_events(seq BIGINT IDENTITY, id UUID (UUIDv7, not unique), type TEXT,
              repository_id UUID, format TEXT, coordinate TEXT, digest TEXT,
              payload JSONB, dedupe_key TEXT, occurred_at TIMESTAMPTZ,
              PRIMARY KEY (occurred_at, seq))
-- non-unique per-partition index (dedupe_key, occurred_at); no FK points here
```

Types: `artifact.published`, `artifact.scan.completed`,
`artifact.quarantined`, `artifact.released`,
`artifact.promoted`, `promotion.pending_approval`,
`promotion.approved`, `promotion.rejected`,
`promotion.failed`, `replication.completed`,
`replication.failed`, and `repository.visibility_changed` (payload
`{repositoryId, from, to, groups}`, emitted by ADR 0029 slice 8; its `cdn_purge_requests`
row is inserted in the same transaction as the event). The payload of
`artifact.scan.completed` is `{previous, current, newFindings}` (ADR 0030). The Cargo-only names are dropped; `format` is
a payload field and every format emits the same set.

`dedupe_key` is `type|repo|coordinate|digest|version` and makes
re-emission idempotent: emission inside the state-change transaction is
exactly-once by construction, and the reconciler and repair paths insert only
`WHERE NOT EXISTS` a row with the same `dedupe_key` inside the retention window,
under `pg_advisory_xact_lock(hashtextextended(dedupe_key,0))` (ADR 0023 section 2). `version` is defined per type: the scan result id
for `artifact.scan.completed` (so a re-scan is a new event and re-pends a
waiting rule), the lifecycle transition id for quarantine/release, the
promotion id for promotion events, and empty for `artifact.published`
(one per visible publication of a digest).

Emission happens inside the transaction that commits the state change, via a
single helper `events.Emit(tx, Event)` which also inserts, in the same
transaction, (1) one `webhook_deliveries` row per matching enabled
subscription (unchanged) and (2) one `promotion_evaluations` row per
enabled rule whose source repository and event type match. `events.Emit`
consults the cached subscription and rule sets (ADR 0023 section 6) and writes
no `domain_events` row when no subscription, rule or internal consumer exists
for the type and repository; a rule created later recovers history through
`backfill: true`. Fan-out is a
bounded insert (rule cap per repository, below) using row-level locks only on
the new rows, so a rules-table problem cannot block unrelated publishes; a
failure in rule fan-out fails the publish transaction by design, because a
silently missed event would break the chain guarantee, and is covered by a
test. No poller reconstructs events from audit.

A catch-up reconciler lists `visible` publications that lack an
`artifact.published` event and emits it (idempotent by `dedupe_key`);
it exists so format publish paths can be converted in waves (slices 1b, 1c)
and as a repair tool. Slice 1c removes it from the default worker set once
all formats emit natively, and keeps only the repair CLI.

`domain_events` is range-partitioned monthly on `occurred_at` by the ADR 0023
partition manager (`internal/partitions`, slice 5 of that ADR) with retention
`NOTARX_EVENTS_RETENTION` (default 14d, free tier 3d) that drops old partitions
(MySQL: plain table, batched delete by the RetentionJanitor); `artifact_promotions` (below) is the long-lived record, not
the event table. `artifact.scan.completed` is emitted by the same
transaction that persists the vulnerability summary.
`publicationScanScheduler` becomes an event consumer instead of a
post-commit call site.

Slice 1a implementation decisions. The subscription set is not cached: `events.Emit`
calls the database function `emit_domain_event`, which reads the (tiny, administrator-managed)
`webhook_subscriptions` table in the same statement that inserts the event, so the gate and
the delivery fan-out see one consistent set and a new subscription never misses an event; the
`internal/readcache` rule set is used by the evaluator slice, where the set is large. The
Cargo triggers call the same function, so there is one emission rule. Internal consumers
(`events.Dispatcher`) read the log by `(occurred_at, seq)` from a per-consumer row in
`domain_event_cursors`. `occurred_at` is the database clock, so the dispatcher reads only below
the start of the oldest open transaction and below a five second settle horizon: an event that
commits late is never behind the cursor. `occurred_at` is always the database clock at emission, never a caller-supplied time. One node serves a
consumer at a time through a lease row of `scheduled_tasks` (kind `event-dispatcher`) that it renews on every
pass; a consumer without a cursor starts at the present; delivery is at least once, a failed event is
retried with an exponential backoff (to five minutes), and one that fails ten times in a row is logged and
skipped so a poisoned event cannot block the consumer.
`webhook_deliveries` carries `event_occurred_at` so its join to `domain_events` is pruned to one
partition, and its terminal rows are pruned on the event retention (14 d, 3 d free tier) because a
delivery cannot be shown or replayed once its event is gone. `artifact.scan.completed` carries
`newFindings` as a count of distinct unsuppressed vulnerability ids that the previous result did
not have.

### Rules

```
promotion_rules(id UUID PK, name UNIQUE, source_repository_id FK,
  target_kind 'local'|'remote', target_repository_id FK NULL,
  remote_instance_id FK NULL, remote_repository TEXT NULL,
  match JSONB, conditions JSONB, approval JSONB NULL, schedule JSONB NULL,
  run_as_service_account_id FK, enabled BOOL, version BIGINT, created_by,
  updated_by, created_at, updated_at)
```

`match`: `{coordinate: {glob|regex}, version: {semver range}?, refs: ["latest","v*"]?, properties: {key: glob}?}`.
Glob `*` does not cross `/`, `**` does; regex is Go RE2 (linear
time), anchored, at most 512 bytes, compiled at create time. The pattern is
applied to the format's canonical coordinate after `ValidCoordinate`.
`refs` uses an optional `format.Tagged` capability (OCI tag, npm
dist-tag). `properties` is valid only for versioned-generic sources (ADR 0028), where the
properties are frozen at seal, so evaluation stays pinned; a cross-format property store
remains out of scope; if demand appears it will be an immutable-once-set
post-publish property API (frozen before a rule evaluates), not a change to publish
APIs. A `version` semver range simply does not match a
non-semver version string. Rules never match on mutable data that could change after
the decision, so the evaluation always pins `(coordinate, digest)`.

`conditions` (all must hold): `securityPolicy: pass` (evaluates the
target's admission policy now, for early feedback; the authoritative
evaluation runs again inside the promotion job at the point of publish, see
Evaluator), `scan: {completed: true, maxSeverity: "medium"}` (read from `artifact_vulnerability` joined with
`scanner_db_generation`; a result against a database older than `NOTARX_SCANNER_DATABASE_MAX_AGE`
does not satisfy `completed: true`, ADR 0030),
`minAge: "72h"` (since `artifact.published`), `notQuarantined`
(always implied), `requireSignature/sbom/provenance` (reuses the policy
evaluator inputs).

`mode` is `copy` only: reuse bytes (ADR 0002) within an
installation, push bytes to a remote one. Move and overwrite are not offered;
sources are never mutated.

Authorization of rule changes (confused-deputy guard):

- Creating or updating a rule requires `RepositoryAdmin` on source and
  target (the existing `RA x 2` shape, `docs/cli-command-map.md:251`)
  and that the caller holds at least the permissions the chosen
  `run_as_service_account_id` holds on source and target, or is an
  administrator of that service account. The service account must be unable
  to do more through the rule than its creator could do directly.
- Execution runs as `run_as_service_account_id`, and source-read plus
  target-write are re-authorized at every evaluation (same durable
  re-authorization as the preview replication jobs). A revoked account parks
  the rule with state `authorization_lost` and emits
  `promotion.failed`.
- A rule is also parked in state `paused` (reason `target_became_public`) when its target
  repository becomes public after the rule was created, until an approver resumes it
  (ADR 0029 slice 11).
- Every rule edit increments `version`; it is recorded in the audit log
  with `updated_by`.

Slice 3a implementation decisions. The rule's `scope` is a member of `match` (`match.scope`, default `coordinate`),
so `promotion_rules` keeps exactly the columns above. `match.version` is `{range: "<semver range>"}`. The source repository of
a rule never changes; an edit replaces everything else. Rules whose source format lacks the `PromotionPublisher`
capability are refused with `422 unsupported_format`, as are `scope: release` rules for formats without `ReleaseSet`;
`properties` and `refs` the source cannot satisfy are `422 unsupported_match`. A remote target needs a platform
administrator, because remote instances are visible to administrators only. The confused-deputy guard compares the
caller and the run-as service account for every repository operation on the source and a local target, and exempts a
platform administrator. The dry run reads the artifact search projection (versioned Raw: the sealed versions), examines at
most 5000 artifacts, and reports `not_evaluated` for what cannot be evaluated yet (a remote target's admission policy).

### Chains

A chain is the graph induced by rules (dev -> staging -> release), not a
separate resource. `GET /repositories/{id}/chain` returns upstream and
downstream paths. Rule create/update runs under
`pg_advisory_xact_lock(hashtextextended('promotion_graph',0))` (64-bit, per ADR 0023 section 10), loads the local
edge set, and rejects an edge that closes a cycle (`409 promotion_cycle`,
listing the path). Fan-out is allowed; depth is capped by
`NOTARX_PROMOTION_MAX_HOPS` (default 8).

Cross-instance cycles cannot be detected statically, so every promotion
carries a `chain_path`. The importing instance does not trust it as the
only control: it rejects a unit when `len(chain_path) > NOTARX_PROMOTION_MAX_HOPS`,
when the path already contains its own `(instance_id, repository)`, and
it always appends its own hop. A sender that omits `chain_path` is
treated as depth 0 by the receiver and still bounded by the receiver's own
per-rule rate and the idempotency key. A `remote_instances` row whose
`instance_id` equals the local instance id is rejected at registration.

### Evaluator

`promotion_evaluations(id, event_seq, event_occurred_at, rule_id FK, repository_id, coordinate, digest, state, next_attempt_at, attempts, lease_owner/token/expires_at, last_reason, UNIQUE(event_seq, rule_id))`
(no foreign key to `domain_events`: a partitioned, expiring table cannot be a
referent), with claim index `(next_attempt_at) WHERE state IN ('pending','waiting')`,
`(repository_id, digest) WHERE state='waiting'` for scan-completed re-pends, and
terminal rows (`skipped`, `failed`, finished) pruned after 7d (free tier 1d) by
the RetentionJanitor. `fillfactor` and autovacuum settings follow ADR 0023
section 5.
states `pending | waiting | queued | skipped | failed`. A worker
(lease/heartbeat pattern copied from `webhook_deliveries`, woken by
`LISTEN`) loads the artifact, re-checks the match, evaluates conditions,
and either skips with a recorded reason, goes `waiting` (with
`next_attempt_at = publishedAt + minAge` or until an
`artifact.scan.completed` event for the same digest re-pends it), or
calls the shared `PromotionService.Request`.

`PromotionService` is extracted from the handler at
`repository_distribution_api.go:25-106` and is the only path to
promotion, for manual API, rules and batches alike; it runs quarantine,
admission policy, authorization and writes provenance. The promotion job
re-evaluates the target admission policy and quarantine immediately before
publish, so a policy tightened between enqueue and execution refuses the job
(new behaviour, with a test).

Guarantee: at-least-once evaluation with effectively-once promotion. The
idempotency key is `rule:<ruleId>:<sha256(target|coordinate|digest)>`;
redelivery, worker crash and rule edits never produce a second promotion row
or job. The key identifies the promotion, not the attempt: `retry` of a
`failed` row creates a new attempt row linked to the same key
(`attempt` counter), and re-promotion after a target tombstone starts a
new generation (the key includes the target tombstone generation, so a new
key is minted only after the previous one is tombstoned). A digest already
present at the target is a success no-op; different bytes at the same
coordinate is `conflict` (immutability,
`docs/nuget-replication-preview.md:79-85`).

`schedule` (`{cron, timezone: "UTC"}`, min interval
`NOTARX_PROMOTION_SCHEDULE_MIN_INTERVAL`, default 5m) runs a sweep job
(`lifecycle_jobs` kind `promotion_sweep`) that pages the source
catalogue, bounded to 1000 candidates per run, and inserts evaluations with
synthetic dedupe keys. It also serves backfill after a rule is created
(`backfill: true`) and recovery. A rule may be event-driven, scheduled, or
both.

### Provenance and approval

```
artifact_promotions(id UUID PK, target_repository_id, format, coordinate,
  digest, origin_instance_id UUID NULL, origin_repository TEXT,
  rule_id UUID NULL, rule_version BIGINT NULL, chain_path JSONB,
  requested_by, state, attempt INT, idempotency_key TEXT, job_id NULL,
  approvals JSONB, created_at, completed_at,
  UNIQUE(idempotency_key, attempt),
  -- indexes (ADR 0023 section 5): (target_repository_id, coordinate, digest);
  -- (state) WHERE state IN ('pending_approval','queued','running')
  CHECK state IN ('pending_approval','queued','running','completed',
                  'failed','rejected','expired','cancelled'))
```

Every promotion, manual or by rule, local or imported, writes one row per
attempt; `chain_path` is `[{instance, repository, ruleId, at}]` and
is appended, never rewritten. Console shows it on the artifact detail page;
`artifact.promoted` is emitted at `completed`.

Rules with `approval: {required: 1..3, expiresAfter: "168h", allowSelfApproval: false}`
create the row in `pending_approval` and emit
`promotion.pending_approval`. Approve/reject needs `RepositoryAdmin`
on the target (group principals from ADR 0018 count) and is re-authorized at
approval time. Approvers must be distinct. For manual promotions they must
differ from the requester unless `allowSelfApproval`. For rule-initiated
promotions the requester is the service account, so that check is vacuous;
instead the approver must differ from the rule's `created_by`, its
`updated_by` and the owner of the run-as service account. Approvals are
bound to `(digest, rule.version)`: a rule edit invalidates pending
approvals. On the last approval the service re-evaluates conditions,
quarantine and admission policy (state may have changed), then enqueues.
Expired rows move to `expired` via the sweep. Approvals stay in-product in 0.2:
delegation uses documented temporary grants, and an external approval callback, if
ever added, is an extra approver that still records an in-product approval row.

Public targets (ADR 0029 slice 11): promoting into a public repository needs the per-repository
capability `promote:public`, a rule flag `publishing: bool`, and the evaluator forces
`approval.required >= 1` at run time when the target is public, except for a `publishing` rule
when `NOTARX_PROMOTE_PUBLIC_REQUIRE_APPROVAL=false`. `artifact_promotions` records
`target_visibility` (payload field or column, set at request time). Remote push never sets
visibility on the receiver.

Promotion copies SBOM bodies (hash-verified), never scanner URLs (ADR 0030 slice 2c).

### Remote targets

```
remote_instances(id, name UNIQUE, base_url, instance_id UUID,
  auth_kind 'workload_identity'|'static_token', audience,
  token_sealed TEXT NULL, trust_evidence BOOL DEFAULT false, state,
  last_checked_at)
```

Managed by platform administrators only. `base_url` must pass
`internal/egress` SSRF policy (private ranges refused unless
`NOTARX_REMOTE_ALLOW_PRIVATE_NETWORKS=true`). `GET {base}/.well-known/notarx`
(root-mounted, specified in `api/openapi/protocols/notarx-instance.yaml`)
returns `{instanceId, apiVersion, formats[], limits}` and is checked at
registration and in `/remote-instances/{id}/test`.

Authentication: preferred, the sender mints an assertion with ADR 0020
(`POST /api/v2/service-accounts/{id}/assertions`, `aud` = remote
`audience`) and presents it to the receiver as a bearer JWT. The receiver
verifies it directly through the ADR 0019 trusted-issuer path (the sender's
issuer is registered as a trusted issuer on the receiver and mapped by a
trust policy to a service account with `ImportWrite`); the RFC 8693
exchange (`POST /auth/token-exchange`) is optional and only needed to
obtain a shorter-lived token. This ADR owns the peer client (the sender);
ADR 0020 has no peer-client slice. Fallback: a static token sealed with
`internal/secrets` (`NOTARX_SETTINGS_ENCRYPTION_KEY`, stored as
TEXT). The token is never logged or put into events.

Receiver API (OpenAPI-first, `api/openapi/management.yaml`, operation
permission `ImportWrite` on the target repository):

1. `POST /repositories/{id}/imports` with
   `{coordinate, digest, formatManifest, blobs:[{digest,size}], chainPath, idempotencyKey, evidence?}`
   returns an import session and the `missing` blob list. A blob is
   reported present only if it already exists in the target repository's own
   namespace, or the session principal can read it in some repository.
   Everything else is listed as missing and must be uploaded; the response
   never distinguishes "exists elsewhere" from "unknown", so a caller cannot
   publish or probe a blob it does not hold (no cross-repository read by
   digest; same authorization rule as OCI mount-from).
2. `PUT /imports/{importId}/blobs/{sha256}` streams one blob; the server
   hashes while streaming into a staging key, and rejects on digest or size
   mismatch before anything becomes visible.
3. `POST /imports/{importId}/commit` re-verifies every digest, runs the
   receiver's quarantine and admission policy, then publishes through the
   format's `Publish` callback, so lifecycle remains
   `staged -> visible`. Idempotent by `idempotencyKey`.

The receiver never trusts sender metadata: `formatManifest` is parsed by
the format's own `Importer`, and coordinate, size and layout are
recomputed from the verified bytes; a mismatch with the claimed coordinate
rejects the import.

Limits (all configurable, defaults in Configuration): max blob size, max
session bytes, max blobs per session, max open sessions per principal, and
a staging quota. Abandoned sessions and staging keys are removed by a reaper
whose TTL equals `NOTARX_IMPORT_SESSION_TTL`.

OCI: the unit is the manifest digest plus all child manifests/blobs plus
referrers (signatures, SBOMs as referrer manifests) by digest. Blob sharing
(`MountOCIBlobFrom`) is local only; across instances every missing blob is
uploaded and hash-verified. A tag from `refs` is applied at the target
only when it is absent or already points at the same digest; otherwise the
evaluation is `conflict` and tag moves stay an explicit operator action.
Coordinates containing `@` stay rejected as today.

The sender is a new `lifecycle_jobs` kind `remote_push` reusing the
`replication.Worker` checkpoint, lease and quarantine-fence machinery
with a remote `Destination`. Failure handling: 4xx policy, authorization
or immutability responses are terminal (`failed` with the remote reasons);
5xx, timeouts and network errors are retryable with exponential backoff and
a max attempt count (`NOTARX_REMOTE_PUSH_MAX_ATTEMPTS`). If a timeout hits
after `commit` was sent, the sender re-sends `commit` with the same
`idempotencyKey` before failing, which is safe by idempotency.

Shipped `evidence` (scan, SBOM, signature) is stored as imported claims
and only counts toward policy when `trust_evidence` is true; otherwise
the remote scans (`autoScanOnPublish`). The remote's policy always has
the final word. The flag stays binary in 0.2; signed evidence will be DSSE/in-toto
statements verified against the pinned sender issuer key (ADR 0020), at which point
`trust_evidence` means "verified signature required".

### Format capabilities and scope

The `switch` at `repository_distribution_api.go:66-92` and the
per-format replication wiring are replaced by registry lookups. New optional
capabilities in `internal/format/capabilities.go`:
`PromotionPublisher` (extends `Promotion`), `Exporter`
(`ExportUnit` returns manifest plus blob descriptors) and
`Importer` (`ImportUnit`), `Tagged`, and `ReleaseSet` (the format package returns the
members of the release identified by a coordinate; implemented for Raw, then OCI and Maven,
no generic inference). Rule creation consults the
registry and returns `422 unsupported_format` for a format lacking the
capability, including `scope: release` on a format without `ReleaseSet`. All nine core formats (OCI, Maven, npm, PyPI, Go, Cargo, Raw,
Conan, APT) are in scope for local rules; remote push lands in waves (Raw,
OCI, Maven, npm first). Later formats join by registration through their own ADR slices, with
no engine change: RPM as a tenth format (ADR 0026 slice 13: `PromotionPublisher`, `Exporter`,
`Importer`; the importer coalesces into one target snapshot per `NOTARX_RPM_SNAPSHOT_COALESCE`
window), Hugging Face (ADR 0027 slice 12: `Tagged`, `Governance`, `Importer`, preserving the
upstream `commit_sha`; its digest is the sha256 of the canonical commit manifest) and versioned
Raw (ADR 0028 slices 6 and 9: `Promotion`, `Exporter`, `Importer`, coordinate `name@version`). **NuGet, Helm and RubyGems previews are out of scope
for this ADR**: their replication jobs and endpoints stay as they are, and
they join rules by implementing the capabilities when they leave preview (a
registration, no engine change). The APT preview gate stays until APT leaves
preview in ADR 0026 slices 6 and 18 (slice 18 removes `supportsAPTDistributionPreview`
(`internal/app/repository_distribution_api.go`)).

### Batch and version sets

`POST /promotion-batches` takes up to 100 `(coordinate, digest)`
items (the preview batch limit) plus target. Admission is all-or-nothing:
every item passes quarantine, policy and authorization before any is queued;
execution is per item and idempotent, and the batch state aggregates
(`completed`, `partially_failed`). A rule may set
`scope: "release"` with a format-defined grouping; versioned Raw repositories define it as
the version, identified by `(name@version, manifest_digest)` (ADR 0028); until other formats
define a release grouping (through `ReleaseSet`: Raw, then OCI and Maven) the rule engine
promotes per coordinate, and batches are explicit.

### Console and CLI

Console: a **Promotions** area with a chain graph (repositories as nodes,
rules as edges, pending counts), a rule editor with a dry-run result table, an
approvals queue, and a Provenance tab on artifact detail. CLI
(`notarxctl`): `promotion rule create|list|show|update|delete|dry-run`,
`promotion list|show|approve|reject|cancel|retry`,
`promotion batch create`, `chain show --repo`,
`remote add|list|test|delete`. The existing
`promotion create --repo --target` stays as the manual path through
`PromotionService`. `POST /promotion-rules/{id}/dry-run` returns
matched artifacts with a per-condition verdict and never enqueues.

### Configuration

`NOTARX_PROMOTION_ENABLED` (true), `NOTARX_PROMOTION_EVALUATOR_INTERVAL`
(5s), `NOTARX_PROMOTION_EVALUATOR_CONCURRENCY` (4),
`NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY` (32),
`NOTARX_PROMOTION_MAX_HOPS` (8), `NOTARX_PROMOTION_APPROVAL_TTL`
(168h), `NOTARX_PROMOTION_SCHEDULE_MIN_INTERVAL` (5m),
`NOTARX_EVENTS_RETENTION` (14d),
`NOTARX_PROMOTE_PUBLIC_REQUIRE_APPROVAL` (true; ADR 0029),
`NOTARX_REMOTE_ALLOW_PRIVATE_NETWORKS` (false),
`NOTARX_REMOTE_PUSH_MAX_ATTEMPTS` (8), `NOTARX_IMPORT_SESSION_TTL`
(24h), `NOTARX_IMPORT_MAX_BLOB_BYTES` (10 GiB),
`NOTARX_IMPORT_MAX_SESSION_BYTES` (50 GiB),
`NOTARX_IMPORT_MAX_BLOBS_PER_SESSION` (2000),
`NOTARX_IMPORT_MAX_OPEN_SESSIONS_PER_PRINCIPAL` (4).

## Security considerations

- Rules are privilege-bearing: they run as a named service account, are
  authorized on both repositories at create time and at every run, and the
  creator cannot select a service account more privileged than themselves.
  Every promotion is audited with the rule id and version.
- A rule never bypasses admission policy, quarantine, or immutability;
  conditions only add gates. Policy and quarantine are re-evaluated inside the
  promotion job at publish, not only at request time.
- Approvals are bound to the rule version and re-authorized at approval time;
  rule creators and editors cannot approve their own rule's promotions.
- Remote import verifies sha256 and size on every blob, commits only after
  full verification, and re-runs the receiver's policy; the sender's evidence
  and metadata are untrusted by default. Blob dedupe never reveals or exposes
  blobs the principal cannot already read.
- Import resource limits (session count, bytes, blobs, staging quota, reaper)
  bound abuse by a compromised sender.
- SSRF: remote URLs go through the egress policy; redirects are not followed
  across hosts; TLS verification is mandatory (no insecure flag).
- Credentials: assertions are short-lived and audience-bound; static tokens are
  sealed at rest and write-only in the API.
- Loop and amplification: cycle rejection locally, receiver-side
  `chain_path` and hop-cap rejection remotely, self-instance rejection,
  per-repository rule cap, sweep bounds.
- Regex rules use RE2 and a length cap to avoid ReDoS; evaluation of untrusted
  coordinates never uses backtracking engines.
- Webhook and event payloads exclude credentials and raw worker errors (as the
  existing contract).

## Consequences

- Automatic promotion, approvals and an auditable chain become first-class;
  the existing manual API keeps working through the same service, so behaviour
  is uniform.
- Every format's publish transaction must emit an event; the reconciler bounds
  the migration risk but the transactional path is the target. Cargo's webhook
  names change (acceptable under ADR 0013, no compatibility promise).
- Two engines remain (`replication.Worker` for byte transfer, evaluator
  for decisions); that separation is deliberate. Preview formats stay on their
  own tables until graduated.
- Cross-instance promotion needs both sides to run a notarx version with the
  import API; there is no negotiation with other products.
- The schema lands as additive numbered migrations after the 0.1.0 baseline;
  once v0.1.0 is tagged, released migrations are immutable (ADR 0015).
- Local rules do not depend on ADR 0018-0020; remote push does.

## Implementation slices

Each slice is one PR. Every slice must pass `make test`, `make lint`
(includes `architecture-check`) and `make coverage` (floors in
`scripts/coverage-check.sh`); API changes edit
`api/openapi/management.yaml` (or the named protocol spec) first and pass
`make openapi-check`, with CLI mappings classified in
`docs/cli-command-map.md` (`make cli-contract-check`). All slices
depend on ADR 0015 slice 4 (cut-over) and use numbered migrations.

1a. **Domain events core.** Scope: migration for `domain_events`
   (partitioned, retention job) and relaxed webhook CHECKs, `internal/events`,
   webhook event-type validation, `docs/webhook-delivery-contract.md`,
   OpenAPI event enums, `autoScanOnPublish` as a consumer, scan-completed
   emission. Acceptance: [ ] `Emit` is transactional (rollback test shows
   no event/delivery); [ ] dedupe-key idempotency test with per-type
   `version`; [ ] re-scan re-emits; [ ] retention test; [ ] Cargo-specific
   names removed; [ ] event type enum also lists `repository.visibility_changed` (ADR 0029 slice 8) and the catalogue documents the `artifact.scan.completed` payload `{previous, current, newFindings}` (ADR 0030); [ ] `make openapi-check`; [ ] partition and retention behaviour
   per ADR 0023 (default partition empty, 14d drop). Depends on ADR 0015 slice 4
   and ADR 0023 slice 5 (partition manager).
1b. **Published emission for Raw, OCI and Maven.** Scope: emit
   `artifact.published` in those publish transactions, catch-up
   reconciler. Acceptance: [ ] events emitted in the same transaction
   (rollback test per format); [ ] reconciler test; [ ] publish still succeeds
   with zero rules. Depends on 1a.
1c. **Published emission for the other six formats.** Scope: npm, PyPI, Go,
   Cargo, Conan, APT; remove the reconciler from the default worker set (repair
   CLI stays). Acceptance: [ ] per-format emission test; [ ] reconciler no
   longer scheduled. Depends on 1b.
2a. **PromotionService extraction.** Scope: extract from
   `repository_distribution_api.go` behind unchanged behaviour, including
   the APT preview gate. Acceptance: [ ] existing promotion API tests
   unchanged and green; [ ] no behaviour change in OpenAPI. Depends on ADR 0015
   slice 4.
2b. **Format registry and provenance.** Scope: register all nine formats in
   the registry (remove the `switch`), `artifact_promotions` table,
   `GET /artifact-promotions`, `artifact.promoted`/`promotion.failed`
   events, policy re-evaluation at publish. Acceptance: [ ] every manual
   promotion writes one provenance row; [ ] idempotent replay returns the same
   row; [ ] unsupported-format test; [ ] `ReleaseSet` capability declared in the registry;
   [ ] test tightening target policy between
   enqueue and execution refuses the job. Depends on 1a, 2a.
3a. **Rules schema, matcher, API and dry-run.** Scope: `promotion_rules`,
   `internal/promotionrules`, `/promotion-rules`, dry-run, creator
   privilege check against the service account. Acceptance: [ ] glob/regex
   table tests including ReDoS-length limits; [ ] `match.properties` accepted only for versioned-generic sources (ADR 0028), rejected with `422` otherwise, `scope: release` rejected with `422 unsupported_format` for formats without `ReleaseSet`, and `match.version` ranges do not match non-semver versions; [ ] confused-deputy refusal
   test; [ ] dry-run never enqueues. Depends on 2b.
3b. **Chain graph and cycle check.** Scope: advisory-locked cycle check,
   `GET /repositories/{id}/chain`, hop cap. Acceptance: [ ] cycle and
   hop-cap rejection (`409 promotion_cycle` with path); [ ] concurrent
   create race test. Depends on 3a.
3c. **Evaluator worker (local).** Scope: `promotion_evaluations`, worker,
   fan-out in `Emit`, `authorization_lost` parking. Acceptance:
   [ ] duplicate delivery and worker crash produce one promotion row and one
   job (kill-and-restart test); [ ] retry creates a linked attempt row; [ ]
   re-promotion after target tombstone; [ ] `scope: release` on Raw expands to the version's files through `ReleaseSet`;
   [ ] integration test dev -> staging ->
   release on OCI and Maven. Depends on 3a, 1b.
4. **Conditions, waiting and schedules.** Scope: scan/minAge/signature
   conditions, re-pend on `artifact.scan.completed`, cron sweep job kind
   `promotion_sweep` and backfill. Acceptance: [ ] clean and dirty scan
   fixtures with the reference scanner (the scanner smoke target, renamed with `cmd/notarx-scanner` by ADR 0030 slice 1b); [ ] the `scan` condition reads through one reader port so ADR 0030 slice 8a can add the stale-database rule (a result older than `NOTARX_SCANNER_DATABASE_MAX_AGE` does not satisfy `completed: true`);
   [ ] minAge uses an injected clock; [ ] sweep bound test. Depends on 3c.
5. **Approval workflow.** Scope: `pending_approval` states,
   approve/reject/cancel endpoints, distinct-approver rules, rule-version
   binding, expiry, re-evaluation on final approval. Acceptance: [ ]
   self-approval refused; [ ] rule creator/editor cannot approve; [ ] rule
   edit invalidates pending approvals; [ ] expiry sweep; [ ] state change
   between request and approval is honoured. Depends on 3c; group approvers
   depend on ADR 0018 (user approvers can land first).
6. **Batch promotion.** Scope: `/promotion-batches`, aggregate state.
   Acceptance: [ ] all-or-nothing admission; [ ] partial-failure aggregation;
   [ ] 100-item cap. Depends on 2b.
7a. **Remote instances and well-known endpoint.** Scope:
   `remote_instances`, `/remote-instances` CRUD and test,
   `GET /.well-known/notarx` in
   `api/openapi/protocols/notarx-instance.yaml`, `internal/egress`
   integration, self-instance rejection. Acceptance: [ ] SSRF tests; [ ]
   self-instance rejected; [ ] static token sealed as TEXT and write-only.
   Depends on 2b; ADR 0018 slice 1 (secrets key-ring) for sealing.
7b. **Import sessions and Raw importer.** Scope: `ImportWrite`
   permission, the three import endpoints, staging reaper and limits,
   `format.Importer` for Raw. Acceptance: [ ] digest/size mismatch
   rejected before visibility; [ ] missing-blob list never exposes blobs the
   principal cannot read; [ ] coordinate recomputed from bytes; [ ] limits and
   reaper tests; [ ] receiver policy and `chain_path`/hop-cap rejection.
   Depends on 7a. Authentication for tests uses a stub principal until ADR
   0019 slice 4 (trusted-issuer authenticator) lands.
7c. **OCI importer.** Scope: `format.Importer` for OCI (children,
   referrers, tag rules) and its `ReleaseSet` (index plus child manifests). Acceptance: [ ] tag conflict yields `conflict`;
   [ ] referrers imported by digest; [ ] cross-instance blob re-upload
   verified; [ ] `scope: release` on OCI promotes index and children together. Depends on 7b.
8. **Remote push sender.** Scope: `remote_push` job kind on
   `replication.Worker`, peer client with bearer-assertion auth plus
   static-token fallback, rule `target_kind=remote`, `trust_evidence`,
   failure classification and backoff, make target `promotion-remote-e2e`
   with two notarx instances. Acceptance: [ ] two-instance e2e for Raw and OCI;
   [ ] receiver verifies the assertion directly via the trusted-issuer path;
   [ ] credential never logged (test over log output); [ ] remote denial
   surfaces reasons and is terminal; [ ] post-commit timeout re-sends commit.
   Depends on 3c, 7b, 7c, ADR 0020 slice 4 (assertion minting) and ADR 0019
   slice 4.
9. **Format waves for remote push.** Maven, npm, then PyPI, Go, Cargo, Conan,
   APT; one PR per wave with `Exporter`/`Importer` (the Maven wave also ships its `ReleaseSet`,
   all files of one version). RPM, Hugging Face and versioned Raw are not waves of this slice: they register their own `Exporter`/`Importer` in ADR 0026 slice 13, ADR 0027 slice 12 and ADR 0028 slice 9. Acceptance per
   wave: [ ] round-trip digest equality test; [ ] native client install from
   the remote. Depends on 8.
10a. **Console: rules and chain graph.** Scope: `console/` Promotions area,
   chain graph with keyboard list alternative, rule editor, dry-run table,
   i18n keys in en-CA and fr-CA (ADR 0014). Acceptance: [ ] `make console-test`,
   `make console-check`, `make console-api-check`; [ ] accessibility
   checks on the graph. Depends on 3b, 4.
10b. **Console: approvals queue and provenance tab.** Scope: approvals queue,
   Provenance tab on artifact detail, `make console-visual` updated with
   the brand tokens (ADR 0013). Acceptance: [ ] `make console-visual`;
   [ ] fr-CA keys complete. Depends on 5, 2b.
10c. **Console: remote instances.** Scope: remote instance screens. Acceptance:
   [ ] token write-only in UI; [ ] `make console-test`. Depends on 7a.
11. **notarxctl commands.** Scope: `internal/notarxctl` command groups
   (package named by ADR 0013 slice 3), `docs/cli-command-map.md`.
   Acceptance: [ ] `make cli-test`, `make cli-contract-check`.
   Depends on 3a-3b, 5, 6, 7a as commands land.
12. **Docs.** Scope: new `docs/promotion-rules.md`, update
   `docs/security-admission-policy.md` and
   `docs/nexus-gap-analysis.md`; document delegation via temporary grants, `ReleaseSet`
   support per format and the binary `trust_evidence` flag. Acceptance: [ ] `make docs-check`.
   Depends on 3c, 5, 8.

## Resolved questions

**Q1 — Add a cross-format artifact property store so rules can match properties?** Decision: No; it stays deferred and rules match coordinate, ref and (for versioned Raw, ADR 0028) frozen properties only. If demand appears, add an immutable-once-set post-publish property API (set before a rule evaluates, frozen after promotion) rather than changing publish APIs. Rules must never match mutable data and a publish-time store would touch every format.

**Q2 — Which other formats define a release set for `scope: release`, and who owns it?** Decision: Add an optional `ReleaseSet` capability beside `Exporter`/`Importer`, owned by each format package; ship Raw (the version, ADR 0028) first, then OCI (index plus children) and Maven, and return `422 unsupported_format` for any other format. The set is format-defined and cannot be inferred generically.

**Q3 — Should the receiver accept signed sender evidence instead of the binary `trust_evidence` flag?** Decision: Keep the binary flag for 0.2; signed evidence is planned as DSSE/in-toto statements verified against the pinned sender issuer key (ADR 0020), after which `trust_evidence` means "verified signature required". It is additive and the sender's JWKS already provides the verification root.

**Q4 — Should approvals support time-bounded delegation or an external approval webhook?** Decision: Stay in-product for 0.2; delegation is done with documented temporary grants, and an external approval callback is revisited only on a design-partner request, modelled as an additional approver that still records an in-product approval row. This matches GitHub environments, where third-party approval is a preview add-on.
