# Workload Identity Federation (Inbound)

[Documentation index](../README.md)

Status: accepted (2026-11-17)

See also: [ADR 0026](0026-debian-and-rpm-repositories.md), [ADR 0027](0027-hugging-face-repositories.md).

## Context

CI systems and cluster workloads authenticate to notarx with long-lived static
secrets. `docs/service-account-operations.md` describes service-account API keys
that live up to 365 days and are copied into Jenkins or GitLab CI variables.
GitHub Actions, GitLab CI and Kubernetes all issue short-lived, signed workload
JWTs that could replace those secrets (the "trusted publisher" model of PyPI and
npm). notarx cannot accept them today.

Facts in the current code, inherited from upstream:

- `internal/authorization/oidc.go` is a hand-written JWT verifier. It accepts
  only RS256 with a `kid`, checks `iss`, one `aud`, `exp` and an optional nonce,
  skips `nbf`/`iat`, has no skew allowance, parses only RSA JWKS keys, caches the
  JWKS for 5 minutes, and decodes a fixed claim struct (`sub`, `email`, `groups`,
  `roles`, ...). Provider claims such as `repository`, `ref`, `environment` or
  `kubernetes.io` are discarded. `go.mod` has no JOSE dependency.
- Trust is a single row: `oidc_settings` has `singleton BOOLEAN PRIMARY KEY`
  (upstream migration `000074`), loaded by `internal/app/oidc_runtime.go`. Only
  one issuer can be trusted, and it identifies a human (ADR 0018 replaces this
  with an IdP registry).
- `Authenticator.AuthenticateContext` (`internal/authorization/authenticator.go`
  ~102) tries static admin/resolver tokens, HMAC v2/v1 tokens, web sessions, API
  keys and local sessions, then the OIDC validator (~149). A valid OIDC token is
  mapped through `user_identities` to a user, or falls back to the raw `sub`.
- `AuthenticateBasic` (~201) accepts only the resolver password or a
  service-account API key. A JWT used as a Basic password is never examined, so
  Maven, pip/twine, cargo and `docker login` cannot carry one.
- `Principal` (~20) is an actor string (`service-account:<uuid>`) plus
  `AuthenticationKind` (~43-47: `static_admin`, `static_resolver`,
  `local_session`, `api_key`, `service_account_credential`, `oidc`). Repository
  grants (`principal TEXT`, evaluated in `internal/authorization/repository.go`)
  already accept `service-account:<id>`, so a workload mapped to a service
  account needs no grant changes.
- `OCIHandler.Token` (`internal/app/oci.go` ~756) serves `GET /auth/token`
  (route in `internal/app/repository_api.go` ~462): Basic credentials or an
  `Authorization` header produce a 5-minute `IssuePrincipalToken` HMAC "v2" token
  (`authenticator.go` ~350). These tokens are opaque, signed with
  `ResolverToken`, and carry `Authentication` kind in claim `k`.
- Management endpoints are OpenAPI-first (`api/openapi/management.yaml`, merged
  by `make openapi-bundle`, verified by `make openapi-check`). Domain handlers
  compose per ADR 0012. `make architecture-check` runs under `make lint`;
  `make coverage` enforces floors through `scripts/coverage-check.sh`.

Fork decisions: ADR 0013 (naming, `NOTARX_` prefix, `NOTARX_PUBLIC_URL`, global
slice ordering), ADR 0015 (cut-over to `000001_baseline.sql`; later schema
changes are ordinary numbered migrations), ADR 0018 (human IdP registry; this ADR
covers non-human principals), ADR 0020 (notarx as issuer, uses the JOSE package
created here), ADR 0021 (promotion between instances).

Release scope: v0.1.0 contains ADRs 0013-0017 only. This ADR ships in 0.2.x or
later, so its schema lands as new numbered migrations (`000002` or higher), not
as baseline edits; slice 1 (JOSE) is the first slice after the v0.1.0 tag.

Management API paths in this ADR are relative to `/api/v2` (the `servers` entry
of `api/openapi/management.yaml`). Root-mounted protocol endpoints (token
exchange here) are specified in `api/openapi/protocols/*.yaml`.

## Considered designs

1. **Extend `oidc_settings` to N rows and map claims to roles.** Smallest change,
   but mixes human SSO with workload trust, has no per-workload binding, and
   cannot express "only this repository on this branch may publish".
