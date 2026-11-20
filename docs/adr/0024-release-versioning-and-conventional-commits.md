# Release Versioning, Conventional Commits and Nightly-to-Stable Promotion

[Documentation index](../README.md)

Status: accepted (2026-11-17)

See also: [ADR 0026](0026-debian-and-rpm-repositories.md), [ADR 0030](0030-sbom-generation-and-vulnerability-scanning.md).

## Context

- `VERSION` holds `0.9.0` and is the root of every version string: `Makefile:12`
  reads it into `CLI_LDFLAGS` (`Makefile:15`, `-X .../internal/buildinfo.injectedVersion`),
  `Makefile:258` passes it as a Docker build argument, and
  `scripts/release-version-check.sh` requires that it be a stable SemVer core
  (line 9) and that eleven other places equal it: `console/package.json`,
  `tools/openapi/package.json` (lines 22-24), the lockfiles, `info.version` of three OpenAPI files,
  a dated heading in `CHANGELOG.md` and `CHANGELOG.zh-CN.md` (lines 61-63) and
  `.github/release-notes/v<version>.md` (lines 64-65). A release is therefore a
  hand-made commit that edits all of them (ADR 0013 slice 10 would do it once).
- `.github/workflows/release.yml` runs on `push: tags: v*` and has four jobs
  (`verify-candidate`, `build-assets`, `publish-images`, `publish-release`). Its
  `verify-candidate` requires the tag to equal `VERSION`, `GITHUB_SHA` to equal
  `origin/main`, and the named check runs `quality`, `contract-and-test`, `console`,
  `mainline-assets` and two `mainline-images` runs to be green. Images are rebuilt
  at release time and promoted by `imagetools create`.
- `internal/buildinfo/buildinfo.go` reads `injectedVersion` and falls back to
  the Go module version from `debug.ReadBuildInfo`, then to `dev`.
  `scripts/build-release-artifacts.sh:50` already accepts `-nightly.N` style
  suffixes, and writes the version into archive names and each `VERSION.txt`
  (lines 138-227).
- `CHANGELOG.md` is a hand-curated `## Unreleased` bullet list. `CONTRIBUTING.md`
  asks for an "imperative Conventional Commit subject" but nothing enforces it and
  no scope list exists. Recent history already uses `feat(platform):`,
  `fix(deps):`, `docs(cli):`, `test(rubygems):`.
- ADR 0013 fixes the first stable version at `0.1.0`, ADR 0015 requires the single
  baseline to be re-squashed before that tag, and ADR 0016 forbids cross-compilation
  and builds each architecture natively. The owner now requires semantic versioning,
  automatic nightly pre-releases, a manually triggered stable release and enforced
  conventional commits.
- A workflow run started with `GITHUB_TOKEN` does not trigger further workflow runs
  (except `workflow_dispatch` and `repository_dispatch`), so a bot-created tag cannot
  start a tag-triggered release. Scheduled workflows run only from the default branch
  and GitHub disables them after 60 days without repository activity.
  ([events that trigger workflows](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows))

## Considered designs

1. **semantic-release (npm).** Mature, plugin rich, derives versions from conventional
   commits and publishes in one run. It fits npm packages best. Against notarx:
   it wants to own tagging and publishing, while notarx needs a read-only "plan" step
   that feeds a native per-architecture matrix (ADR 0016) and a later stable rebuild of
   a verified commit; its first release is always `1.0.0` and a breaking change bumps
   the major, so staying in `0.x` needs a custom `releaseRules` entry and a seeded
   tag; prereleases are branch based (a `nightly` channel branch), which turns
   promotion into a branch fast-forward that re-runs analysis and, by default, commits
   version bumps back to `main`, which the protected branch must permit. It adds a
   Node plugin chain to a Go release path. Workable, but every one of those is a
   workaround. Rejected.
2. **release-please.** Maintains a release pull request; merging it tags and
   releases. Good for human-reviewed cadence and supports `release-as` and
   `bump-minor-pre-major`. Against notarx: the release is a new commit (version bump
   plus changelog), so the released commit is not the commit that nightlies tested
   and the nightly evidence does not transfer to it; it has no nightly concept; the release
   PR needs a non-`GITHUB_TOKEN` token to get CI. Rejected.
3. **git-cliff plus a small, tested plan script (chosen).** git-cliff computes the
   bump (`--bumped-version`) and renders changelog and release notes from conventional
   commits, including 0.x semantics (`[bump] features_always_bump_minor = true`,
   `breaking_always_bump_major = false`, `initial_tag = "0.1.0"`, and a
   `no_increment_regex` for the non-releasing types; git-cliff documents these keys
   but no pre-release tag handling, so the plan never lets it see nightly tags). It only reads git history, so it
   composes with a workflow that owns tagging. The cost is that notarx owns about
   150 lines of shell (`scripts/release-plan.sh`) and its fixture tests.
