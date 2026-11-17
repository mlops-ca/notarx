# Human Identity Providers: OIDC and SAML Registry, SCIM Provisioning, Groups

[Documentation index](../README.md)

Status: proposed (2026-11-17)

## Context

Human sign-in is a singleton. `migrations/000074_oidc_settings.sql` defines
`oidc_settings` with `singleton BOOLEAN PRIMARY KEY CHECK (singleton)`, and
`migrations/000088_oidc_user_identities.sql` adds `provisioning_mode`
(`disabled|jit`), `email_linking_enabled` and `jit_default_role` to it.
`internal/app/oidc_runtime.go` (`OIDCRuntime`) merges that row with the
environment bootstrap read in `internal/config/config.go` (`OIDCIssuer`,
`OIDCAudience`, `OIDCJWKSURL`, `OIDCClientID`, `OIDCClientSecret`,
`OIDCRedirectURL`, `OIDCScopes`, `OIDCAdminSubjects`, `OIDCRoles`). One issuer
per instance is therefore the ceiling. Customers on Microsoft Entra ID, Okta and
Keycloak together, or with a SAML-only IdP, cannot be served.

Other facts that bound the design:

- `internal/authorization/oidc.go` validates only RS256 ID tokens against one
  JWKS and maps claim roles (`realm_access.roles`, `roles`) through
  `OIDCRoleMapping{Member, Admin}`. Per ADR 0006 a mapped role only approves an
  account (`member`) or makes it `admin`; repository reach never comes from a
  mapped role. A `groups` claim is parsed but not used for authority.
- `user_identities` (000088) has `kind TEXT CHECK (kind = 'oidc')`,
  `UNIQUE (kind, issuer, subject)` and `UNIQUE (user_id, kind, issuer)`.
  `user_sessions.kind` (000089) is `local_session|oidc`.
- The browser flow (`internal/app/oidc_login.go`: `start`, `callback`,
  `logout`, `session`) signs a flow cookie (`signOIDCFlow`), issues a
  `user_sessions` row and a session cookie. JIT creates a pending (`none`)
  account unless `jit_default_role` says otherwise (`TestOIDCBrowserJITRegistersPendingUserWithoutRepositoryAccess`).
- Authorization (`internal/authorization/repository.go`,
  `ManagedResourceDecision`) matches `grant.Principal != principal.Actor`
  string-wise against `repository_grants.principal` (000027, PK extended by
  000048). `Principal` (`authenticator.go`) has one `Actor`, `UserID`, `Role`;
  no group list. There is no group-of-users concept anywhere. The word "Group"
  in the codebase means a repository group (ADR 0012, `oci_group_members`,
  `maven_group_members`), so user groups must be named distinctly.
- API keys are stored as `secret_hash TEXT UNIQUE` (000047). Service accounts
  exist (000106) and are the machine-identity model that ADR 0019 extends.
- `deploy/oidc-test/` holds a Keycloak realm fixture. No SAML or SCIM code,
  route or dependency exists (verified: no `scim` or `saml` in `internal/`).
- ADR 0015 cuts over to a single `000001_baseline.sql`. Everything below is
  therefore a normal numbered migration (`000002+`) landing after the cut-over;
  the baseline is never edited in place. This ADR is not part of v0.1.0 (ADR 0013
  release scope: ADRs 0013-0017); it ships in 0.2.x as additive migrations, and
  the pre-tag re-squash (ADR 0015) folds them in only if they land before the
  tag. ADR 0013 renames env vars to `NOTARX_` with no aliases, so the OIDC env
  bootstrap keys change anyway.
- `internal/secrets` (`Seal(purpose, plaintext)` returning a base64 string,
  `Open`) uses one key from `GATEWAY_SETTINGS_ENCRYPTION_KEY` (renamed
  `NOTARX_SETTINGS_ENCRYPTION_KEY` by ADR 0013) with no key id and no ring, so
  rotation is impossible today. This ADR owns fixing that (slice 1) because ADR
  0020 depends on it.
- `users.disabled_reason` does not exist in any migration; the SCIM
  deactivate rules below need it and this ADR adds it.
- Management API paths in this ADR are relative to `/api/v2`
  (`api/openapi/management.yaml` `servers`). Root-mounted protocol endpoints
  (`/auth/...`, `/scim/v2/...`) are specified in `api/openapi/protocols/*.yaml`.
