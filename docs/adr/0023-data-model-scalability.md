# Data Model Scalability: Write Decoupling, Partitioning, Retention, Counters and Caches

[Documentation index](../README.md)

Status: accepted (2026-11-17)

See also: [ADR 0027](0027-hugging-face-repositories.md), [ADR 0029](0029-repository-visibility-public-private-feeds.md), [ADR 0030](0030-sbom-generation-and-vulnerability-scanning.md).

## Context

Design targets for this ADR: 10M artifact versions, 100M or more download events
per year, 1k read requests/s, 50 publishes/s, five nodes, multi-year retention,
and, as a separate profile, the single-node free tier of ADR 0017 (2 OCPU, 12 GB
VM; PostgreSQL self-hosted or the 50 GB MySQL HeatWave of ADR 0022). Three
independent reviews of migrations `000001` to `000190`, the Go stores and ADRs
0018 to 0022 found the following. Nothing was measured; every figure is an
estimate until slice 17 produces numbers.

- **Request-path writes.** `RecordAudit` (`internal/repository/postgres_audit.go:12-75`)
  opens a transaction per download, inserts into `resolver_audit_log` (five
  secondary indexes: `000002`, `000049`, `000064` BRIN, `000073`, `000134`; the
  `000049` and BRIN indexes are subsumed by `000073`) and upserts
  `artifact_usage_stats` (`postgres_artifact_usage.go`). At 1k req/s that is 1k
  commits/s, and every download of one popular artifact serialises on one row.
  `FindActiveAPIKeyByHash` (`postgres_api_keys.go:42`) runs `UPDATE api_keys SET
  last_used_at=now()` per request, so one CI key is a 1k writes/s hot row.
- **Retention.** `audit_retention_policy` defaults to `enabled=false, keep_days=0`
  (`000049`); `DeleteAuditsBefore` (`postgres_audit_retention.go:111`) deletes at
  most 1000 rows per statement (`audit_cleanup_jobs.batch_size` CHECK 1..1000).
  No `DELETE` exists for `lifecycle_jobs`, `webhook_events`, `webhook_deliveries`,
  `scheduled_task_runs`, `replication_checkpoints`, `repository_quota_alert_events`,
  `nexus_migration_entries` and others. Scan-on-publish creates one
  `lifecycle_jobs` row per version (about 4.3M rows/day at 50 publishes/s).
- **Statistics scans.** `ListRepositoryRequestStatistics` aggregates 30 days of
  audit rows per call; `QueryArtifactUsage` uses `strpos`, `COUNT(*)` and `OFFSET`
  (`postgres_artifact_usage.go:29`); `ListAuditPage` (`postgres_audit.go:103`)
  uses `($n='' OR col=$n)` catch-alls with no actor or outcome index.
- **Quota triggers.** `assert_hosted_repository_capacity` (`000046`, bodies
  replaced in `000076`, `000080`, `000081`, `000090`, `000110`, `000126`,
  `000131`) runs `SELECT ... FOR UPDATE` on the quota row and then `SUM()`s the
  whole repository from `AFTER INSERT OR UPDATE ... FOR EACH ROW` triggers:
  O(assets) per inserted row, serialised per repository.
- **Untyped keys.** About 600 non-test queries compare `col::text = $n` on uuid
  columns (604 by `grep -rn '::text *= *\$' internal`, for example
  `postgres_lifecycle.go:30,44,96`), which defeats the btree on `lifecycle_jobs`,
  `user_sessions`, `native_npm_versions` and others.
- **No read caching.** `singleflight` exists only in `internal/protocol/oci/cache.go`.
  Anonymous reads, group resolution, grants (three queries in
  `GetRepositoryGrants`), users and sessions are read per request: an estimated
  5 to 15 queries per request on pools of 46 connections per node
  (`internal/database/postgres.go:19-47`).
- **Queues.** `claimLifecycleJobs` (`postgres_lifecycle.go:128`) sorts every
  pending row with `DISTINCT ON (repository_id)`, filters with `NOT EXISTS` on
  running rows, and evaluates `pg_try_advisory_xact_lock` in a `WHERE` before
  `LIMIT`; about 30 runtimes share one `LISTEN` channel
  (`internal/repository/postgres_notify.go`), so each enqueue wakes all of them.
- **Search and payloads.** `SearchArtifactProjection` computes `row_number()`
  windows over the whole `artifact_search_projection` UNION view and applies the
  keyset afterwards (`postgres_search.go:57-100`); Maven and Conan branches
  LATERAL-join publish sessions. Large `bytea`/`jsonb` payloads (npm manifests,
  `nuspec`/`chart_yaml`, up-to-8 MB proxy bodies, SBOMs in `artifact_intelligence`)
  live in TOAST; `metadata_json TEXT` columns re-parse on every write through
  `CHECK (jsonb_typeof(metadata_json::jsonb)=...)`.
- **Proposed ADR tables.** ADR 0021 declares `domain_events` partitioned monthly
  with `id UUID PRIMARY KEY` and `dedupe_key TEXT UNIQUE`, which PostgreSQL
  rejects (every unique key on a partitioned table must include the partition
  key) and MySQL cannot partition with foreign keys at all; `promotion_evaluations`
  has no claim index. ADR 0018 `user_group_members` is keyed `(group_id, user_id)`
  but is read by `user_id` on every request; `saml_assertion_ids` has no index.
  ADR 0019 `workload_jti_seen` churns with no stated batch size.

Binding constraints: after the ADR 0015 cut-over (slice 4) schema changes are
numbered migrations `000002+`, and ADR 0015 slice 7 re-squashes them into one
baseline before `v0.1.0`. Schema-shape changes made before that tag therefore
cost nothing in upgrade terms; after it they are forward-only migrations with the
immutability check. Pre-tag databases are disposable, so a migration in this ADR
may drop and recreate a table (for example to make it partitioned) without a data
copy; it says so in its header comment and is folded away by the re-squash.

## Considered designs

### A. Where audit and counters are written

1. **Keep synchronous writes, tune the table** (fewer indexes, `fillfactor`,
   `synchronous_commit=off` for the audit transaction). Smallest change and keeps
   audit exactly-once, but still one transaction per download and the usage row
   stays a lock queue; MySQL gains nothing. Rejected as the primary design, but
   `NOTARX_AUDIT_READ_MODE=sync` keeps it as a switch.
2. **Per-node batched asynchronous writer with in-memory aggregation (chosen).**
   Bounded memory, one multi-row statement per flush; cost is losing, on a crash,
   the records not yet flushed (up to the flush interval in normal operation, up to
   the whole buffer when the database is failing), which is stated and
   configurable.
3. **External log pipeline (Kafka, ClickHouse).** Right for very large fleets but
   adds a mandatory dependency, contrary to ADR 0017's single-node free tier.
   Left as a later sink behind the same writer interface.

### B. Retention of append-only data

1. **Batched `DELETE` for everything.** Works on both engines, but 1000-row
   batches cannot keep up with 1k inserts/s and leave bloat in every index.
2. **Native range partitioning with `DROP PARTITION`, in-app manager (chosen for
   PostgreSQL, for pure append-only tables only).** O(1) retention, no bloat, no
   `pg_partman` dependency. MySQL has no equivalent that coexists with ADR 0022's
   schema, so it uses plain tables with throttled batched deletes.