4. **Tag-triggered releases with hand-bumped `VERSION` (status quo).** Keeps today's
   gates, but needs a bump commit per nightly and cannot start from a bot tag.
   Rejected for nightlies; its checks are reused.
5. **Digest promotion: retag the nightly's bytes as stable.** Stable would be the
   nightly's image index digest and archives, copied or retagged, with no second
   build. It gives byte-identity between what was tested and what ships and costs
   nothing. Rejected on version identity: the artifacts would keep reporting
   `X.Y.Z-nightly.N` from `notarx version`, `notarxctl version`, `VERSION.txt`, the
   `org.opencontainers.image.version` label, archive names and the .deb/.rpm
   version, so a "stable" install would claim to be a pre-release; stamping the bare
   core into a nightly instead would make nightlies claim to be stable, and
   re-stamping after the fact changes every checksum and breaks the attestation
   chain. Rebuilding the verified commit makes every surface report the clean
   version at the price of a second build.

Not evaluated empirically: cocogitto, svu and go-semantic-release; none has a
documented 0.x plus prerelease flow that git-cliff plus a script does not already cover.

## Decision

Option 3, with promotion of the tested **commit**, not of bytes (design 5 is
rejected). One workflow, `.github/workflows/release.yml`, with two triggers:
`schedule` (nightly) and `workflow_dispatch` (stable). The tag-push trigger is removed.

**Ownership.** ADR 0016 owns the per-architecture mechanics as reusable
`workflow_call` workflows: `image-build.yml` (native per-arch build by digest, index
merge, attestation) and the archive matrix. This ADR owns release orchestration:
`release.yml` (plan, gates, call of the reusable workflows, publish for both channels, prune),
the version policy, commit policy and repository governance.

### Version policy

- Versions are SemVer 2.0. While the major is `0`: `feat` bumps minor, `fix`, `perf`
  and `revert` bump patch, a breaking change (`!` or `BREAKING CHANGE:` footer) bumps
  minor. `1.0.0` is only ever produced by an explicit `version_override` input of the
  stable run, so a breaking change cannot jump the line by accident. The criteria for `1.0.0` (API
  stability promises per ADR) are a separate "1.0 readiness" ADR, written when the 0.x scope nears
  completion; this ADR only requires the explicit `version_override`. After `1.0.0`
  the standard rules apply (breaking bumps major) via flipping `breaking_always_bump_major`
  in `cliff.toml` in that same PR.
- Nightly: `X.Y.Z-nightly.N`, tag `vX.Y.Z-nightly.N`. `X.Y.Z` (the core) is the next
  stable version computed from commits since the last stable tag; before any stable
  tag the core is fixed at `0.1.0`. `N` is the highest existing
  `vX.Y.Z-nightly.*` counter plus one (maximum, never a count, so pruning cannot reuse
  a number). A counter beats a date: it is collision free for two runs in one day,
  sorts correctly under SemVer precedence (numeric identifiers), and resets when the
  core bumps. The date goes into the OCI `created` label. Docker tags cannot hold `+`,
  so no build metadata is used.
- Stable: `X.Y.Z`, tag `vX.Y.Z`, always the core of the verified nightly whose
  commit is rebuilt (or `version_override`, which the rebuild makes possible for
  `1.0.0` without any relabelling).
- Milestones are themed, never numbered, because versions are computed:
  `Foundation`, `Data scale`, `Identity & federation`, `Promotion chains`,
  `MySQL HeatWave`, `Formats`, `Public feeds`, `Supply-chain security` and `Backlog`; unscheduled work has none. A milestone closes when its theme is
  complete. The stable release notes list the milestones whose last issue closed since
  the previous stable. ADRs refer to milestones by name.

### Commit policy

- Squash-merge only, squash message defaults to the PR title and description; merge
  commits and rebase merging are disabled. The PR title is the unit of enforcement,
  so contributors need not rewrite branch commits.