- `docs/user-governance.md` documents the account lifecycle (`pending`,
  `active`, `disabled`, session revocation via `session_version`); this ADR must
  reuse those states, not add a parallel lifecycle.

## Considered designs

### A. Identity providers

1. **Keep the singleton, add SAML beside it.** Cheapest, but a second singleton
   table doubles the config surface and still allows only one IdP per protocol.
2. **Delegate to an external broker (Keycloak/Dex) and keep OIDC only.** Zero
   SAML code in notarx, but forces every customer to operate a broker, and SCIM
   from Entra ID/Okta cannot target a broker that lacks our group model.
3. **Registry of identity providers (OIDC and SAML) in PostgreSQL.** One table,
   per-IdP settings, per-IdP SCIM credentials, login-page chooser. More code,
   but matches the control-plane-in-PostgreSQL rule and is the only option that
   serves mixed-IdP tenants. Chosen.

### B. SAML library

1. **`github.com/crewjam/saml`** (with its `samlsp` helpers). Full SP and IdP,
   metadata parsing, built on `russellhaering/goxmldsig`. Active enough, widely
   used, but pulls a large surface (IdP side, middleware) we do not need, and
   has a long advisory history, including the same `goxmldsig` signature-bypass
   (CVE-2026-33487, GHSA-479m-364c-43vc, fixed in `goxmldsig` 1.6.0).
2. **`github.com/russellhaering/gosaml2` + `goxmldsig`.** SP-only, smaller,
   validates response/assertion signatures and conditions, used by large SPs.
   Also has advisories: CVE-2020-29509/29510/29511 are a Go `encoding/xml`
   round-trip class (advisory filed on `gosaml2`, fixed there in v0.6.0) whose
   root cause Go upstream does not fix, and the August 2026 advisories cover
   allocation amplification, an unsigned `LogoutResponse` accepted, and an
   unauthenticated panic on `EncryptedAssertion`. Latest releases at the time of
   writing are `gosaml2` v0.12.0 and `goxmldsig` v1.6.1.
3. **Hand-rolled XML-DSig verification.** Rejected: XML signature wrapping and
   canonicalization are the classic failure class; do not own that code.

Both libraries share `goxmldsig`, so the real risk is that dependency. The
decision is made on surface area, below.

### C. Group-to-authority model

1. **Keep claim-role mapping only (`OIDCRoleMapping`).** Coarse: it can approve
   or promote an account but cannot express per-repository grants for a team.
2. **Resolve groups only at login from the IdP claim, never store them.** No
   SCIM needed, but grants cannot be audited or previewed offline and SAML/OIDC
   claims overflow (Entra ID drops `groups` over 200 entries).
3. **Persist groups and memberships (from SCIM and/or login claims) and let
   grants name `group:<id>`.** Auditable, previewable in the effective-access
   simulation, unaffected by claim overflow. Chosen.

## Decision

### 1. Identity provider registry

Replace `oidc_settings` (and the env-only OIDC bootstrap) with:

```sql
CREATE TABLE identity_providers (
  id UUID PRIMARY KEY,
  slug TEXT NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
  kind TEXT NOT NULL CHECK (kind IN ('oidc','saml')),
  display_name TEXT NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT FALSE,
  settings JSONB NOT NULL,            -- kind-specific, validated in Go
  secrets_sealed TEXT,                -- OIDC client secret, sealed via internal/secrets
  attribute_mapping JSONB NOT NULL,   -- username/email/name/groups sources
  provisioning_mode TEXT NOT NULL DEFAULT 'disabled'
    CHECK (provisioning_mode IN ('disabled','jit','scim_only')),
  email_linking_enabled BOOLEAN NOT NULL DEFAULT FALSE,
  jit_default_role TEXT NOT NULL DEFAULT 'none' CHECK (jit_default_role IN ('none','member')),
  version BIGINT NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (slug)
);
```

`jit_default_role` drops `admin` (an IdP-driven first login must never mint an
administrator by default; admin comes from group-role mapping, section 5).
The env bootstrap becomes optional seeding of one OIDC provider (`NOTARX_OIDC_*`
keys, slug `default`) when the table is empty, to keep the Keycloak dev fixture
and e2e simple; there is no ongoing env-versus-database merge.

