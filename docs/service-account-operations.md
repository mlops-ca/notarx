# Service Account Operations

[简体中文](service-account-operations.zh-CN.md) | [Documentation index](README.md)

Service Accounts are stable, non-human identities for CI systems and external
applications. A Repository Grant binds to `service-account:<id>`; one or more
short-lived credentials authenticate as that same principal. Rotating a
credential therefore does not require changing Repository Grants.

Use a Service Account for Jenkins, GitLab CI, release automation, scanners, or
another application that must retain one auditable identity across credential
rotation. Keep standalone API Keys for bounded management clients that need a
global role. Do not create a human User for unattended automation.

## Security model

- A Service Account has no global role. It can reach only Repositories granted
  to its stable principal.
- Credential plaintext is returned once. The Gateway stores only its verifier.
- A credential expires within 365 days and may be revoked independently.
- Disabling the Service Account rejects all of its direct credentials and any
  short-lived OCI principal token on the next request.
- Revoking one credential does not interrupt a newer overlapping credential.
  An OCI principal token already exchanged from the old credential can live
  for at most its five-minute protocol-token lifetime.
- Management audit records identify account and credential lifecycle events,
  but never contain the plaintext token.

Prefer [workload identity federation](workload-identity.md) where the CI platform issues
signed workload tokens (GitHub Actions, GitLab CI, Kubernetes): a trust policy maps the
platform token to this Service Account, so no stored secret is needed at all.

Store credentials in the CI platform's secret store. Never put them in a
Jenkinsfile, npm configuration committed to Git, Maven `settings.xml` in the
repository, a container image, shell tracing, or a release record.

## OIDC assertions

A Service Account can also mint short-lived signed JWT assertions for an audience that an administrator
allowed for it, for example to authenticate to Microsoft Entra ID without a stored secret. Its
assertion subject is stored at creation and never changes, including when the account is renamed. See
[notarx as an OIDC issuer](oidc-issuer.md) for the audience allow-list, the subject rules and the Entra procedure.

## Create and grant

Create an account in **Management → Service Accounts**, then issue its first
credential. Copy the token immediately; it cannot be retrieved later.

Open the target Repository's **Access Grants** tab, select the Service Account,
and grant the minimum scope and resource prefix. The stored principal looks
like this:

```text
service-account:2f8b5e9d-7f52-4e54-956a-c82860f3ae67
```

The Console's Access Control evaluator can verify the same principal before a
pipeline is enabled.

## Client configuration

Service Account credentials support direct Bearer authentication and HTTP
Basic authentication for native clients. When a client requires a username,
use a descriptive non-empty label such as `jenkins`; authorization always uses
the stable Service Account principal, not that label.

Maven `settings.xml`:

```xml
<server>
  <id>notarx</id>
  <username>jenkins</username>
  <password>${env.NOTARX_TOKEN}</password>
</server>
```

npm project or user configuration should inject the token at runtime:

```ini
registry=https://gateway.example.com/npm/npm-group/
//gateway.example.com/npm/npm-group/:_authToken=${NOTARX_TOKEN}
always-auth=true
```

OCI clients exchange the same credential through the standard Registry token
flow:

```sh
printf '%s' "$NOTARX_TOKEN" |
  docker login gateway.example.com --username jenkins --password-stdin
```

PyPI clients may use a non-empty username and the credential as the password.
Prefer a generated, ephemeral configuration file and delete it after the job.

## Zero-downtime rotation

1. Issue a new credential while the old credential remains active.
2. Add the new token to the CI secret store without changing the Repository
   Grant.
3. Run a read and, where authorized, a publication with the new credential.
4. Revoke the old credential.
5. Confirm the old credential receives `401`, the new credential still works,
   and the audit shows the expected stable `service-account:<id>` actor.
6. Remove the old value from the CI secret store.

Run `make service-account-rotation-e2e` on every release candidate. The gate
uses an isolated Gateway, PostgreSQL, and RustFS stack and verifies overlapping
credentials, a stable Repository Grant, native Raw publication/read through
Basic authentication, individual revocation, account disable, and sanitized
audit output.

## Incident response

- Revoke one credential when only that secret is suspected.
- Disable the Service Account when the application identity itself is no
  longer trusted; this blocks all credentials at once without deleting audit
  history or Repository Grants.
- Review `service_account.credential.create`,
  `service_account.credential.revoke`, and `service_account.update` audit
  records.
- Re-enable an account only after issuing fresh credentials and removing every
  affected value from downstream secret stores.