- Format: `type(scope)!: imperative subject`. Types: releasing `feat`, `fix`, `perf`,
  `revert`; non-releasing `docs`, `test`, `refactor`, `build`, `ci`, `chore`, `style`.
  Scope is optional, may list several separated by commas, and must be one of the
  following, matched to the real tree (`internal/`, `cmd/`, `deploy/`, `console/`):
  - formats (`internal/protocol/*`, `internal/format/*`): `oci`, `maven`, `npm`, `pypi`,
    `go`, `cargo`, `raw`, `conan`, `apt`, `rpm`, `huggingface`, `nuget`, `helm`, `rubygems`;
  - platform: `server` (`cmd/notarx`), `cli` (`internal/agctl`, `cmd/notarxctl`),
    `console`, `api`, `openapi`, `auth` (`internal/authorization`, `internal/upstreamauth`),
    `identity` (IdP, SCIM, workload federation, issuer: ADRs 0018-0020), `storage`
    (`internal/objectstore`, `internal/uploadspool`), `db` (`internal/database`,
    `migrations/`), `cache`, `replication`, `promotion`, `scanning`, `lifecycle`,
    `backup` (`internal/backupops`, `internal/backupmanifest`), `migration`
    (`internal/nexus*`, `internal/snapshotimport`), `notification`
    (`internal/emailnotification`, `internal/quotaalert`), `config`, `i18n`, `brand`,
    `observability`, `deploy`, `signer` (`cmd/notarx-signer`, `internal/signer`, ADR 0026),
    `scanner` (`cmd/notarx-scanner`, the Trivy pin and its image, ADR 0030),
    `access` (`internal/access`, ADR 0029), `repository` (`internal/repository`, the
    repository and group model);
  - meta: `deps`, `docs`, `ci`, `release`, `changelog` (the generated `docs(changelog): vX.Y.Z` pull request), `security`, `repo`.
- The policy lives in one file, `.github/commit-policy.json` (types, releasing types,
  scopes). `scripts/commit-policy-check.mjs` (tested like the other `scripts/*.mjs`)
  validates a title against it; a `pr-title` workflow runs it on `opened`, `edited`,
  `synchronize`; the release plan runs it over commit subjects on `main` as a
  warning. git-cliff ignores unconventional commits (`filter_unconventional = true`).
  Adding a scope is a one-line PR. No third-party action is needed.
- A Trivy pin change is `build(scanner)` and requires a reviewer from `CODEOWNERS` (ADR 0030 slice 1a);
  it is non-releasing on its own, like every `build`.
- `fix(security)` and `fix(deps)` release a patch. Dependabot titles must match the policy
  through its `commit-message` configuration.

### Stamping instead of bump commits

- Committed value everywhere: `VERSION`, `console/package.json`,
  `tools/openapi/package.json`, lockfiles and OpenAPI `info.version` carry the
  development value `0.0.0-dev`. No commit ever changes them for a release, so no
  bot commit needs to bypass branch protection.
- `scripts/stamp-version.sh <version>` rewrites those files in the build workspace
  (npm `version --no-git-tag-version`, `yq` for OpenAPI), regenerates the OpenAPI
  bundles that embed the version, and is run by every build leg before `make`.
  The same value is passed as `-X ...buildinfo.injectedVersion` and as the image
  `VERSION` build argument. Nothing stamped is committed.
- `scripts/release-version-check.sh` keeps ADR 0008's module-path and `go install`
  invariants, accepts `0.0.0-dev` in pull-request CI (all files must agree) and takes
  `--expect <version>` after stamping in release builds. It no longer requires a
  dated changelog heading or `.github/release-notes/v<version>.md`.

### Plan job

`plan` (ubuntu, `fetch-depth: 0`, read-only) is the only place the version is computed.
Outputs: `skip`, `channel`, `version`, `core`, `sha`, `previous`.

Nightly (`schedule`, default branch only):
1. `S` = latest tag matching `^v[0-9]+\.[0-9]+\.[0-9]+$`. `core` =
   `git cliff --bumped-version --tag-pattern '^v[0-9]+\.[0-9]+\.[0-9]+$'` since `S`
   (the pattern hides nightly tags from git-cliff); `0.1.0` if no `S`. The output is
   normalized (prefix stripped) by the script and checked by fixtures.
2. `sha` = the newest first-parent commit of `main` (looking back at most 10) whose
   required check runs are green. The required names are read from
   `scripts/release-required-checks.txt`, the single list that replaces the hard-coded
   names in today's `verify-candidate`. (Superseded by the amendment of 2026-11-19 at the
   end: `sha` is the tip of `main` and no check run is read.)
3. Skip (success, with a notice) when `sha` equals the commit of the latest nightly tag,
   or when no `feat`, `fix`, `perf`, `revert` or breaking commit exists between that
   tag and `sha`.
4. `version = core-nightly.N` as above; refuse if the tag already exists.

Stable (`workflow_dispatch` inputs `nightly_tag` default latest verified nightly,
`version_override`, `dry_run` default true):
1. Resolve the nightly tag, its commit (`sha`) and its core; `version` = core, or the
   override. Refuse if `vX.Y.Z` already exists.
