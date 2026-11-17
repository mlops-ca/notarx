# notarx as an OIDC Token Issuer

[Documentation index](../README.md)

Status: accepted (2026-11-17)

Management API paths in this ADR are relative to `/api/v2` (the `servers` entry of
`api/openapi/management.yaml`). Root-mounted protocol endpoints (OIDC discovery
and JWKS) are specified in `api/openapi/protocols/oidc-issuer.yaml`.

## Context

Every token the server issues today is a symmetric HMAC string with no public
verification key: the OCI `GET /auth/token` principal token (route at
`internal/app/repository_api.go:462`, `IssuePrincipalToken` in
`internal/authorization/authenticator.go`) and the Conan repository token
(`internal/authorization/conan_token.go`). Nothing in `internal/` or `cmd/`
signs a JWT, and `go.mod` carries no JOSE library. The only OIDC code verifies
one external issuer for human SSO (`internal/authorization/oidc.go`, RS256
only, singleton `oidc_settings` row from `migrations/000074_oidc_settings.sql`,
issuer env var `GATEWAY_OIDC_ISSUER` in `internal/config/config.go`). That
variable names an *external* IdP and must not be reused for "who we are".

Consequences for cloud integration:

- Microsoft Entra ID can exchange an externally issued JWT for an Entra token
  (workload identity federation, a "federated identity credential" on an app
  registration). The external party must be an OIDC issuer: public HTTPS
  discovery document, public JWKS, RS256-signed assertion (Microsoft Learn
  documents RS256 issuers only; other algorithms "may work" but are untested).
  notarx cannot be that party today.
- Upstream proxy credentials (`internal/repository/model.go:131-175`,
  `internal/upstreamauth/credentials.go`) are static Basic or Bearer secrets
  sealed with AES-256-GCM via `internal/secrets` (`secrets.Seal(purpose,
  plaintext)`, key from `GATEWAY_SETTINGS_ENCRYPTION_KEY`, to become
  `NOTARX_SETTINGS_ENCRYPTION_KEY` under ADR 0013). Proxying ACR, Azure
  Artifacts or Blob Storage therefore needs a long-lived client secret.
- ADR 0021 needs instance A to authenticate to instance B for remote
  promotion without a shared static secret. ADR 0019 already defines trusted
  issuers and trust policies on the *receiving* side; the *sending* side needs a
  verifiable token source.
- The distributed deployment (`docs/distributed-deployment.md`) runs several
  stateless nodes. Signing keys cannot live on one node's disk the way the APT
  OpenPGP key does (`internal/refaptsigner/key.go`); PostgreSQL is the shared
  control plane and already uses advisory locks for cross-node coordination
  (for example `internal/repository/postgres_raw.go`, `hashtextextended`).
- Service accounts (`migrations/000106_service_accounts.sql`, folded into the
  baseline by ADR 0015) are the existing non-human principal, and repository
  grants key on `service-account:<id>`. Federated principals from ADR 0019
  resolve to the same actor shape.

Release scope: ADR 0020 ships in 0.2.x or later (v0.1.0 is ADRs 0013-0017, see
the ordering table in ADR 0013). All schema in this ADR therefore lands as normal
numbered migrations after the ADR 0015 cut-over, never as baseline edits.

## Considered designs

1. **Static client secret for Entra.** Store a client secret or certificate in
   the upstream credential. Zero new infrastructure, but it is the status quo:
   secrets expire (at most 24 months in Entra), need manual rotation, and sit in
   the database. Does nothing for notarx-to-notarx trust.
2. **Delegate issuance to an external service** (SPIRE, cert-manager
   csi-driver, a cloud KMS-backed signer). Strong key custody, but adds a
   mandatory component to a product whose goal is a single binary plus
   PostgreSQL (and a single-node filesystem mode, ADR 0017). It also still needs
   a public JWKS endpoint, which must be served by notarx or a sidecar.
3. **notarx is itself an OIDC issuer, keys in PostgreSQL.** Discovery and JWKS
   served by the server, private keys sealed with the existing secrets
   mechanism, rotation coordinated through the database. Works on the Oracle
   Always Free reference deployment (ADR 0017) with no extra services. Cost: we
   own a key lifecycle and a signing code path, and the sealing key
   (`NOTARX_SETTINGS_ENCRYPTION_KEY`) becomes a root of trust.
4. **Option 3 with a pluggable signer interface**, PostgreSQL-sealed keys being
   the first implementation and external KMS (AWS KMS, Azure Key Vault, OCI
   Vault) later. Same as 3 but the interface costs little now and avoids a
   rewrite.

