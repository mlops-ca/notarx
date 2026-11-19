# OIDC Browser SSO

[简体中文](oidc-sso.zh-CN.md) | [Documentation index](README.md)

notarx supports two OIDC credential paths:

- bearer validation for CI and API clients;
- browser sign-in through Authorization Code + PKCE.

Identity providers live in a registry in PostgreSQL (ADR 0018): any number of OIDC
providers can be configured and enabled at once, each with its own issuer,
client, role mapping and provisioning policy. The Console login page reads
`GET /auth/idp` at runtime, which lists only the enabled providers (slug,
display name, kind), and offers one sign-in button per provider. Provider URLs
and client identifiers are therefore not compiled into the frontend bundle.
Each provider signs in on its own routes, `GET /auth/oidc/{slug}/login` and
`GET /auth/oidc/{slug}/callback`, with its own flow cookie, so two providers
never interfere. A disabled provider answers exactly like an unknown slug.

## Required configuration

```dotenv
NOTARX_OIDC_ISSUER=https://login.example.com/realms/acme
NOTARX_OIDC_AUDIENCE=notarx-api
NOTARX_OIDC_CLIENT_ID=notarx-console
NOTARX_OIDC_CLIENT_SECRET=
NOTARX_OIDC_REDIRECT_URL=https://gateway.example.com/auth/oidc/default/callback
NOTARX_OIDC_SCOPES=openid profile email
NOTARX_SETTINGS_ENCRYPTION_KEY=<32-byte-key>
```

Set `NOTARX_OIDC_CLIENT_SECRET` for a confidential client. Leave it empty for
a public client that requires PKCE. Issuer and JWKS endpoints must use HTTPS,
except for the loopback hosts `localhost`, `127.0.0.1`, and `::1` in local
development. The redirect URL must use HTTPS except for those same local
development callbacks. API-only bearer validation may leave both
`NOTARX_OIDC_CLIENT_ID` and `NOTARX_OIDC_REDIRECT_URL` empty; when browser
login is enabled, those two values must be configured together.

The environment variables only seed the registry: when the registry is empty at
startup they create one provider with slug `default`, so its callback URL is
`/auth/oidc/default/callback`. There is no ongoing merge; later changes to the
variables are ignored once a provider exists. Administrators manage providers
with the management API (`/api/v2/authentication/providers`: list, create, get,
replace, delete and `:test`, admin only) and, for the `default` provider, the
**Authentication** page of the Console. Replace and delete use versioned
`If-Match` updates; every response carries the version as an `ETag`. A provider
that still has linked identities cannot be deleted. The slug and kind of a
provider never change. `NOTARX_SETTINGS_ENCRYPTION_KEY` encrypts confidential
client secrets with AES-256-GCM. Responses expose only
`clientSecretConfigured` and never return plaintext or ciphertext.

Each provider has an ID token signature algorithm allow-list (default `RS256`;
`ES256` and `EdDSA` can be selected) and an attribute mapping. Entra ID uses
the issuer `https://login.microsoftonline.com/{tenant}/v2.0` and the stable
subject `tid:oid`, selected with `attributeMapping.subject: "tid:oid"`; the
Entra `groups` overage is not resolved through Graph.

The provider discovery document must contain matching `issuer`,
`authorization_endpoint`, and `token_endpoint` values. notarx never
stores provider access or refresh tokens. After validating the ID token,
issuer, audience, signature, expiry, state, and nonce, it creates a bounded
12-hour HttpOnly `SameSite=Lax` Gateway session cookie.

When the OIDC identity is linked to a local account, the signed cookie contains
a random session identifier backed by server-side metadata. The database stores
the identifier, account, login kind, bounded client address and user agent, and
lifecycle timestamps, but never the cookie, access token, refresh token, or ID
token. Administrators can inspect and revoke individual sessions from the user
detail view. `POST /auth/logout` revokes the current linked session before
clearing the browser cookie.

API Bearer tokens are checked against `NOTARX_OIDC_AUDIENCE`. Browser ID
tokens are checked independently against `NOTARX_OIDC_CLIENT_ID`, so the API
and Console may use separate audiences.

## Local account linkage

Validated OIDC identities can be bound to local accounts by identity provider and
stable subject (`sub`, or the mapped subject). The same subject string at two
providers is two different identities, and one account holds at most one
identity per provider. Administrators may manage links from the Users page. The
runtime Authentication settings can also enable just-in-time account creation,
choose its default role, and opt into linking an existing account by a unique
verified email claim. These provisioning options default to disabled.

Once linked, both OIDC Bearer and browser-session authentication use the local
account's current role and security state. Disabling the account, requiring a
password change, or incrementing its session version therefore takes effect on
the next authenticated request. Linked browser sessions additionally require an
active, unexpired server-side session record. An unlinked identity continues to
use the external-principal behaviour and a stateless browser session when JIT
provisioning is disabled.

