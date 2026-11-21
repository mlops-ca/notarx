# notarx as an OIDC issuer and Microsoft Entra setup

[Documentation index](README.md) · [ADR 0020](adr/0020-notarx-oidc-issuer.md) · [Workload identity federation](workload-identity.md)

notarx can be an OpenID Connect token issuer. It signs short-lived JWT assertions with keys that
live (sealed) in PostgreSQL and publishes the matching public keys, so another system can verify
them without a shared secret. Two consumers use this:

- **Microsoft Entra ID** exchanges a notarx assertion for an Entra access token (workload identity
  federation), so a proxy repository can read Azure Blob Storage, Azure Artifacts or Azure Container
  Registry without a stored client secret. This is the `entra-workload` upstream scheme.
- **Another notarx instance** can verify an assertion as a Bearer JWT through
  [workload identity federation](workload-identity.md), which lets two instances trust each other
  without a shared secret.

The issuer is off by default. While `NOTARX_ISSUER_ENABLED` is not `true`, no route, worker, minting
endpoint or upstream scheme exists, so air-gapped installs expose nothing. Configuration, key
lifecycle and the anonymous endpoints are described under
[OIDC issuer signing keys](distributed-deployment.md#oidc-issuer-signing-keys); this page is the
operator procedure.

## What an assertion is

An assertion is an RS256 or ES256 JWT with these claims and no others:

| Claim | Value |
| --- | --- |
| `iss` | `NOTARX_ISSUER_URL` |
| `sub` | the assertion subject of the service account (below) |
| `aud` | the requested audience, one string |
| `iat`, `nbf`, `exp` | now, now, and now plus the lifetime (at most 10 minutes, default 5) |
| `jti` | a random 128-bit id, recorded in the audit log |
| `notarx_sa` | the service account id |
| `notarx_cred` | `api_key` or `workload_federation`: how the service account authenticated |

There are no email, groups or roles claims, and a caller never chooses `sub`.

### Who may mint

`POST /api/v2/service-accounts/{id}/assertions` with `{"audience": "...", "lifetimeSeconds": 300}` is
the outbound minting endpoint. It is separate from the inbound `POST /auth/token-exchange` of
workload identity federation. The caller must be the service account itself, authenticated with one
of its API keys or as a federated workload principal that resolves to it. A workload principal can
therefore mint assertions for the account it maps to: the blast radius of a trust policy, and of every
API key of the account, includes this capability. There is no narrower scope for minting; the audience
allow-list is the control, so keep it as short as the use requires. Human users, the static administrator token and platform API keys receive 403, so
a human session cannot be turned into a workload assertion.

### Audience allow-lists

Minting is off for an account until an administrator allows an audience for it. There is no
wildcard and no default list. A request for an audience that is not on the list answers 403 with the
problem code `assertion_audience_not_allowed`.

```sh
notarxctl service-account assertion-audience add SERVICE_ACCOUNT_ID \
  --audience api://AzureADTokenExchange --max-lifetime 5m
notarxctl service-account assertion-audience list SERVICE_ACCOUNT_ID
notarxctl service-account assertion-audience remove SERVICE_ACCOUNT_ID \
  --audience api://AzureADTokenExchange --yes
```

Each entry caps the lifetime (1 to 10 minutes); the global ceiling `NOTARX_ISSUER_MAX_TOKEN_LIFETIME`
(default and maximum `10m`) caps every request. Changes to the list and every mint are audited:
the mint event names the service account, audience, `jti`, key id and credential id, never the
token. A refused mint (an audience that is not allowed, a lifetime above the cap) is audited too, as an
access denial, so a credential that probes for allowed audiences leaves a trail.

### The assertion subject

Entra matches `iss`, `sub` and `aud` by exact string, and the federated credential is created by
hand, so the subject must be predictable and must never change silently.

- The subject is stored when the service account is created. The default is
  `notarx:service-account:<name>`; `notarxctl service-account show` prints it.
- It is immutable. Renaming an account does not change it.
- While the issuer is enabled, a name with characters outside `[A-Za-z0-9._:-]` is refused unless you
  pass a custom subject (`notarxctl service-account create NAME --assertion-subject SUBJECT`). Use a
  custom subject to keep an existing Entra federated credential working.
- Subjects are reserved for good. A subject that an account holds or once held cannot be given to
  another account, so a new account of the same name cannot inherit the trust that was built on the
  old one. An account created before the issuer existed received the default subject when its name
  allows one, and has none otherwise.
- Only an administrator can free a reservation. `notarxctl service-account assertion-subject list` shows every
  reserved subject and flags the orphaned ones, whose service account was deleted. `notarxctl service-account
  assertion-subject release SUBJECT --yes` removes an orphaned reservation and is audited
  (`service_account.assertion_subject.release`); a subject held by a live account answers 409
  `assertion_subject_held`. Release a subject only when the Entra federated credential built on it is gone,
  because the next account that takes it inherits that trust.

## Entra setup

1. Publish notarx at a public HTTPS `NOTARX_ISSUER_URL` and set `NOTARX_ISSUER_ENABLED=true`
   (`NOTARX_ISSUER_ALG` stays `RS256`: Entra documents RS256 issuers only). Verify:

   ```sh
   curl -s "$NOTARX_ISSUER_URL/.well-known/openid-configuration"
   curl -s "$NOTARX_ISSUER_URL/.well-known/jwks.json"
   notarxctl issuer status
   ```

2. Choose the identity that signs. Either create a service account, for example `acr-puller`, and
   allow the Entra audience for it (see above), then note its subject with
   `notarxctl service-account show`; or use the built-in instance principal
   `notarx:instance:<deployment-id>`, which needs no service account (below).
3. In Entra, create an App registration (single tenant). Record the tenant id and the application
   (client) id. Do not create a client secret.
4. In the app registration open Certificates and secrets, Federated credentials, Add credential,
   scenario "Other issuer", and enter:
   - **Issuer**: `NOTARX_ISSUER_URL`, exactly.
   - **Subject identifier**: the subject from step 2, exactly.
   - **Audience**: `api://AzureADTokenExchange` (the default). National clouds use
     `api://AzureADTokenExchangeUSGov` or `api://AzureADTokenExchangeChina`, or override it with
     `NOTARX_ENTRA_AUDIENCE`.

   Entra fetches the discovery document and the key set from the issuer when it exchanges a token, so
   the URL must be reachable from the internet: public DNS and a valid certificate.
5. Grant the app's service principal the data-plane role it needs, for example Storage Blob Data
   Reader on a storage account or AcrPull on a registry. Scope roles minimally: Entra trusts every
   token with a matching issuer, subject and audience.
6. Configure the proxy repository's upstream authentication with the scheme `entra-workload`, the
   tenant id, the client id, the scope (for example `https://storage.azure.com/.default`, or
   `https://management.azure.com/.default` with `registryKind: acr` for Azure Container Registry)
   and, when you chose a service account, its subject. The scheme needs no secret and is refused while
   the issuer is disabled. Every exchange is audited with the subject, tenant, client and repository.
7. Rotation needs no change in Entra beyond waiting for key propagation (below). A changed issuer URL
   or subject requires a new federated credential.

### One credential for the instance principal

When `subject` is not set, an upstream signs as the instance principal
`notarx:instance:<deployment-id>`. That subject is a single Entra federated credential that every
upstream bound to it shares, which keeps you well inside the limit of 20 federated credentials per
app registration. A dedicated service account per upstream needs one credential each, so use that only
when you need to separate trust. The deployment id is created at first start and is the same on every
node.

## Key rotation and propagation

`NOTARX_ISSUER_KEY_ROTATION_INTERVAL` (default 720 hours) decides how long a key signs. A new key is
generated one publish lead (`NOTARX_ISSUER_KEY_PUBLISH_LEAD`, default 24 hours) before it is needed,
published in the key set the whole time, and only then promoted, so verifiers already trust it when it
first signs. Microsoft documents no cache lifetime for federated credential keys, only a region-wide
propagation delay, so treat 24 hours as a heuristic that must exceed the `Cache-Control` max-age of
the key set (300 seconds). A retired key stays published until every token it signed has expired.

```sh
notarxctl issuer status                 # keys, states and timestamps
notarxctl issuer rotate --yes           # promote the next key now
notarxctl issuer revoke KID --yes       # emergency: the key leaves the key set at once
notarxctl issuer reseal --yes           # re-seal the signing keys after an encryption key change
```

Wait for propagation before relying on a key that you promoted by hand: `rotate` prints a warning when
the promoted key was published for less than the lead time. `revoke` removes a compromised key from
the key set and replaces it at once. A token that Entra already exchanged stays valid until its own
expiry, and notarx has no per-token deny list. A notarx peer that trusts this issuer refreshes its copy
of the key set within 60 seconds, so a revoked key stops working there well inside the 10 minute
assertion lifetime.

## Root of trust and backups

- `NOTARX_SETTINGS_ENCRYPTION_KEY` plus read access to the database yields the private signing keys
  and therefore every identity federated through them. Treat both as signing-key equivalent: escrow
  the key separately from the database, restrict who can read either, and never log them.
- Database write access alone cannot inject a trusted key: each key row carries an integrity MAC keyed
  from the encryption key, and a row that fails it never signs and is left out of the key set.
- Changing the encryption key without re-sealing makes signing fail closed. After you move the old key to
  `NOTARX_SETTINGS_ENCRYPTION_KEY_PREVIOUS`, run `notarxctl issuer reseal --yes` (or
  `notarxctl secrets reseal --yes`, which covers every sealed column) before you remove the old key.
- Backups hold the private keys only as ciphertext. **A restore needs the same encryption key ring**
  that sealed them; without it the restored deployment cannot sign, and the federated credentials you
  created in Entra keep trusting the same public keys. Escrow the key ring with the backup procedure of
  [the recovery runbook](recovery-runbook.md).

## A changed issuer URL is disruptive

The issuer string appears in every token and in the discovery document, and Entra matches it exactly.
Changing `NOTARX_ISSUER_URL` invalidates all outstanding trust: every federated credential that names
the old URL stops working until it is recreated, and every peer that trusts the old issuer must be
updated. notarx logs a warning at start when the URL differs from the one it last used. Treat the URL
as permanent and put it behind a stable DNS name.

## notarx-to-notarx trust

Instance B registers instance A as a trusted issuer in
[workload identity federation](workload-identity.md): the issuer is A's `NOTARX_ISSUER_URL`, discovery
is enabled and the allowed audience is B's own issuer URL (which defaults to `NOTARX_PUBLIC_URL`). A trust
policy on B maps the subject of A's service account, for example `notarx:service-account:promote-to-b`,
to a service account on B with the grants it needs. A mints an assertion for the audience that is B's
URL and sends it to B as a Bearer JWT; no exchange step is required. The audience allow-list on A names
B's URL, so a service account on A cannot obtain assertions for arbitrary peers, and no secret is shared.

## Console

The service accounts page shows the assertion subject and the audience allow-list of the selected
account, and lets an administrator allow or remove an audience. Creating an account has an optional
field for a custom assertion subject.