2. Require `sha` to be an ancestor of `main` (not equal to its tip: the release must
   work after `main` moved) and the required checks green on it (superseded by the amendment of 2026-11-19: no check
   run of `main` is read; the gates of a release run decide).
3. Require the nightly release to carry its build record (a `release-manifest.json`
   asset, below) whose `sha` matches, and require `gh attestation verify` to accept
   every image digest and archive in it against this repository's release workflow
   identity. This proves the commit went through the full nightly pipeline; the
   stable run then repeats the same gates and builds itself (below).
4. First-release gate (ADR 0015): while no stable tag exists, fail unless
   `scripts/baseline-gate.sh` confirms `migrations/` holds exactly one file,
   `000001_baseline.sql`, and the nightly commit contains it; `version_override`
   other than `0.1.0` is rejected while no stable tag exists. After `v0.1.0` exists the
   gate is replaced by ADR 0015's `scripts/migration-immutability-check.sh`.

### Nightly path (`publish` with `channel=nightly`)

1. `plan` then `gates`: the full arm64 `contract-native` run on the chosen commit
   (ADR 0016's arm64 gate; it moves from the old tag release into the common pipeline, so
   it runs for nightly and again, on the same commit, for stable).
2. `build` legs call ADR 0016's reusable workflows with `version`, `sha` and the tag list
   (`:nightly`, `:X.Y.Z-nightly.N`): images are built natively per architecture on
   `ubuntu-24.04` and `ubuntu-24.04-arm`, pushed by digest, merged by
   `docker buildx imagetools create`, attested on the index digest (every published image: `notarx`,
   `notarx-console` and, from ADR 0030 slice 1b, `notarx-scanner`; the release gate requires all
   of them signed and attested, and `release-manifest.json` lists each index digest, including the
   scanner image digest); archives are built
   natively on the four runner labels of ADR 0016, including darwin.
3. `publish` (needs everything): generates notes with
   `git cliff <previous>..<sha> --tag <version> --strip all` over the range since the
   previous nightly (or stable), assembles `SHA256SUMS` and `release-manifest.json`
   (`{version, core, sha, built_at, images: {name: index digest}, assets: [{name, sha256}]}`),
   and as the last step creates the tag and the pre-release in one call:
   `gh release create vX.Y.Z-nightly.N --prerelease --target <sha> --notes-file ...`.
   Because the tag appears last, a failed run leaves only untagged digests and the
   next night retries naturally.
4. `prune` (below).
5. Jobs owned by other ADRs run after `publish` and never alter the plan: ADR 0025 (channels) and
   ADR 0026 slices 15 and 16, `publish-feeds` (uploads the deb and rpm packages to the public
   notarx feeds, nightly first and stable after the ADR 0026 promotion criteria) followed by
   `verify-feeds` (installs from the feeds and compares bytes with the R2 channel).

### Stable path: rebuild of the verified commit

Stable runs the same job graph as nightly (`plan`, `gates`, `build`, `publish`) at
`sha` with `channel=stable`; it is not a copy step. Promotion is of the tested
commit, not of bytes.

1. `gates` and `build` repeat the nightly's native matrix and tests on `sha`: the same
   reusable workflows, the same four runner labels, the same arm64 gate and required
   checks, with `version = X.Y.Z` stamped (`scripts/stamp-version.sh`) so `notarx
   version`, `notarxctl version`, `VERSION.txt`, the image `VERSION` build argument and
   `org.opencontainers.image.version`, archive names and the .deb/.rpm versions
   (ADR 0025) all carry the clean stable version. Build inputs are fixed by the commit
   (actions pinned by SHA, base images by digest, ADR 0016), so only the stamped
   version differs from the nightly.
2. Images are attested and tagged `:X.Y.Z` and `:X.Y` by the merge job from the stable
   build's own per-architecture digests, with the index annotation
   `io.mlops.notarx.verified-nightly=<nightly tag>`. `:0` is never published while the
   major is 0. `:latest` moves last, after the Git tag. While `vX.Y.Z` does not exist
   a re-dispatch may re-point `:X.Y.Z` and `:X.Y`; once it exists nothing is rewritten.
3. As the last pre-tag step, `gh release create vX.Y.Z --target <sha>` creates the tag
   and the GitHub Release (as draft, published after assets upload) with the stable
   build's archives, `SHA256SUMS`, and a `release-manifest.json` that adds
   `verified_nightly` (the nightly tag). Stable release notes say which nightly's commit
   was rebuilt.