### Register users before granting access

To manage all sign-ins from the Gateway Users page without granting automatic
repository access:

1. In **Authentication**, set **Unlinked identities** to **Provision on first sign-in**
   and **JIT default role** to **none · awaiting approval**. Leave verified-email
   linking off unless existing-account merging is explicitly required.
2. Ask each user to sign in again. Gateway creates one local account and OIDC
   binding keyed by the provider's issuer and stable subject. The account has
   no local password and appears in **Users** as **Pending**.
3. Open the user in **Users** and change the role to `member`, `reader`,
   `writer`, or `admin`. Choose `member` to approve the account without giving
   it any repository access, then grant repository access per repository. The
   user can choose **Check access** on the waiting screen to refresh their
   session view. Change the role back to **Pending** to revoke repository
   access, or disable the account to block sign-in.

The pending `none` role denies repository reads, writes, administration, and
intelligence even when legacy read defaults or repository grants would
otherwise allow them. An administrator subject or an explicitly mapped OIDC
role can bootstrap a higher role on first registration. Subsequent sign-ins
refresh identity metadata but do not overwrite an administrator-assigned
Gateway role. Existing linked accounts retain their role and state.

`reader`, `writer`, and `admin` are **global** Gateway roles; assigning
one gives its corresponding capability across repositories. `member` is the
opposite: it approves the account while granting no repository capability, so
its reach comes entirely from per-repository grants. Collapsing the remaining
global roles into per-repository grants is a separate design.

After approval, `reader` and `writer` can use the Console **Repositories**
page to browse repositories and artifacts for which they have read access.
`writer` can also use Raw file upload, the Maven publish wizard, and
OCI/npm/PyPI publish guides where repository write access applies. OCI,
Conan, Go, and APT publishing continues through their native protocol clients.
Docker/Podman publishing to OCI requires an administrator-issued
service-account credential with repository write access, delivered through a
secure channel; the SSO browser session is not a Docker credential. `reader` has no
upload or publish controls. Creating repositories, changing configuration,
managing grants, and managing users remain administrator operations.

## Keycloak

Create an OpenID Connect client, enable Standard Flow, and register the exact
Gateway callback URL. The browser ID token must include the client ID in its
`aud` claim; API Bearer tokens may use the separate Gateway API audience.

Realm roles, client roles, top-level `roles`, and `groups` claims can be mapped
onto Gateway account levels:

```dotenv
NOTARX_OIDC_MEMBER_ROLES=artifact-member
NOTARX_OIDC_ADMIN_ROLES=artifact-admin
```

A mapping approves the account rather than granting repository capability: a
matched administrator mapping makes the account an administrator, and a matched
member mapping approves it with no repository access of its own, because reach
comes from per-repository grants. Administrator wins when both match.

## GitLab

Register an OAuth/OpenID Connect application with the exact Gateway callback
URL, then use the GitLab issuer and application ID as the client ID. When role
mapping is required, include the relevant group claims in the ID token and map
their exact values through the same member and administrator variables above.

## Upgrade and rollback

The identity provider registry replaces the former `oidc_settings` singleton. The saved settings become the provider `default` (with their
sealed client secret and version), stored identities stay bound to it, and
`oidc_settings` is dropped. An identity whose issuer differs from the saved one
is bound to its own disabled provider so no link is lost. A stored JIT default
role of `admin` becomes `member`: an IdP-driven first login never mints an
administrator. The `/api/v2/authentication/oidc` operations and
`GET /auth/oidc/config`, `/auth/oidc/login` and `/auth/oidc/callback` are
removed without aliases; update the redirect URI registered at the identity
provider to `/auth/oidc/{slug}/callback`. `notarxctl secrets reseal` covers the
sealed secret of every provider.

The saved OIDC settings move to a single member role list. The reader list and
the writer list merge into it, a stored JIT default of `reader` or `writer`
becomes `member`, and `NOTARX_OIDC_READER_ROLES` and `NOTARX_OIDC_WRITER_ROLES`
are replaced by `NOTARX_OIDC_MEMBER_ROLES`. An external realm role listed in
either legacy variable has to be listed in the new one to keep matching.

The migration replaces the reader and writer columns rather than leaving them
behind, so an application-only rollback — deploying the previous image against
the migrated schema — cannot read the saved settings. Roll back by restoring the
pre-upgrade database and the matching binary, as the APT lifecycle migration
also requires.

## Operational checks

`notarx preflight` reports `oidc_enabled`, `oidc_browser_login_enabled`, and
whether the runtime settings encryption key is configured. The Console can
test the saved discovery document from the Authentication page. Runtime saves
take effect immediately on the current node; other API nodes observe the new
database version after the bounded settings-cache window. No Gateway restart
or Console rebuild is required.

For a real Keycloak browser callback check in the local Kubernetes environment,
see [Keycloak Kubernetes acceptance](oidc-keycloak-k8s.md).