## Decision

Option 4: implement option 3 behind a `Signer` port. Only the PostgreSQL-sealed
signer ships initially.

### Package split

JOSE primitives (JWT verify and sign helpers, JWK/JWKS encoding, RFC 7638
thumbprints) live in `internal/jose` (go-jose/go-jose v4, at least v4.1.9),
created by ADR 0019 slice 1. `internal/tokenissuer` contains only the `Signer`
port, claim building and the key lifecycle. Sealing uses `internal/secrets`
with the key-ring (key ids, rotation, reseal) owned by ADR 0018's first slice.
Sealed values are stored as TEXT.

### Issuer identity

`NOTARX_ISSUER_URL` defaults to `NOTARX_PUBLIC_URL` (owned by ADR 0013's env
table); set it only to override. Startup validation requires `https`, no query
or fragment, no trailing slash (normalised), and rejects an empty result while
issuance is enabled. Plain `http` is accepted only for loopback hosts
(`localhost`, `127.0.0.1`, `::1`) and logs a warning; there is no separate
environment switch. Startup also rejects an issuer URL equal to an issuer
registered under ADR 0019 unless self-trust is explicitly configured. The issuer
string in tokens and discovery is exactly this value; it is deliberately distinct
from any external IdP setting of ADR 0018. `NOTARX_ISSUER_ENABLED` (default
`false`) gates the endpoints, the minting API and the rotation worker, so
air-gapped installs expose nothing.

A stable deployment id (random UUID) is created in `settings` at first
bootstrap and shared by all nodes. It is used for the built-in instance subject
(below). The per-node `GATEWAY_INSTANCE_ID` (default `gateway-<hostname>`,
`internal/config/config.go:172`) is not used, because it differs per node and
would require one federated credential per node.

### Signing key lifecycle

New numbered migration (after the ADR 0015 cut-over):

```
token_signing_keys(
  kid            text primary key,        -- RFC 7638 thumbprint of the public JWK
  alg            text not null check (alg in ('RS256','ES256')),
  state          text not null check (state in ('next','active','retired')),
  public_jwk     jsonb not null,
  private_sealed text not null,           -- secrets.Seal("token-signing-key:"||kid, PKCS#8 DER base64)
  sealing_ref    text not null default 'local',  -- 'local' now; KMS key id later
  integrity_mac  text not null,           -- see below
  created_at     timestamptz not null,
  published_at   timestamptz,             -- first time visible in JWKS
  activated_at   timestamptz,
  retired_at     timestamptz,
  expires_at     timestamptz              -- removal from JWKS; null until retired
)
-- partial unique indexes: at most one 'active' and at most one 'next' row per alg
```

- Default algorithm `RS256`. Entra federated assertions are RS256 only per
  Microsoft Learn; `ES256` is used only for notarx-to-notarx and generic
  consumers. The `entra-workload` scheme refuses to sign unless the active key
  for the requested algorithm is RS256. `NOTARX_ISSUER_ALG` selects the
  signing algorithm for new tokens (default `RS256`); keys of both algorithms may
  be published, each algorithm having its own `next`/`active` pair and
  independent rotation. Switching `NOTARX_ISSUER_ALG` only changes which active
  key signs; a missing `next` or `active` for the new algorithm is created by
  the bootstrap path below. RSA is 3072-bit, ECDSA P-256.
- States: `next` (published in JWKS, never signs), `active` (signs and is
  published), `retired` (published, never signs, until `expires_at`).
- Rotation interval `NOTARX_ISSUER_KEY_ROTATION_INTERVAL` (default `720h`).
  **Publish before use:** a `next` key is generated at
  `rotation_interval - publish_lead` and promoted to `active` only after it has
  been in JWKS for at least `NOTARX_ISSUER_KEY_PUBLISH_LEAD` (default `24h`).
  Microsoft documents no JWKS cache TTL for federated credentials (only
  region-wide propagation delay, AADSTS70021), so 24 h is a heuristic that must
  exceed the JWKS `Cache-Control` max-age; the operator guide tells operators to
  wait for propagation before relying on a new key.
- **Retention:** a retired key stays in JWKS until
  `retired_at + max_token_lifetime + publish_lead`, i.e. strictly longer than
  any token it signed can live. Only then is the row deleted by the worker.
- Bootstrap: with no `active` key for an algorithm the first node creates one,
  activates it immediately (safe only because no token of that algorithm has
  ever been issued) and creates a `next`.
