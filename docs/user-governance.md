# Local User Governance

[简体中文](user-governance.zh-CN.md) | [Documentation index](README.md)

notarx local users are administrator-managed accounts for Console and
management API access. They are separate from API keys, break-glass static
tokens, and OIDC identities.

## Account Model

Every local account has an immutable username and mutable display name, email,
description, global role, and active/disabled state. Usernames are unique
without regard to case. The stored security state includes:

- last successful sign-in and password-change timestamps;
- consecutive failed-sign-in count and an optional lock deadline;
- whether the user must change the password at the next sign-in;
- a monotonically increasing session version;
- bounded server-side metadata for each local or linked OIDC session.

Passwords are stored only as hashes. Password updates and explicit all-session
revocation increment the session version, invalidating every previously issued
local and linked OIDC session. Each new sign-in also creates a random session
identifier whose record contains only the user, login kind, bounded client
metadata, and lifecycle timestamps; bearer tokens and provider tokens are
never stored. An administrator can therefore revoke one client without
invalidating unrelated sessions.

A user required to change their password can authenticate only to
`POST /auth/change-password`; management roles are withheld until that change
succeeds. Local passwords must contain at least 8 characters and at most 72
bytes; the byte upper bound is enforced explicitly because bcrypt cannot
process longer inputs safely.

## Management API

All `/api/v2/users` operations require an administrator identity.

| Operation | Behaviour |
| --- | --- |
| `GET /users` | Server-side search by username, display name, or email; role/state filters; offset pagination |
| `POST /users` | Creates a profile, role, initial password, and optional mandatory password change |
| `PATCH /users/{userId}` | Updates profile, role, or state with `If-Match` concurrency control |
| `POST /users/{userId}/password` | Resets the password, optionally requires another change, and revokes sessions |
| `POST /users/{userId}/sessions:revoke` | Invalidates all current local and linked OIDC sessions with `If-Match` control |
| `GET /users/{userId}/sessions` | Lists active session metadata; `includeInactive=true` includes retained revoked and expired history |
| `DELETE /users/{userId}/sessions/{sessionId}` | Revokes one session without affecting the account's other clients |
| `GET /users/{userId}/identities` | Lists external identities linked to the account |
| `POST /users/{userId}/identities` | Links an OIDC issuer and subject from the configured provider |
| `DELETE /users/{userId}/identities/{identityId}` | Removes an external identity link |
| `DELETE /users/{userId}` | Permanently removes the account |

The final active administrator cannot be disabled, demoted, or deleted. Failed
optimistic updates return `412`; last-administrator protection returns `409`.

## External Identities

OIDC identities are stored separately from local accounts and linked by their
normalized issuer and stable `sub` claim. A linked OIDC sign-in resolves to the
local account and therefore uses its current role, active/disabled state,
mandatory password-change restriction, and session version. Disabling the
account or revoking all sessions invalidates an existing browser session on its
next authenticated request.

Administrators can link and unlink identities explicitly. Runtime OIDC settings
can also enable just-in-time provisioning, select its default role, and allow a
verified `email` claim to link an existing account. Email linking requires
`email_verified=true` and rejects ambiguous matches. With JIT disabled, an
unlinked subject retains the legacy external-principal behaviour and is not
silently attached to a local account.

## User Groups

A user group is a persisted set of accounts that a repository grant can name as the
principal `group:<id>`, so a team gets the same access as the sum of its members' grants
without listing every account. The name is `user_groups` because a Group elsewhere in
notarx is a repository group.

| Source | How it is populated |
| --- | --- |
| `local` | An administrator creates the group and edits its members. |
| `scim` | The identity provider's SCIM server writes it (ADR 0018). |
| `claims` | An administrator registers the group with its provider and external id; the groups claim of an OIDC sign-in or bearer token (at most every 15 seconds per account while unchanged; a bearer token without the groups claim leaves the memberships as they are, while a sign-in ID token without it asserts none), or the groups attribute of a SAML assertion, adds and removes the membership. |

A claim only ever matches a `claims` group of the signing-in provider by its external id,
never by display name, and never a group that has a global role. Microsoft Entra ID drops
the `groups` claim for a user in too many groups (`_claim_names` or `hasgroups`);
notarx does not chase it through Microsoft Graph, so such a token carries no groups (never
a partial list; a SAML assertion with more than 512 groups the same, as `auth.saml.groups_overage`), an `auth.oidc.groups_overage` audit event is written, and large
directories should provision groups through SCIM or use app roles instead.