4. Edit the verified nightly's notes to point to the stable release; move `:latest`;
   run the ADR 0025 stable jobs; open the changelog PR (next section). These tail jobs
   are idempotent and repaired with "Re-run failed jobs"; a failure before the tag is
   repaired by dispatching again.

`dry_run` executes the plan and verification and prints the intended actions.

**Version identity.** Every surface of a stable release reports `X.Y.Z`, and a
nightly reports `X.Y.Z-nightly.N`; no stable artifact contains the string `nightly`
(asserted by a script over the built binaries, labels and archives). The stable
artifacts are not byte-identical to the nightly's, because the stamped version
differs; the evidence that they are the same program is the identical commit and
inputs, the repeated gates, and the nightly's verified provenance. `buildinfo.Core()`
(slice 2) still lets compatibility checks (for example ADR 0021 peers) compare the
SemVer core only, so nightly and stable of one core interoperate. An optional future
check rebuilds the commit with the nightly's version string and compares digests with
the nightly's to demonstrate build reproducibility; it is not part of this decision.

### Changelog and release notes

`CHANGELOG.md` records stable releases only and is generated: after a stable
release a job opens a PR titled `docs(changelog): vX.Y.Z` prepending the git-cliff
section for the range since the previous stable tag. The tag stays on the tested
commit; the changelog lands afterwards. The hand-written `Unreleased` list is
retired (one source of truth: PR titles, plus `BREAKING CHANGE:` footers). The file starts
with ADR 0013's upstream fork-point preamble. `CHANGELOG.zh-CN.md` is dropped
(ADR 0014). The PR is opened with a GitHub App installation token
(`actions/create-github-app-token`), because a PR opened by `GITHUB_TOKEN` does not
trigger CI.

### Retention and pruning

`prune` runs after each successful nightly and on `workflow_dispatch` (dry run default).
- GitHub releases and tags: keep the newest 14 nightlies, the newest nightly of each
  core, and every nightly whose commit equals a stable tag's commit (the verified ones,
  computed, not stored). Delete others with `gh release delete --cleanup-tag`.
  Stable releases and tags are never deleted.
- GHCR: `scripts/prune-ghcr.mjs` (tested on fixtures) works by mark and sweep. Mark:
  every version reachable from a retained tag (the tagged index, its child manifests
  for each platform, and the attestation or referrer manifests that point at them).
  Sweep: delete only package versions that are unmarked and older than 7 days (the
  in-flight guard for builds that have pushed digests but not yet tags). There is no
  other age condition: a cutoff pinned to the oldest retained nightly would age without
  bound, because retention keeps the newest nightly of every core and the nightlies of
  stable tags, and expired indexes, their children and orphans younger than that
  nightly would never be swept. Reachability from retained tags already protects
  retained images. Per-architecture digests behind
  an index are untagged versions, so "delete untagged" is forbidden; the script must
  never call a bulk untagged-deletion action. Stable image versions are always marked.
  Expired nightly indexes are deleted first and their orphans swept on the next run.

### Repository governance (applied by `scripts/repo-governance-apply.sh`, idempotent, needs admin)

| Item | Setting |
| --- | --- |
| Merge methods | squash only, default message = PR title and body |
| Ruleset `stable-tags` | `refs/tags/v*` excluding `v*-nightly.*`: block update and deletion, no bypass |
| Ruleset `nightly-tags` | `refs/tags/v*-nightly.*`: block update; deletion allowed only to the release App |
| Ruleset `tag-creation` | `refs/tags/v*`: creation restricted to the release App |
| Release App | installation token for tag creation, prune and the changelog PR; contents, pull requests, packages write |
| Environment `stable-release` | optional required reviewers on the stable `publish` job; the manual dispatch is the trigger |
| Scheduled-workflow liveness | the plan job calls the workflow-enable API on every run as a best-effort measure (GitHub documents only repository activity as resetting the 60-day inactivity timer, so nothing relies on the call); the monthly alert issue if no nightly or skip notice was recorded in 35 days is the backstop |

Attestation verification for artifacts produced by the reusable workflows uses
`gh attestation verify <file> --repo mlops-ca/notarx --signer-workflow mlops-ca/notarx/.github/workflows/<called>.yml`
(the signer is the called workflow, and the value must be repo-qualified; a bare path fails). `--source-ref`
or `--cert-identity` are added only where a tag is pinned. `--signer-repo` alone is not used, because it
accepts any workflow in the repository.

## Security considerations

- Release jobs run only from `main` (schedule) or dispatch with write access; third-party
  actions stay pinned by commit SHA (ADR 0016). `plan` has read-only permissions; only
  `publish` and `prune` get `contents: write`, `packages: write`, and the
  attest jobs `id-token: write` and `attestations: write`.