3. **`pg_partman` / `pg_cron`.** Mature, but an extension dependency on managed
   PostgreSQL offerings and absent on MySQL. Rejected; the manager is about 300
   lines.

Queue-like tables (`lifecycle_jobs`, `webhook_deliveries`, `promotion_evaluations`)
are not partitioned: they are updated through their life, looked up by `id` from
APIs and workers (a partition key would have to be carried everywhere), and have
referrers. They get terminal-row pruning plus autovacuum tuning.

### C. Quota accounting

1. **Statement-level triggers with transition tables** (PostgreSQL only). Keeps
   coverage of every writer without touching Go, but has no MySQL equivalent and
   keeps business logic in plpgsql.
2. **Application-level counter reservation in the publishing transaction with
   periodic reconciliation (chosen).** One code path on both engines; the risk is
   an uncovered writer, closed by a per-format equality test and the reconciler.
3. **Compute on read only, enforce approximately.** Rejected: ADR 0006 treats
   quotas as hard limits.

### D. Read-path configuration lookups

1. **Cache nothing, add indexes.** Still 5 to 15 queries per request.
2. **Shared cache (Redis).** New mandatory dependency; rejected for the same
   reason as A3.
3. **In-process versioned TTL caches with `singleflight` (chosen).** Staleness is
   bounded by a stated revocation SLO and shortened by `NOTIFY` (polling on
   MySQL).

## Decision

All new numbered migrations ship in both dialect directories per ADR 0022; where
a feature has no MySQL form the decision names the substitute. Metrics use the
`notarx_` prefix; config keys use `NOTARX_` (ADR 0013).

### 1. Request-path write decoupling

New package `internal/auditwriter` behind the existing `repository.AuditRecorder`
port. Every `AuditRecord` gets a durability class from `AuditRecord.Durability()`:

| Class | Events | Path | Why |
| --- | --- | --- | --- |
| `sync` | authorization denials for an authenticated principal, management API mutations, publish, delete, quarantine, release, promotion, token mint, settings changes, retention and key operations | in the caller's transaction or an immediately committed insert (unchanged semantics) | the record is the evidence of a security decision or a state change; it must not be lost or reordered relative to it |
| `async` | successful reads and downloads, cache hits and misses, group resolution, anonymous 401/403 | enqueue to a bounded in-process buffer, return immediately | high volume, attacker-controlled volume in the anonymous case, loss of a trailing second is acceptable |

State changes and management mutations are audited in the same transaction as the
change and are never dropped, never subject to the semaphore and never queued: if
the audit insert fails the change fails (fail closed). Only the high-rate `sync`
sub-class, authorization denials for authenticated principals, uses an
immediately committed insert bounded by a per-node semaphore
(`NOTARX_AUDIT_SYNC_MAX_INFLIGHT`, default 64). When that is exhausted the denial
record is queued on the async path with priority `security`, which waits up to
250 ms for buffer space and then drops with
`notarx_audit_dropped_total{class="security"}` incremented and an alert. So
security-denial records can be lost under overload by a credentialed flooder or on
a crash after queueing; the denial itself is always enforced. Operators who need
every denial durable set `NOTARX_AUDIT_DENIAL_MODE=sync`, which blocks (and
applies backpressure to) the request instead of dropping.

Async writer: a buffered channel (`NOTARX_AUDIT_BUFFER_SIZE`, default 65536 records),
one flusher goroutine per node, flush when `NOTARX_AUDIT_BATCH_SIZE` (default 1000)
records are pending or `NOTARX_AUDIT_FLUSH_INTERVAL` (default 1s) elapsed, one
statement per flush: `INSERT ... SELECT * FROM unnest($1::text[], ...)` on
PostgreSQL, multi-row `VALUES` on MySQL (driver-agnostic; `COPY` is not required).
A full buffer drops the new `async` record and increments
`notarx_audit_dropped_total{class="read",reason="buffer_full"}`; the request path
never blocks. `notarx_audit_buffer_depth` and `notarx_audit_flush_seconds` are
exported. Failed flushes retry with backoff while the buffer holds; persistent
failure turns into buffer-full drops, with a `notarx_audit_flush_errors_total`
alert. On SIGTERM the writer stops accepting after the HTTP server has drained,
then flushes within `NOTARX_SHUTDOWN_FLUSH_TIMEOUT` (default 10s). A process crash, SIGKILL or node loss loses every record still in the buffer: at
steady state about one flush interval (about 1k records at 1k req/s); when the
database is down or slow, up to `NOTARX_AUDIT_BUFFER_SIZE` records (65536, about a
minute at 1k req/s), after which new records are dropped. Nothing is spooled to
disk. Read audit is therefore best-effort evidence of access, not a complete
log, and the product documentation says so; installations whose compliance regime
requires complete read audit (for example per-access logging obligations) set
`NOTARX_AUDIT_READ_MODE=sync`, accepting one commit per request and the
throughput ceiling that implies, and are told so in the capacity guidance.

Anonymous traffic on public repositories (ADR 0029 slice 9) adds the audit fields `visibility`,
`cache_disposition` and `rate_limited`, and samples anonymous `allow` records with
`NOTARX_AUDIT_ANON_ALLOW_SAMPLE` (one in N written; counters stay exact) and
`NOTARX_AUDIT_ANON_DENY_BURST` (cap on anonymous denials written per interval); sampled-out
records increment a drop metric, in the same buffer and under the same overflow rules as above.

Usage counters are aggregated before they reach the database. The writer keeps a
map keyed by `(repository_id, format, resource)` holding count, bytes, last time and
last actor, flushed every `NOTARX_USAGE_FLUSH_INTERVAL` (default 5s) in key order as
`INSERT ... ON CONFLICT DO UPDATE SET download_count = artifact_usage_stats.download_count + excluded.download_count`
(`ON DUPLICATE KEY UPDATE` on MySQL). Sorted keys prevent deadlocks between nodes.
The map is capped at 100k keys; overflow forces an early flush. Counters are not dropped under
load (when the database is unreachable the map is held up to its cap, then new
keys are dropped and counted); they are, like raw rows, lost for the unflushed
interval on a crash, and a flush whose commit acknowledgement is lost can be
retried and double-count, so counters are approximate by up to one interval per
node and are not billing-grade. Retention policies that depend on last download
time apply a grace of at least 2 x `NOTARX_USAGE_FLUSH_INTERVAL`. A hot artifact now costs one row update per
node per interval instead of one per download.

Rollups replace the 30-day scan. New table
`repository_request_rollup(repository_id UUID, bucket_start TIMESTAMPTZ, requests BIGINT, denied BIGINT, bytes BIGINT, PRIMARY KEY (repository_id, bucket_start))`
at hourly granularity, incremented by the same flush from the in-memory
aggregate (sync denials increment through the same aggregator, so counts are
eventually consistent within seconds). `ListRepositoryRequestStatistics` and the
overview read it (about 24 x 30 rows per repository). Rollup retention is 400 days.
Retention evidence that today depends on `artifact_usage_stats` (last download)
lags by at most `NOTARX_USAGE_FLUSH_INTERVAL`.

### 2. Partitioning and retention

