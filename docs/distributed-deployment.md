# Distributed Deployment

[简体中文](distributed-deployment.zh-CN.md) · [Documentation index](README.md)

notarx stores metadata and durable work in PostgreSQL and Asset bytes
in shared S3-compatible object storage. Gateway processes keep no artifact state
that another instance must read locally, so the service can scale horizontally.

## Repositories on First Installation

New installations default to `NOTARX_REPOSITORY_PRESETS=standard`. Before serving
requests, the first API or standalone node creates the following catalog in one
PostgreSQL transaction.

| Format | Hosted | Public upstream Proxy | Group entry point |
| --- | --- | --- | --- |
| Maven | `maven-hosted` | `maven-central` → `https://repo.maven.apache.org/maven2` | `maven-all` |
| npm | `npm-hosted` | `npmjs` → `https://registry.npmjs.org` | `npm-all` |
| PyPI | `pypi-hosted` | `pypi-proxy` → `https://pypi.org` | `pypi-all` |
| Go | `go-hosted` | `go-proxy` → `https://proxy.golang.org` | `go-all` |
| Cargo | `cargo-hosted` | `crates-io` → `https://index.crates.io` | `cargo-all` |
| Raw | `raw-hosted` | — | — |
| OCI | `oci-hosted` | — | — |
| Conan | `conan-hosted` | — | — |

This creates 8 Hosted repositories, 5 Proxies, and 5 Groups. Each Group orders its
local Hosted before the upstream Proxy. Authentication remains required and
anonymous reads remain disabled. Grant user and CI identities access through the
existing authorization workflow. Public Proxies use the environment egress policy
and exact download-host allowlists: PyPI includes `files.pythonhosted.org`, Go
includes `storage.googleapis.com`, and Cargo includes `static.crates.io`.
Initialization writes only the database; upstream downloads occur on later client
requests.

These are ordinary native repositories that administrators can modify, authorize,
and delete. `installation_repository_presets` records the one-time outcome.
Concurrent API startup creates one catalog, and an SQL failure rolls back the
entire set. Restarting preserves administrator changes and deletions. Upgrades
with an existing repository or Group catalog skip initialization. Worker-only and
scheduler-only nodes do not initialize repositories. Apply the standard migrations before starting new nodes; the baseline
migration (`000001_baseline.sql`) seeds the pending preset record.

To start empty, set `NOTARX_REPOSITORY_PRESETS=none` on every API/standalone node.
This deletes nothing. A later explicit change to `standard` initializes only an
empty catalog that has never been initialized. NuGet/Helm/RubyGems previews, APT signing,
and registries requiring additional upstream credentials retain their individual
admission requirements.