`user_identities` is generalised: `kind IN ('oidc','saml')`, a new
`provider_id UUID NOT NULL REFERENCES identity_providers(id)`, and exactly one
row per `(provider_id, user_id)`:

```sql
  subject TEXT NULL,            -- login key: OIDC sub (Entra: tid:oid), SAML NameID
  scim_external_id TEXT NULL,   -- IdP externalId set by SCIM
  UNIQUE (provider_id, user_id)
  -- partial unique indexes: (provider_id, subject) WHERE subject IS NOT NULL,
  --                         (provider_id, scim_external_id) WHERE scim_external_id IS NOT NULL
```

A SCIM-created user has a row with `scim_external_id` set and `subject` NULL. On
first login the verifier looks up `(provider_id, subject)`; if absent and the
provider is `scim_only`, it matches `scim_external_id` against the `externalId`
attribute the IdP asserts (`attribute_mapping.externalId`) and binds `subject`
only on that match. `userName` is never used to bind a login (it is mutable); a
provider that does not assert `externalId` relies on the admin link flow
(`/links`, section 4). `issuer` stays as informational text. The 000088
constraints (`UNIQUE (kind, issuer, subject)`, `UNIQUE (user_id, kind, issuer)`)
are dropped in the same migration. `user_sessions.kind` becomes
`local_session|oidc|saml`.

Routes (browser, public, rate limited):
`GET /auth/idp` (enabled providers only for the chooser: slug, display name,
kind; disabled providers are never revealed),
`GET /auth/oidc/{slug}/login|callback` (the existing `/auth/oidc/login|callback`
flow parameterised by slug; the handler method is `start`, the route stays
`login`),
`GET /auth/saml/{slug}/login`, `POST /auth/saml/{slug}/acs`,
`GET /auth/saml/{slug}/metadata` (SP metadata). These root-mounted routes are specified in
`api/openapi/protocols/saml.yaml` and `api/openapi/protocols/oidc-login.yaml`.
Management API (relative to `/api/v2`; admin only, OpenAPI-first, optimistic
`ETag` like `oidc_settings`): `/authentication/providers`
CRUD, `:test`, `:import-metadata`, plus SCIM token management below. The old
`/authentication/oidc` operations are removed, not aliased.

### 2. OIDC

Token verification is delegated to the shared verifier in `internal/jose`
(ADR 0019 slice 1: go-jose v4 >= v4.1.9); this ADR consumes that interface and
does not reimplement it. Behaviour of `internal/authorization/oidc.go` is
preserved (nonce binding, JWKS cache), the algorithm is a per-provider
allow-list (default `RS256`; `ES256`/`EdDSA` selectable), and a validator is
built per provider and cached by `(provider_id, version)`. `NOTARX_OIDC_*` replaces `GATEWAY_OIDC_*` per ADR 0013.
Entra ID specifics: issuer `https://login.microsoftonline.com/{tenant}/v2.0`,
stable subject is `(tid, oid)` stored as `tid:oid` (`oid` is unique only per
tenant; `sub` is pairwise per app) selectable in `attribute_mapping`; `groups` overage (`_claim_names`) is not chased via Graph in
v1 - SCIM is the supported route for large directories.

### 3. SAML 2.0 Web SSO (SP-initiated only)

- Binding: AuthnRequest via HTTP-Redirect (deflated, optionally signed), response
  via HTTP-POST to the ACS. IdP-initiated responses (no `InResponseTo`) are
  rejected by default; `settings.allowIdpInitiated` exists but defaults to false
  and requires a fixed `RelayState` allowlist.
- Request state: AuthnRequest `ID` and a random RelayState are stored in a signed,
  `HttpOnly`, `SameSite=None; Secure` flow cookie (as `signOIDCFlow`) plus a
  PostgreSQL row in `saml_request_state(id, provider_id, expires_at)`; the ACS
  requires `InResponseTo` to match and consumes the row.