- The stable build is attested like the nightly (provenance of the stable images and
  archives from the stable run at the verified commit); verification identity is pinned
  to this repository's release workflow, and a stable run refuses a commit whose nightly
  record or attestations do not verify, so a commit not built by the pipeline is not
  releasable. `actions/attest` needs `id-token: write` and `attestations: write` (`actions/attest-build-provenance` is a wrapper over it).
- Tag rulesets make stable tags immutable and limit tag creation to the release App;
  App credentials are repository secrets scoped to the release workflow and the
  `stable-release` environment.
- The prune script is the only code that deletes package versions, defaults to dry run,
  and refuses to run if the mark set is empty.
- A PR-title policy bypass (a title edited after approval) is caught because the check
  also runs on `edited`; post-merge commit subjects are re-validated by `plan`.

## Consequences

- No version-bump commits and no tag-driven releases; a stable release is one manual
  dispatch that rebuilds an already verified commit, and `main` stays releasable at
  every commit.
- A stable release costs a second full native build and gate run (rare, manual), and its
  bytes differ from the nightly's; in exchange binaries, images, packages, the Homebrew
  formula and `notarxctl version` all report the clean stable version.
- Nightly builds cost runner minutes (two Linux legs, two darwin legs, arm64 gate)
  on days with releasing commits only.
- The release path depends on one shell script, one config file (`cliff.toml`) and
  git-cliff behaviour that is verified by fixtures rather than assumed; a git-cliff
  upgrade must pass the plan tests.
- `CONTRIBUTING.md` and PR templates change; contributors must write conventional
  titles; Dependabot configuration changes.
- ADR 0013 slice 10 shrinks to the development version and stamping; ADR 0016 slice 3
  no longer edits `release.yml` beyond calling reusable workflows until slice 4 below
  replaces it.
- Slices assume ADR 0025 (distribution channels) consumes `release-manifest.json`;
  it adds jobs after `publish` (both channels) and must not alter the plan. ADR 0026 slices 15 and
  16 add `publish-feeds` and `verify-feeds` after `publish` in the same way.

## Implementation slices

1. **Commit policy and PR-title lint.** Milestone: Foundation.
   Scope: `.github/commit-policy.json`, `scripts/commit-policy-check.mjs` and test,
   `.github/workflows/pr-title.yml`, `.github/pull_request_template.md`,
   `.github/dependabot.yml` commit-message prefixes, `CONTRIBUTING.md`, `Makefile`
   target `commit-policy-check` wired into `make preflight`.
   - [ ] tests cover valid, unknown type, unknown scope, multi-scope, `!` and footer breaking, empty subject
   - [ ] the policy file already lists the scopes `rpm`, `huggingface`, `signer` and `scanner` (ADRs 0026, 0027, 0030)
   - [ ] a scope-coverage test fails if `cmd/*` or `internal/protocol/*` has no scope alias
   - [ ] `make lint`, `make docs-check`, `make test` pass
   Dependencies: ADR 0013 slice 7 (contribution rules text).
2. **Version stamping and dev version.** Milestone: Foundation.
   Scope: `VERSION` (`0.0.0-dev`), `scripts/stamp-version.sh` and test,
   `scripts/release-version-check.sh` (`--expect`, dev mode, drop changelog and
   release-note checks), `internal/buildinfo` (`Core()`), `Makefile`,
   `.github/release-notes/` removal, `CHANGELOG.md` restart with ADR 0013 preamble,
   `CHANGELOG.zh-CN.md` removal and docs-i18n allow-list.
   - [ ] stamping `0.3.0-nightly.2` makes `release-version-check.sh --expect` pass and changes only listed files
   - [ ] unit test for `Core()` and a built binary reporting the stamped version
   - [ ] `make release-version-check`, `make openapi-check`, `make console-test`, `make test`, `make coverage` pass
   Dependencies: ADR 0013 slices 2-3 (names). Replaces ADR 0013 slice 10.
3. **Release plan script and git-cliff configuration.** Milestone: Foundation.
   Scope: `cliff.toml`, `scripts/release-plan.sh`, `scripts/release-plan-test.sh`
   (fixture git repositories), `scripts/release-required-checks.txt` (removed by the amendment of 2026-11-19).
   - [ ] fixtures prove 0.x rules (feat to minor, fix to patch, breaking to minor), `0.1.0` before any stable tag, counter-by-maximum after pruned tags, skip on no releasing commits, skip on repeated commit, non-releasing types never bump
   - [ ] stable mode fixtures: nightly tag resolves to its commit and core, `version_override` rules, existing stable tag refused
   - [ ] git-cliff version is pinned and the test fails if its bump behaviour deviates (nightly tags must be invisible through the stable-only tag pattern)
   - [ ] `make test`, `make lint` pass
   Dependencies: slice 1.
