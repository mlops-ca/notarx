# SCIM 2.0 provisioning

[Documentation index](README.md)

notarx is a SCIM 2.0 server (RFC 7643, RFC 7644, and RFC 9865 cursor paging) for Microsoft Entra ID, Okta and any
other client that provisions users and groups (ADR 0018, section 4). It is mounted at the server root under
`/scim/v2`, separate from the management API, and is described by
[`api/openapi/protocols/scim.yaml`](../api/openapi/protocols/scim.yaml). The SCIM base URL is
`NOTARX_PUBLIC_URL` plus `/scim/v2`.

## Turn it on

SCIM is off by default: every `/scim/v2/` route answers 404, with or without a token, until you set

```sh
NOTARX_SCIM_ENABLED=true
NOTARX_SCIM_MAX_PAGE=200   # optional: the cap of the count parameter, 1-1000, advertised as maxResults
```

`notarx preflight` reports `scim_enabled` and `scim_max_page`.

| Key | Default | Meaning |
| --- | --- | --- |
| `NOTARX_SCIM_ENABLED` | `false` | Mounts the SCIM server; while `false` every `/scim/v2/` route answers 404. |
| `NOTARX_SCIM_MAX_PAGE` | `200` | The cap of the `count` parameter (1-1000), advertised as `maxResults`. |