- Validation (all mandatory): accept either (a) a signature whose Reference URI
  is the Assertion's ID, or (b) a Response signature whose Reference URI is the
  Response's ID with the Assertion as its direct child. Claims are read only from
  the element object the verifier returns, never re-queried by XPath from the
  original document. `SubjectConfirmation Method` must be `bearer`, and
  `InResponseTo` must match on both the Response and
  `SubjectConfirmationData`. `EncryptedAssertion` is rejected explicitly
  (decryption is out of scope and has had panics). Signature algorithm allowlist RSA-SHA256/384/512 and
  ECDSA-SHA256, no SHA-1; certificate must equal one pinned in the provider's
  `settings.certificates` (never trust `KeyInfo` from the message); `Issuer`
  equals the registered EntityID; `Destination` equals the ACS URL; `Recipient`
  in `SubjectConfirmationData` equals the ACS URL; `Audience` contains our SP
  EntityID; `NotBefore`/`NotOnOrAfter` and `SubjectConfirmationData` are enforced
  with clock skew `NOTARX_SAML_CLOCK_SKEW` (default `60s`, max `5m`); exactly one
  Assertion and one Signature reference; DTD/entity expansion disabled; payload
  capped at 1 MiB; `AuthnContext` is recorded, not enforced.
- Replay: assertion `ID` is stored in `saml_assertion_ids(provider_id, assertion_id,
  expires_at)` with a unique constraint until `NotOnOrAfter + skew`; a duplicate
  is refused. A background task (the existing scheduled-tasks mechanism, 000082)
  prunes expired rows in batches through the ADR 0023 RetentionJanitor
  (`expires_at` + skew, plain table, indexed `saml_assertion_ids(expires_at)` and
  `saml_request_state(expires_at)`; a global unique key rules out partitioning).
- NameID: requested format `persistent` by default; `emailAddress` and
  `unspecified` selectable. Identity key is `(provider_id, NameID value)`
  for persistent/unspecified; for `emailAddress` the key is the lower-cased
  value, but a NameID of `emailAddress` never implies linking to an existing
  account. Linking requires `email_linking_enabled`, and the target account is
  not `admin`, has no local password and has an admin-verified email; otherwise
  an admin confirms through `/links`. `transient` is refused.
- Attribute mapping (`attribute_mapping`, shared with OIDC): each of `username`,
  `email`, `displayName`, `groups` names an attribute or claim; defaults cover
  Entra ID (`http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress`,
  `.../name`, `http://schemas.microsoft.com/ws/2008/06/identity/claims/groups`),
  Okta (`email`, `displayName`, `groups`) and Keycloak. `groups` values are
  matched per section 5 (claims-sourced groups only, by `external_id`).
- Metadata: `:import-metadata` accepts an HTTPS URL (fetched through the egress
  proxy rules in 000070, SSRF-guarded: no private ranges, 5 s timeout, 1 MiB) or
  pasted XML; it extracts EntityID, SSO endpoints and signing certificates. A
  periodic refresh of URL-imported metadata is optional and off by default.
- Certificate rollover: `settings.certificates` is a list of
  `{pem, notAfter, state: active|next}`. Any listed cert verifies; the console
  warns 30 days before `notAfter`; metadata refresh adds the new cert as `next`
  before the IdP cuts over. SP signing (optional signed AuthnRequests) uses a
  notarx-generated keypair stored encrypted, also rotatable with two active keys
  published in SP metadata.
- Single Logout is not in v1: logout revokes the notarx `user_sessions` row only
  and the UI says so. SLO is listed under Open questions.
- Library: use `github.com/russellhaering/gosaml2` with `goxmldsig`, wrapped in
  `internal/identity/saml` so the rest of the code sees only a narrow
  `Verifier` port. Reason: SP-only, smaller surface than `crewjam/saml`, which
  adds IdP and middleware code we would not run. Minimum versions are
  `goxmldsig` v1.6.1 and `gosaml2` v0.12.0 (CVE-2026-33487 and the August 2026
  advisories); a CI check fails the build if `go.mod` resolves lower, and the
  consumed-element check above is mandatory because the stdlib XML parser root
  cause cannot be fixed upstream. `govulncheck` (already `make dependency-audit`) must stay green, and
  the wrapper is fuzzed (see slice 3). Our own post-verification checks
  (Destination, Recipient, single-assertion, allowlists) run regardless of what
  the library validates, so a library regression is not sole defence.