**Partition manager.** New `internal/partitions` with a registry of
`{table, partition_column, granularity: month|day, ahead: 3}`. On start (before
the node reports ready) and then every 6 h it runs
`CREATE TABLE IF NOT EXISTS <table>_pYYYYMM PARTITION OF <table> FOR VALUES FROM (...) TO (...)`
for the current period plus `NOTARX_PARTITION_AHEAD` (default 3) periods, and drops
partitions entirely older than the table's retention with
`SET LOCAL lock_timeout='2s'; DROP TABLE <partition>` retried with backoff (the
drop takes a brief `ACCESS EXCLUSIVE` lock on the parent; the timeout stops a
lock queue from stalling readers). `DETACH PARTITION ... CONCURRENTLY` is not used:
it cannot run in a transaction block and is refused while the table has a
`DEFAULT` partition. One cluster-wide runner is
chosen by a lease row in `scheduled_tasks` (no session advisory lock). Every
partitioned table also has a `DEFAULT` partition so a manager outage cannot make
inserts fail; `notarx_partition_default_rows` must be 0 and alerts otherwise. A
partition whose range overlaps rows already in the default partition cannot be
created directly, so the manager creates it standalone
(`CREATE TABLE t_pYYYYMM (LIKE t INCLUDING ALL)`), moves the rows
(`WITH moved AS (DELETE FROM t_default WHERE <range> RETURNING *) INSERT INTO t_pYYYYMM SELECT * FROM moved`)
and runs `ATTACH PARTITION` in one transaction; the attach scans the default
partition, which is why it is kept empty and small.

**Partitioned tables (PostgreSQL), keys include the partition key, no foreign
keys point into them:**

| Table | Partition key | Primary key | Default retention |
| --- | --- | --- | --- |
| `resolver_audit_log` | `occurred_at`, monthly | `(occurred_at, id)` | 90 d |
| `domain_events` (ADR 0021) | `occurred_at`, monthly | `(occurred_at, seq)` | 14 d |
| `webhook_events` | removed by ADR 0021 slice 1a | | |
| `artifact_scan_history` (ADR 0030) | scan time, monthly, with a `DEFAULT` partition | `(partition key, id)` | 180 d |

`artifact_vulnerability_finding` (ADR 0030) is not partitioned: its rows are replaced per scan and
swept with the artifact. The final shape of `artifact_sbom` and `artifact_vulnerability` is
owned by ADR 0030 section 2.

`resolver_audit_log` indexes shrink from six to three: the primary key (serves
`ORDER BY occurred_at DESC, id DESC` cursors, replacing `000049`, BRIN `000064` and
`000073`), `(repository, occurred_at DESC)` and `(actor, occurred_at DESC)`; the
`group_name` index (`000002`) is kept only if the group audit view needs it
(slice 5 decides with `EXPLAIN`). Repository identity is added as nullable
`repository_id UUID` beside the historical `repository` name; no foreign key, so
audit survives repository deletion. `domain_events.id` is a UUIDv7 with no unique
constraint; lookups by id derive a one-minute `occurred_at` window from its
timestamp so partition pruning applies. Because a global unique `dedupe_key` is
impossible, emission inside the state-change transaction is exactly-once by
construction, and the reconciler and re-emission paths insert with
`WHERE NOT EXISTS (SELECT 1 FROM domain_events WHERE dedupe_key=$k AND occurred_at >= $retention_floor)`
under `pg_advisory_xact_lock(hashtextextended($k,0))`, backed by a non-unique
`(dedupe_key, occurred_at)` index per partition.

**MySQL (ADR 0022).** Same tables, no partitions: `resolver_audit_log` and
`domain_events` are plain InnoDB tables with `AUTO_INCREMENT` keys, and retention
runs through the RetentionJanitor below as `DELETE ... WHERE id <= ? ORDER BY id LIMIT n`
by primary-key range (never by an unindexed time scan), READ COMMITTED, with
throttling.

**RetentionJanitor.** New `internal/retention`. Each entry is
`{table, time_column, terminal_predicate, key_columns, batch (default 2000), default_keep, freetier_keep}`.
Per batch: PostgreSQL
`DELETE FROM t WHERE ctid = ANY(ARRAY(SELECT ctid FROM t WHERE <pred> AND col < $cutoff ORDER BY col LIMIT $n FOR UPDATE SKIP LOCKED))`,
MySQL `DELETE ... ORDER BY col LIMIT n`. The loop sleeps `max(100ms, 2 x batch time)`
between batches, stops after `NOTARX_RETENTION_RUN_BUDGET` (60s) or an empty batch,
and runs one table at a time per cluster under a `scheduled_tasks` lease. It is
default-on. Keys: `NOTARX_RETENTION_PROFILE=standard|freetier` (the free-tier
reference deployment of ADR 0017 sets `freetier`) and per-table overrides
`NOTARX_RETENTION_<TABLE>=<duration>|off`. The `audit_retention_policy` row
(`000049`) stays as the console-editable override of the audit entry; its baseline
default changes to `enabled=true, keep_days=90`, and the 1000-row cap on
`audit_cleanup_jobs.batch_size` is lifted. Metrics:
`notarx_retention_deleted_rows_total{table}` and `notarx_retention_lag_seconds{table}`.

**Every append-only or churning table and its default retention:**

| Table | Standard | Free tier | Mechanism |
| --- | --- | --- | --- |
| `resolver_audit_log` | 90 d | 7 d | partition drop (MySQL: batched) |
| `repository_request_rollup` | 400 d | 400 d | batched |
| `domain_events` | 14 d | 3 d | partition drop (MySQL: batched) |
| `artifact_scan_history` (ADR 0030) | 180 d | 30 d | partition drop (MySQL: batched) |
| `cdn_purge_requests` (ADR 0029) | 7 d | 3 d | batched delete |
| `hf_refs` (ADR 0027) | `expires_at` + keep (`NOTARX_RETENTION_HF_REFS`) | same | batched; `hf_commits` and `hf_files` are not append-only and are managed by the format's cache GC |
| `webhook_deliveries` | succeeded 14 d, failed/dead 90 d | 3 d / 30 d | batched, terminal rows |
| `lifecycle_jobs` | completed/cancelled 7 d, failed 30 d | 1 d / 7 d | batched, index `(completed_at) WHERE terminal` |
| `promotion_evaluations` | terminal 7 d | 1 d | batched |
| `replication_plans` | terminal 30 d | 7 d | batched |
| `replication_checkpoints` | deleted when the plan completes (keep the high-water mark) | same | on plan completion |
| `cache_tasks`, `audit_cleanup_jobs`, `scheduled_task_runs` | 3 d / 30 d / 30 d | 1 d / 7 d / 7 d | batched |
| `user_sessions` | expired + 7 d | expired + 1 d | batched (loops until drained; today 500 per run) |
| `runtime_node_sessions` | ended + 7 d | 1 d | batched |
| `repository_quota_alert_events`, `email_test_deliveries` | 365 d / 7 d | 30 d / 1 d | batched |
| `nexus_migration_entries` | batch terminal + 30 d | + 7 d | batched |
| idempotency tables (`hosted_repository_idempotency`, `native_*_publish_idempotency`) | `expires_at` + 1 d | same | batched sweep (today only on key collision) |
| import sessions (ADR 0021) | `expires_at` + 24 h | same | batched |
| `workload_jti_seen` (ADR 0019), `saml_assertion_ids`, `saml_request_state` (ADR 0018) | `expires_at` | same | batched; plain tables (a global unique key rules out partitioning) |
| tombstones and soft-deleted rows (`artifact_tombstones`, `native_*_manifest_tombstones`, rows with `collected_at`) | `collected_at` + 30 d | + 7 d | batched hard delete |
| `artifact_promotions`, `artifact_usage_stats`, `artifact_intelligence` | none (provenance and aggregates) | none | not append-only logs; `artifact_intelligence.sboms` and `.vulnerability` stop being written once ADR 0030 slice 2c lands |