2. **Trusted issuers plus trust policies with a fixed matcher language,
   binding to service accounts.** Explicit, auditable, validated at write time,
   evaluated in microseconds, no embedded interpreter. Cost: new conditions need
   code, not configuration.
3. **Trust policies written in CEL (`google/cel-go`).** Maximal flexibility and
   familiar to Kubernetes and GCP users. Costs: a large dependency, expression
   cost limits and sandboxing to maintain, harder static validation of "does this
   policy contain an immutable identity claim", and policies that are hard to
   review and to explain in audit logs. Rejected for the first release of this feature.
4. **Only token exchange through an external broker** (for example SPIFFE/SPIRE
   or Vault). Offloads verification but requires an extra mandatory component,
   contrary to the single-binary-plus-PostgreSQL deployment goal.

## Decision

Option 2. Workload JWTs are verified against configured trusted issuers, matched
by explicit claim conditions in a trust policy, and mapped to an existing
service account. A CEL evaluator may be added later behind the same policy
interface without a data migration. If added, it uses `cel-go` with Kubernetes-style
budgets (about 1,000,000 cost units and 0.1 s per evaluation, expression-size
caps) and the matcher's mandatory immutable-claim conditions still apply in
addition to the expression.

### Shared JOSE package

New `internal/jose`, created by this ADR's slice 1 and built on
`github.com/go-jose/go-jose/v4` pinned at v4.1.9 or later (earlier releases have
advisories for DoS while parsing RSA JWKs, the exact JWKS-fetch path). It holds
everything JOSE-related for the repository: JWT verification, JWK/JWKS encoding,
RFC 7638 thumbprints and signing helpers. ADR 0020 consumes the signing and
encoding helpers (its `internal/tokenissuer` keeps only the `Signer` port and
key lifecycle). The hand-written RS256 code in `oidc.go` is retired and the
human OIDC validator (ADR 0018) is rewritten on top of this package; slice 1
defines the verifier interface that ADR 0018's OIDC slice consumes.