### 4. SCIM 2.0 server

Mounted at `/scim/v2/` (RFC 7643/7644), at the root and separate from the
management API (`/api/v2`), with its own generated OpenAPI contract
`api/openapi/protocols/scim.yaml`. The SCIM base URL is
`NOTARX_PUBLIC_URL` + `/scim/v2`. Accepts `application/scim+json` and
`application/json`.

- Resources: `/Users`, `/Groups`, `/ServiceProviderConfig`, `/Schemas`,
  `/ResourceTypes`. `ServiceProviderConfig` advertises `patch: supported`,
  `bulk: unsupported`, `filter: supported (maxResults 200)`, `excludedAttributes=members` honoured on `GET /Groups`,
  `changePassword: unsupported`, `sort: unsupported`, `etag: supported`.
- Auth: per-provider SCIM bearer tokens in `identity_provider_scim_tokens(id,
  provider_id, name, secret_hash UNIQUE, created_at, expires_at, revoked_at)`,
  hashed with the same function as `api_keys.secret_hash`; the plaintext is shown
  once. `expires_at` is mandatory and capped at 365 days (the console warns
  before expiry), since Entra and Okta tokens tend to be long-lived. A token acts only for its provider; users and groups it creates carry
  that `provider_id`. Admin CRUD under `/authentication/providers/{id}/scim-tokens`.
- Users: `userName` (maps to account name, unique case-insensitive per
  `users_name_case_insensitive_idx`), `externalId`, `displayName`, `name`,
  `emails[primary]`, `active`. Create makes a `users` row with level `none`
  (pending per ADR 0006) plus a `user_identities` row with `scim_external_id` set and `subject` NULL, unless the group
  to role mapping (section 5) already grants a level.
- Filters: only `eq` on `userName`, `externalId` (Users) and `displayName`,
  `externalId` (Groups), combined with nothing else; anything else returns
  `400 invalidFilter`. This is exactly what Entra ID and Okta emit for
  correlation. Pagination via `startIndex`/`count`, count capped at 200.
- PATCH: RFC 7644 `Operations`; `op` is case-insensitive (Entra sends
  `Replace`/`Add`/`Remove`) and `active` accepts a boolean or the strings
  `"True"`/`"False"` (Entra sends strings unless its `aadOptscim062020` flag
  is set). Supported paths: simple attributes (`active`, `userName`,
  `name.givenName`), multi-valued selectors `attr[type eq "x"].sub` (for
  example `emails[type eq "work"].value`, tolerating Entra's occasionally
  malformed filter syntax where unambiguous), `members[value eq "id"]`, and
  `members` with a value array; Okta sends pathless `replace` with a value
  object (`{"active": false}`) and Entra may send pathless replaces with dotted
  extension attributes. Unsupported attributes that the IdP maps are logged and
  ignored with `200`; only core identity attributes (`userName`, `externalId`,
  `active`, `id`) return `400 invalidPath`, because Entra treats 400s as sync
  failures and quarantines the app. Golden fixtures cover both vendors and both
  Entra flag-on and flag-off shapes.
- Deactivate versus delete: `active=false` sets `users.state='disabled'`,
  bumps `session_version`, revokes `user_sessions` and API/personal tokens of
  that account in the same transaction; identity rows and grants are kept, so
  re-activation restores access. `DELETE /Users/{id}` removes only the SCIM
  link and disables the account; physical deletion of an account stays a
  console-admin action (audit history references users). `active=true` on an
  account an admin disabled manually is refused with `409` unless the account's
  `disabled_reason` is `scim`. The `users.disabled_reason` column (nullable
  text, values `admin|scim`) is added by this ADR's registry migration.
- Conflicts with local users: a SCIM create whose `userName` matches an existing
  local (password) account returns `409 uniqueness`; linking requires an admin
  to confirm in the console (`POST /authentication/providers/{id}/links`), never
  automatic, unless `email_linking_enabled` and the existing account has a
  verified-by-admin email. A SCIM request can never modify or deactivate a
  platform `admin` account (`403`), to stop an IdP compromise from locking out
  or editing administrators.
