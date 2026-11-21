# Identity provider setup guides

[Documentation index](README.md)

Step-by-step setup of Microsoft Entra ID, Okta and Keycloak as human identity providers for the Console and
the API, over OpenID Connect (OIDC) or SAML 2.0. Both protocols register through the same provider registry
(ADR 0018, `/api/v2/authentication/providers`, or the Console Identity providers page). Protocol details live in
[OIDC SSO](oidc-sso.md) and [SAML SSO](saml-sso.md); user groups are described in
[Local user governance](user-governance.md).

Every provider has a slug (`corp`, `entra`, ...). The slug appears in the callback URLs, so choose it before you
register anything at the identity provider. Set `NOTARX_PUBLIC_URL` (https) first: every URL below derives from it.

| Protocol | URL to register at the identity provider |
| --- | --- |
| OIDC redirect URI | `$NOTARX_PUBLIC_URL/auth/oidc/{slug}/callback` |
| SAML SP metadata | `$NOTARX_PUBLIC_URL/auth/saml/{slug}/metadata` |
| SAML ACS (reply) URL | `$NOTARX_PUBLIC_URL/auth/saml/{slug}/acs` |
| SAML SP entity id | the metadata URL above |

Register the provider disabled, verify it, then enable it. A provider that is disabled or unknown answers 404
and does not appear on the sign-in page.

## OIDC

All three identity providers use the Authorization Code flow with PKCE and a confidential client. The client
secret is sealed with `NOTARX_SETTINGS_ENCRYPTION_KEY` and never returned; `notarxctl secrets reseal` rewrites it
under the current key.

```sh
curl -sS -X POST "$NOTARX/api/v2/authentication/providers" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"slug":"corp","kind":"oidc","displayName":"Corp SSO","enabled":false,
       "oidc":{"issuer":"ISSUER","audience":"CLIENT_ID","clientId":"CLIENT_ID","clientSecret":"SECRET",
               "redirectUrl":"https://notarx.example.com/auth/oidc/corp/callback",
               "scopes":["openid","profile","email"]},
       "provisioningMode":"jit","jitDefaultRole":"none"}'
```

The Console wizard offers the same fields with the issuer and attribute defaults of each template below.
`jitDefaultRole` is `none` or `member`: a first sign-in never creates an administrator.

### Microsoft Entra ID (OIDC)

1. In the Microsoft Entra portal, App registrations, New registration. Platform Web, redirect URI
   `$NOTARX_PUBLIC_URL/auth/oidc/{slug}/callback`.
2. Certificates and secrets: create a client secret and copy its value.
3. Issuer: `https://login.microsoftonline.com/{tenant}/v2.0` (a tenant id, never `common`). Audience and client id:
   the application (client) id.
4. Subject: set the attribute mapping `subject` to `tid:oid`. The `oid` claim is stable per user and tenant,
   while `sub` is pairwise per application and changes when you register another app.