### 3. Quota and capacity accounting

New table `repository_usage_counters(repository_id UUID PRIMARY KEY REFERENCES hosted_repositories(id) ON DELETE CASCADE, bytes BIGINT NOT NULL, objects BIGINT NOT NULL, quota_bytes BIGINT NULL, reconciled_at TIMESTAMPTZ, updated_at TIMESTAMPTZ)`
with `fillfactor=70`; `quota_bytes` is written by the quota API in the same
transaction as `repository_capacity_quotas` (the only writer), so the check needs
one row. Publishing code calls `capacity.Reserve(tx, repositoryID, deltaBytes, deltaObjects)`
as the last statement before commit (short lock hold):

```sql
UPDATE repository_usage_counters
   SET bytes = bytes + $2, objects = objects + $3, updated_at = now()
 WHERE repository_id = $1 AND ($2 <= 0 OR quota_bytes IS NULL OR quota_bytes = 0 OR bytes + $2 <= quota_bytes)
```

Zero rows affected on a positive delta means `ErrQuotaExceeded` (same HTTP result
as today). Deltas are computed per format as new minus replaced bytes; deletion and
tombstoning release bytes at the state transition. The `FOR UPDATE` on the quota
row, the `SUM()`, the Maven `EXISTS` path join and all capacity triggers
(`000046`, `000076`, `000080`, `000081`, `000090`, `000110`, `000126`, `000131`)
are dropped; ADR 0022 does not port them. A reconciler (`NOTARX_QUOTA_RECONCILE_INTERVAL`,
default 6h, per repository with jitter, in batches) recomputes usage with the
existing aggregation query, corrects drift in one UPDATE and records
`notarx_quota_drift_bytes`; an alert fires above 0.1 percent. `ListRepositoryCapacityRecords`
and quota alert evaluation (`postgres_quota_evaluation.go`) read counters, so the
overview is O(repositories). Required test: for every format that the dropped
triggers covered, publish, replace, delete and re-publish through the store and
assert `counter == SUM` from the reconciler query, so an uncovered writer fails CI.

### 4. Typed keys and identifiers

- Bind uuid parameters as `uuid.UUID` (pgx) or `$n::uuid`, and scan uuid columns
  into `uuid.UUID`; no `col::text = $n`. A lint test
  (`internal/repository/sqlcast_lint_test.go`, run by `make lint`) scans SQL string
  literals in non-test Go for `::text\s*=\s*\$\d`, `\$\d+::text\s*=` and
  `id::text` in `WHERE`, against a ratchet file `tools/sqllint/allowlist.txt` of
  per-file counts that may only decrease; it reaches an empty allowlist in slice 4.
- New `internal/ids`: `ids.New()` returns a UUIDv7 (`uuid.NewV7`, google/uuid
  v1.6.0 is already a dependency). The lint forbids `uuid.New()` in `internal/`
  outside `internal/ids`. Existing rows are not backfilled; nothing may depend on
  id order. This matches ADR 0022's UUIDv7 choice for MySQL clustered keys.
- Surrogate repository ids: `artifact_usage_stats` is re-keyed to
  `(repository_id UUID, format, resource)` and `repository_request_rollup` uses
  `repository_id`; the per-process repository cache (section 6) resolves
  name to id on write. Slice 9 inventories other tables whose primary key repeats a
  repository name and either re-keys them or records why not.

### 5. Index hygiene

- A schema test (`make test`, integration) fails for any foreign key without an
  index that has its columns as the leading prefix, with a reviewed allowlist.
  Known candidates from the reviews: `native_oci_repository_blobs(digest)`,
  `native_oci_tags(repository_id, digest)`, `native_maven_assets(digest)`,
  `promotion_evaluations(event_seq)` is not needed (no FK).
- Add: `(repository_id, coordinate, expires_at DESC) WHERE state='committed'` on
  `native_maven_publish_sessions` and `native_conan_publish_sessions`;
  `user_group_members(user_id, group_id)`; `promotion_evaluations(next_attempt_at) WHERE state IN ('pending','waiting')`
  and `(repository_id, digest) WHERE state='waiting'`;
  `artifact_promotions(target_repository_id, coordinate, digest)` and
  `(state) WHERE state IN ('pending_approval','queued','running')`;
  `saml_assertion_ids(expires_at)`, `saml_request_state(expires_at)`;
  `lifecycle_jobs(kind, next_attempt_at) WHERE state IN ('pending','retrying')`
  (replaces the shape of `lifecycle_jobs_actionable_idx`).
- Drop: audit indexes per section 2; the `artifact_usage_stats` index that
  duplicates the primary key prefix (`000143`), keeping one index for top downloads.