- Precedence of JIT versus SCIM: `provisioning_mode` is exclusive per provider.
  `scim_only` means SAML/OIDC login succeeds only for identities already present
  (created by SCIM or linked); `jit` means login creates accounts and SCIM, if
  used, must find them by `externalId`/`userName`. Group membership from SCIM
  is authoritative for SCIM-managed groups; login-time claims never alter
  SCIM-managed memberships (they may populate `jit` groups only).
- Audit: every SCIM write emits an audit event (actor `scim:<provider slug>`,
  operation, resource id, outcome, request id) through the existing audit
  recorder; PATCH bodies are not logged verbatim, only changed attribute names.

### 5. User groups and grants

```sql
CREATE TABLE user_groups (
  id UUID PRIMARY KEY,
  provider_id UUID REFERENCES identity_providers(id) ON DELETE SET NULL,
  external_id TEXT NOT NULL DEFAULT '',
  display_name TEXT NOT NULL,
  source TEXT NOT NULL CHECK (source IN ('local','scim','claims')),
  global_role TEXT NOT NULL DEFAULT 'none' CHECK (global_role IN ('none','member','admin')),
  version BIGINT NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ON user_groups (lower(display_name)) WHERE provider_id IS NULL;
CREATE UNIQUE INDEX ON user_groups (provider_id, external_id) WHERE external_id <> '';
CREATE TABLE user_group_members (
  group_id UUID NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  source TEXT NOT NULL CHECK (source IN ('local','scim','claims')),
  PRIMARY KEY (group_id, user_id)
);
-- every authenticated request resolves "groups of this user" (ADR 0023 section 5)
CREATE INDEX ON user_group_members (user_id, group_id);
```

Login claims (`groups` attribute) match only rows with `source='claims'` and
`provider_id` equal to the login provider, by `external_id` only, never by
`display_name`. Groups with `global_role` other than `none` are never created
or matched from claims, so an IdP-asserted string cannot reach a privileged
local group.

The table is `user_groups` (not `groups`) to avoid clashing with repository
Groups. `global_role` replaces `OIDCRoleMapping`: a user's effective level is the
maximum of its own `users.role` and the `global_role` of its groups, evaluated at
authentication time (not copied). OCI and Conan principal tokens live about
5 minutes, so they carry no group ids and groups are re-resolved on every
request that uses them; removing a membership therefore withdraws authority on
the next request. `none` stays a hard stop only for the user's own level per
ADR 0006: a disabled account is blocked regardless of groups. `admin` via group is
allowed only for `source IN ('local')` groups or SCIM groups an administrator
explicitly flagged `global_role='admin'`; the default for any provisioned group
is `none`.

Grants: `repository_grants.principal` accepts `group:<uuid>` in addition to the
existing actor string. `Principal` gains `GroupIDs []string`, loaded by one
indexed query on `user_group_members(user_id, group_id)`, cached in the
per-request context and in the ADR 0023 `readcache` (versioned by the group
`version`, TTL `NOTARX_AUTHZ_CACHE_TTL`, default 5s, so membership removal is
effective on every node within that window; bounded to 512 groups).
SCIM list endpoints page by keyset on `(created_at, id)` (ADR 0023 section 8). `ManagedResourceDecision` evaluates grants whose
principal equals `Actor` or `group:<id>` for any id in `GroupIDs` and unions
the scopes; an unmanaged grant set that names any of these principals counts as
`attributed` per ADR 0006. Deny stays the default and no grant ever overrides
`none`. The effective-access simulation and the console grant editor list
groups as selectable principals. Group principals on `resource_prefix` grants
compose exactly as user principals do.

### 6. Configuration keys

`NOTARX_OIDC_*` (seed only, see 1), `NOTARX_SAML_CLOCK_SKEW` (default `60s`),
`NOTARX_SAML_REQUEST_TTL` (default `10m`), `NOTARX_SAML_MAX_RESPONSE_BYTES`
(default `1048576`), `NOTARX_SCIM_ENABLED` (default `false`; routes return 404
when off), `NOTARX_SCIM_MAX_PAGE` (default `200`). ACS, metadata and SCIM base URLs derive
from `NOTARX_PUBLIC_URL` (owned by ADR 0013's env table; ADR 0019 and 0020
reuse it). Secrets are sealed with `NOTARX_SETTINGS_ENCRYPTION_KEY` through
`internal/secrets` (ring in slice 1).