5. Groups: Token configuration, Add groups claim. Entra omits the claim and sends `_claim_names` when a user is in
   more than 200 groups (see [Groups overage](#groups-overage-microsoft-entra-id)).

### Okta (OIDC)

1. Applications, Create App Integration, OIDC, Web Application. Sign-in redirect URI
   `$NOTARX_PUBLIC_URL/auth/oidc/{slug}/callback`; assign the people or groups that may sign in.
2. Issuer: your Okta org URL, or its custom authorization server (`https://example.okta.com/oauth2/default`).
   Client id and secret come from the application.
3. Groups: add a Groups claim to the ID token (filter by name or regex) so that the `groups` claim is present.

### Keycloak (OIDC)

1. Clients, Create client, OpenID Connect, enable Client authentication and the Standard flow. Valid redirect
   URI `$NOTARX_PUBLIC_URL/auth/oidc/{slug}/callback`.
2. Issuer: `https://keycloak.example.com/realms/{realm}`. The ID token must carry the client id in `aud`.
3. Groups: add the Group Membership mapper to the client scope with the claim name `groups` and Full group
   path turned off.

## SAML

notarx is a service provider for SP-initiated Web SSO only (HTTP-Redirect request, HTTP-POST response). Create the
provider disabled, import the IdP metadata, then register the SP metadata at the IdP:

```sh
curl -sS -X POST "$NOTARX/api/v2/authentication/providers" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"slug":"corp","kind":"saml","displayName":"Corp SSO","enabled":false,
       "saml":{"entityId":"","ssoUrl":"https://idp.example.com/sso","certificates":[]},
       "provisioningMode":"jit"}'
curl -sS -X POST "$NOTARX/api/v2/authentication/providers/$ID:import-metadata" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"url":"https://idp.example.com/metadata"}'
```

The import reads the entity id, the HTTP-Redirect SSO endpoint and the signing certificates. Responses and
assertions must be signed (SHA-1 is refused) and unencrypted. The default NameID format is `persistent`.

### Microsoft Entra ID (SAML)

1. Enterprise applications, New application, Create your own application (non-gallery), then Single sign-on, SAML.
2. Basic SAML Configuration: Identifier (Entity ID) is the SP entity id and Reply URL is the ACS URL from the table.
3. Attributes and claims: the defaults already emit the claim URIs that notarx maps (email address, name, and
   `http://schemas.microsoft.com/ws/2008/06/identity/claims/groups` once you add a groups claim).
4. SAML Certificates: copy the App Federation Metadata Url into `:import-metadata`.

### Okta (SAML)

1. Applications, Create App Integration, SAML 2.0. Single sign-on URL is the ACS URL, Audience URI is the SP entity
   id, Name ID format Persistent.
2. Attribute statements `email`, `displayName`; a Group Attribute Statement named `groups` with a filter.
3. Copy the Metadata URL of the application into `:import-metadata`.

### Keycloak (SAML)

1. Clients, Import client (the SP metadata URL) or Create client with type SAML and the SP entity id. Valid redirect
   URI and Master SAML Processing URL: the ACS URL. Turn on Sign assertions or Sign documents, keep client
   signature required off unless you set `signRequests`.
2. Name ID format persistent. Add a User Attribute mapper for `email` and a Group list mapper named `groups`.
3. Import `https://keycloak.example.com/realms/{realm}/protocol/saml/descriptor`.

## SAML configuration keys

| Key | Default | Meaning |
| --- | --- | --- |
| `NOTARX_SAML_CLOCK_SKEW` | `60s` (at most `5m`) | Tolerance when checking assertion validity windows. The assertion replay window is `NotOnOrAfter` plus this value. |
| `NOTARX_SAML_REQUEST_TTL` | `10m` | Lifetime of a stored AuthnRequest id. A login must complete within it. |
| `NOTARX_SAML_MAX_RESPONSE_BYTES` | `1048576` | Largest accepted SAML response. |
| `NOTARX_SAML_METADATA_REFRESH_INTERVAL` | `24h` | How often URL-imported metadata is refreshed, for providers that opt in. |
| `NOTARX_PUBLIC_URL` | none | Required for SAML; the entity id, ACS and metadata URLs derive from it. |
| `NOTARX_SETTINGS_ENCRYPTION_KEY` | none | Seals the OIDC client secret and the SAML service provider signing key. |

### Key and certificate rotation

- IdP signing certificates: a metadata refresh adds a new certificate as `next`; both verify until you replace the
  provider settings to drop the retired one. The Console warns 30 days before a certificate's `notAfter`.
- Service provider signing key (`signRequests`): `POST /api/v2/authentication/providers/{id}:rotate-sp-key`
  generates a new key and keeps the previous one published in the SP metadata until the next rotation. Re-import
  the SP metadata at the IdP after rotating.
- Encryption key: add the new key to `NOTARX_SETTINGS_ENCRYPTION_KEY`, keep the old one in
  `NOTARX_SETTINGS_ENCRYPTION_KEY_PREVIOUS`, restart, run `notarxctl secrets reseal --yes`, then drop the previous
  key. Sealed OIDC client secrets and SAML signing keys are rewritten.

## Security notes

### Sessions and Single Logout

notarx does not support SAML Single Logout, and OIDC back-channel logout is not implemented. Signing out of notarx
revokes the notarx session only; the IdP session stays signed in, and signing out at the IdP does not end the
notarx session. A notarx session lasts up to 12 hours (`UserSessionLifetime`). To cut access sooner, disable the
user (a disabled account is refused regardless of groups) or revoke its sessions
(`notarxctl user sessions`). The Console states this at logout.

### Groups overage (Microsoft Entra ID)

Entra replaces the `groups` claim with `_claim_names` when the user belongs to too many groups. notarx does not
call Microsoft Graph: an overage yields no groups at all, never a partial list, writes an
`auth.oidc.groups_overage` audit event, and the Console shows a warning on the provider. Alternatives:

- SCIM provisioning of groups from Entra (recommended for large directories; follows with the SCIM server).
- Emit app roles instead of groups, or restrict the claim to groups assigned to the application, and name them in
  the groups attribute mapping.
- Create local user groups and manage their members in notarx.

### Account linking

A first sign-in creates an account only when the provider provisions just in time. An asserted email address links
an existing account only with `emailLinkingEnabled` and an administrator-verified email; administrators and
accounts with a local password are never linked automatically. See [SAML SSO](saml-sso.md#accounts).