A group may carry a global role (`none`, `member` or `admin`). An account's level is the
highest of its own role and the roles of its groups, evaluated on every request and never
copied into a credential, so removing a membership, clearing the role or deleting the
group takes effect on the next request (within `NOTARX_AUTHZ_CACHE_TTL` on other nodes). Deleting a group also removes the repository and authorization template grants that name it, and a SCIM or claims group whose identity provider was deleted grants nothing (it stays visible so an administrator can delete it). A
disabled account stays blocked whatever its groups say, a pending account is not lifted by a
grant, and OCI and Conan protocol tokens carry no group ids. New groups start at `none`;
SCIM can never create a group with, or set, `admin`, an administrator may flag an existing
group (audited), and deleting a group clears the flag. At most 512 groups count per account.

| Endpoint (relative to `/api/v2`) | Purpose |
| --- | --- |
| `GET`, `POST /user-groups` | List (keyset paged) and create a local or claims group |
| `GET`, `PUT`, `DELETE /user-groups/{groupId}` | Read, rename (`If-Match`) and delete |
| `PUT /user-groups/{groupId}/global-role` | Flag the group with a global role (`If-Match`) |
| `GET`, `POST /user-groups/{groupId}/members` | List and add members |
| `DELETE /user-groups/{groupId}/members/{userId}` | Remove a member |

The effective-access simulation of a `user:<name>` actor includes the groups of that
account.

## SCIM Provisioning

An identity provider can provision accounts and groups through the SCIM server (see [SCIM 2.0
provisioning](scim.md)). A SCIM-created account has level `none` and an identity row with the provider's
`externalId` and no subject; on a `scim_only` provider its first login binds the subject by that `externalId`, never
by `userName`. `users.disabled_reason` records who disabled an account: `active=false` from SCIM sets `scim`, bumps the
session version and revokes the account's sessions in one transaction, and only an account disabled by `scim` is
re-activated by `active=true`; an administrator's disable (`admin`) stays until an administrator lifts it. SCIM
never modifies, deactivates or deletes a platform administrator, and `DELETE` removes only the SCIM link and
disables the account.

## Lockout Policy

`NOTARX_LOCAL_AUTH_MAX_FAILED_ATTEMPTS` controls consecutive failures before a
temporary lock and defaults to `5`. `NOTARX_LOCAL_AUTH_LOCKOUT_DURATION`
defaults to `15m`. The supported configuration bounds are 1 to 100 attempts and
1 minute to 24 hours.

Locked, disabled, unknown, and wrong-password accounts return the same login
error to avoid username enumeration. A successful sign-in clears the failure
count and lock deadline. An administrator password reset also clears both.

## Audit And Operations

Successful and failed local sign-ins, self-service password changes, user
creation/update/deletion, administrator password resets, and session revocation
are audited. Management audit actors identify the administrator performing the
operation; the resource identifies the target user. Session listing and
single-session revocation use the `user.session.list` and
`user.session.revoke` audit actions.

Expired session metadata is retained for 30 days and then removed in bounded
500-row batches by the scheduler role. PostgreSQL cleanup uses locked,
skip-locked batches so multiple scheduler nodes do not contend for the same
records. The record expiry is enforced independently of the signed token
expiry, account state, and session version.

Operators should verify the following after changing authentication policy:

1. Repeated failed logins lock the test account at the configured threshold.
2. A password reset invalidates a token issued before the reset.
3. Explicit session revocation invalidates every current token for the account.
4. Revoking one listed session leaves an unrelated session usable.
5. The final active administrator cannot be disabled or deleted.
6. Audit queries distinguish the performing administrator from the target user.

## Current Limitations

Deletion is permanent rather than a recoverable tombstone. A local account has
one level, chosen from Pending, Member, and Platform administrator. The `member`
level carries no repository capability at all, so a member reaches only the
repositories it is granted. The removed `reader` and `writer` levels were
narrowed to `member` on upgrade, with equivalent per-repository grants
materialized on the repositories that existed then, so nothing lost access on
the repositories it already had. Custom roles composed from arbitrary
privileges, and multiple simultaneous role assignments, are not yet part of the
model. Password expiry and configurable complexity rules are not enforced.
OIDC back-channel logout and identity-provider-initiated logout are not yet
supported. Unlinked external OIDC principals retain stateless browser sessions
and therefore do not appear in a local account's session inventory. Signed
sessions issued before server-side session identifiers were introduced remain
compatible until their normal expiry, but still honour account state and session
version checks.