- pg_trgm GIN indexes (`000064`, `000145`) become partial on live rows
  (`WHERE state='visible'` or the table's equivalent) where such a column exists,
  `fastupdate=on` with `gin_pending_list_limit=1024`, and exist only where
  substring search is a supported feature. Tombstoned rows are purged per section 2.
- Persist `publisher` on `native_maven_*` and `native_conan_*` artifact rows at
  publish time, removing the per-row LATERAL.
- Queue and hot-update tables (`lifecycle_jobs`, `webhook_deliveries`,
  `replication_plans`, `cache_tasks`, `promotion_evaluations`, `artifact_usage_stats`,
  `repository_usage_counters`, `api_keys`, `user_sessions`, `runtime_nodes`) get
  `fillfactor=80` (70 for counters) and `autovacuum_vacuum_scale_factor=0.02`,
  `autovacuum_analyze_scale_factor=0.02` in the migration. A lower `fillfactor`
  only enables HOT updates when no indexed column (including columns used in a
  partial-index predicate) changes: that holds for `repository_usage_counters`,
  `api_keys` (`last_used_at` stays unindexed) and `runtime_nodes` heartbeats, but
  not for state-machine queues (`state` and `next_attempt_at` are indexed) or for
  `artifact_usage_stats.download_count` if the top-downloads index keeps it. There
  the setting only reserves page space for pruning and autovacuum tuning carries
  the load; slice 9 verifies with `pg_stat_user_tables.n_tup_hot_upd` and does not
  promise HOT. Monitor
  `pg_stat_user_tables.n_dead_tup` (slice 17).
- MySQL: where a partial index becomes a generated-column index (ADR 0022), the
  generated column is part of the index specification written here, not a port
  afterthought.

### 6. Read-path caching

New `internal/readcache`: a generic `Cache[K,V]` (bounded LRU, per-entry TTL,
`singleflight` from golang.org/x/sync, already a dependency) with a version stamp.
Cached: repositories by id and name, repository groups with members, grant sets
keyed by `(repository, grants_version)`, repository and group visibility (the access-decision inputs of ADR 0029, replacing the anonymous access policy), user and
service-account state, user group ids (ADR 0018), API-key principals by key hash,
workload issuers, trust policies and JWKS (ADR 0019), signing-key sets (ADR 0020),
quarantine state, and the verified-token result keyed by `sha256(token)` until
`min(exp, TTL)`.

- **Revocation SLO.** Any change to authorisation-relevant state (key revoked,
  user disabled, grant removed, group membership changed, policy disabled,
  quarantine) is effective on every node within `NOTARX_AUTHZ_CACHE_TTL`
  (default 5s, validated range 1s to 30s, or `0` which disables every cache for
  regulated deployments at the cost of the per-request query load described in Context). It is the hard upper bound even when an invalidation is lost. The
  window is a property of the product, not an implementation detail: it is
  documented, shown in the console security settings and covered by the revocation
  test.
- **Invalidation.** Management mutations call `pg_notify('notarx_cache', scope||':'||id)`
  in their transaction (delivered at commit; the notification connection already
  exists, ADR 0022 `Notifier`); the mutating node evicts synchronously. Typically
  below 100 ms on PostgreSQL. A listener that reconnects (or detects a gap) flushes
  every cache before serving again, because notifications sent while it was
  disconnected are lost. A load that started before an eviction must not store its
  result: entries are written only if the scope epoch observed at load start is
  unchanged (an in-process counter bumped per notification on PostgreSQL, the polled epoch on MySQL; otherwise a slow read re-installs stale data for a full TTL). Writes,
  token minting, login and every management-API authorization read through to the
  database and never use these caches. On MySQL there is no push: each node polls a
  `cache_epochs(scope PRIMARY KEY, epoch BIGINT)` table (mutations bump the epoch
  in their own transaction) every
  `NOTARX_CACHE_POLL_INTERVAL` (default 2s, about 10 rows), so the TTL still bounds
  staleness.
- **Keys.** Every key carries the cache name, the entity id (or `sha256` of the
  credential) and the entity's version or the scope epoch: grant sets by
  `(repository_id, grants_version)`, verified-token results by
  `(sha256(token), policy_version, issuer_epoch)`. A hit whose embedded version
  differs from the current one is a miss, so a version bump invalidates dependents
  even if an explicit eviction is missed; no key is derived from request-controlled
  free text.
- **Negative results** (not found, denied) are cached at most 1s, only to absorb
  floods; a denial is never cached longer than the window. Cache hits never bypass
  per-request checks that depend on request data.
- **`last_used_at`.** `FindActiveAPIKeyByHash` becomes a read; `last_used_at` is
  recorded in memory and flushed by the writer of section 1 at most once per
  `NOTARX_API_KEY_LAST_USED_INTERVAL` (default 60s) per key with
  `UPDATE ... WHERE last_used_at < $threshold`. Console "last used" is accurate to
  that interval.
- Metrics: `notarx_cache_hits_total{cache}`, `..._misses_total`, `notarx_cache_evictions_total`.
  Overview aggregates are cached for `NOTARX_OVERVIEW_CACHE_TTL` (default 30s).

### 7. Queue claiming

- **Channels.** `NOTIFY` per kind (`notarx_jobs_<kind>`, `notarx_deliveries`,
  `notarx_evaluations`); a node listens only to kinds its `GATEWAY_NODE_ROLES`
  (renamed `NOTARX_NODE_ROLES`) enable, and runs one claimer per process that hands
  work to workers, instead of about 30 runtimes each polling. The triggers `000066`
  to `000068` are re-pointed; the idle poll stays a fallback (5 s backoff, ADR 0022).
- **Claim.** `SELECT id FROM lifecycle_jobs WHERE state IN ('pending','retrying') AND kind = ANY($1) AND next_attempt_at <= now() ORDER BY next_attempt_at LIMIT $n FOR UPDATE SKIP LOCKED`
  served by the actionable partial index; no `DISTINCT ON`, no global sort.
- **Per-repository concurrency.** Each kind declares `RepoConcurrency` in the job
  registry (default 1, preserving today's behaviour; scan kinds 4). Enforcement:
  after the `LIMIT`, in the same transaction, `pg_advisory_xact_lock(hashtextextended(repository_id::text,0))`
  is taken only for the claimed rows, then a probe `SELECT count(*) FROM lifecycle_jobs WHERE repository_id=$1 AND state='running'`
  served by a partial index `(repository_id) WHERE state='running'` (a set the size of
  the worker count) decides; rows over the limit are released unclaimed. A counter
  row was rejected because crashed leases would drift it; the probe cannot drift.
  MySQL uses the lock-bucket table of ADR 0022 in the same position.
- **Lease recovery** moves from every claim to a scheduler task every 30s
  (`UPDATE ... WHERE state='running' AND lease_expires_at < now()` in batches).
- Ordering inside the claim transaction matters: take the advisory lock, then
  mark the candidate rows `running` (visible to this transaction), then run the
  probe and release the excess by reverting them to `pending`; counting before
  marking would let two claimers each see room. `FOR UPDATE SKIP LOCKED` is safe
  here because the queue is a work list, not a consistent view; the advisory lock
  is taken after `LIMIT`, only on rows already claimed, so it cannot be
  evaluated for rows that are filtered out (unlike the `WHERE`-clause form it
  replaces).
- Risk: a hot repository whose backlog heads the index makes the claim scan past
  rows it cannot take; `notarx_queue_claim_scanned_rows` exposes it and a scan cap
  with kind round-robin bounds it. Slice 17 includes a hot-repository scenario.

### 8. Search and listing

- **Keyset everywhere.** No `OFFSET` and no `COUNT(*)` on tables above 100k rows.
  `QueryArtifactUsage` pages on `(resource, format)`, `ListAuditPage` on
  `(occurred_at, id)` with SQL built from only the supplied filters, a required
  time window, and `(actor, occurred_at)` and `(repository, occurred_at)` indexes,
  SCIM lists on `(created_at, id)` with `startIndex` mapped to an opaque cursor
  where the client allows. Totals become `{value, relation: "eq"|"gte"|"estimate"}`:
  exact below 10001 (`SELECT count(*) FROM (... LIMIT 10001)`), planner estimate or
  the rollup above that. This changes OpenAPI responses (pre-1.0, no deprecation).
- **Search.** `SearchArtifactProjection` pushes the keyset predicate and `LIMIT`
  into each per-format subquery (`ORDER BY coordinate LIMIT n+1`), merges with
  `UNION ALL ... ORDER BY LIMIT`, and joins intelligence data only after the
  limit. The `row_number()` windows go away because "latest per coordinate" is a
  column of the new `artifact_catalog(repository_id, format, coordinate, latest_version, latest_digest, latest_published_at, publisher, version_count, size_bytes)`
  (one row per coordinate), upserted in the publish transaction and recomputed on
  yank or tombstone; the trigram index lives on `artifact_catalog.coordinate`
  (a fraction of the per-file rows). Formats join the catalogue in waves; the view
  stays until the last one. MySQL substring search stays bounded by the row-scan
  cap and catalogue size limit that ADR 0022 slice 10 records; the catalogue makes
  that limit about one row per coordinate rather than per file.

### 9. Large payloads out of the database

Threshold `NOTARX_INLINE_MAX_BYTES` (default 16384). At or below it a payload stays
inline; above it the row stores `body_digest`, `body_size` and `body_key` and the
bytes live in the object store at `meta/sha256/<hex>`, content-addressed, read
through a small in-process LRU. Applies to SBOMs and other `artifact_intelligence`
jsonb (also split into `artifact_sbom` and `artifact_vulnerability` so a CVE
re-scan does not rewrite the SBOM, keyed `(repository_id, digest)`; the final shape, including
`artifact_vulnerability_finding` and `artifact_scan_history`, is owned by ADR 0030 section 2), `nuspec` and
`chart_yaml`, proxy documents (`native_nuget_source_documents`, Helm `body`, Cargo
index bodies, `oci_discovery_snapshots`) and npm version manifests. A mark-and-sweep
job deletes unreferenced `meta/` objects after a grace period. `metadata_json TEXT`
becomes `jsonb` (MySQL `JSON`); object-shape validation moves to Go and the
`jsonb_typeof(metadata_json::jsonb)` and `octet_length(payload::text)` CHECKs are
dropped. Queries select only the columns they need so TOAST values are not
detoasted. On the free tier this also keeps cached proxy bodies out of the 50 GB.

### 10. Connections and locks

- Pools are sized by `NOTARX_NODE_ROLES`: a node with only the API role runs
  `NOTARX_DATABASE_MAX_OPEN_CONNS=24`, coordinator 2, lock 2; a worker-only node
  8, 4, 4; a combined node 32, 8, 4 (today's defaults). The documented budget is
  `sum(nodes x pools) + 20 <= max_connections`; five combined nodes are 230 and
  need `max_connections >= 250` or a pooler.
- Session-level advisory locks held for a whole operation
  (`postgres_advisory_lock.go`, `postgres_raw.go:36`, `cache/coordinator.go`) are
  replaced, for operations longer than about one second, by lease rows
  `operation_leases(key_hash BYTEA PRIMARY KEY, owner TEXT, token BIGINT, expires_at TIMESTAMPTZ)`
  with a heartbeat every `lease/3` and a fencing token checked on commit; short
  critical sections use `pg_advisory_xact_lock` (ADR 0022 lock buckets on MySQL).
  Singleton locks keep the two-int namespaced form; ADR 0021's
  `hashtext('promotion_graph')` uses `hashtextextended`.
- With session locks gone, only `LISTEN` still needs a direct connection. The
  supported pooler topology is PgBouncer in transaction mode for the main pool
  (prepared-statement protocol disabled in pgx, or PgBouncer 1.21+) and a direct
  connection for `LISTEN` and migrations.

### 11. Measurement

- `make perf-seed PROFILE=tiny|freetier|target` runs `tools/datagen`: `tiny` (CI,
  100k versions, 1M audit rows), `freetier` (1M versions, 20M audit rows) and
  `target` (10M versions across all formats, 100M audit rows, 20M webhook
  deliveries) via bulk `COPY`/multi-row inserts with Zipfian popularity so hot
  artifacts exist.
- `make perf-load` extends `scripts/performance-baseline.sh` with a mixed profile
  (95 percent reads, 5 percent publishes, plus a hot-artifact stampede and a
  hot-repository job burst) and writes `docs/perf/<profile>.json`.
- Initial SLOs, ratified by slice 17 measurements: server-side p95 of metadata
  reads at most 25 ms at 1k req/s on `target` (8 vCPU, 32 GB, NVMe) and at most
  50 ms at 100 req/s on `freetier`; small-artifact publish p95 at most 250 ms
  (`target`) and 500 ms (`freetier`); database CPU at most 60 percent at the
  target mix; audit flush lag p99 at most 5 s; zero sequential scans on tables
  above 100k rows for the hot queries.
- `make explain-check` seeds `tiny` plus `ANALYZE` and asserts, for a registry of
  the top 30 queries (`internal/repository/hotqueries.go`, curated from
  `pg_stat_statements` on `target`), that the plan uses the named index and
  contains no `Seq Scan` on listed tables; it runs in `make integration-test`.
- `deploy/observability` ships a `pg_stat_statements` dashboard (top by total
  time, calls, rows per call), dead-tuple, partition and retention-lag panels.
- Capacity guidance (replaced by measured values in slice 17): `target` expects
  about 35 to 60 GB per year of raw audit plus indexes at 100M events, 40 to 80 GB
  for 10M versions of metadata, and a working set of 16 GB or more; `freetier`
  expects a catalogue of 1 to 2M versions, 7-day raw audit (about 5 GB at 10
  req/s) and about 5 GB of free headroom on the 50 GB MySQL volume.

### 12. Edits to the proposed ADR tables

Applied to ADRs 0013, 0018, 0019, 0021 and 0022 in the same change as this
document: `domain_events` key and retention shape, no foreign key from
`promotion_evaluations`, claim and lookup indexes, emit-only-when-consumed
(`events.Emit` consults the cached subscription and rule sets of section 6 and
skips the row when neither a subscription, a rule nor an internal consumer
exists for the type and repository; a rule created later with `backfill: true`
recovers history through the sweep; a subscription created in the last
`NOTARX_AUTHZ_CACHE_TTL` can miss events from that window, which is documented); `user_group_members(user_id, group_id)`, SAML
index and janitor, group-id cache; jti and issuer caching; MySQL notes; and the
0013 ordering row.

## Security considerations

- Dropping async read audit under pressure is an availability-over-completeness
  choice: it never applies to the `sync` class, and drops are counted and alerted.
  Operators needing complete read audit set `NOTARX_AUDIT_READ_MODE=sync`.
- Compliance: the audit guarantees are tiered, not uniform. State changes and
  management mutations are exactly-once and fail closed; authenticated denials are
  durable unless the node is overloaded (counted, alertable, switchable to
  blocking); read and download audit and usage counters are best-effort and lose
  the unflushed window on a crash. This is stated in the product documentation and
  in the audit API (`GET` audit responses carry a `completeness` note), and
  `NOTARX_AUDIT_READ_MODE=sync` is the supported answer for complete read audit.
- Anonymous denial floods cannot amplify into database writes: they use the async
  path and the semaphore bound.
- Caches never extend authority beyond the TTL; denials are never cached beyond
  it; a lost `NOTIFY` degrades to the TTL. Principal caches key on hashes, never
  store raw secrets or tokens, and are process-local.
- Dropping capacity triggers removes a database-level guard; the guard now lives
  in `capacity.Reserve` and is proven by the per-format equality test and the
  reconciler. Writers outside the store layer are not supported.
- Retention default-on deletes data operators may have expected to keep; defaults
  and the `off` switch are in the release notes and the audit-retention API.
- `meta/sha256/*` objects are content-addressed and immutable; reads re-verify the
  digest.

## Consequences

- The download path no longer writes to the database synchronously; one node does
  about two statements per second for audit and usage instead of 2k+. Loss
  window on crash is bounded and documented.
- Retention becomes O(1) per month of audit and bounded for every other table;
  backups shrink and restore time stops growing with history.
- Pre-tag schema churn (partitioned audit, dropped triggers) is absorbed by the
  re-squash; after `v0.1.0` the same changes would need data-moving migrations.
- More moving parts: partition manager, janitor, writer, reconciler, caches. Each
  has metrics and an off switch, and each adds a failure mode that tests cover.
- The MySQL profile is deliberately weaker (no partitions, no trigram search);
  it relies on retention, short raw audit and the catalogue size cap.
- Staleness of up to 5s on revocation is a new, stated property of the product.

## Implementation slices

Every slice is one PR for an autonomous agent and must pass `make test`,
`make lint` (includes `architecture-check`; new packages are registered in the
same slice), `make coverage` at the floors in `scripts/coverage-check.sh` and, when
it touches the database, `make integration-test` and `make migrate`. API changes
edit `api/openapi` first, regenerate the server and client and pass
`make openapi-check`; console-visible changes pass `make console-test`; CLI
mappings are classified in `docs/cli-command-map.md` (`make cli-contract-check`).
MySQL twins are delivered by the ADR 0022 store slices; each slice defines the
dialect-neutral port and the catalogue entry so the start-up gate of ADR 0022 lists
the missing twin. All slices depend on ADR 0015 slice 4 (cut-over). Slices marked
v0.1.0 land after the cut-over and before ADR 0015 slice 7 (re-squash).

1. **Identifiers and typed-key lint.** Milestone: Foundation.
   - Scope: `internal/ids`, `tools/sqllint`, `internal/repository/sqlcast_lint_test.go`, `make lint` wiring, `CONTRIBUTING.md`.
   - [ ] `ids.New()` returns UUIDv7; unit test for version bits and monotonic prefix
   - [ ] lint fails on a new `::text = $n` and on `uuid.New()` outside `internal/ids`, with a per-file ratchet allowlist
   - [ ] `make test`, `make lint`
   - Depends on: ADR 0015 slice 4.
2. **Typed keys, wave A: control plane.** Milestone: Foundation. Two PRs by file group, each lowering the ratchet.
   - 2a scope: `internal/repository` files for lifecycle, sessions, webhooks, scheduled tasks, API keys, users.
   - 2b scope: hosted repositories, grants, capacity (rewritten again by slice 8, so only the casts are fixed here), groups, settings.
   - All new inserts use `ids.New()`.
   - [ ] allowlist counts for the PR's files reach zero
   - [ ] integration test per hot query asserts index use (`EXPLAIN`), starting with `lifecycle_jobs` by repository and `user_sessions` by id
   - [ ] `make test`, `make lint`, `make integration-test`
   - Depends on: slice 1 (2b: 2a not required).
3. **Typed keys, wave B: one PR per format.** Milestone: Foundation. About 600 casts in total across waves A to C, so each PR is bounded by one store family and its ratchet entries (the ratchet is what keeps the waves independent and mergeable in any order).
   - 3a OCI (`postgres_oci.go` and siblings), 3b Maven (`postgres_maven.go`), 3c npm (`postgres_npm.go`), 3d Conan (`postgres_conan_native.go`).
   - [ ] allowlist reaches zero for the PR's files; the format's e2e target (`make native-oci-e2e`-class) passes
   - [ ] `make test`, `make lint`, `make integration-test`
   - Depends on: slice 1.
4. **Typed keys, wave C: remaining formats; lint at zero.** Milestone: Foundation.
   - 4a raw, PyPI, Go, Cargo; 4b APT, Helm, NuGet, RubyGems; 4c Nexus and replication stores, then delete `tools/sqllint/allowlist.txt` (4c lands last). Tables introduced by ADRs 0026 to 0030 (`native_rpm_*`, `hf_*`, `native_raw_versions`, scan tables) are created typed from the start and add nothing to this ratchet; 4b re-keys only the existing APT tables. No new partitioned tables beyond `artifact_scan_history`.
   - [ ] lint passes with no allowlist after 4c; each format's e2e target passes
   - Depends on: slices 1, 2a, 2b, 3a-3d for 4c; slice 1 for 4a and 4b.
5. **Partition manager and partitioned audit table.** Milestone: Foundation.
   - Scope: `internal/partitions`, migration recreating `resolver_audit_log` partitioned (header comment: pre-tag, no data copy) with the three indexes and `repository_id`, default partition, `internal/repository/postgres_audit*.go` (dynamic filters, keyset), `ListAuditPage` index-backed, composition root start-up ordering, metrics, `docs/postgresql-capabilities.md`.
   - [ ] manager creates N future partitions idempotently, drops expired ones, survives two concurrent nodes (lease test), moves default-partition rows
   - [ ] audit cursor and filter queries pass `make explain-check`-style assertions with partition pruning
   - [ ] retention drop test: 13 monthly partitions, expiry drops only old ones
   - [ ] `make test`, `make lint`, `make integration-test`, `make openapi-check` (if filter parameters change)
   - Depends on: slice 1; ADR 0015 slice 4 (cut-over).
6. **Asynchronous audit writer, usage aggregation and rollups.** Milestone: Foundation.
   - Scope: `internal/auditwriter`, `AuditRecord.Durability()`, migration `repository_request_rollup` and `artifact_usage_stats` re-key, `postgres_artifact_usage.go`, `ListRepositoryRequestStatistics`, config keys of section 1, graceful-shutdown hook in `cmd`.
   - [ ] race tests (`make race`) for writer, full-buffer drop with metric, never blocks the request path
   - [ ] mutation audit failure fails the mutation; denial semaphore exhaustion drops with metric unless `NOTARX_AUDIT_DENIAL_MODE=sync`
   - [ ] shutdown flush test; crash-loss bound test; `sync` class never dropped
   - [ ] 1k concurrent downloads of one artifact produce at most one usage UPDATE per interval per node
   - [ ] statistics read from rollups; overview no longer scans audit
   - Depends on: slice 5.
7. **RetentionJanitor and default retention.** Milestone: Foundation.
   - Scope: `internal/retention`, scheduled-task lease, per-table registry per the table in section 2, baseline default for `audit_retention_policy`, lift the 1000 cap, `NOTARX_RETENTION_*` config, docs.
   - [ ] each registry entry has a test deleting only terminal, expired rows in throttled batches and honouring `FOR UPDATE SKIP LOCKED`
   - [ ] free-tier profile values asserted
   - [ ] metrics and one-runner-per-table lease test
   - Depends on: slice 5; ADR 0017 slice 6.
8. **Incremental quota counters.** Milestone: Foundation.
   - Scope: migration for `repository_usage_counters`, drop of the capacity triggers and functions, `internal/capacity` (`Reserve`, reconciler), all format stores, `postgres_capacity.go`, `postgres_quota_evaluation.go`.
   - [ ] per-format equality test (publish, replace, delete, re-publish) against the reconciler query
   - [ ] concurrent publishes at the quota edge reject exactly the overflow; 1M-row repository publish is O(1) (plan assertion)
   - [ ] ADR 0022 slice 7 `AssertRepositoryCapacity` is superseded by `Reserve`
   - Depends on: slices 2a, 2b, 3a-3d, 4a-4c (every store that writes capacity-bearing rows).
9. **Index hygiene, publisher persistence, vacuum settings.** Milestone: Foundation.
   - Scope: migration (adds, drops, partial trigram, `fillfactor`, autovacuum, `publisher` columns), FK-index schema test, `internal/repository/explaincheck` helper.
   - [ ] FK-without-index test passes with a reviewed allowlist
   - [ ] each added index has an `EXPLAIN` assertion; dropped indexes have no remaining dependent query
   - [ ] search of Maven and Conan no longer uses LATERAL for publisher
   - Depends on: slices 5 and 8.
10. **Payload schema shape.** Milestone: Foundation.
    - Scope: migration (`metadata_json` to `jsonb`, CHECKs dropped, `body_digest/body_size/body_key` columns, `artifact_intelligence` split), Go validation of metadata objects.
    - [ ] validation moved to Go with tests for each dropped CHECK
    - [ ] `make test`, `make lint`, `make integration-test`
    - Depends on: slice 4.
11. **Externalise large payloads.** Milestone: Data scale.
    - Scope: `internal/objectstore` meta namespace, writers and readers for the payload kinds of section 9, mark-and-sweep job, `NOTARX_INLINE_MAX_BYTES`.
    - [ ] payload above and below the threshold round-trips on filesystem and S3 drivers (ADR 0017 contract suite)
    - [ ] sweep never deletes referenced objects (race test with concurrent publish)
    - Depends on: slice 10, ADR 0017 slice 1.
12. **Read caches and key-usage throttling.** Milestone: Data scale.
    - Scope: `internal/readcache`, invalidation via `NOTIFY` and `cache_epochs` poll, repository, group, grant, repository and group visibility (the access decision of ADR 0029, invalidated by `repository.visibility_changed` and the scope epoch), principal, quarantine caches, `last_used_at` batching, config keys of section 6.
    - [ ] revocation test: key revoked on node A is rejected on node B within `NOTARX_AUTHZ_CACHE_TTL` with NOTIFY dropped
    - [ ] lost-NOTIFY and listener-reconnect tests (all caches flushed on reconnect); a load racing an eviction does not re-install stale data; `NOTARX_AUTHZ_CACHE_TTL=0` disables caching
   - [ ] a bumped grants or policy version turns a cached entry into a miss
   - [ ] a visibility change (ADR 0029) is effective on every node within `NOTARX_AUTHZ_CACHE_TTL`, and the access-decision cache never serves a stale `public` after a repository or member group is made private
   - [ ] singleflight stampede test; negative cache at most 1s; max entries enforced
    - [ ] query-count test: authenticated read issues at most 1 database statement on a warm cache
    - Depends on: slice 6. ADR 0018 slice 3 and ADR 0019 slice 4 depend on this slice and add their caches through this package; this slice therefore lands before ADR 0018 starts (ADR 0013 ordering table, step 6b).
13. **Queue claiming rework.** Milestone: Data scale.
    - Scope: `postgres_lifecycle.go`, webhook, replication and evaluation claimers, per-kind channels (re-pointing `000066` to `000068` in a numbered migration), registry `RepoConcurrency`, scheduler lease recovery.
    - [ ] claim no longer sorts the pending set (plan assertion); advisory lock only on claimed rows
    - [ ] per-repository limit honoured under 8 concurrent claimers; hot-repository scenario does not starve others
    - [ ] lease recovery runs from the scheduler only
    - Depends on: slices 2a and 9.
14. **Keyset pagination and estimated counts.** Milestone: Data scale.
    - Scope: `api/openapi/management.yaml` (total object), usage, audit, jobs, webhook list endpoints, stores (SCIM lists are keyset from birth in ADR 0018 slice 6, per section 8), console tables, `notarxctl` output.
    - [ ] no `OFFSET` and no unbounded `COUNT(*)` remain on listed tables (lint)
    - [ ] `make openapi-check`, `make console-test`, `make cli-contract-check`
    - Depends on: slices 6 and 9.
15. **Artifact catalogue and search rewrite.** Milestone: Data scale.
    - Scope: migration `artifact_catalog` (post-tag, so a forward migration with an idempotent backfill from the per-format tables, resumable in batches and verified by a count-equality test), publish-transaction upserts per format wave, `postgres_search.go`, retire `artifact_search_projection`.
    - [ ] search latency at `target` meets the SLO (slice 17); plans show per-format LIMIT push-down
    - [ ] yank and tombstone recompute tests per format
    - Depends on: slices 9, 14 and 17 (for measurement).
16. **Pool sizing and lease rows.** Milestone: Data scale.
    - Scope: `internal/config`, `internal/database`, `operation_leases` migration, replacement of session locks in `postgres_advisory_lock.go`, `postgres_raw.go`, `cache/coordinator.go`, `docs/distributed-deployment.md`.
    - [ ] fencing-token test: an expired holder cannot commit; heartbeat loss releases
    - [ ] suite passes through PgBouncer in transaction mode for the main pool
    - Depends on: slice 12; ADR 0022 slice 6 (store group A, so lease rows exist on both engines). Ordered after ADR 0022 slice 6 (ADR 0013 step 12), not before ADR 0018.
17. **Data generator, load harness, EXPLAIN suite, dashboards, capacity guidance.** Milestone: Data scale.
    - Scope: `tools/datagen`, `make perf-seed`, `make perf-load`, `make explain-check`, `internal/repository/hotqueries.go`, `deploy/observability`, `docs/performance-baseline.md`, new capacity document.
    - [ ] `tiny` profile runs in CI; `target` runs on demand and its numbers replace the estimates of section 11
    - [ ] explain registry covers the top 30 queries; CI fails on a Seq Scan regression
    - [ ] SLOs ratified or amended in this ADR
    - Depends on: slices 5 to 9.

## Deferred and out of scope

Findings of the three reviews that are not decided here, with the reason:

- **Parallel and incremental backup (`pg_dump --format=directory --jobs`,
  excluding history tables, WAL archiving).** Owned by ADR 0017 slice 4 (backup on
  both object drivers) and ADR 0022 slice 9; this ADR only shrinks what is dumped
  (retention, partitioned audit that can be archived per partition later). A
  follow-up slice there must add the directory format and an exclusion flag before
  any installation reaches the target size.
- **`TEXT[]` columns and the `api_keys.secret_hash` text key.** Small config
  tables, not a growth risk; classified per column in ADR 0022 slice 3.
- **Archiving `artifact_promotions` and `artifact_intelligence` history.**
  Provenance is kept; revisit with slice 17 measurements.
- **Splitting security audit from read audit into two tables.** See open
  questions.
- **Statement-level quota triggers** were rejected in considered design C.

## Open questions

- Should raw read audit and security audit be split into two tables so security
  events can be retained for years while read events keep 90 days? One partitioned
  table with one retention is simpler; operators needing long security retention
  currently raise the whole retention.
- Is hourly rollup granularity enough for the console (per-minute graphs are not
  provided today), and should denied counts break down by reason?
- Which formats are quota-enforced today beyond Raw, Maven, OCI and Conan (the
  `000046` set), and should quota extend to all formats now that enforcement is
  O(1)? Slice 8 inventories it.
- Is 5s the right default revocation window for regulated deployments? (`0`, no
  cache, is supported; the question is only the default.)