- **Distributed safety:** the rotation worker runs on every node but acts only
  while holding `pg_try_advisory_xact_lock(hashtextextended('notarx:issuer-rotation', 0))`
  (the same function `postgres_raw.go` uses) inside one transaction that
  re-reads all key rows `FOR UPDATE`. All transitions are idempotent and
  time-based, so a crashed or duplicated worker converges. Nodes cache the active
  key for at most 30 s; because a key is published for the full lead time before
  activation, a node with a stale cache still signs with a key that every
  verifier already trusts. Manual `rotate-now` demotes active to retired and
  promotes `next` but keeps the lead as a warning only; emergency `revoke`
  (compromise) sets `expires_at = now()`, removes the key from JWKS and fails
  tokens signed by it at the notarx-to-notarx verifier side, with the documented
  consequence that Entra-issued tokens already exchanged stay valid until their
  own expiry.
- **Row integrity.** `integrity_mac = HMAC(derived(NOTARX_SETTINGS_ENCRYPTION_KEY),
  kid || alg || state || public_jwk)`, with the derivation using the current
  key-ring key id. The JWKS handler and the signer verify it; rows that fail are
  omitted from JWKS, never sign, and raise an alert. `private_sealed` binds
  `kid` through the `Seal` purpose string. This stops an attacker with database
  write access alone from inserting their own public key as `active`.
- Private keys are decrypted only in the signer, never logged, never returned
  by any API, and appear in backups only as ciphertext (the backup docs must
  state that restoring needs the same key-ring). Changing the encryption key
  without re-sealing makes signing fail closed; the key-ring reseal of ADR 0018
  covers these rows, and `notarxctl issuer reseal` triggers it for this table.
- `Signer` port: `Sign(ctx, claims) (compact string, kid string, error)` and
  `PublicKeys(ctx) []JWK`. A `kms` implementation (`sealing_ref` holding the key
  id, `private_sealed` empty) is a later slice.

### Public endpoints

Both unauthenticated, GET-only, mounted at the server root next to `GET /livez`
and described in `api/openapi/protocols/oidc-issuer.yaml` (not in the
`/api/v2` management contract):

- `GET /.well-known/openid-configuration` returns `issuer`, `jwks_uri`
  (`<issuer>/.well-known/jwks.json`), `id_token_signing_alg_values_supported`
  (`["RS256","ES256"]`), `response_types_supported: ["id_token"]`,
  `subject_types_supported: ["public"]`, `claims_supported`. No
  `authorization_endpoint` or `token_endpoint`; notarx is a token source, not a
  browser IdP.
- `GET /.well-known/jwks.json` returns the public JWKs of `next`, `active` and
  unexpired `retired` keys that pass the integrity check (`kty`, `kid`,
  `use: sig`, `alg`, `n/e` or `crv/x/y`).
- Headers: `Cache-Control: public, max-age=300`, strong `ETag`, `Content-Type:
  application/json`, `Access-Control-Allow-Origin: *`. Both are served from an
  in-process cache invalidated on key change so anonymous load never reaches
  PostgreSQL per request, and are rate limited per IP like other anonymous
  routes.

### Token minting API

`POST /service-accounts/{id}/assertions` (OpenAPI-first, tag
`service-accounts`, relative to `/api/v2`), body
`{ "audience": "api://AzureADTokenExchange", "lifetimeSeconds": 300 }`. This is
outbound minting. It is distinct from ADR 0019's inbound workload token exchange,
`POST /auth/token-exchange` (RFC 8693); the OCI `/auth/token` route is not
touched. Authenticated callers: the service account itself via API key, or a
federated principal of ADR 0019 resolved to that service account (the latter
means a `workload_federation` principal may mint assertions, which ADR 0019's
blast-radius section must state). Human users and static admin tokens are refused
(403) so human sessions cannot be laundered into workload assertions.

Per service account, a new table (numbered migration) controls what may be
requested:

```
service_account_assertion_audiences(
  service_account_id uuid references service_accounts(id) on delete cascade,
  audience text not null,
  max_lifetime_seconds int not null default 600 check (max_lifetime_seconds between 60 and 600),
  primary key (service_account_id, audience))
```

managed through `/service-accounts/{id}/assertion-audiences`. A requested
audience absent from the allow-list returns 403 with a stable problem code
`assertion_audience_not_allowed`; there is no wildcard and no default
allow-list, so enabling issuance for a service account is an explicit act
recorded in the audit log, as are all mints (service account, audience, `jti`,
kid, credential id; never the token). A global ceiling
`NOTARX_ISSUER_MAX_TOKEN_LIFETIME` (default `10m`, hard maximum `10m`) bounds
every request; default lifetime is 5 minutes.

