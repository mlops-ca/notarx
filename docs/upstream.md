# Upstream Record

[Documentation index](README.md)

notarx is a hard fork of `ccsert/artifact-gateway`, governed by
[ADR 0013](adr/0013-notarx-fork-charter.md). This page is the durable record of
the fork point and of every upstream change that notarx has picked or declined.

## Fork point

- Upstream project: `ccsert/artifact-gateway`, version `0.9.0`.
- Upstream 0.9.0 release preparation commit: `e2a96fa0904274884b6ef662d0c658953fb7a747`
  (`chore(release): prepare v0.9.0`, 2026-10-13).
- Last upstream commit before the first notarx commit: `c4d5081cc53509f417603dfc37368725eacffb62`
  (`feat(platform): complete CLI management and RubyGems replication (#473)`).
  This is the effective fork point; it contains `0.9.0` plus later upstream
  work, and the first notarx commit is `9e6d2c81` (ADR 0013 charter).

Upstream is not merged and no standing `upstream` remote is required. History up
to the fork point is retained so `git blame` and licence provenance stay intact.

## Cherry-pick procedure

Only security fixes, and bug fixes in code paths that still exist unchanged in
notarx, are picked. Features are re-implemented, not picked, because the
migration baseline and auth model differ.

1. Add a temporary remote and fetch: `git remote add upstream https://github.com/ccsert/artifact-gateway.git && git fetch upstream`.
2. Pick with provenance: `git cherry-pick -x <sha>`.
3. In the same commit, apply the mechanical identity rewrite (module path,
   names, prefixes) so no upstream identity is reintroduced.
4. Keep the original author's `Co-authored-by:` line and add the trailer
   `Upstream: ccsert/artifact-gateway@<sha>`.
5. Record the pick in the table below in the same pull request. Identify the notarx commit by subject, or fill in its SHA in a follow-up after the squash merge; for declined commits write "n/a" and the reason in Notes.
6. Remove the temporary remote.

Upstream security advisories are reviewed at each notarx minor release.

## Picked and declined upstream commits

| Upstream commit | Summary | Decision | notarx commit | Notes |
| --- | --- | --- | --- | --- |
| _none yet_ | | | | |

## Licence

The MIT licence is unchanged. `LICENSE` retains the upstream copyright line and
adds the mlops.ca line; `NOTICE` states the fork origin and non-endorsement.
Per-file licence headers are not used.