## Security considerations

- XML signature wrapping is the principal SAML threat. Mitigations: pinned
  certificates, single-assertion rule, reference-to-ID check, algorithm
  allowlist, library pinned exactly and fuzzed, our own post-checks, and
  parser limits (no DTD, depth and size caps).
- Open redirect and login CSRF: RelayState is an opaque id looked up server
  side; the post-login target goes through `safeConsoleRedirect`.
- Secrets at rest: OIDC client secret and SAML SP private key are sealed by
  `internal/secrets` with `NOTARX_SETTINGS_ENCRYPTION_KEY` (AES-256-GCM; stored
  as TEXT; the key-ring from slice 1 adds a key id prefix, rotation and a
  `reseal` command) rather than stored plain as `oidc_settings.client_secret`
  is today.
- SCIM tokens are hashed, scoped to one provider, rate limited, and have a
  mandatory expiry of at most 365 days; SCIM can never touch platform administrators nor grant `admin`
  except via an admin-flagged group.
- Account takeover by email: linking by email requires `email_linking_enabled`
  and a provider-asserted verified email; Entra ID emails are mutable, so the
  default key is `(tid, oid)`/NameID, not email, and a SAML `emailAddress`
  NameID never implies linking (section 3); local admin accounts are never
  linked automatically.
- All SSO failures return a generic error to the browser and a precise reason to
  the audit log; no assertion contents in logs.

## Consequences

- Multi-IdP tenants and SAML-only enterprises become servable; Entra ID, Okta and
  Keycloak are first-class via templates in the console.
- `oidc_settings`, `/authentication/oidc` and `OIDCRoleMapping` disappear; the
  console OIDC settings page is rewritten. The migration copies an existing
  `oidc_settings` row into a `default` provider so upgrades from 0.1.0 keep
  working.
- Group principals add one query per request; mitigated by indexes and the
  per-request cache, and measured in slice 5.
- New dependency tree (`gosaml2`, `goxmldsig`, `etree`) increases the audit
  surface; it is isolated behind one package so it can be replaced.
- SLO absent in v1: IdP-side logout does not end notarx sessions (12 h max,
  `UserSessionLifetime`); documented.

## Implementation slices

Each slice is one PR. Every slice must keep `make test`, `make lint`
(including `architecture-check`), `make coverage` and, when OpenAPI changes,
`make openapi-check` green; console slices also need `make console-test`.
Management paths are relative to `/api/v2`. Schema changes are numbered
migrations (`000002+`), never baseline edits. Global ordering is in ADR 0013:
v0.1.0 tag, then ADR 0019 slice 1 (`internal/jose`), then this ADR (key-ring
first), then the rest of ADR 0019.

1. **Secrets key-ring.** Scope: `internal/secrets` (key ids, ring of keys from
   `NOTARX_SETTINGS_ENCRYPTION_KEY` plus `NOTARX_SETTINGS_ENCRYPTION_KEY_PREVIOUS`,
   sealed format `v1:<kid>:<base64>` with the legacy format still readable,
   rotation, `notarxctl secrets reseal`), callers of `Seal`/`Open`, TEXT storage.
   Acceptance: [ ] round-trip and legacy-read tests; [ ] rotation test (seal with
   key A, add key B, reseal, drop A); [ ] wrong-key and tamper failures; [ ]
   `purpose` still bound as AAD. Dependencies: ADR 0013 slice 4 (env rename),
   ADR 0015 cut-over slice. ADR 0020 slice 1 depends on this slice.
2. **Provider registry schema and OIDC port.** Scope: migration (`identity_providers`,
   generalised `user_identities`, `user_sessions.kind`, `users.disabled_reason`,
   copy of `oidc_settings`), `internal/identity` store, `OIDCRuntime` rewritten per
   provider on the `internal/jose` verifier, env seed,
   `api/openapi/management/authentication.yaml` provider CRUD,
   `api/openapi/protocols/oidc-login.yaml`, regenerate `internal/admin/openapi`.
   Acceptance: [ ] two OIDC providers sign in independently (Keycloak fixture
   plus a stub IdP); [ ] old `/authentication/oidc` removed; [ ] secret sealing
   round-trip test; [ ] `make openapi-check`. Dependencies: ADR 0018 slice 1,
   ADR 0019 slice 1, ADR 0015 cut-over slice.