Claims:

| claim | value |
|---|---|
| `iss` | `NOTARX_ISSUER_URL` |
| `sub` | the account's stored `assertion_subject` (default `notarx:service-account:<name>`) |
| `aud` | requested audience, a single string |
| `iat`, `nbf` | now (nbf = iat) |
| `exp` | now + lifetime, at most 10 minutes |
| `jti` | random 128-bit, recorded in the audit event |
| `notarx_sa` | service account UUID (informational) |
| `notarx_cred` | credential kind: `api_key` or `workload_federation` |

JOSE header: `alg`, `kid`, `typ: JWT`. No email, groups or roles claims, to
keep assertions small and non-escalating.

**Subject stability.** Entra matches `iss`, `sub` and `aud` by exact string and
the federated credential is created by hand, so the subject must be predictable
and never change silently. The subject is stored when the service account is
created (`service_accounts.assertion_subject`, default
`notarx:service-account:<name>`) and is immutable afterwards; renaming a service
account does not change it. Characters outside `[A-Za-z0-9._:-]` in the name are
rejected for issuer-enabled accounts. A custom subject may be set at creation
for migrations of existing Entra credentials. Subjects are reserved permanently
in `service_account_assertion_subjects(subject primary key, service_account_id
uuid null references service_accounts(id) on delete set null)`: deleting an
issuer-enabled account keeps its row, so a later account with the same name
cannot reuse the subject and inherit the Entra trust (an explicit admin release
is required).

### Internal consumer: `entra-workload` upstream scheme

Add `UpstreamAuthSchemeEntraWorkload = "entra-workload"` to
`internal/repository/model.go` and a branch in `upstreamauth.Apply`
(`internal/upstreamauth/credentials.go:39`). Its non-secret configuration is
stored in the upstream auth object (extended JSON, no `Secret`):
`tenantId`, `clientId`, `scope` (for example
`https://storage.azure.com/.default`), optional `subject` (a service account
whose subject to use; default a built-in instance principal
`notarx:instance:<deployment-id>`, the stable id described above). Algorithm:

1. Self-sign an assertion in-process (no HTTP hop, no API key) with the
   audience derived from the authority host: `api://AzureADTokenExchange`
   (global), `api://AzureADTokenExchangeUSGov` (US Government) or
   `api://AzureADTokenExchangeChina` (China), overridable by
   `NOTARX_ENTRA_AUDIENCE`; lifetime 5 min, RS256 key only.
2. POST `https://login.microsoftonline.com/<tenant>/oauth2/v2.0/token` with
   `grant_type=client_credentials`, `client_id`, `scope`,
   `client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer`,
   `client_assertion=<assertion>`. Sovereign clouds via
   `NOTARX_ENTRA_AUTHORITY_HOST` (default `https://login.microsoftonline.com`).
3. Cache the access token per (tenant, client, scope) until `exp - 5 min`, with
   singleflight so concurrent proxy requests share one exchange; on 401 from
   the upstream, drop the cache entry once and retry once.
4. Set `Authorization: Bearer <token>`. Azure Blob and Azure Artifacts accept
   this directly. ACR additionally needs the AAD token exchanged at
   `/oauth2/exchange` for an ACR refresh token and then an access token; that
   registry-specific step lives in a separate slice and reuses the same cache.

The scheme requires `NOTARX_ISSUER_ENABLED=true` and is rejected by
`UpstreamAuth.Validate` otherwise. The SSRF and same-origin protections around
`Apply` (`SameOrigin`) remain: the token host is a fixed allow-list derived
from the authority host, never user-supplied.

### Entra setup (operator procedure, to be published in `docs/`)

1. Publish notarx at a public HTTPS `NOTARX_ISSUER_URL`; set
   `NOTARX_ISSUER_ENABLED=true`; verify
   `curl -s $NOTARX_ISSUER_URL/.well-known/openid-configuration` and the JWKS.
2. Create a service account `acr-puller` (or use the instance principal) and
   allow the audience for it; note its subject
   `notarx:service-account:acr-puller` (`notarxctl service-account show`).
3. In Entra, create an App registration (single tenant). Record the tenant id
   and application (client) id. No client secret.