4. **Nightly release pipeline.** Milestone: Foundation.
   Scope: `.github/workflows/release.yml` (rewritten: `schedule`, `workflow_dispatch`,
   `plan`, `gates`, `build`, `publish` for both channels, concurrency group `release`,
   `cancel-in-progress: false`), `scripts/release-manifest.sh`, removal of the tag
   trigger and `verify-candidate`, and the manual registration of the release App
   (checklist in the PR; `scripts/repo-governance-apply.sh` of slice 8 later restricts
   tag creation to it).
   - [ ] a `workflow_dispatch` dry run in the PR shows plan output and the intended tags
   - [ ] a real nightly on a sandbox tag publishes a pre-release with SHA256SUMS, manifest and attestations; `gh attestation verify` accepts them
   - [ ] failed arm64 leg publishes no tag
   - [ ] a negative test shows `gh attestation verify --repo mlops-ca/notarx --signer-workflow mlops-ca/notarx/.github/workflows/image-build.yml` accepts the real attestation and rejects a bare-path `--signer-workflow` and an attestation from another workflow; the exact flags are recorded in `docs/releasing.md`
   - [ ] `release-manifest.json` `images` and the attestation check cover every image of the ADR 0016 matrix list (three once ADR 0030 slice 1b lands)
   - [ ] job graph keeps an extension point after `publish` for `publish-feeds` and `verify-feeds` (ADR 0026) without changing `plan`
   - [ ] `make release-version-check`, `make docs-check` pass
   Dependencies: slices 2-3; ADR 0016 slices 3-4 (reusable workflows).
5. **Stable release build and first-release gate.** Milestone: Foundation.
   Scope: `release.yml` stable branch of `plan` and `publish`, `scripts/baseline-gate.sh`
   and test, `scripts/promote-release.sh` (verifies the nightly record and creates tags,
   release and `:latest`).
   - [ ] dry run on a sandbox repository rebuilds a verified nightly commit as stable: `notarx version`, `notarxctl version`, `VERSION.txt`, image label and archive names all report the clean version and no artifact contains `nightly`
   - [ ] a stable run refuses a nightly without a manifest, with failing attestations or with a commit not on `main`
   - [ ] re-running a half-failed stable run converges; an existing `vX.Y.Z` fails the plan
   - [ ] `baseline-gate.sh` fixtures: extra migration, missing baseline, override other than `0.1.0` rejected
   - [ ] `make test`, `make docs-check` pass
   Dependencies: slice 4; ADR 0015 slice 7 (re-squash) before the real first run.
6. **Changelog PR and notes.** Milestone: Foundation.
   Scope: `release.yml` `changelog` job, `scripts/changelog-section.sh`.
   - [ ] opens `docs(changelog): vX.Y.Z` with a section that matches git-cliff output; CI runs on it via App token
   - [ ] milestone summary lists closed milestones
   Dependencies: slice 5 (the App is registered in slice 4).
7. **Retention and pruning.** Milestone: Foundation.
   Scope: `release.yml` `prune` job, `scripts/prune-releases.sh`, `scripts/prune-ghcr.mjs` and fixture tests.
   - [ ] fixtures: multi-arch index with attestation children is never orphaned; verified nightly kept; counter maximum kept; empty mark set aborts
   - [ ] dry-run default; real run proven once on a sandbox package
   - [ ] `make test`, `make lint` pass
   Dependencies: slice 4.
8. **Repository governance and documentation.** Milestone: Foundation.
   Scope: `scripts/repo-governance-apply.sh`, `docs/releasing.md` (release runbook,
   0.x rules, milestones convention, verification commands), docs index and
   `site-map.json`, creation of the themed milestones, liveness alert job.
   - [ ] script is idempotent and prints the effective rulesets
   - [ ] `docs/releasing.md` records the empirical outcome of the workflow-enable call (whether a schedule disabled by inactivity is revived) and states that the monthly liveness alert, not the call, is the guarantee
   - [ ] the themed milestones created are exactly `Foundation`, `Data scale`, `Identity & federation`, `Promotion chains`, `MySQL HeatWave`, `Formats`, `Public feeds`, `Supply-chain security` and `Backlog`
   - [ ] `make docs-check` passes
   Dependencies: slice 4.
