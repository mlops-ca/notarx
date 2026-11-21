# Promotion rules

[Documentation index](README.md) · [ADR 0021](adr/0021-promotion-rules-and-repository-chains.md) · [Remote instances](remote-instances.md)

A promotion rule says which artifacts of a source repository are promoted, under which conditions, to which target and as
which service account. This page covers the rule resource and its dry run, `/api/v2/promotion-rules`, and the evaluator
that runs enabled rules when artifacts are published (see [Evaluation](#evaluation)). The dry run shows what a rule
would select without enqueueing anything.

## The rule

| Field | Meaning |
| --- | --- |
| `name` | Unique, 1 to 128 characters. |
| `sourceRepositoryId` | The Hosted repository the rule watches. It cannot change after the rule is created. |
| `target` | `{"kind":"local","repositoryId":...}` or `{"kind":"remote","remoteInstanceId":...,"remoteRepository":...}`. A local target is another active Hosted repository of the same format; a remote target names a registered [remote instance](remote-instances.md). |
| `match` | Which artifacts are selected, below. |
| `conditions` | Gates that must all hold, below. |
| `mode` | `copy`: bytes are reused inside an installation and pushed to a remote one. Move and overwrite are not offered; the source is never changed. |
| `runAsServiceAccountId` | The service account the rule runs as. |
| `enabled` | Whether the rule is evaluated. |
| `state` | `active`, or `authorization_lost` when the evaluator parked the rule because its service account could no longer run it (see [Evaluation](#evaluation)). Read-only. |

Every edit increments `version` (the `ETag` and the `If-Match` of an update), records the editor in `updatedBy` and writes
an audit record (`promotion_rule.create`, `promotion_rule.update`, `promotion_rule.delete`) with the rule id, the new
version and the editor. A source repository holds at most `NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY` rules (default 32,
at most 1024); one more is `409 rule_limit_reached`.

### Match

```json
{
  "coordinate": {"glob": "releases/**"},
  "version": {"range": ">=1.2.0 <2.0.0"},
  "refs": ["latest", "v*"],
  "properties": {"channel": "stable"},
  "scope": "coordinate"
}
```

- `coordinate` is required and takes exactly one of `glob` and `regex`, applied to the whole canonical coordinate of the
  format. A glob has two wildcards: `*` matches any run of characters except `/`, `**` matches any run including `/`;
  every other character, `?` and `[` included, matches itself. A regex is Go RE2 (linear time; no look-around and no
  back-references), anchored to the whole coordinate. A pattern is at most 512 bytes and is compiled when the rule is
  created, so a bad one is `400` at once. A coordinate the format refuses as non-canonical is never matched.
- `version.range` is a semantic version range. A version that is not a semantic version (`1.2`, `latest`, `v1.2.3`) never
  matches a range.
- `refs` are globs over the mutable refs that point at the artifact (npm dist-tags; other formats follow as they declare
  tags). A format without tags answers `422 unsupported_match`.
- `properties` maps a property key to a glob over its value and exists only for a versioned generic repository, where
  properties are frozen when a version is sealed, so a rule never matches data that can change after the decision. On any
  other source it is `422 unsupported_match`.
- `scope` is `coordinate` (default) or `release`. A release rule promotes the whole release of a matching coordinate as
  the format defines it, and is available only for formats with the `ReleaseSet` capability; for any other format it is
  `422 unsupported_format`. A format without the promotion capability cannot be a source at all (`422 unsupported_format`).

### Conditions

All members that are set must hold, and an artifact that is quarantined is never promoted.

| Member | Holds when |
| --- | --- |
| `securityPolicy: "pass"` | The target repository's admission policy admits the artifact. The authoritative check runs again inside the promotion job. |
| `scan: {"completed": true, "maxSeverity": "medium"}` | The artifact has a completed scan against a current database and no finding above `maxSeverity`. |
| `minAge: "72h"` | The artifact has been published for at least that long. |
| `requireSignature`, `requireSbom`, `requireProvenance` | The artifact carries that evidence. |

## Who may change a rule

A rule is privilege-bearing: it moves artifacts as its service account. Creating or updating one needs repository
administration on the source and on a local target, and the caller must hold at least the permissions the
`runAsServiceAccountId` holds on both repositories, so a rule cannot do more than its author could. A service account that
holds an operation the caller does not is refused with `403 confused_deputy`. A platform administrator administers every
service account and is exempt; a remote target needs a platform administrator because remote instances are registered and
visible to administrators only. Reading, deleting and dry-running need administration of the source (a dry run also of a
local target). A list shows a caller the rules of the repositories it administers.

## Dry run

`POST /api/v2/promotion-rules/{id}/dry-run` evaluates the rule against the current artifacts of its source and returns each
matched artifact with a verdict per condition. It never enqueues a promotion and writes nothing.

```json
{
  "ruleId": "...", "ruleVersion": "3", "examined": 41, "matched": 2, "truncated": false,
  "items": [{
    "coordinate": "releases/app-1.1.0.tgz", "digest": "sha256:...", "outcome": "blocked",
    "conditions": [
      {"condition": "notQuarantined", "verdict": "pass"},
      {"condition": "securityPolicy", "verdict": "fail", "reason": "signature_required"},
      {"condition": "minAge", "verdict": "pass"}
    ]
  }]
}
```

A verdict is `pass`, `fail` or `not_evaluated` with a reason (a remote target's admission policy belongs to the remote
instance and is `not_evaluated`). The outcome is `blocked` when any condition fails, `undetermined` when none fails but one
was not evaluated, and `eligible` otherwise. `limit` (default 100, at most 500) bounds the returned items; at most 5000
artifacts are examined. `truncated` says whether either bound cut the answer short.

## Evaluation

Publishing an artifact to a source repository writes `artifact.published`. In the transaction of the publication,
`events.Emit` inserts one pending row of `promotion_evaluations` for every enabled rule of that repository (a parked rule
included: its evaluations wait for it) whose `coordinate` and `version` can match. The rule set comes from an in-process cache that every rule change flushes on every
node (`NOTARX_AUTHZ_CACHE_TTL` bounds a lost notification), so a created, enabled, disabled, edited or parked rule starts or
stops fanning out within the TTL; an artifact published in that window on a node whose cache is behind is not evaluated by
a rule created in it; the backfill of a later slice recovers such history. A cache miss reads the rules through the connection of the
publishing transaction. A failure of the fan-out fails the publication, because a failed evaluation insert would break the
chain; a publication that no rule matches does not touch the rules table in its transaction while the cache is warm.

A worker with the `evaluator` kind claims due rows (`FOR UPDATE SKIP LOCKED`, woken by the `notarx_promotion_evaluations`
channel and by a poll), and for each one:

1. reloads the rule and the artifact and checks the rule is still enabled and active (a disabled rule skips the evaluation; a
   parked rule makes it wait like a failing condition, so the artifacts published meanwhile are promoted once an edit
   restores the rule, which makes them due at once; the evaluation that finds the authority lost waits too. They hold
   without polling until a week after publication, when they are dropped; an evaluation that finds the rule active
   again when it records its wait is looked at again at once);
2. re-authorizes the run-as service account: it must exist, be active, be able to read the source and write the target.
   A refusal parks the rule in state `authorization_lost`, announces `promotion.failed` with the rule id and the reason, and
   and the evaluation waits (the park, its event and its audit record happen only if the rule is still active at the version
   the evaluation read, so a rule edited meanwhile is evaluated again instead). Evaluation stops until an edit of the rule
   returns it to `active`. A grant set that cannot be
   read is a transient error that retries the evaluation, never a reason to park; an evaluation that finds the rule edited
   meanwhile is looked at again without using up an attempt;
3. re-checks the match against the artifact as it is now (refs, and for a versioned Raw source the properties frozen at
   seal; a file of a versioned Raw source is evaluated as its version `name@version`, published when it is sealed, and
   waits while the version is open and still being written to; a version idle for a week is dropped);
4. expands `scope: release` through the format's `ReleaseSet` and evaluates the conditions with the dry run's verdicts for
   every artifact the rule promotes (every member of a release). The target's admission policy as it is now is always one of
   them, whether the rule writes `securityPolicy` or not. An
   age condition waits until `published + minAge` (never less than five seconds ahead). Any other failing condition (a
   scan that has not completed or found too much, evidence that is missing, the target's policy, a quarantine) can still
   change, so the evaluation waits and looks again after a quarter of the artifact's age, between one minute and one
   hour, and is dropped with the reason `condition_failed:<condition>:<reason>` a week after the age condition was met (or
   after publication, without one). A release is checked member by member and the first member that fails decides. The re-pend on
   a new scan result, which makes the recheck immediate, belongs to a later slice;
5. admits every member against a target resolved once before queueing any, and calls `PromotionService.Request` for each
   member as the service account (admission is not repeated inside the request). Every file of a release announces itself, so
   a release is evaluated once per file: the evaluation of a file whose own attempt already exists under its key returns it
   without checking the release again. A release of more than 1000 members fails with `release_too_large`, one with
   none is skipped with `release_has_no_members`, and `scope: release` on a format without `ReleaseSet` fails with
   `unsupported_format`. A refusal by quarantine or the target's admission policy skips the artifact with its code (one audit
   record; the admission policy is checked as a condition first, so this is only a race); A member whose digest the target already
   holds (a completed promotion into it that is newer than the target's tombstone; the provenance, not the target's contents, decides) is a no-op; different bytes at its coordinate fail the evaluation with
   `conflict` (targets are immutable; an OCI image name holds many digests, so only the exact manifest decides there) and a log line. Quarantine and the target's admission policy still apply: a refusal skips with its code.

The outcome of a row is `queued` (promotion requested; the `artifact_promotions` row shows its progress), `waiting`,
`skipped` or `failed`, with the reason in `last_reason`. A row that is claimed eight times without an outcome (a worker that dies on it every time) is failed by the claim with
`attempts_exhausted`, and a panic in an evaluation is recorded as a failed attempt like any other error. Delivery is at
least once; the promotion is effectively once because
its provenance idempotency key is `rule:<ruleId>:<sha256(target|coordinate|digest|generation>`, where the generation is the
time the target coordinate was tombstoned, so promoting again after a tombstone mints a new key and nothing else does. A
promotion whose last attempt failed, was rejected, expired or was cancelled is retried by the next request under its key,
which writes attempt N+1 under the same key with its own job; a request whose last attempt is queued, running or completed
returns that attempt. Rows of a worker that died become due again when their two-minute lease ends. Terminal rows are pruned
after 7 days (1 day on the free tier), and a pending or waiting row that was due 30 days ago (7 on the free tier) is dropped too: it
means no node runs the evaluator, so a deployment that sets `NOTARX_WORKER_KINDS` must include `evaluator` on at least one
worker, or set `NOTARX_PROMOTION_ENABLED=false` everywhere. `notarx reconcile-events` fans the events it repairs out to the
rules like a publication does; the age of such an artifact counts from the repaired event. Remote targets are evaluated by the remote push slice; until then an evaluation for a
remote rule is skipped with `remote_target_not_supported`.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `NOTARX_PROMOTION_ENABLED` | `true` | `false` turns the evaluator off: publications write no evaluations and no worker runs |
| `NOTARX_PROMOTION_EVALUATOR_INTERVAL` | `5s` | Poll interval of the evaluator between wake-ups |
| `NOTARX_PROMOTION_EVALUATOR_CONCURRENCY` | `4` | Evaluations one node claims and evaluates at once (1 to 64) |
| `NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY` | `32` | The most promotion rules one source repository can have (1 to 1024) |