4. App registration, Certificates and secrets, Federated credentials, Add
   credential, scenario "Other issuer": **Issuer** = `NOTARX_ISSUER_URL`
   exactly; **Subject identifier** = `notarx:service-account:acr-puller`
   exactly; **Audience** = `api://AzureADTokenExchange` (default; national
   clouds use the variants above); name it. Entra fetches discovery and JWKS from
   that issuer on exchange, so the URL must be reachable from the internet (no
   private DNS, valid certificate).
5. Grant the app's service principal the data-plane role it needs (for example
   Storage Blob Data Reader on the account, AcrPull on the registry).
6. Configure the notarx proxy repository with scheme `entra-workload`, tenant
   id, client id, scope. Verify with `notarxctl repository upstream test`.
7. Rotation needs no Entra change beyond waiting for key propagation. A *changed
   issuer URL or subject* requires a new federated credential (an app has at
   most 20).

### notarx-to-notarx trust

Instance B (promotion target in ADR 0021) registers instance A as a trusted
issuer under ADR 0019 (`issuer = A's NOTARX_ISSUER_URL`, discovery enabled,
allowed audience = B's own issuer URL, defaulting to `NOTARX_PUBLIC_URL`) and a
trust policy mapping subject `notarx:service-account:promote-to-b` to a service
account on B with the promote grants. Instance A mints an assertion with
`aud=<B issuer URL>` through the internal signer (ES256 or RS256) and presents it
as a Bearer JWT to B, which verifies it directly through the ADR 0019
trusted-issuer path; no exchange step is required (the ADR 0019 exchange is
optional). The sender-side peer client is owned by ADR 0021; ADR 0020 ships no
peer client. The audience allow-list on A names B's URL, so a service account on
A cannot obtain assertions for arbitrary peers. No shared secret exists between
instances.

## Security considerations

- Root of trust: `NOTARX_SETTINGS_ENCRYPTION_KEY` plus database read access
  yields the private signing keys and therefore any federated identity; both
  are treated as signing-key-equivalent in the operations docs. Database
  *write* access alone must not suffice to inject a trusted key, which the
  `integrity_mac` on each row prevents (a writer cannot forge it without the
  encryption key). KMS signing removes the residual exposure and is the
  recommended production hardening.
- Confused deputy: audience allow-lists are per service account and exact; the
  issuer never signs for a caller-chosen `sub`. Tokens are short lived and
  non-renewable; there is no refresh. A `workload_federation` principal can mint
  only for the service account it resolves to, and only for allow-listed audiences.
- Entra federated credentials trust *every* token with a matching `iss`/`sub`/
  `aud`; operators must scope Azure roles minimally and treat the subject as a
  secret-adjacent identifier. Subjects are never reused after deletion.
- The discovery document is the only place the issuer string is announced;
  a changed `NOTARX_ISSUER_URL` invalidates all outstanding trust (documented,
  guarded by a startup log warning when it differs from the last used value
  stored in `settings`).
- JWKS endpoint is anonymous: no key metadata beyond the standard JWK, bounded
  response size, rate limited. `jti` is logged only in audit events.
- Clock: tokens carry `nbf`/`iat`; hosts must run NTP, verifiers apply 60 s
  skew (ADR 0019).

## Consequences

- No static Azure secrets for proxy repositories; credentials expire in
  minutes and rotation is automatic.
- notarx instances can trust each other with no shared secret, enabling ADR 0021
  remote promotion.
- A new signing code path and key table to secure, test and document; one more
  public anonymous surface. JOSE comes from `internal/jose` (ADR 0019 slice 1)
  rather than extending the hand-written parser in `oidc.go`.
- Schema (`token_signing_keys`, `service_account_assertion_audiences`,
  `service_account_assertion_subjects`, `service_accounts.assertion_subject`,
  the deployment id setting) arrives as additive numbered migrations in 0.2.x+;
  the baseline is not edited.
- Changing the issuer URL is disruptive by design.

## Implementation slices

Global ordering is in ADR 0013 (ADR 0020 follows 0019 slice 1, 0018 and the rest
of 0019, and precedes 0021; slice 9 trails ADR 0021 slice 8).

1. **Signer port and key generation.** Scope: `internal/tokenissuer` (new:
   `Signer`, key generation, claim builder, `integrity_mac`) using
   `internal/jose` and `internal/secrets`. Acceptance: [ ] RS256 and ES256
   sign/verify round trips; [ ] JWK thumbprint `kid`; [ ] private key sealed with
   purpose `token-signing-key:<kid>`; [ ] tampered `integrity_mac` or `public_jwk`
   is rejected; [ ] claim builder rejects lifetime above 10 min; [ ] `make
   test`, `make lint`, `make architecture-check` pass; coverage floor met.
   Depends on ADR 0019 slice 1 (jose), ADR 0018 slice 1 (secrets key-ring),
   ADR 0015 slice 4 (cut-over).
2. **Key store and rotation worker.** Scope: new migration
   (`token_signing_keys`, deployment id setting),
   `internal/repository/postgres_signing_keys.go`, worker in `internal/app`,
   config keys. Acceptance: [ ] partial unique indexes enforce one active and one
   next key per alg; [ ] integration test with two concurrent workers (advisory
   lock) converges; [ ] publish-before-use, retention, per-alg bootstrap and
   algorithm switch tested with a fake clock; [ ] stale-cache node still signs
   with a published key; [ ] `make test`, `make lint` pass. Depends on 1.
3. **Discovery and JWKS endpoints.** Scope:
   `api/openapi/protocols/oidc-issuer.yaml`, handlers in `internal/app`, root
   mounts next to `/livez` in `repository_api.go`. Acceptance: [ ] both routes
   anonymous with cache and ETag headers; [ ] 404 when
   `NOTARX_ISSUER_ENABLED=false`; [ ] rows failing `integrity_mac` are omitted;
   [ ] startup validation of `NOTARX_ISSUER_URL` (default from
   `NOTARX_PUBLIC_URL`, loopback-only http); [ ] `make openapi-check`, `make
   test`, `make lint` pass. Depends on 2.
4. **Assertion minting API and allow-lists.** Scope: `api/openapi/management.yaml`
   (`POST /service-accounts/{id}/assertions`,
   `/service-accounts/{id}/assertion-audiences`), new migration
   (`service_account_assertion_audiences`, `service_account_assertion_subjects`,
   `service_accounts.assertion_subject`), audit events, `AuthenticationKind`
   check refusing human principals. Acceptance: [ ] 403 for unlisted audience,
   humans and admin token; [ ] immutable subject and permanent subject
   reservation after delete; [ ] audit event per mint without token; [ ] `make
   openapi-check`, `make test`, `make lint` pass. Depends on 2, ADR 0019
   slice 4 (federated principal), ADR 0006.
5. **CLI.** Scope: `notarxctl issuer status|rotate|revoke|reseal`,
   `notarxctl service-account assertion-audience ...`, CLI command-map doc.
   Acceptance: [ ] command map updated; [ ] `make test`, `make lint` pass.
   Depends on 3, 4.
6. **Console and docs.** Scope: console page under service-account detail, i18n
   keys en-CA and fr-CA (ADR 0014), issuer and Entra setup guide in `docs/`.
   Acceptance: [ ] `make console-test`, `make test`, `make lint` pass. Depends
   on 5.
7. **`entra-workload` upstream scheme.** Scope:
   `internal/repository/model.go`, `internal/upstreamauth`, management OpenAPI
   for upstream auth, preflight check. Acceptance: [ ] token exchange against an
   `httptest` Entra stub verifies assertion claims and form fields; [ ]
   audience derived per authority host; [ ] refuses non-RS256 signing; [ ] cache,
   singleflight and 401-retry-once; [ ] fixed host allow-list; [ ] `make
   openapi-check`, `make test`, `make lint` pass. Depends on 2, 3.
8. **ACR token exchange.** Scope: `internal/upstreamauth` ACR branch.
   Acceptance: [ ] AAD to ACR refresh to access token chain tested against a
   stub; [ ] cache reuse. Depends on 7.
9. **notarx-to-notarx docs and two-instance e2e.** Scope: docs, e2e test only
   (no peer client; the sender is owned by ADR 0021). Acceptance: [ ] A mints, B
   verifies as a Bearer JWT via ADR 0019; [ ] unlisted peer audience refused.
   Depends on 4, ADR 0021 slice 8 (remote push sender).
10. **KMS signer (deferred).** Scope: `kms` `Signer` implementations. Not
    scheduled.

## Open questions

- Should the instance principal (`notarx:instance:<deployment-id>`) exist for
  `entra-workload` without a service account, or must every use bind to an
  explicit service account for audit clarity? Draft leans to allowing both.
- Is 3072-bit RSA acceptable on the Always Free Ampere A1 (key generation is
  seconds, signing is cheap)? RS256 stays mandatory for Entra, so the answer only
  affects key size.
- Should `revoke` support an immediate, issuer-side deny list (`jti` or `kid`)
  for notarx-to-notarx verifiers beyond JWKS removal?
