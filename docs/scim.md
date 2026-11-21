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
  provider's logins. A peer address whose failures exceed the 120 per minute budget of the anonymous routes gets 429 for
  its further failures; a valid token is never throttled.

Send it as `Authorization: Bearer nxscim_...`.

## Accounts and logins

- A created user has level `none` (pending) and a `user_identities` row with the `externalId` and no subject.
  A `userName` that matches any existing account, local accounts included, is `409 uniqueness`; linking such an
  account is an administrator action (`POST /authentication/providers/{id}/links`, below).
- Provider mode `scim_only`: a login succeeds only for an identity that already exists. The first login binds the
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
  the login subject stays bound. The same provider brings it back by creating the user again: a `POST /Users` with
  the same `userName` re-links and re-activates it as long as it is still at level `none`, has no local password
  and is still disabled by SCIM (an account an administrator disabled stays disabled), while another provider still
  gets `409`. Deleting the account also removes the group memberships SCIM wrote for it, so a revived account comes
  back without access the provider has since withdrawn.
- SCIM can never modify or deactivate a platform administrator: every write answers `403`.
- Groups are created with source `scim` and global role `none`; a request cannot name a role, and SCIM changes never
  touch an administrator's flag. Memberships SCIM writes are authoritative for the SCIM-managed ones; an
  administrator's own membership of the group stays (SCIM does not show or change it), and logins never alter either.

## What the server implements

| Capability | Behaviour |
| --- | --- |
| Resources | `/Users`, `/Groups`, `/ServiceProviderConfig`, `/Schemas`, `/ResourceTypes` |
| Media types | Answers `application/scim+json`; accepts that and `application/json` |
| Filter | Only `eq` on `userName` and `externalId` (Users) and `displayName` and `externalId` (Groups); anything else is `400 invalidFilter` |
| Paging | Keyset on creation time and id. `count` is capped at `NOTARX_SCIM_MAX_PAGE`; `startIndex` up to 10001 is mapped onto a cursor by hopping keyset pages, and every list response carries an RFC 9865 `nextCursor` for anything further. `totalResults` is exact up to 10000 rows and a lower bound beyond |
| PATCH | Any letter case for `op`; `active` as a boolean or `"True"`/`"False"`; simple paths, `attr[type eq "x"].sub`, `members[value eq "id"]`, member arrays and pathless replace objects (Okta) |
| Unsupported attributes | Logged and ignored with `200`; only `userName`, `externalId`, `active` and `id` answer `400 invalidPath` |
| ETag | Weak ETags on resources; `If-Match` on PUT, PATCH and DELETE is compared under the row lock, `412` on a mismatch |
| Bulk, sort, change password | Not supported |
| Groups | `excludedAttributes=members` is honoured; a group read returns at most 10000 members and a list returns at most 1000 per group (use `excludedAttributes=members`, as Entra ID does, or read the group) |

The account stores one display name and one email: `name.formatted` and `emails` map to them, the other name
parts are ignored.

## Audit

Every call is audited with the actor `scim:<provider slug>`, the operation (`scim.user.patch`, ...), the resource
and the outcome. A PATCH records the names of the attributes it changed and of those it ignored, never their
values. Writes commit with their audit record.

## Fuzzing

The filter parser and the PATCH normalizers have native fuzz targets with a committed corpus under
`internal/scim/testdata/fuzz`; CI runs `make scim-fuzz`.