3. **Group model and grant principals.** Scope: `user_groups`,
   `user_group_members`, `internal/authorization` (`Principal.GroupIDs`,
   `ManagedResourceDecision`), grants API accepting `group:<id>`, `global_role`
   replacing `OIDCRoleMapping`, claims-sourced group population for OIDC.
   Acceptance: [ ] table-driven authorization tests for user+group union, `none`
   hard stop, revoked membership, claims never matching privileged or other-provider
   groups; [ ] principal tokens carry no group ids; [ ] effective-access
   simulation includes groups; [ ] benchmark shows <1 ms added at 100 groups; [ ]
   `make openapi-check`. Dependencies: ADR 0018 slice 2 and ADR 0023 slice 12 (`internal/readcache`, ordered
   before this ADR by ADR 0013 step 6b); the `user_group_members(user_id, group_id)`
   index is part of this migration (ADR 0023 section 5) and the group cache is built
   on `internal/readcache`.
4. **SAML SP core.** Scope: `internal/identity/saml` wrapper over
   `gosaml2`/`goxmldsig`, ACS/login/metadata routes
   (`api/openapi/protocols/saml.yaml`), replay and request-state tables, attribute
   mapping. Acceptance: [ ] negative suite (wrapped assertion, unsigned, wrong
   audience/destination/recipient, expired, replay, SHA-1, unknown cert, DTD,
   EncryptedAssertion, bad Reference URI) all rejected; [ ] Go native fuzz target
   on response parsing run 60 s in CI, corpus committed; [ ] happy path against
   Keycloak SAML client and an Entra ID response fixture; [ ] CI check enforces
   `goxmldsig` >= v1.6.1 and `gosaml2` >= v0.12.0; [ ] `make dependency-audit`.
   Dependencies: ADR 0018 slice 2.
5. **SAML metadata import and rollover.** Scope: `:import-metadata` (URL with
   SSRF guard, XML), certificate list states, expiry warnings, scheduled prune
   task. Acceptance: [ ] SSRF tests for private ranges and redirects; [ ] two-cert
   rollover test; [ ] `make openapi-check`. Dependencies: ADR 0018 slice 4.
6. **SCIM server.** Scope: `api/openapi/protocols/scim.yaml`, `/scim/v2/*`, token
   table and admin API, filters, PATCH normaliser, audit events. Acceptance: [ ]
   golden Entra ID (flag on and off) and Okta provisioning transcripts pass; [ ]
   deactivate revokes sessions and tokens; [ ] admin accounts untouchable; [ ]
   local conflict `409`; [ ] scim_only first-login binding by `externalId`;
   [ ] token expiry cap enforced; [ ] fuzz target for the filter parser; [ ]
   coverage floors met. Dependencies: ADR 0018 slice 3.
7. **Console identity provider UI.** Scope: provider list/wizards for OIDC and
   SAML (Entra ID, Okta, Keycloak templates), SCIM token reveal-once dialog,
   group management and group principals in the grant editors, login chooser;
   en-CA and fr-CA strings per ADR 0014. Acceptance: [ ] `make console-test`,
   `make console-api-check`; [ ] Playwright login-chooser test
   (`make console-e2e`). Dependencies: ADR 0018 slices 2, 3, 6; ADR 0014.
8. **Docs and runbooks.** Scope: Entra ID, Okta, Keycloak setup guides, update
   `docs/user-governance.md` for SCIM/group lifecycle, security note on SLO
   absence. Acceptance: [ ] `make docs-check`. Dependencies: ADR 0018 slices 1-7.

## Open questions

- Is SAML Single Logout (and OIDC back-channel logout) a 0.2 requirement for any
  design-partner customer, which would need a session-to-IdP-session index?
- Do we need Entra ID `groups` overage resolution via Microsoft Graph for
  tenants that cannot run SCIM, or is "use SCIM" an acceptable answer?
- Should SCIM-managed groups be allowed to carry `global_role=admin` at all, or
  must platform administrators always be local/console-assigned?
- Should the key-ring (slice 1) later support an external KMS/OCI Vault key
  source? The env-based ring is the v1 decision and is shared with ADR 0020.