9. **First stable release.** Milestone: Foundation.
   Run the stable dispatch with `version_override=0.1.0` after ADR 0015 slice 7 and
   ADR 0023 slices 1-10; no code change besides confirming the gate and the runbook.
   - [ ] `v0.1.0` exists, images `:0.1.0`, `:0.1`, `:latest` report `0.1.0`, and the tag targets the verified nightly's commit
   - [ ] `scripts/migration-immutability-check.sh` (ADR 0015 slice 6) is enforcing
   Dependencies: slices 5-8, ADR 0015 slices 6-7, ADR 0013 slices 1-9.

## Resolved questions

**Q1 — Does calling the workflow-enable API on every run reset GitHub's 60-day inactivity timer.** Decision: keep the call as best-effort alongside the monthly liveness alert and do not rely on it; record the empirical outcome in slice 8. GitHub documents only repository activity as resetting the timer, so the 35-day alert covers the gap.

**Q2 — Which `gh attestation verify` flags are correct for reusable-workflow attestations.** Decision: `--repo` plus the repo-qualified `--signer-workflow <owner>/<repo>/<path>`, adding `--source-ref` or `--cert-identity` only when pinning tags; confirmed by the slice 4 negative test. For a reusable workflow the signer is the called workflow, and `--signer-repo` alone is too loose.

**Q3 — Do the 1.0.0 criteria get their own ADR.** Decision: yes, a separate "1.0 readiness" ADR written when 0.x scope nears completion; this ADR requires only an explicit `version_override` for `1.0.0`. Stability promises depend on APIs not yet settled and the override guard already prevents an accidental 1.0.

## Amendment (2026-11-19): pushes to main run no tests; the release gates test the released commit

Status: accepted (2026-11-19)

Owner decision. CI runs no tests on pushes to `main`: every merge reran the full suite (66 jobs, about 225 runner-minutes) that pull request CI had just run, and it competed with pull requests for the concurrency limit of the public repository. Real results come from pull request CI and from the release run. This amendment supersedes the places above that read check runs of `main`:

1. `ci.yml` on a push to `main` runs only `mainline-plan`, `mainline-assets`, `mainline-images` (the `main` archives and images of ADR 0016) and `ci-ok`. Pull request behaviour is unchanged.
2. The test jobs live once, in the reusable workflow `test-suite.yml`, which pull request CI and the release gates both call. The `gates` job of `release.yml` runs the complete suite on the planned SHA: `quality`, `quality-arm64`, `console`, the Console E2E shards, `object-store-contract`, `docs`, `image-test-stages` and every mainline contract lane on both architectures (not only arm64). A gate failure means no release that night; the liveness alert covers a run of failed nights.
3. Nightly step 2 becomes: `sha` = the tip of `main`. `release-plan.sh` reads no check runs and `scripts/release-required-checks.txt` is removed, as is the `green` mode. Skip rules (same commit as the latest nightly, no releasing commit) are unchanged.
4. Stable step 2 no longer requires green checks on `main`; `release-plan.sh stable` reads no check runs. A stable run releases a commit only if the gates of a release run pass on it. The nightly tag it promotes exists only because those gates passed on that commit, so reusing that green nightly result for the same SHA is allowed. The stable workflow of slice 5 still runs its own `gates` (the "Stable runs the same job graph" section) by calling `test-suite.yml` on that SHA.
5. Consequences accepted by the owner: a push to `main` publishes the `main` archives and images without any test having run on that commit (consumers of the `main` tag take that risk; the nightly is the tested channel), and a failing gate on the tip of `main` blocks every nightly until the tip is fixed forward, because the plan no longer falls back to an older green commit. The liveness alert detects only an inactive schedule, so a run of red nights is noticed through the failed workflow runs of `release.yml`.

The "required check runs" language in the Context, in the `verify-candidate` description of the old workflow and in slice 3 describes the design before this amendment. ADR 0016's list of check names for `verify-candidate` is likewise historical.

## Amendment (2026-11-19): the Go checks of the test suite run in a lint job and one unit-test pass

Status: accepted (2026-11-19)

Issue #482 (pull request feedback took 26 to 35 minutes). The `quality` job of `test-suite.yml` ran the Go unit tests three times (`make race`, then `make coverage`, then each floored package again). It is now two jobs, and the list of gate jobs in item 2 of the amendment above gains `lint`:

1. `lint` runs `make lint-checks`, `make vet`, the native-build rules (`make native-build-check`, which ADR 0016's Guard section places in "the `quality` CI job"), the migration checks, the release and packaging checks, the SAML fuzz run and the dependency audit.
2. `quality` runs `make race-coverage`: the unit tests of every package once, with the race detector and `-coverprofile`, and every coverage floor computed from that profile.
3. Pull request CI and the release gates call the same suite, so both run both jobs; `suite-ok` requires `lint` with the Go class. Nothing that `quality` checked before is dropped.
