# Distributed Deployment

[简体中文](distributed-deployment.zh-CN.md) · [Documentation index](README.md)

Artifact Gateway stores metadata and durable work in PostgreSQL and Asset bytes
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