- Algorithms: allow-list per issuer from `RS256`, `ES256`, `EdDSA` (default all
  three; `none` and HMAC algorithms are never accepted; `alg` must be in the
  issuer's list before key lookup, which prevents algorithm confusion). The
  allow-list is per issuer, so ADR 0018 providers may restrict themselves to
  RS256. Hardening: RSA keys under 2048 bits are rejected, the `crit` header is
  not honoured, and the JWK `kty` must match the `alg`.
- Claims: required `iss`, `sub`, `aud`, `exp`; `nbf` and `iat` validated when
  present. Clock skew `NOTARX_WORKLOAD_CLOCK_SKEW` (default `60s`, max `5m`).
  Maximum token age `NOTARX_WORKLOAD_MAX_TOKEN_AGE` (default `1h`, applied to
  `now - iat`, and to `exp - iat` when both exist) caps issuers that mint
  day-long tokens.
- Key sources: `jwks_uri` through OIDC discovery (`/.well-known/openid-configuration`,
  issuer value in the document must equal the configured issuer), or a static
  JWKS stored on the issuer (clusters whose API server is private). Per-issuer
  CA bundle PEM for self-hosted GitLab and Kubernetes. All fetches go through an
  SSRF-guarded client: https only, no redirects to other hosts, and private,
  loopback and link-local ranges rejected unless the issuer sets
  `allow_private_network`. The check runs on the resolved IP at dial time
  (`net.Dialer.Control`), for every redirect hop and for `jwks_uri` hosts that
  differ from the issuer host, which closes DNS rebinding.
- Cache and refetch: JWKS cached per issuer (`NOTARX_WORKLOAD_JWKS_TTL`, default
  `10m`). An unknown `kid` triggers one refetch, rate limited to one per issuer
  per `30s` and shared across concurrent requests (singleflight); a negative
  result is cached for the same window so forged `kid` values cannot cause
  amplification. Fetch failure serves stale keys up to `NOTARX_WORKLOAD_JWKS_STALE_MAX`
  (default `24h`) and raises a metric. Static JWKS mode (typical for private
  Kubernetes API servers) never rotates automatically: the operator must update
  the stored set, and a `kid` miss on a static issuer increments a dedicated
  metric so a rotated cluster key is noticed.
- The package returns `Verified{Issuer, Claims map[string]any, KeyID, Alg}`
  with the full claim map preserved for policy matching and audit.

### Data model (new numbered migration, `000002` or higher)

```sql
CREATE TABLE workload_issuers (
  id UUID PRIMARY KEY, name TEXT UNIQUE NOT NULL,      -- slug used in the CLI and UI
  kind TEXT NOT NULL CHECK (kind IN ('github_actions','gitlab_ci','kubernetes','generic')),
  issuer TEXT UNIQUE NOT NULL,                         -- exact iss value
  audiences TEXT[] NOT NULL CHECK (cardinality(audiences) > 0),
  allowed_algs TEXT[] NOT NULL DEFAULT '{RS256,ES256,EdDSA}',
  jwks_mode TEXT NOT NULL CHECK (jwks_mode IN ('discovery','static')),
  jwks_uri TEXT, static_jwks JSONB, ca_bundle_pem TEXT,
  allow_private_network BOOLEAN NOT NULL DEFAULT false,
  state TEXT NOT NULL DEFAULT 'active', version BIGINT NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now());

CREATE TABLE workload_trust_policies (
  id UUID PRIMARY KEY, name TEXT UNIQUE NOT NULL,      -- also the Basic username alias
  issuer_id UUID NOT NULL REFERENCES workload_issuers(id),
  service_account_id UUID NOT NULL REFERENCES service_accounts(id),
  conditions JSONB NOT NULL,                           -- [{claim, op, values}]
  max_token_ttl_seconds INT NOT NULL DEFAULT 900,      -- exchanged token lifetime cap
  single_use BOOLEAN NOT NULL DEFAULT false,           -- enforce jti one-time use; off by default for every kind
  state TEXT NOT NULL DEFAULT 'active', version BIGINT NOT NULL DEFAULT 1,
  expires_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now());

CREATE TABLE workload_jti_seen (
  issuer_id UUID NOT NULL, jti_hash BYTEA NOT NULL,    -- SHA-256 of jti
  expires_at TIMESTAMPTZ NOT NULL, PRIMARY KEY (issuer_id, jti_hash));
CREATE INDEX ON workload_jti_seen (expires_at);
-- plain table (a global unique key rules out partitioning); deleted in
-- batches by the ADR 0023 RetentionJanitor, fillfactor 80 and autovacuum
-- scale factor 0.02 (ADR 0023 section 5). Insert-only ON CONFLICT DO NOTHING.
```

Migration numbers are assigned when the slice lands; the baseline is never edited
after the ADR 0015 cut-over. Policy-to-account mapping is many policies to one
service account. The service
account owns grants; a policy confers nothing itself.

### Matcher language

`conditions` is an AND list of `{claim, op, values}`. `claim` is a path
into the claim object, written as a JSON array (`["kubernetes.io","namespace"]`)
or as a `/`-delimited pointer with `~1` and `~0` escapes (RFC 6901 style). A dotted
shorthand (`kubernetes.io.namespace`) is accepted and resolved by longest
existing-key match, because Kubernetes tokens carry a top-level claim literally
named `kubernetes.io`; the validator rejects shorthand that is ambiguous for the
issuer kind and stores the canonical array form. A missing claim fails the
condition. Operators: `eq`, `in` (any of a list), `glob` (`*` within a
segment, `**` across `/`; anchored, no regular expressions), `prefix`. All
comparisons are exact, case-sensitive, on string, boolean or integer claims.
Limits: at most 16 conditions, 8 values each, 256 bytes per value. Condition
sets are validated on write against the issuer `kind`:

| kind | mandatory conditions | commonly added |
| --- | --- | --- |
| `github_actions` | `repository_id` (or `repository_owner_id` plus `repository`), immutable numeric ids because names can be re-registered after rename or deletion; and at least one of `ref`, `environment`, `job_workflow_ref`, or an explicit `allow_any_ref: true` acknowledgement | `event_name`, `repository` |
| `gitlab_ci` | `project_id` (or `namespace_id` plus `project_path`); and `ref_protected` = `"true"` or a `ref` condition, or `allow_any_ref: true` | `ref_type`, `environment` |
| `kubernetes` | `sub` equal to `system:serviceaccount:<ns>:<name>` | `kubernetes.io.namespace`, `kubernetes.io.serviceaccount.name` |
| `generic` | `sub` (eq/in/glob/prefix) | any claim |

A policy that is satisfied by an owner or namespace id alone would let any
workflow in any repository of that owner, including `pull_request_target`
workflows holding `id-token`, publish as the service account, so a repository or
project id plus a ref-style restriction is mandatory. A policy is rejected if it
lacks the mandatory conditions or if a bare `**`/`*` glob would match every
`sub`. `sub` and the issuer's `aud` are always checked implicitly. Audience:
every issuer lists accepted audiences (default `NOTARX_PUBLIC_URL`); a token
matches if any `aud` value intersects.

### Authentication flow and integration

New `internal/authorization/workload.go` defines `WorkloadAuthenticator`
(interface over a store port for issuers, policies and jti), injected into
`Authenticator` next to `OIDCSource`. Steps:

1. Detect a JWT cheaply (three dot-separated base64url segments, header decodes
   to JSON with `alg`); anything else skips the path so existing HMAC and API-key
   behaviour and latency are unchanged.
2. Read unverified `iss`, find an active `workload_issuers` row (issuers,
   policies and JWKS come from the ADR 0023 `readcache`, versioned and
   `singleflight`-loaded, with the 5-second revocation bound; the verified token is
   cached by `sha256(token)` until `min(exp, TTL)`, so repeated layer pulls do not
   re-verify or re-query) (unknown issuer
   returns not authenticated; the human OIDC validator of ADR 0018 is tried only
   when `iss` matches a human IdP, so the two registries never overlap; startup
   and write validation reject an issuer URL present in both).
3. Verify through `internal/jose`; evaluate all active, unexpired policies of
   the issuer; require exactly one matching policy, or the deterministic first by
   name when several match with the same service account, else reject as
   ambiguous (logged, never silently pick between accounts).
4. Require the service account to be active. If `single_use`, insert `(issuer,
   sha256(jti))` with `ON CONFLICT DO NOTHING` and reject on conflict; the row
   expires at `exp + skew`; the ADR 0023 RetentionJanitor deletes expired
   rows in throttled batches. Replay protection is optional per policy (default off for all kinds) because
   Actions and Kubernetes tokens are legitimately reused across steps within their
   lifetime. Saving `single_use=true` is refused when the issuer's sample token
   has no `jti`; the policy tester warns when a token lacks `jti`.
5. Return `Principal{Actor: "service-account:<uuid>", AuthenticationKind:
   AuthenticationWorkloadFederation ("workload_federation"), CredentialID:
   <policy id>}` plus a bounded `WorkloadClaims` summary on the principal for audit.

Entry points:

- **Direct bearer.** New branch in `AuthenticateContext` immediately before the
  human OIDC branch (~140). Protocols that accept Bearer (npm, OCI after
  challenge, management API) work unchanged.
- **Basic password.** `AuthenticateBasic` gains a context-taking variant
  (`AuthenticateBasicContext`) with the old method delegating. If the password
  looks like a JWT and the username is `oidc` or an existing policy name, the
  workload path runs; a policy name additionally pins that policy (the match must
  succeed against that policy only). Resolver password and API-key paths keep
  priority and behaviour. This serves Maven (`<username>oidc</username>`),
  pip/twine (`__token__`-style username `oidc`), cargo and `docker login`.
- **OCI `/auth/token`.** `OCIHandler.Token` already calls `AuthenticateBasic`,
  so `docker login -u oidc -p $JWT` yields the usual 5-minute registry token with
  no OCI code change beyond switching to the context variant. The issued token
  carries `k=workload_federation` and the policy id; `principalToken` decoding is
  extended to accept both fields. For tokens whose kind is `workload_federation`,
  `principalToken` revalidates policy and service-account state against a
  5-second cache, so disabling a policy takes effect within seconds, not at token
  expiry.

### Token exchange endpoint

`POST /auth/token-exchange`, root-mounted and specified in
`api/openapi/protocols/token-exchange.yaml`. It deliberately does not reuse
`/auth/token`: that path is the OCI token endpoint (`GET`, and `POST` with
`grant_type=password|refresh_token` in the Docker token spec, which containerd
and docker try first), and it is left untouched. RFC 8693 form body:

```
grant_type=urn:ietf:params:oauth:grant-type:token-exchange
subject_token=<jwt>
subject_token_type=urn:ietf:params:oauth:token-type:jwt
audience=<optional, informational>
scope=<optional, ignored in 0.2; grants are never widened or narrowed by scope>
policy=<optional policy name>
```

Response `200`: `{"access_token","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in"}`.
Narrowing exchanged tokens is by a future per-policy token profile (not a
client-supplied `scope`), to be added if ADR 0021 needs publish-only tokens.
The access token is a principal token (existing v2 format, extended with the
policy id), TTL `min(policy.max_token_ttl_seconds, NOTARX_WORKLOAD_EXCHANGE_MAX_TTL)`
(default 900 s, ceiling 1 h). Errors follow RFC 6749 (`invalid_request`,
`invalid_grant` for any verification or policy failure with a uniform message to
avoid an oracle, `invalid_target`). Per-source-IP and per-issuer rate limits
(`NOTARX_WORKLOAD_EXCHANGE_RATE`, default 60/min) apply; failed verification does
not reveal which condition failed to the caller. Operator-facing detail goes to
audit. Notarx-to-notarx trust (ADR 0020 assertions, sent by ADR 0021's peer
client) needs no exchange: instance B registers instance A as a trusted issuer
here and verifies A's assertion directly as a bearer JWT; the exchange is
optional for chatty clients.

### Management API and tooling

OpenAPI (`api/openapi/management.yaml` plus `api/openapi/management/...` parts;
paths are relative to `/api/v2`):
`/workload-identity/issuers` and `/workload-identity/policies`
(list, create, get, patch with `version` precondition, delete), `POST .../policies:test`
(dry-run a pasted token or claim set against policies and report which condition
failed; admin only; no state change), and `POST .../issuers:probe` (fetch
discovery and JWKS, report algorithms and key ids). Handlers form a new domain
composed per ADR 0012. CLI: `notarxctl workload issuer|policy ...` plus
`notarxctl login --workload-token-file` for the exchange. Console: a "Workload
identity" page with policy builder templates per issuer kind. Preflight
(`internal/preflight`) probes each active issuer's reachability.

### Configuration

`NOTARX_WORKLOAD_ENABLED` (default `false` until configured),
`NOTARX_WORKLOAD_CLOCK_SKEW`, `NOTARX_WORKLOAD_MAX_TOKEN_AGE`,
`NOTARX_WORKLOAD_JWKS_TTL`, `NOTARX_WORKLOAD_JWKS_STALE_MAX`,
`NOTARX_WORKLOAD_EXCHANGE_MAX_TTL`, `NOTARX_WORKLOAD_EXCHANGE_RATE`,
`NOTARX_WORKLOAD_ALLOW_PRIVATE_NETWORK` (global default, per-issuer override),
`NOTARX_PUBLIC_URL` (owned by ADR 0013; default audience). No `GATEWAY_*` aliases (ADR 0013).
When audit rows record a client IP, they use the proxy-aware resolution of
`NOTARX_TRUSTED_PROXIES` and `NOTARX_CLIENT_IP_HEADER` (registered in ADR 0013, defined by ADR 0029).

### Documentation examples (shipped under `docs/`)

- GitHub Actions: `permissions: { id-token: write }`; fetch the token with
  `curl -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN"
  "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=https://notarx.example.com"`, then
  `docker login notarx.example.com -u oidc --password-stdin`, or POST to
  `/auth/token-exchange` and use the bearer for Maven or npm.
- GitLab CI: `id_tokens: { NOTARX_TOKEN: { aud: https://notarx.example.com } }`
  and the same login; policy on `project_path`, `ref_protected: "true"`.
- Kubernetes: a `serviceAccountToken` projected volume with `audience` and
  `expirationSeconds: 600`; the policy binds
  `sub = system:serviceaccount:team-a:builder`; issuer registered with static JWKS
  (`kubectl get --raw /openid/v1/jwks`) when the API server is private.
- Release publishing (ADR 0026 slice 15): the GitHub Actions service account `feeds-publisher`
  has a trust policy bound to the repository, the workflow, the environment `packages-publish`
  and the ref (all required, per the Security considerations), and publishes deb and rpm
  packages to the feeds instance `feeds.mlops.ca`.

## Security considerations

- **Confused deputy and name squatting.** Mandatory immutable-id conditions per
  kind (table above); policies on mutable names alone are rejected.
- **Audience binding.** Tokens minted for other relying parties are refused; the
  audience is mandatory on every issuer.
- **Algorithm and key confusion.** Per-issuer algorithm allow-list, `kid`
  required for multi-key sets, no HMAC, `jku`/`x5u`/`jwk` header parameters
  ignored.
- **Fork and pull-request execution.** Docs and templates recommend `ref`,
  `environment`, `event_name` and `job_workflow_ref` conditions so untrusted
  workflows cannot publish; `gitlab_ci` templates require `ref_protected`.
- **Blast radius.** The service account's grants bound access; the exchanged
  token is short-lived, cannot be exchanged again, and cannot mint API keys or
  perform admin actions unless the account is admin. A `workload_federation`
  principal may mint outbound assertions (ADR 0020, `POST
  /api/v2/service-accounts/{id}/assertions`) when the account is enabled for it,
  so that capability is part of the account's blast radius and is off by default
  (policy create rejects mapping onto admin-role accounts
  without an explicit `allow_admin` flag).
- **SSRF and fetch abuse.** Guarded client with dial-time IP checks (DNS
  rebinding), https only, kid-miss rate limiting, size limits (1 MiB JWKS),
  timeouts, go-jose pinned at v4.1.9 or later.
- **Audit.** Every success and failure writes an audit event with issuer, policy
  id, service account, `jti` hash, `sub`, and the claims referenced by the matched
  conditions (not the whole token, never the raw JWT). Failures record the
  failing condition for operators only.
- **Revocation.** Disabling a policy, issuer or service account is effective for
  new requests immediately and for issued principal tokens within the 5-second
  revalidation cache.

## Consequences

- CI and Kubernetes no longer need stored notarx secrets; credential leakage
  windows shrink to minutes.
- The hand-written verifier is deleted; one JOSE package serves human SSO,
  workload federation and ADR 0020, adding `go-jose/go-jose/v4` (v4.1.9 or later) as a dependency
  (covered by `make dependency-audit`).
- Each Basic or bearer request with a JWT costs one policy evaluation and a
  possible PostgreSQL write (single-use only); the exchange endpoint is the
  recommended path for chatty clients.
- The matcher is deliberately less expressive than CEL; unusual provider claims
  require a `generic` issuer with explicit conditions, or a code change.
- Tokens issued by the exchange are still symmetric HMAC principal tokens, not
  JWTs; third parties cannot verify them (ADR 0020 addresses outbound assertions).
- Existing `oidc_settings` behaviour is not preserved; ADR 0018 owns that
  replacement, including dropping the legacy table in a numbered migration.

## Implementation slices

Each slice is one PR. ADR 0013 holds the global ordering; this ADR starts after
the v0.1.0 tag, and slice 1 is first.

1. **Shared JOSE package.** Add `internal/jose` (go-jose v4.1.9 or later):
   verifier with algorithm allow-list, skew, max age, JWKS discovery/static
   sources, kid-miss singleflight refetch with rate limit, a per-issuer JWKS TTL override (60 s
   for notarx-trusted issuers per ADR 0020), dial-time SSRF-guarded
   client, claim-map output; plus JWK/JWKS encoding, RFC 7638 thumbprints and
   signing helpers for ADR 0020. Replace `internal/authorization/oidc.go`
   internals and publish the verifier interface that ADR 0018's OIDC slice
   consumes.
   - [ ] Table tests: RS256/ES256/EdDSA, `none`/HS256 rejected, expired, nbf,
     iat in the future, skew edge, over-age, wrong `aud`/`iss`, unknown kid
     refetch once and rate limited, notarx-trusted issuer JWKS refreshed within 60 s, stale-on-failure, RSA under 2048 bits and
     `kty`/`alg` mismatch rejected, DNS-rebinding and redirect-to-private tests.
   - [ ] `make test`, `make lint` (architecture-check), `make coverage`,
     `make dependency-audit` pass; existing OIDC tests unchanged in behaviour.
   - Dependencies: ADR 0013 (all slices), ADR 0015 slice 4 (cut-over), v0.1.0 tag.
2. **Schema and store.** Add the three tables in a new numbered migration and
   PostgreSQL plus memory store implementations with ports in
   `internal/repository` (`WorkloadStore`), including jti insert and expiry sweep.
   - [ ] Store tests on both backends, `make integration-test` covers jti
     conflict atomicity, `make test`, `make lint`.
   - Dependencies: ADR 0015 slice 4 (cut-over), v0.1.0 tag.
3. **Matcher and policy validation.** `internal/authorization/workloadpolicy`:
   condition model, claim paths (array and pointer forms, longest-key shorthand),
   glob semantics, per-kind mandatory-condition validation including ref-style
   restrictions, evaluation with ambiguity detection.
   - [ ] Unit tests including rejected broad globs, owner-id-only policies,
     rename-squat cases, nested claims, a Kubernetes fixture with a real token
     body (`kubernetes.io` claim), type mismatches; fuzz test for the glob
     matcher; `make test`, `make coverage`.
   - Dependencies: ADR 0019 slice 1.
4. **Authenticator integration.** `WorkloadAuthenticator`,
   `AuthenticationWorkloadFederation`, `AuthenticateBasicContext`, bearer branch
   ahead of human OIDC, principal-token `k`/policy fields and revalidation cache,
   OCI `Token` switched to the context variant, metrics and audit events.
   - [ ] Tests: bearer, Basic `oidc` and policy-name username, pinned policy
     mismatch, replay with `single_use`, disabled policy/account, regression of
     API-key, HMAC and resolver paths; OCI `docker login` e2e via
     `make native-oci-e2e` using a local JWKS fixture.
   - [ ] The Hugging Face route `/hf` (ADR 0027 slice 6) accepts the exchanged Bearer
     token and a workload JWT through the same authenticator; Hub-shaped 401/403 asserted.
   - [ ] `make test`, `make race`, `make lint`, `make coverage`.
   - Dependencies: ADR 0019 slices 1, 2, 3; ADR 0023 slice 12
     (`internal/readcache`, ordered before this ADR by ADR 0013 step 6b); the
     issuer, policy, JWKS and verified-principal caches use it.
5. **Exchange endpoint and management API.** Author
   `api/openapi/protocols/token-exchange.yaml` for `POST /auth/token-exchange` and
   the `/workload-identity/*` management resources (relative to `/api/v2`,
   including `policies:test`, `issuers:probe`); regenerate server and client;
   domain handler per ADR 0012; rate limiting; RFC 6749 errors.
   - [ ] `make openapi-check`, `make api-contract`, handler tests including
     `single_use=true` refused when the sample token has no `jti`, `scope` ignored,
     version preconditions, uniform `invalid_grant`, and a test that
     `POST /auth/token` with `grant_type=password|refresh_token` is unaffected;
     `make test`, `make lint`.
   - Dependencies: ADR 0019 slice 4.
6. **CLI and console.** `notarxctl workload ...`, `login --workload-token-file`,
   console page with per-kind policy templates (exchange-only templates recommend
   `single_use`) and the dry-run tester, which warns when a token lacks `jti`; fr-CA and
   en-CA strings (ADR 0014).
   - [ ] `make cli-test`, `make cli-contract-check`, `make console-check`,
     `make console-test`, `make console-api-check`, `make docs-check`.
   - Dependencies: ADR 0019 slice 5, ADR 0014 (locale slices).
7. **Preflight, docs and e2e.** Issuer reachability in `internal/preflight`;
   docs pages and the three examples above (English; zh-CN optional per
   ADR 0014); a Kubernetes projected-token e2e and a GitHub-style mock issuer
   fixture in `deploy/`.
   - [ ] `make preflight`, `make docs-check`, new `make workload-identity-e2e`
     target listed in the Makefile `.PHONY` and help text, `make integration-test`.
   - Dependencies: ADR 0019 slices 4, 5.

## Resolved questions

**Q1 — Offer a CEL policy kind later, and with what cost limits?** Decision: Stay deferred. If a real request arrives, add `cel-go` behind the policy interface with Kubernetes-style budgets (per-evaluation cost about 1,000,000 units, about 0.1 s, plus expression-size caps) and require the matcher's mandatory immutable-claim conditions in addition to the expression. CEL is non-Turing-complete and Kubernetes ships this pattern, so adding it later is safe.

**Q2 — Is single-use `jti` default-on for generic issuers?** Decision: No; `single_use` defaults off for all kinds. The tester warns when a token lacks `jti`, saving `single_use=true` is refused when the issuer's sample token has no `jti`, and UI templates recommend it for exchange-only policies. Tokens are legitimately reused across steps and pulls, so default-on would break the common path.

**Q3 — Should exchanged tokens be narrowable per request through `scope`?** Decision: No; `scope` stays ignored in 0.2 (RFC 8693 makes it optional). If ADR 0021 needs publish-only tokens, a per-policy token profile is preferred over a client-supplied scope, which avoids a cross-format scope vocabulary.
