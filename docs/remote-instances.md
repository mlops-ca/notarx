# Remote instances

[Documentation index](README.md) · [ADR 0021](adr/0021-promotion-rules-and-repository-chains.md)

A remote instance is another notarx deployment that this one can push promoted artifacts to. Platform
administrators register remote instances through `/api/v2/remote-instances`; nobody else can list, read or change
them. This page covers registration, the instance document and the network policy. Promotion rules that target a remote
instance and the push itself arrive in later slices of ADR 0021.

## Registration

`POST /api/v2/remote-instances` takes a name, the base URL of the remote (HTTPS, no credentials, query or fragment) and
an authentication kind:

| authKind | Required | Meaning |
| --- | --- | --- |
| `workload_identity` | `audience` | The sender presents a short-lived workload assertion minted for `audience`. No secret is stored. |
| `static_token` | `token` | A static bearer token, the fallback when the remote cannot trust this instance's issuer. |

A static token is sealed with the settings encryption key (`NOTARX_SETTINGS_ENCRYPTION_KEY`, see
[workload identity](workload-identity.md) for the key ring) and stored as TEXT. It is write-only: responses report
`tokenConfigured` and never return it, and it is never logged, audited or put into an event. Omit `token` on update to
keep the stored one, but only while `baseUrl` and `audience` are unchanged: changing either of a `static_token`
instance needs a new `token` in the same request (`422 token_required`), so a credential sealed for one host is never
sent to another. The key-ring reseal command re-seals the column when the settings encryption key rotates.

`trustEvidence` (default `false`) says whether scan, SBOM and signature evidence shipped by this instance counts toward
the remote's policy. The remote's own policy always has the final word.

Registration fetches `GET {baseUrl}/.well-known/notarx` first and stores the `instanceId` the remote reports. It fails
with `422 remote_check_failed` when the remote is unreachable or answers an invalid document, and with
`422 self_instance` when the remote reports the id of this deployment, so a rule can never push to its own instance.
`POST /api/v2/remote-instances/{id}/test` repeats the check, records the state (`active` or `unreachable`) and
`lastCheckedAt`, and reports the remote's formats and limits. A disabled instance stays `disabled`.

## The instance document

Every notarx instance answers `GET /.well-known/notarx` at the server root, anonymously and read-only:

```json
{
  "instanceId": "0197c0de-0000-7000-8000-000000000001",
  "apiVersion": "1",
  "formats": ["raw"],
  "limits": {"maxBlobBytes": 10737418240, "maxSessionBytes": 53687091200, "maxBlobsPerSession": 2000, "maxOpenSessionsPerPrincipal": 4}
}
```

`instanceId` is a UUID created the first time a node needs it and stored in the database, so every node of a deployment
reports the same id. `formats` lists the formats the instance accepts as import targets. The contract is
`api/openapi/protocols/notarx-instance.yaml`.

## Network policy

Requests to remote instances go through `internal/egress`:

- HTTPS only; the certificate is always verified and there is no option to skip it.
- Redirects are never followed.
- The host is resolved once, every resolved address is checked, and the connection goes to exactly the checked
  addresses. Private, loopback, link-local, carrier-grade NAT and other non-public addresses are refused unless
  `NOTARX_REMOTE_ALLOW_PRIVATE_NETWORKS=true` (default `false`).
- No proxy is used for these requests.

| Variable | Default | Purpose |
| --- | --- | --- |
| `NOTARX_REMOTE_ALLOW_PRIVATE_NETWORKS` | `false` | Let remote instance requests reach private addresses, for deployments whose peers live on a private network |

## Import sessions

A push from another notarx instance is received through an import session (ADR 0021, Remote targets). The `ImportWrite`
permission (`repositories:import`) lets a principal create sessions in a repository; it implies no read access. Each
session records what the sender claims and the blobs it intends to send, under these limits, which the instance document
advertises. The session store enforces them and each limit has its own problem code (below):

