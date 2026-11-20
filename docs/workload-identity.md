# Workload identity federation

[Documentation index](README.md) · [ADR 0019](adr/0019-workload-identity-federation.md)

CI systems and cluster workloads can sign in to notarx with the short-lived signed token
their own platform issues, instead of a service-account API key copied into a CI variable.
notarx verifies the token against a trusted issuer, matches its claims against a trust
policy and acts as the service account the policy names. The service account owns the
repository grants, so a policy confers nothing by itself. Rotation, storage and leakage of
a long-lived secret disappear: a token is valid for minutes.

## Concepts

- **Trusted issuer.** A platform whose tokens notarx verifies: GitHub Actions, GitLab CI, a
  Kubernetes cluster or any OIDC issuer (`generic`). It lists the accepted audiences, the
  allowed signature algorithms (`RS256`, `ES256`, `EdDSA`) and where its keys come from:
  OIDC discovery, or a static key set for a private API server. An issuer URL that is already
  a human identity provider is refused.
- **Trust policy.** A list of claim conditions, all of which must hold, that maps a verified
  token to one service account. Operators are `eq`, `in`, `glob` and `prefix`; there are
  no regular expressions. Policies are validated on write against the issuer kind (below).
- **Service account.** The identity that holds the grants. A policy that maps onto a service
  account with an administrator grant needs an explicit `--allow-admin`.

## Mandatory conditions

A policy that one owner or namespace id alone satisfies would let every workflow of that owner
publish, so each kind requires immutable identifiers plus a restriction on where the code runs.

| Kind | Mandatory conditions |
| --- | --- |
| `github_actions` | `repository_id` (or `repository_owner_id` with `repository`), and at least one of `ref`, `environment`, `job_workflow_ref`, or `--allow-any-ref` |
| `gitlab_ci` | `project_id` (or `namespace_id` with `project_path`), and `ref_protected` = `"true"` or a `ref` condition, or `--allow-any-ref` |
| `kubernetes` | `sub` equal to `system:serviceaccount:<namespace>:<name>` |
| `generic` | `sub` (any operator) |

Add `event_name`, `environment` and `job_workflow_ref` conditions so a pull request or a fork
cannot publish. A bare `*` or `**` glob on `sub` is rejected.

## Signing in

A workload token reaches notarx in three ways.

1. **Bearer.** `Authorization: Bearer <jwt>` works on every protocol that accepts a bearer
   token (npm, the management API, the Hugging Face route).
2. **Basic password.** Use the token as the password with the username `oidc` (or the name of
   a policy, which pins that policy). This serves `docker login`, Maven, pip and twine, and cargo.
   The username `oidc` is reserved; no policy can have that name.
3. **Token exchange.** `POST /auth/token-exchange` (RFC 8693) returns a notarx token valid for
   at most 15 minutes (`NOTARX_WORKLOAD_EXCHANGE_MAX_TTL`, up to 1 hour; a policy can lower it).
   Chatty clients should exchange once instead of verifying a token per request. `notarxctl auth login
   --workload-token-file FILE` does it for you.

Every verification or policy failure answers the same `invalid_grant`; the failing condition
goes to the audit log only. An exchanged token is revalidated against its policy, issuer and
service account on every use, so disabling any of them takes effect within the authorization
cache window (`NOTARX_AUTHZ_CACHE_TTL`, 5 seconds by default). A token cannot be exchanged again.

Replay protection is optional: a policy with `--single-use` accepts each token once (the token
needs a `jti`). Use it for policies reached only through the exchange. Registry logins and
chatty clients present one token many times and must not use it.

## GitHub Actions

Give the job `permissions: id-token: write`, fetch the token for the notarx audience and log in.

```yaml
permissions:
  id-token: write
  contents: read
jobs:
  publish:
    runs-on: ubuntu-latest
    environment: packages-publish
    steps:
      - name: Sign in to notarx
        run: |
          jwt=$(curl -sSf -H "Authorization: bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \
            "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=https://notarx.example.com" | jq -r .value)
          echo "::add-mask::$jwt"
          printf '%s' "$jwt" | docker login notarx.example.com -u oidc --password-stdin
```

Register the issuer and a policy bound to the repository id, the environment and the branch:

```sh
notarxctl workload issuer create github --kind github_actions \
  --issuer https://token.actions.githubusercontent.com --audience https://notarx.example.com
notarxctl workload policy create gh-publish --issuer ISSUER_ID --service-account ACCOUNT_ID \
  --condition repository_id:eq:1296269 --condition environment:eq:packages-publish \
  --condition ref:eq:refs/heads/main --max-token-ttl 10m
```

Instead of `docker login`, POST the token to `/auth/token-exchange` and use the returned bearer
for Maven or npm. The release publisher of the Debian and RPM feeds is one such policy: the service
account `feeds-publisher`, bound to the repository, the workflow, the environment `packages-publish`
and the ref.

## GitLab CI

Declare an ID token with the notarx audience and sign in with it.

```yaml
publish:
  id_tokens:
    NOTARX_TOKEN:
      aud: https://notarx.example.com
  script:
    - printf '%s' "$NOTARX_TOKEN" | docker login notarx.example.com -u oidc --password-stdin
```

```sh
notarxctl workload issuer create gitlab --kind gitlab_ci --issuer https://gitlab.com --audience https://notarx.example.com
notarxctl workload policy create gl-publish --issuer ISSUER_ID --service-account ACCOUNT_ID \
  --condition project_id:eq:42 --condition ref_protected:eq:true
```

A self-hosted GitLab with a private certificate authority takes `--ca-bundle-file` (and
`--allow-private-network` when it sits on a private address).

## Kubernetes

Project a service account token with the notarx audience and a short life. The ready-made
manifest is `deploy/workload-identity/kubernetes-projected-token.yaml`.

```yaml
volumes:
  - name: notarx-token
    projected:
      sources:
        - serviceAccountToken:
            path: token
            audience: https://notarx.example.com
            expirationSeconds: 600
```

When the API server is private, register the cluster with its key set; notarx then never calls out.
The policy binds the subject:

```sh
kubectl get --raw /openid/v1/jwks > jwks.json
notarxctl workload issuer create cluster --kind kubernetes --issuer https://kubernetes.default.svc.cluster.local \
  --audience https://notarx.example.com --jwks-mode static --static-jwks-file jwks.json
notarxctl workload policy create k8s-builder --issuer ISSUER_ID --service-account ACCOUNT_ID \
  --condition sub:eq:system:serviceaccount:team-a:builder
```

A static key set never rotates on its own: update it when the cluster rotates its signing key.

## Operating it

- `notarxctl workload issuer probe --id ISSUER_ID` (and `make preflight`, check `workload_issuers`) fetch the
  discovery document and key set and report the key ids and algorithms.
- `notarxctl workload policy test --token-file FILE` shows which policy matches and which condition fails,
  without changing anything. The Console page **Workload identity** offers the same tester, policy templates
  per issuer kind and issuer and policy management.
- Every success and failure is an audit event with the issuer, policy id, service account, `jti` hash,
  subject and the claims the matched conditions refer to. The raw token is never recorded. The metric
  `notarx_workload_auth_total{outcome}` counts decisions.
- `make workload-identity-e2e` runs a GitHub-style mock issuer and a projected Kubernetes token against a real gateway.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `NOTARX_WORKLOAD_ENABLED` | `false` | Accept workload tokens and serve `/auth/token-exchange` |
| `NOTARX_WORKLOAD_CLOCK_SKEW` | `60s` | Leeway for `exp`, `nbf` and `iat` (at most `5m`) |
| `NOTARX_WORKLOAD_MAX_TOKEN_AGE` | `1h` | Cap on the age and the lifetime of a token |
| `NOTARX_WORKLOAD_JWKS_TTL` | `10m` | How long an issuer's keys are cached |
| `NOTARX_WORKLOAD_JWKS_STALE_MAX` | `24h` | How long expired keys are served when a refresh fails |
| `NOTARX_WORKLOAD_ALLOW_PRIVATE_NETWORK` | `false` | Let every issuer's key fetch reach private addresses |
| `NOTARX_WORKLOAD_EXCHANGE_MAX_TTL` | `15m` | Longest life of an exchanged token (at most `1h`) |
| `NOTARX_WORKLOAD_EXCHANGE_RATE` | `60` | Exchanges per minute per source address and per issuer |
| `NOTARX_AUTHZ_CACHE_TTL` | `5s` | Revocation window of issuers, policies, accounts and verified tokens |

See also [service account operations](service-account-operations.md) for long-lived credentials and
[ADR 0019](adr/0019-workload-identity-federation.md) for the design.