The setup of Microsoft Entra ID, Okta and Keycloak, token rotation and the attribute mapping are in the
[identity provider setup guides](identity-provider-guides.md#scim-provisioning).

## Create a token

A SCIM token belongs to one identity provider and acts only for it: it sees the accounts linked to that provider and
the groups of source `scim` that carry its provider id, and the users and groups it creates carry that provider.
Create it as an administrator (the console has a dialog that does the same):

```sh
curl -sS -X POST "$NOTARX/api/v2/authentication/providers/$PROVIDER_ID/scim-tokens" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Entra ID provisioning","expiresAt":"2027-06-30T00:00:00Z"}'
```

- `expiresAt` is mandatory, in the future and at most 365 days ahead. The console warns before a token expires.
- The plaintext `token` (it starts with `nxscim_`) is in this response only. notarx stores its hash, never the
  plaintext, and neither logs nor audits it. Lose it and you create a new token.
- `GET` on the same path lists the tokens with their state (`active`, `expired`, `revoked`); `DELETE
  .../scim-tokens/{tokenId}` revokes one at once. Creating and revoking are audited.
- Every failed authentication is the same 401, and a token of a disabled provider stops authenticating with the
  provider's logins. A revoked or expired token, a disabled provider and a deleted provider stop authenticating on the node that handled the change at once and on every other node within `NOTARX_AUTHZ_CACHE_TTL` (default `5s`, shortened by the cache notification; `0` reads the database on every request). A peer address whose failures exceed the 120 per minute budget of the anonymous routes gets 429 for
  its further failures; a valid token is never throttled.

Send it as `Authorization: Bearer nxscim_...`; a credential that does not have the shape of an issued token is refused
without a database read.

## Accounts and logins

- A created user has level `none` (pending) and a `user_identities` row with the `externalId` and no subject.
  SCIM only sees the accounts it provisioned (the identity rows that carry a SCIM `externalId`): an account a login
  created (`jit`) or an administrator linked is invisible to it and can never be changed or deactivated by it.
- A `userName` that matches an existing account is `409 uniqueness`, except for one account the provider is
  already linked to: a `POST /Users` takes over an account that a login or an administrator linked, or that the
  provider deleted earlier, as long as it is at level `none`, has no local password and is active or disabled by
  SCIM (an account an administrator disabled stays a conflict). This is how a provider in `jit` mode correlates its
  users. The account keeps its name and takes the profile and state of the request.
- `userName` cannot be changed through SCIM (`400 mutability`; a change of letter case only is ignored). The account
  name is the identity that grants, sessions and job actors are keyed on, and no other path renames an account: a
  rename would orphan its grants and hand them to whoever takes the old name. Rename the account in the console, or
  provision the user again.
- Provider mode `scim_only`: a login succeeds only for an identity that already exists (the subjects an administrator listed in the provider's admin subjects keep their break-glass sign-in). The first login binds the
  subject by matching the `externalId` the identity provider asserts (`attributeMapping.externalId`, for example the
  `employee_id` claim or the `employeeNumber` SAML attribute) against the SCIM `externalId`, and never by
  `userName`, which is mutable. An unknown subject is refused and a bound account cannot be taken over by another
  subject. Mode `jit` creates accounts at login instead; the two modes are exclusive.
- A provider that does not assert `externalId` (Okta sends none by default) links accounts with the admin link flow:

  ```sh
  curl -sS -X POST "$NOTARX/api/v2/authentication/providers/$PROVIDER_ID/links" \
    -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
    -d '{"userId":"'$USER_ID'","subject":"tid:oid-or-NameID"}'
  ```

- `active=false` (or Entra's `"False"`) disables the account with reason `scim`, bumps its session version and
  revokes its sessions in one transaction. Identity rows and grants stay, so `active=true` restores access. An
  account an administrator disabled stays disabled: re-activating it through SCIM is `409`.
- `DELETE /Users/{id}` removes the SCIM link and disables the account; it never deletes the account (that stays a
  console action). SCIM no longer sees the account (`404`, not listed, `PATCH active=true` cannot bring it back);
  the login subject stays bound. The same provider brings it back by creating the user again (the take-over above),
  while another provider still gets `409`. Deleting the account also removes the group memberships SCIM wrote for it, so a revived account comes
  back without access the provider has since withdrawn.
- SCIM can never modify or deactivate a platform administrator: every write answers `403`.
- Groups are created with source `scim` and global role `none`; a request cannot name a role, and SCIM changes never
  touch an administrator's flag. Memberships SCIM writes are authoritative for the SCIM-managed ones; an
  administrator's own membership of the group stays (SCIM does not show or change it) until the provider names the same account,
  which makes it SCIM-managed so the provider can see and remove it, and logins never alter either.

## What the server implements

| Capability | Behaviour |
| --- | --- |
| Resources | `/Users`, `/Groups`, `/ServiceProviderConfig`, `/Schemas`, `/ResourceTypes` |
| Media types | Answers `application/scim+json`; accepts that and `application/json` |
| Filter | Only `eq` on `userName` and `externalId` (Users) and `displayName` and `externalId` (Groups); anything else is `400 invalidFilter` |
| Paging | Keyset on creation time and id. `count` is capped at `NOTARX_SCIM_MAX_PAGE`; `startIndex` up to 10001 is mapped onto a cursor by hopping keyset pages that read only the keys of the rows they skip, and every list response carries an RFC 9865 `nextCursor` for anything further. `totalResults` is exact up to 10000 rows and a lower bound beyond |
| PATCH | Any letter case for `op`; `active` as a boolean or `"True"`/`"False"`; simple paths, `attr[type eq "x"].sub`, `members[value eq "id"]`, member arrays and pathless replace objects (Okta) |
| Unsupported attributes | Logged and ignored with `200`; only `userName`, `externalId`, `active` and `id` answer `400 invalidPath` |
| ETag | Weak ETags on resources (RFC 7644 section 3.14); `If-Match` on PUT, PATCH and DELETE takes the opaque value of a weak or strong tag, because the resource tags are weak, and is compared under the row lock, `412` on a mismatch |
| Bulk, sort, change password | Not supported |
| Groups | `excludedAttributes=members` is honoured; a group read returns at most 10000 members, and the groups of one list page share a budget of 1000 members; a group whose members do not fit its share is listed without them, never with a partial list (use `excludedAttributes=members`, as Entra ID does, or read the group) |
| Locations | `meta.location`, `Location` and member `$ref` are absolute from `NOTARX_PUBLIC_URL`; without it they are paths only, never built from the request's Host header |

The account stores one display name and one email: `name.formatted` and `emails` map to them, the other name
parts are ignored.

## Audit

Every call is audited with the actor `scim:<provider slug>`, the operation (`scim.user.patch`, ...), the resource
and the outcome. A PATCH records the names of the attributes it changed and of those it ignored, never their
values. Writes and refusals commit with their audit record before the response; the audit of a successful read (`GET` or `HEAD`) goes through the tiered audit writer (ADR 0023 section 1), which batches it, so a sync of thousands of resources costs one insert per batch instead of one per call. The writer drops a read record only when its bounded buffer is full, and counts it (`notarx_audit_dropped_total`); `NOTARX_AUDIT_READ_MODE=sync` commits every read audit before the response when no loss is acceptable.

## Fuzzing

The filter parser and the PATCH normalizers have native fuzz targets with a committed corpus under
`internal/scim/testdata/fuzz`; CI runs `make scim-fuzz`.
