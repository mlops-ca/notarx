# Webhook Delivery Contract

[简体中文](webhook-delivery-contract.zh-CN.md) | [Documentation index](README.md)

Status: normative contract for durable operational webhook delivery.

## Event Catalog

Every event is a row of the `domain_events` log (ADR 0021), written in the same
database transaction as the state change that causes it, together with one
delivery per matching enabled subscription. No audit-log poller reconstructs
events after the fact. `format` is a payload field: every format emits the same
set of types.

| Type | Emitted when |
| --- | --- |
| `artifact.published` | a publication of a digest becomes visible |
| `artifact.scan.completed` | a scan result replaces the vulnerability summary of a digest |
| `artifact.quarantined` | an Artifact enters quarantine |
| `artifact.released` | an Artifact is released from quarantine |
| `artifact.promoted` | a promotion completes |
| `promotion.pending_approval` | a promotion waits for approval |
| `promotion.approved` | a pending promotion is approved |
| `promotion.rejected` | a pending promotion is rejected |
| `promotion.failed` | a promotion reaches its final failed state |
| `replication.completed` | a replication plan completes |
| `replication.failed` | a replication plan reaches its final failed state |
| `repository.visibility_changed` | a repository changes visibility; payload `{repositoryId, from, to, groups}` |

Subscriptions may list any type of the catalogue before its emitter ships; such a
subscription simply receives nothing yet.

The Cargo-specific names (`cargo.promotion.*`, `cargo.replication.*`) no longer
exist. Today the database emits `artifact.promoted`, `promotion.failed`,
`replication.completed` and `replication.failed` for Cargo with `format`
`cargo`, and `artifact.quarantined` and `artifact.released` for every format;
the remaining types are emitted by the slices of ADR 0021 that own their state
changes, and a subscription may already list them.

Each event carries a stable UUIDv7, the repository id, format, coordinate and
digest when it has them, a type-specific `version` that forms its dedupe key
`type|repository|coordinate|digest|version`, and an immutable payload:

- `version` is the scan result id for `artifact.scan.completed` (so a re-scan
  is a new event), the transition version for quarantine and release, the
  operation id for promotion and replication events (a replication plan adds its
  attempt count, because an explicit replay is a new run), and empty for
  `artifact.published`.
- `artifact.quarantined` and `artifact.released` carry the repository ID and
  format, the canonical coordinate and digest, the quarantine state, reason,
  actor and version.
- `artifact.scan.completed` carries `{repositoryId, format, digest, previous,
  current, newFindings}`. `current` and `previous` (null for the first scan of
  the digest) are summaries `{id, status, critical, high, medium, low, unknown,
  scannedAt}`; `newFindings` counts the distinct unsuppressed vulnerability ids
  of `current` that `previous` did not have.
- Cargo operation events carry `operationId`, `kind` (`promotion` or
  `replication`), `format` (`cargo`), `sourceRepositoryId`,
  `targetRepositoryId`, `coordinate` (`crate@version`), `digest`, final `state`
  and `attempts`. They exclude raw worker error messages and credentials. They are
  emitted only when the operation reaches its final completed or failed state: an
  attempt scheduled for automatic retry does not emit a failure event, a parked
  replication plan emits a failed event, and an explicit later replay can emit a
  later outcome. An expired worker lease that exhausts attempts follows the same
  rule.

The envelope `occurredAt` field records the time of the state transition.

An event is written only when something consumes it: an enabled subscription for
its type, or an internal consumer (the publication scan scheduler reads
`artifact.published`). Otherwise the state change commits without an event row.
Internal consumers read the log with a durable cursor, in order and at least once, one node
at a time; a failed event is retried with an exponential backoff, and one that fails ten times
in a row is logged and skipped. The `occurredAt` of an event is the database time of its
emission.

### Retention

`domain_events` is range-partitioned monthly and a partition is dropped once it
is entirely older than `NOTARX_EVENTS_RETENTION` (default 14 days, 3 on the
free-tier profile). A delivery can be listed and replayed while its event is
kept, so `webhook_deliveries` rows are pruned on the same schedule, and a pending or
retrying delivery whose event is gone is parked as `dead` with the error
`event is no longer retained`; it is no longer listed, and replaying it answers
`409 event_not_retained`. The table
that records promotions (`artifact_promotions`) is the long-lived provenance
record, not the event log.

Delivery is **at least once**. Consumers must deduplicate by event ID. Ordering
between different Artifacts or subscriptions is not guaranteed.

## Subscriptions

Webhook subscriptions are global administrator-managed resources. A
subscription has a unique name, HTTPS endpoint, non-empty event-type filter,
enabled flag, and optimistic-concurrency version. A 32-to-256-byte signing
secret is accepted on write, encrypted with
`NOTARX_SETTINGS_ENCRYPTION_KEY`, and never returned. Responses expose only
`secretConfigured`.

The endpoint must be an HTTPS URL without user information or a fragment.
Production delivery rejects private, loopback, link-local, unspecified, and
multicast destinations and pins the validated address for the connection.
Redirects are never followed. Tests may inject a local TLS client without
weakening production validation.

Disabling a subscription stops creation of new deliveries. Already-created
deliveries remain inspectable and retryable; administrators may explicitly
replay a dead delivery after correcting the endpoint or subscription.

## Envelope And Signature

Webhook requests use `POST application/json` with this envelope:

```json
{
  "id": "event UUID",
  "type": "artifact.quarantined",
  "occurredAt": "RFC3339 timestamp",
  "data": {}
}
```

Requests include:

- `X-Notarx-Event-ID`
- `X-Notarx-Event-Type`
- `X-Notarx-Timestamp` (Unix seconds)
- `X-Notarx-Signature: v1=<hex HMAC-SHA256>`

The signature input is `<timestamp>.<exact request body>` using the decrypted
subscription secret. A 2xx response completes the delivery. Every other
status or transport error is retried with bounded exponential backoff. Each
claim owns a unique fencing token and an unexpired lease so another Worker can
resume after process failure. A Worker claims one delivery immediately before
sending it; higher concurrency is permitted only when every claimed delivery
starts within its lease or the implementation renews leases while queued.
PostgreSQL lease, retry, completion, and replay timestamps use the database
clock so node clock skew cannot extend or prematurely expire a lease.

## Retry And Replay

- Maximum attempts: 8
- Initial retry delay: 5 seconds
- Maximum retry delay: 1 hour
- Default delivery lease: 30 seconds
- Response bodies are ignored and never persisted
- Persisted errors are bounded and must not contain secrets or response bodies
- Attempt 8 transitions the delivery to `dead`
- Explicit replay changes only `dead` deliveries back to `pending`, clears the
  attempt count/error/status, and preserves the event ID

## Management Surface

The management API exposes administrator-only subscription list/create/get/CAS
update and delivery list/get/replay operations. Delivery responses include
event identity, state, attempts, next attempt, HTTP status, bounded error, and
timestamps, but never the signing secret or event payload credentials.

Console Operations renders subscriptions and recent deliveries, including an
explicit replay action for dead deliveries.

Dedicated worker nodes may select only this workload with
`NOTARX_NODE_ROLES=worker NOTARX_WORKER_KINDS=webhook`. Webhook delivery is a
global job kind and ignores `NOTARX_WORKER_FORMATS`.