Public upstream references: [npm Registry](https://docs.npmjs.com/cli/v11/using-npm/registry/),
[PyPI Index API](https://docs.pypi.org/api/index-api/),
[Go modules services](https://proxy.golang.org/), and
[Cargo registries](https://doc.rust-lang.org/cargo/reference/registries.html).

## Node Roles

The same image selects responsibilities with `NOTARX_NODE_ROLES`:

| Role | Responsibility | External routes |
| --- | --- | --- |
| `api` | Protocol handling, management API, public search | Full Gateway API |
| `scheduler` | Periodic scans and durable retention/reclaim/cache/audit task creation | Operational endpoints only |
| `worker` | Claims and executes durable work, replication, and promotion | Operational endpoints only |
| `standalone` | Enables all three roles | Full Gateway API |

An unset value means `standalone`. Do not combine `standalone` with another role.

Workers can narrow their format and job responsibilities:

```env
NOTARX_NODE_ROLES=worker
NOTARX_WORKER_FORMATS=oci
NOTARX_WORKER_KINDS=reclaim,replication
```

`NOTARX_WORKER_FORMATS` accepts `maven`, `oci`, `raw`, `conan`, `npm`, `pypi`,
`go`, and `apt`. APT currently runs only reclaim for management-preview uploads;
this does not make Hosted APT public. Job kinds include `promotion`, `replication`,
`retention`, `reclaim`, `intelligence`, `deletion`, `scan`, `recovery`, `cache`,
`audit`, `webhook`, and `email`.

`intelligence` copies delayed artifact intelligence after promotion. `scan`
starts only when the node has `NOTARX_SCANNER_ENDPOINT`; its independent
`NOTARX_SCANNER_FORMATS` may include Proxy-only Go and allows scanner isolation.
`webhook` claims global deliveries from the PostgreSQL durable outbox and ignores
format filters. Without filters, a worker handles every applicable format and job.
`email` likewise ignores format filters and sends the durable email outbox.
API, Scheduler and email Worker must share the same email configuration and
settings encryption key. See the [opt-in Compose email example](email-notification-delivery.md#opt-in-compose-example);
API email capability is local configuration readiness, not cluster sender health.

Filters constrain Workers only; the Scheduler discovers work for every format.
Cache reclaim is claimed separately for OCI, Raw, and Conan. Maven cache expiry
is evaluated during reads and has no independent reclaim job.

## Deployment Constraints

1. Every node uses the same PostgreSQL database and S3 bucket. The bundled
   baseline uses RustFS.
2. A separate migration job finishes before application replicas start.
   Gateway processes do not race to run migrations.
3. Give each instance a stable `NOTARX_INSTANCE_ID` for logs and diagnosis.
   Every process start also receives a unique session ID, so duplicate instance
   IDs cannot overwrite sessions.
4. Budget primary, cache coordinator, artifact lock, and notification pools per
   replica. The default maximum is `32 + 8 + 4 + 2 = 46` connections per
   instance. Size the three database pool variables against PostgreSQL
   `max_connections`, leaving room for migrations and administration.
5. `LISTEN/NOTIFY` is only a wake hint. Durable tables and leases are authoritative,
   and polling recovers work after lost notifications.

## Recommended Topology

```text
               +-------------------+
clients ------>| api x 2            |
               +---------+---------+
                         |
                 PostgreSQL + S3
                         |
       +-----------------+------------------+
       |                                    |
  scheduler x 1                     workers x N
  all formats                       format-specific
```

API nodes scale behind a load balancer. One Scheduler is normally sufficient.
With multiple replicas, `scheduled_tasks` uses `FOR UPDATE SKIP LOCKED`, advances
`next_run_at` during the claim, and creates downstream idempotency keys from the
schedule and delivery identity. Recovery enqueues one missed delivery rather
than replaying every missed interval. Workers scale by format; claims combine
`FOR UPDATE SKIP LOCKED`, advisory locks, and lease tokens.

Each process writes instance ID, startup session, roles, Worker filters, and
heartbeat to `runtime_node_sessions`, while maintaining the `runtime_nodes`
compatibility projection. Graceful shutdown marks the session `offline`.
Abnormal exits become `stale` after 30 seconds without a heartbeat and `offline`
after two minutes. Offline records are retained for seven days by default:

```env
NOTARX_RUNTIME_NODE_RETENTION=168h
NOTARX_RUNTIME_NODE_PRUNE_INTERVAL=1h
```

Every node starts the partition manager for the audit tables. One node at a time
(elected through a lease row in `scheduled_tasks`) creates the current month and
`NOTARX_PARTITION_AHEAD` future months (default `3`) and drops expired partitions;
see [PostgreSQL capabilities](postgresql-capabilities.md#partitioned-audit-tables).
Alert when `notarx_partition_default_rows` is not zero.

## Quota counters

Quota accounting (ADR 0023 section 3) keeps one row per repository in
`repository_usage_counters` (`bytes`, `objects`, `quota_bytes`). Publishing code
calls `capacity.Reserve` as the last statement of its transaction; a positive
delta that would pass `quota_bytes` is rejected (HTTP 507, as before) and a release
never is. The overview and quota alert evaluation read the counters, so they cost
O(repositories). Every hosted format is covered (the registry in
`internal/capacity/registry.go` lists each format as pending or covered), and
migration 000014 dropped the capacity triggers and their functions: `Reserve` is the only
quota enforcement, and it supersedes the `AssertRepositoryCapacity` of ADR 0022 slice 7. Lowering a
quota below the current usage is rejected by the quota API against the live usage of the format (the counter can drift until the reconciler runs). Maven and Conan
count an asset only while a visible artifact or revision covers it, so their writers bracket
the transaction with a measurement of the touched coordinate or recipe revision under a
per-repository advisory lock and reserve the difference. Helm, NuGet and RubyGems reserve
bytes for pending sessions at reservation time, as their store contracts assert, and
reserve the committed bytes in the counter at commit.

A reconciler recomputes every repository's usage from the asset tables every
`NOTARX_QUOTA_RECONCILE_INTERVAL` (default `6h`, jittered, in batches of 50 with
a pause between them) under the counter row lock and corrects drift in one UPDATE.
Metrics: `notarx_quota_drift_bytes` (absolute drift corrected by the last pass),
`notarx_quota_drift_ratio_max` and `notarx_quota_drift_alert` (1 when a
repository drifted by more than 0.1 percent of its usage; alert on it, a non-zero
value means a writer does not call `Reserve`).

## Retention

Every append-only or churning table is bounded by default (ADR 0023 section 2).
The audit tables are pruned by dropping partitions (above); every other table is
pruned by the retention janitor: one node at a time (a lease row in
`scheduled_tasks`, kind `retention-janitor`) deletes terminal rows older than the
keep time in batches of 2000, using `FOR UPDATE SKIP LOCKED` on PostgreSQL, and
sleeps `max(100ms, 2 x batch time)` between batches. A table's loop stops after
`NOTARX_RETENTION_RUN_BUDGET` (default `60s`) except `user_sessions`, which loops
until drained. Rows still in use (pending, running, retrying, or referenced by a
newer record) and the provenance tables `artifact_promotions`,
`artifact_usage_stats` and `artifact_intelligence` are never deleted.

| Table | Standard | Free tier |
| --- | --- | --- |
| `resolver_audit_log` (partition drop) | 90 d | 7 d |
| `security_audit_log` (partition drop) | 730 d | 90 d |
| `repository_request_rollup` | 400 d | 400 d |
| `webhook_deliveries` | succeeded 14 d, dead 90 d | 3 d, 30 d |
| `lifecycle_jobs` | completed or cancelled 7 d, failed 30 d | 1 d, 7 d |
| `replication_plans` | terminal 30 d | 7 d |
| `replication_checkpoints` | removed 1 h after the plan completed | same |
| `cache_tasks`, `audit_cleanup_jobs`, `scheduled_task_runs` | 3 d, 30 d, 30 d | 1 d, 7 d, 7 d |
| `user_sessions` | expired + 7 d | expired + 1 d |
| `runtime_node_sessions` | stopped + 7 d | stopped + 1 d |
| `repository_quota_alert_events`, `email_test_deliveries` | 365 d, 7 d | 30 d, 1 d |
| `nexus_migration_entries` | batch finished + 30 d | + 7 d |
| idempotency tables | `expires_at` + 1 d | same |
| `workload_jti_seen`, `saml_assertion_ids`, `saml_request_state` | `expires_at` | same |

```env
NOTARX_RETENTION_PROFILE=standard        # or freetier (the ADR 0017 reference stack)
NOTARX_RETENTION_RUN_BUDGET=60s
NOTARX_RETENTION_LIFECYCLE_JOBS=720h     # a table override: Go duration, 30d, or off
NOTARX_RETENTION_SECURITY_AUDIT_LOG=1095d
```

`NOTARX_RETENTION_<TABLE>` (the table name in upper case) overrides the keep time of
every row class of that table, and `NOTARX_RETENTION_<LABEL>` (for example
`NOTARX_RETENTION_WEBHOOK_DELIVERIES_DEAD` or `NOTARX_RETENTION_LIFECYCLE_JOBS_FAILED`) overrides one
class and wins over the table key; `off` disables pruning of the table or class. The security
audit table is independent of the resolver audit table. The console-editable audit
retention policy overrides `resolver_audit_log` once an administrator has edited
it; the baseline policy is now enabled with 90 days, and the 1000 row cap of audit
cleanup jobs is lifted. Tables created by ADRs 0018 to 0021 and 0027
(`promotion_evaluations`, `domain_events`, import sessions, `hf_refs`) join the
registry in the slice that creates them. Tombstone tables are not pruned yet.
Metrics: `notarx_retention_deleted_rows_total{table}` and
`notarx_retention_lag_seconds{table}` (how far past its keep time the oldest eligible
row is; alert when it keeps growing).

## Audit durability and capacity

Audit is written in tiers (ADR 0023 section 1); the guarantees are not uniform.

| Class | Records | Guarantee |
| --- | --- | --- |
| sync | management mutations, publish, delete, quarantine, release, promotion, token mint, settings, retention and key operations | committed before the response; if the audit insert fails the change fails (fail closed); never queued, never dropped; stored in `security_audit_log` (kept 730 days by default) |
| sync, denial | authorization denials for an authenticated principal | committed insert bounded by `NOTARX_AUDIT_SYNC_MAX_INFLIGHT`; when exhausted the record queues for up to 250 ms and is then dropped and counted in `notarx_audit_dropped_total{class="security"}` |
| async | successful reads and downloads, cache hits and misses, group resolution, anonymous 401 and 403 | buffered in the node and flushed in batches; stored in `resolver_audit_log` (kept 90 days by default) |

Read and download audit is therefore best-effort evidence of access, not a
complete log. A crash, `SIGKILL` or node loss loses the records still in the
buffer: about one flush interval at steady state (about 1000 records at 1000
requests per second) and, while the database is down or slow, up to
`NOTARX_AUDIT_BUFFER_SIZE` records (about one minute at 1000 requests per
second), after which new records are dropped. Nothing is spooled to disk. Usage
counters and request rollups are aggregated in memory and flushed every
`NOTARX_USAGE_FLUSH_INTERVAL`, so they are approximate by up to one interval per
node and are not billing-grade. The audit API reports this in the
`completeness` field of `GET /api/v2/audits/page`: `best_effort_reads`, or
`complete` when `NOTARX_AUDIT_READ_MODE=sync`.

```env
NOTARX_AUDIT_BUFFER_SIZE=65536
NOTARX_AUDIT_BATCH_SIZE=1000
NOTARX_AUDIT_FLUSH_INTERVAL=1s
NOTARX_AUDIT_SYNC_MAX_INFLIGHT=64
NOTARX_AUDIT_DENIAL_MODE=queue
NOTARX_AUDIT_READ_MODE=async
NOTARX_USAGE_FLUSH_INTERVAL=5s
NOTARX_SHUTDOWN_FLUSH_TIMEOUT=10s
```

- `NOTARX_AUDIT_DENIAL_MODE=sync` blocks a denied request until its record is
  committed instead of dropping it: every authenticated denial is durable, and
  a flood of denials applies backpressure to the requests.
- `NOTARX_AUDIT_READ_MODE=sync` is the supported answer for installations whose
  compliance regime requires a complete read audit. It costs one database commit
  per request, so the audit insert rate becomes the throughput ceiling of every
  node: size the database for the peak request rate of all nodes combined, keep
  `NOTARX_AUDIT_SYNC_MAX_INFLIGHT` at or below the pool size, and expect read latency
  to include the commit. Usage counters and rollups still flush every interval.
- On `SIGTERM` the node stops accepting audit after the HTTP server has drained and
  flushes within `NOTARX_SHUTDOWN_FLUSH_TIMEOUT`; records still buffered after that
  are lost and logged.
- Alert on `notarx_audit_dropped_total{class="security"}` above zero, on any
  sustained increase of `notarx_audit_dropped_total{class="read"}` and of
  `notarx_audit_flush_errors_total`, and on `notarx_audit_buffer_depth` close to
  `NOTARX_AUDIT_BUFFER_SIZE`. `notarx_audit_flush_seconds` is the flush latency histogram.

Counters for a repository that was deleted or renamed before the flush are discarded. Text with a NUL byte or invalid UTF-8 never drops a record: it is rewritten to U+FFFD before insert in the audit row, the evidence and the usage keys, the record gets the evidence key `notarx_sanitized`, and `notarx_audit_sanitized_total` counts it. Records the database still rejects for another reason are dropped and counted in `notarx_audit_dropped_total{class="invalid"}` so they cannot stall the flush.

Repository request statistics and the overview read hourly rollups
(`repository_request_rollup`, kept 400 days) with the outcome `allowed` or a
denial reason, so they no longer scan the audit tables.

`GET /api/v2/runtime/nodes` returns the node inventory and cluster-health
summary, including missing API/Scheduler/Worker roles, duplicate instance IDs,
and stale heartbeats. This is operational visibility only; task leases remain
the claim authority.

`GET /api/v2/diagnostics` reports build, runtime roles, dependency availability,
node health, and repository queues inside the administrator boundary. It returns
fixed states and sanitized detail, never database/S3 endpoints, environment
variables, tokens, or credentials. Operators can copy it from Console system diagnostics.

Optional explicit directory capacity is described in [operational signal sources](operational-signals.md). It observes the responding API process's mount namespace only; Worker/Scheduler-only processes do not expose diagnostics. The values do not represent S3/NAS physical pools.

## Instance Identity, Public URL and Legacy Names

Settings are read only under the `NOTARX_` prefix. There are no aliases and no
fallback reads of the old upstream names. To stop a mis-named variable from
silently falling back to a default, the `notarx` binary (the server and every subcommand) refuses to start, naming the new
key (`... was renamed to NOTARX_...`), when the process environment contains any
name from the closed list of legacy settings in `internal/config/legacy_env.go`
(derived by `scripts/derive-legacy-env.sh`). Unrelated variables are not matched.
The Kubernetes service-link variables `NOTARX_SERVICE_HOST` and `NOTARX_PORT` are
not settings and are ignored.

| Key | Meaning |
| --- | --- |
| `NOTARX_PUBLIC_URL` | Externally visible base URL of this instance, shared by every feature that must emit absolute URLs. An absolute `https://` URL with no trailing slash, query or fragment; `http://` is accepted only for a loopback host. Empty is allowed: features that need it are disabled or refuse to start. An invalid value stops startup. Code reads it through `config.Config.PublicURL()`. |
| `NOTARX_INSTANCE_ID` | Stable identifier of this node (default: derived from the host name). |
| `NOTARX_SETTINGS_ENCRYPTION_KEY` | Key for encrypted stored settings, read through `internal/secrets` only. |

There is no second public-URL key and no `NOTARX_ENV`.

## Standalone Fallback

Local development and small installations continue to use:

```env
NOTARX_NODE_ROLES=standalone
```

This mode requires no separate queue or service discovery and preserves the
original memory and deployment footprint.