| Variable | Default | Purpose |
| --- | --- | --- |
| `NOTARX_IMPORT_SESSION_TTL` | `24h` | Lifetime of a session; the reaper then expires it and deletes its staging objects (at least 1 minute) |
| `NOTARX_IMPORT_MAX_BLOB_BYTES` | `10737418240` | Largest blob of a session |
| `NOTARX_IMPORT_MAX_SESSION_BYTES` | `53687091200` | Most bytes one session claims |
| `NOTARX_IMPORT_MAX_BLOBS_PER_SESSION` | `2000` | Most blobs one session lists |
| `NOTARX_IMPORT_MAX_OPEN_SESSIONS_PER_PRINCIPAL` | `4` | Live sessions one principal holds at once |
| `NOTARX_IMPORT_STAGING_QUOTA_BYTES` | `107374182400` | Bytes claimed by every live session of the instance together |

A request over a limit is refused and writes nothing. Every node runs the reaper once a minute; a finished session row is
pruned by the retention janitor once its staging objects are gone.

## Receiving a push

The receiver exposes three endpoints under `/api/v2`, all needing `ImportWrite` on the target repository (the same
permission `repositories:import`, which implies no read). Only the principal that opened a session may use it; any other
caller is answered `404`. The target must be an active hosted repository whose format has an importer; Raw is the first, and
the instance document lists the formats that accept imports.

1. `POST /repositories/{repositoryId}/imports` with `{coordinate, digest, formatManifest, blobs: [{digest, size}], chainPath,
   idempotencyKey, evidence}` opens a session and answers `201` with the blobs still `missing`. A repeat of the key by the
   same principal for the same format, coordinate, digest, manifest and blobs answers `200` with the same session and checks no limit; any
   other request under the key is `409 idempotency_conflict`. A blob is dropped from `missing` only once this session holds
   its verified bytes: the answer never depends on what other repositories hold or what the caller may read. Limit refusals are
   `413 import_blob_too_large`, `413 import_session_too_large`, `422 import_too_many_blobs`,
   `429 import_too_many_sessions` and `507 import_staging_quota_exceeded`. A `chainPath` longer than
   `NOTARX_PROMOTION_MAX_HOPS` is `422 chain_too_long`, one that already holds this instance and the target repository is
   `422 chain_loop`; every hop needs an `instance` and a `repository` (`422 invalid_chain_path`), the receiver appends its own hop, and a
   missing path is depth 0.
2. `PUT /imports/{importId}/blobs/{sha256}` streams one blob. The server hashes the bytes while it writes them to a staging
   key and refuses a digest or size that differs from the claim (`422 digest_mismatch`, `422 size_mismatch`) before anything is
   visible. Uploading a blob the session already holds answers `204` without reading it. A session that is not open is
   `409 import_session_closed`.
3. `POST /imports/{importId}/commit` re-checks every staged blob, evaluates the target's security policy and quarantine, and
   publishes through the format's importer. The sender's evidence is stored as a claim and never counted: the receiver's policy
   decides (`422 admission_refused`, `422 artifact_quarantined`): a policy that requires a signature, an SBOM, provenance, a
   scan or a license therefore refuses every import until the sender's evidence can count (`trust_evidence`, with the remote
   push sender). A commit is idempotent: a committed session answers `200` again, and an interrupted commit can be sent
   again. A commit the format refuses (`422 import_mismatch`, `422 import_invalid_unit`, `422 unsupported_layout`,
   `409 import_conflict`) aborts the session, so the sender's retry under the same key opens a new one; a failure of storage
   leaves it committing, where the commit can be sent again.

For a flat Raw repository the `formatManifest` is `{"path": "dir/file.bin", "contentType": "..."}`, the unit has exactly one
blob, and its digest is the artifact digest. The importer recomputes the coordinate, size and digest from the bytes it copies;
a coordinate that is not the manifest path, a digest the bytes do not have or a manifest with other fields is rejected
(`422 import_mismatch`, `422 import_invalid_unit`). A name ending in `.sha256` or `.sha512` is the verification sidecar of a path in a flat repository and is refused. A path that already holds the same digest is a replay; another digest is
`409 import_conflict`. A versioned Raw repository does not accept imports (`422 unsupported_layout`).
