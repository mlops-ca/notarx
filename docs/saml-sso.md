# SAML 2.0 browser SSO

[Documentation index](README.md)

notarx is a SAML 2.0 service provider (SP) for browser sign-in (ADR 0018, section 3). It speaks the
Web SSO profile only: SP-initiated login through the HTTP-Redirect binding and responses through the
HTTP-POST binding. Single Logout is not supported; signing out revokes the notarx session only.

## Before you start

- Set `NOTARX_PUBLIC_URL` (https). The SP entity id, the assertion consumer service (ACS) URL and the
  metadata URL derive from it: `/auth/saml/{slug}/metadata` and `/auth/saml/{slug}/acs`.
- Set `NOTARX_SETTINGS_ENCRYPTION_KEY`. The service provider signing key is stored sealed with it and is
  rewritten by `notarxctl secrets reseal`.
- Optional limits: `NOTARX_SAML_CLOCK_SKEW` (default 60s, at most 5m), `NOTARX_SAML_REQUEST_TTL` (10m),
  `NOTARX_SAML_MAX_RESPONSE_BYTES` (1048576) and `NOTARX_SAML_METADATA_REFRESH_INTERVAL` (24h).

## Register a provider

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

`:import-metadata` takes an HTTPS URL (fetched through the SSRF guard: no private, loopback or link-local
address, https only, 5 s timeout, 1 MiB, redirects stay on the host) or pasted `xml`. It reads the entity id,
the HTTP-Redirect SSO endpoint and the signing certificates. Then register the SP metadata
(`GET /auth/saml/{slug}/metadata`) at the identity provider and enable the provider. A disabled or
unknown slug answers 404.

## What the verifier enforces

The verifier runs its own checks before and after the signature library: exactly one Assertion that is a
direct child of the Response, no EncryptedAssertion, no DTD, a signature whose single Reference points at
the ID of the element that carries it, RSA-SHA256/384/512 or ECDSA-SHA256 only (no SHA-1), a certificate
equal to one pinned on the provider (never taken from the message), Issuer, Destination, Recipient and
Audience matches, bearer SubjectConfirmation, InResponseTo on the Response and the confirmation, and the
validity window with the configured skew. The AuthnRequest id is a single-use row that the ACS consumes,
and an assertion id is refused a second time until its `NotOnOrAfter` plus the skew. IdP-initiated
responses are refused unless the provider sets `allowIdpInitiated` with a fixed `relayStateAllowlist`.
The browser sees a generic error; the precise reason is written to the audit log and never contains
assertion content.

## Certificate rollover and signing keys

`settings.certificates` lists `{pem, notAfter, state}`; any listed certificate verifies a response.
A metadata import or refresh adds a certificate the provider does not pin yet as `next`, so both the old
and the new certificate verify while the IdP cuts over. Replace the provider to drop the retired one. The
console warns 30 days before a certificate's `notAfter`.

With `signRequests` on, AuthnRequests are signed with a notarx-generated RSA key. `POST
/authentication/providers/{id}:rotate-sp-key` generates a new key; the previous key stays published in the
SP metadata, so two keys are published until the next rotation. The private key is never returned.

## Accounts

The identity key is the NameID of the provider (lower-cased for the `emailAddress` format; `transient` is
refused). A first sign-in creates an account only when the provider provisions just in time. An asserted
email address never links an existing account: there is no provider-verified email in a SAML response, and
the admin-verified email flag that the ADR requires for email linking does not exist yet, so an
administrator links accounts explicitly.
