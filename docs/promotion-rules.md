# Promotion rules

[Documentation index](README.md) · [ADR 0021](adr/0021-promotion-rules-and-repository-chains.md) · [Remote instances](remote-instances.md)

A promotion rule says which artifacts of a source repository are promoted, under which conditions, to which target and as
which service account. This page covers the rule resource and its dry run, `/api/v2/promotion-rules`. The evaluator that
runs rules when artifacts are published arrives in a later slice of ADR 0021; until then a rule is configuration, and
the dry run shows what it would select.

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

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY` | `32` | The most promotion rules one source repository can have (1 to 1024) |
