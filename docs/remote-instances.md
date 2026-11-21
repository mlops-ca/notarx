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
  "formats": [],
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
advertises. The session store enforces them; the endpoints that create sessions come with the Raw importer (issue 582), so
until then nothing creates a session:

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
