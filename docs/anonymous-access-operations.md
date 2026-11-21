# Anonymous Access Operations

[简体中文](anonymous-access-operations.zh-CN.md) | [Documentation index](README.md)

Anonymous access is default-deny. It is limited to protocol and browse `GET`
and `HEAD` requests; publication, deletion, promotion, cache mutation, and all
other management writes always require authentication.

## Enablement

Every Repository and Group has a `visibility` (ADR 0029), an ordered value:
`private` (the default, principals with a grant or admin authority),
`authenticated` (any valid non-anonymous identity may read; writes still need
a grant) and `public` (anyone may read). An anonymous read is allowed only when
every applicable gate is open:

1. An administrator enables the global gate with `PUT
   /api/v2/public-access-policy` using `If-Match`.
2. The addressed Repository or Group has `visibility: public`.
3. For a Group, the Group's visibility is a ceiling: the resolved member list is
   computed per request from the members whose own visibility admits the caller.

Enabling the global gate does not expose any existing Repository or Group by
itself. While it is disabled, `public` behaves as `authenticated`: the gate
never widens access, and all anonymous reads are denied, including targets
marked `public`. A request for a non-public name from an anonymous caller is
answered exactly like a name that does not exist.

An authenticated operator or scoped user can review its own effective decision
through `GET /api/v2/repositories/{repositoryId}/effective-access`, and an
administrator can also simulate another actor. The endpoint is not anonymous and
answers only for a repository the caller already holds authority over at the
requested resource: read or intelligence authority reveals the repository, and
anything less answers exactly like a repository that does not exist, so the
endpoint cannot be used to discover identifiers.

Use `GET /api/v2/identity` to confirm which authenticated identity, credential
source, and global role the Gateway is evaluating. OIDC diagnostics expose only
whether the validated token matched an administrator subject, never arbitrary
provider claims or token material.

Anonymous successful and denied requests record `actor=anonymous` and an
authorization source/reason in the administrator-only audit log.

The Console presents these same gates as one public-access boundary: the
global gate is shown separately from Repository and Group/member opt-in, and a
public-target count makes the potential blast radius visible before an
administrator changes the global switch. This presentation does not introduce
a new policy or bypass any gate.

The unauthenticated `/browse` catalog presents only effective public targets.
Its repository search, format filters, source-type guidance, and read-only
notices are discovery features; publishing, grants, and administration still
require an authenticated management identity.

The `authenticated` tier is an identity-level allowance: any valid non-anonymous
identity may read, unless it has its own grant entry on that repository, which then decides (a
prefix-scoped grant stays scoped). An account-state block always applies. Writes always need a grant.

## Group visibility

A Group is a visibility ceiling. For each caller the resolved member list is
computed per request from the members whose own visibility admits that caller;
nothing derived is stored, and a member's visibility is always its repository's
own (a proxy member included). Reading a repository directly uses that
repository's visibility only.

`NOTARX_GROUP_VISIBILITY_MODE` selects how writes are checked:

- `strict` (the default) refuses a group more visible than its least visible
  member, and refuses lowering a repository below a group that contains it, with
  `409 visibility_conflict`. The response lists the offending member
  repositories the caller may administer. The Console shows the same message.
  The check runs in the same database transaction as the write, so concurrent
  group and repository changes cannot leave a group more visible than a member,
  and it covers the NuGet and Helm preview group endpoints too.
- `lenient` keeps the computed filtering only: members above the caller's class
  are silently invisible. It exists to migrate existing layouts and is not a
  recommended steady state. Group reads (`GET /api/v2/groups` and
  `GET /api/v2/groups/{groupId}`) carry a read-only `lessVisibleMembers` list
  of those members, and the Console warns on the group row.

Legacy protocol-specific Groups (OCI, Maven, Raw, Conan) carry no per-member
visibility: a legacy Group marked anonymous resolves every member for an
anonymous caller. Migrate to V2 Groups, which bind members to repositories, when
members need different visibility.

## Existence and denial shaping

For an anonymous caller a private repository or group and a name that does not
exist produce the same response: `401` with the format's challenge, the same
body and headers. An authenticated caller without read access receives the
`404` of a missing resource. `internal/access` is the only package that decides
reads, and `make architecture-check` fails when other code reads visibility or the
public-access policy. The conformance harness (`internal/app/access_conformance_test.go`)
drives every format in [the protocol compatibility baseline](protocol-compatibility.md)
with anonymous, authenticated and granted callers, the gate on and off, and asserts
that a private name and an absent one are byte-identical.

## OCI anonymous pulls

A public repository can be pulled two ways. A client may send an unauthenticated
`GET` or `HEAD` directly, as ORAS does first. Docker, containerd and Podman follow
the standard flow: `GET /v2/` answers `401` with a bearer challenge, the client asks
`/auth/token` with no credentials and a `repository:NAME:pull` scope, and the token
endpoint returns an anonymous token. That token is the same for every name and
carries no authority; a request bearing it is served exactly as an anonymous
request, so it reads only what an anonymous caller may read and never writes. A
token request that asks for `push` or names no scope still needs credentials.

## Client Examples

Authenticated Maven pull:

```sh
curl -u resolver:$NOTARX_RESOLVER_TOKEN \
  https://gateway.example.com/maven/releases/org/example/widget/1.0/widget-1.0.jar
```

Anonymous Maven pull after all gates are enabled:

```sh
curl https://gateway.example.com/maven/public/org/example/widget/1.0/widget-1.0.jar
```

Authenticated Raw read:

```sh
curl -H "Authorization: Bearer $NOTARX_TOKEN" \
  https://gateway.example.com/raw/releases/widgets/widget.tar.gz
```

Anonymous OCI manifest pull:

```sh
oras manifest fetch gateway.example.com/public/widget:1.0
```

Conan 2 remotes retain the Group in their URL. Do not use anonymous access for
any endpoint other than the Gateway's normal revision reads.

```sh
conan remote add public https://gateway.example.com/conan/public
conan install --requires=widget/1.0@team/stable
```

## Operational Checks

- Confirm an unauthenticated `GET` succeeds only for the intended target.
- Confirm `POST`, `PUT`, and `DELETE` without credentials remain denied.
- Inspect the audit record for `actor=anonymous` and the policy reason.
- Call `/api/v2/identity` with the operator credential before comparing a
  Repository's effective-access result; this avoids diagnosing the wrong saved
  Console credential.
- Disable the global policy during incident response; do not edit each
  Repository first.
