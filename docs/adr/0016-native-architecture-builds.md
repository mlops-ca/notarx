# Native Per-Architecture Builds

[Documentation index](../README.md)

Status: accepted (2026-11-17)

See also: [ADR 0026](0026-debian-and-rpm-repositories.md), [ADR 0030](0030-sbom-generation-and-vulnerability-scanning.md).

## Context

notarx ships for linux/amd64 and linux/arm64 (the Oracle Cloud Always Free
Ampere A1 reference deployment of ADR 0017 is arm64) plus darwin CLI binaries.
Today every non-native artifact is produced by cross-compilation from one
amd64 Linux runner:

- `Dockerfile` and `Dockerfile.agctl` start with
  `FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine@sha256:...` and run
  `CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build` (`Dockerfile`
  lines 1-21, `Dockerfile.agctl` lines 1-13). `Dockerfile.console` also pins its
  Node build stage to `$BUILDPLATFORM` (line 1). `Dockerfile.scanner` and
  `Dockerfile.apt-signer` carry no platform override and already build natively.
  (ADR 0030 slice 1b renames `Dockerfile.scanner` to `Dockerfile.notarx-scanner` and publishes it as a third image.)
- `.github/workflows/ci.yml` (`mainline-images`, around line 290) and
  `.github/workflows/release.yml` (`publish-images`, around line 111) build one
  `platforms: linux/amd64,linux/arm64` image on `ubuntu-latest` with
  `docker/build-push-action`; the arm64 half exists only because the build stage
  cross-compiles. `release.yml` then promotes the resulting digest with
  `docker buildx imagetools create`.
- `Makefile:14` sets `CLI_TARGETS ?= linux/amd64 linux/arm64 darwin/amd64
  darwin/arm64`, and `cli-build` (Makefile around line 238) loops over them with
  explicit `GOOS`/`GOARCH`. `scripts/build-release-artifacts.sh` (lines 123-190)
  does the same for the server, healthcheck and agctl archives, and
  `build-assets` / `mainline-assets` run it once on `ubuntu-latest`.
- Every CI job is `runs-on: ubuntu-latest` (amd64): `ci.yml`, `contract-lane.yml`
  (line 22), `contract-benchmark.yml`. No test has ever executed on arm64.
  `Dockerfile` runs `go test ./...` inside the build stage, but because that
  stage is pinned to `$BUILDPLATFORM` it tests amd64 code even when producing the
  arm64 image.

Consequences of this shape: the arm64 binary that users run is the one binary
no pipeline has executed; arm64-only defects (atomic alignment, `unsafe`,
architecture-specific dependency code, page-size assumptions) are invisible;
darwin archives are never run before release; and the build is only
"multi-arch" through a flag that is easy to reintroduce in a form that needs QEMU.

GitHub-hosted arm64 Linux runners (`ubuntu-24.04-arm`) are free for public
repositories, so the historical cost argument for cross-compiling does not apply
to this fork.

## Considered designs

1. **Keep cross-compilation.** Fastest wall-clock for the build itself, one
   runner, no manifest assembly. Rejected: untested arm64 artifacts remain, and
   the `$BUILDPLATFORM` pattern is exactly what lets amd64 tests approve an arm64
   release.
2. **QEMU emulation for the target platform.** Removes the `$BUILDPLATFORM`
   trick but runs the Go toolchain and `go test` under emulation. Typically 5-20x
   slower, flaky under memory pressure, and emulated tests prove little about
   real hardware. Rejected.
3. **Native build and test per architecture, manifest assembled afterwards
   (chosen).** Each architecture builds, tests and pushes on matching hardware;
   one small job merges the per-architecture digests into the multi-arch tag.
   Costs a second runner per job and a merge step, and makes the CI graph larger.
4. **Self-hosted arm64 runners (e.g. on the Always Free A1 tenancy).** Cheap, but
   introduces untrusted-code execution on infrastructure we operate, runner
   maintenance, and a supply-chain trust root. Rejected permanently: the repository is public
   and hosted arm64 runners are used on every run.

## Decision

Option 3. notarx performs no cross-compilation and no emulation anywhere in
build, test or release. This ADR is part of the v0.1.0 foundation set (ADRs
0013-0017); it runs in parallel with ADRs 0014 and 0017 after ADR 0013
slices 1-8 and 11, per the ordering table in ADR 0013.

**Ownership of release workflows.** This ADR owns the per-architecture build, merge and attest
mechanics as reusable `workflow_call` workflows (`.github/workflows/image-build.yml` and the archive
matrix). `.github/workflows/release.yml` (plan, gates, publish, promote, prune) is owned by
ADR 0024, which calls these reusable workflows; the tag-triggered release is removed there, and
until ADR 0024 slice 4 replaces the file, slices 3-4 of this ADR edit it only to call the reusable
workflows. ADR 0013 slices that touch `release.yml` land before slice 3 of this ADR as pure
string edits; no other ADR restructures the file. This is consistent with the single-database stance of
ADR 0017 (the only supported runtime targets are those we also test on) and with
the fork charter of ADR 0013.

### Runner matrix

| Target | Runner label | Produces |
|---|---|---|
| linux/amd64 | `ubuntu-24.04` | images, archives, tests |
| linux/arm64 | `ubuntu-24.04-arm` | images, archives, tests |
| darwin/arm64 | `macos-15` | `notarxctl` (and server) archives |
| darwin/amd64 | `macos-15-intel` | `notarxctl` archives; kept until the label retires (August 2027), then dropped |

Labels are pinned to explicit OS versions, not `ubuntu-latest`, so that the
runner image changes only by a reviewed edit. `macos-15-intel` is the last x86_64 hosted macOS image; GitHub states it is
available until August 2027 (actions/runner-images#13045). The guard
(below) fails on or after 2027-09-01 if the label is still referenced, so the
retirement cannot pass unnoticed: `darwin/amd64` ships until then and is removed when the guard fires, never
cross-built. Windows is not a supported target;
the `windows` branch accepted by `scripts/build-release-artifacts.sh:132` is
removed.

### Dockerfiles

- Remove `--platform=$BUILDPLATFORM` from every `FROM` in `Dockerfile` and
  `Dockerfile.console` (and from `Dockerfile.agctl` if it still exists; ADR
  0013 removes it, because the CLI is not published as an image). The build stage runs on the same
  platform it produces; `docker buildx build --platform linux/<arch>` on a
  matching runner is the only supported invocation.
- Remove `ARG TARGETOS`/`TARGETARCH` and the `GOOS=`/`GOARCH=` environment from
  the Go build lines. Go defaults to the host, so the toolchain, the
  `go test ./...` step and the produced binary all share one architecture.
- `Dockerfile.scanner` and `Dockerfile.apt-signer` already comply; they are
  covered by the guard below so they cannot drift.
- Base images stay pinned by index digest (multi-arch indexes), so each runner
  resolves its native variant of the same reviewed digest.
- The Console static bundle is architecture independent, but it is still built
  per architecture image. This is a few minutes of CPU in exchange for one rule
  without exceptions; the bundle is not shared across images.
- **Runners.** The repository is and stays public. Every run (pull requests, `main`, tags) uses
  GitHub-hosted `ubuntu-24.04` (amd64) and `ubuntu-24.04-arm` (arm64) runners. There are no self-hosted
  runners (design 4 is rejected for good) and no single-arch fallback for any run.

### Image publishing

Two images are published by v0.1.0: `ghcr.io/mlops-ca/notarx` and
`ghcr.io/mlops-ca/notarx-console`. `notarxctl` ships as release archives and
`go install`, never as an image (ADR 0013); `notarx-signer` (ADR 0026) is a release
archive, not an image, and the apt reference signer stays an unpublished fixture.
The image list is a single matrix input of `image-build.yml`: ADR 0030 slice 1b adds
`ghcr.io/mlops-ca/notarx-scanner` (built from `Dockerfile.notarx-scanner`) as the
third and last published image by adding one matrix entry, to the `image-manifest`
jobs and to the release gate, and `release.yml` then expects three `mainline-images`
runs. No other image is published without its own ADR. Per image, a two-stage workflow replaces the single `build-push-action` call:

1. `image-build` matrix `{image} x {linux/amd64 on ubuntu-24.04, linux/arm64 on
   ubuntu-24.04-arm}`: `docker/build-push-action` with
   `outputs: type=image,name=ghcr.io/<owner>/<image>,push-by-digest=true,name-only=true,push=true`,
   `provenance: false`, `sbom: false` (so each pushed digest is a plain image
   manifest, see Properties), and `cache-from/to: type=gha,scope=<image>-<arch>`. It uploads the resulting
   digest as an artifact `digest-<image>-<arch>`.
2. `image-manifest` (one job per image, `needs: image-build`): downloads both
   digests, then runs
   `docker buildx imagetools create -t <tag> <image>@<amd64-digest> <image>@<arm64-digest>`
   for the mainline tags (`main`, `main-sha-<12>`) or the release tags
   (`<version>`, `<major.minor>`, `latest` of `release.yml`). It then attests
   the digest of the resulting index (see Properties).

Properties:

- Attestation design (settled here, not deferred). BuildKit attestations
  (`provenance`/`sbom`) turn every pushed per-arch digest into an index holding
  the image plus attestation manifests and referencing per-platform subjects.
  Feeding such digests to `imagetools create` is known to yield index entries
  without a platform and attestations that do not match the final index. So
  per-architecture builds push plain image manifests, and the `image-manifest`
  job attests the **final index digest** with `actions/attest-build-provenance`
  (provenance) and `actions/attest-sbom` (SBOM from `anchore/sbom-action`
  scans that each native leg runs against its own image and uploads as an
  artifact), both with `push-to-registry: true`. Verification is therefore
  `gh attestation verify oci://<image>:<tag>` on the tagged index; acceptance is
  that pulling `<tag>` on each architecture yields a verifiable attestation.
  The per-leg native execution evidence is the version/label check below, not a
  per-platform provenance statement.
- The release verification of `release.yml` (version and revision label checks,
  `docker run ... version`) runs per platform: the amd64 job executes the amd64
  image and the arm64 job executes the arm64 image natively before the manifest
  job is allowed to run. Because push-by-digest tags nothing, each leg pulls its
  image from the registry by the digest it just pushed (`docker pull
  <image>@<digest>`) before running it. The manifest job only tags; it never rebuilds.
- A failure of either architecture fails the release; there is no "amd64-only"
  partial publication because the tags are only created in stage 2.
- The "Confirm this run still owns the main tip" guard of `mainline-images` moves
  into `image-manifest`, immediately before tagging.
- `verify-candidate` in `release.yml` (removed; see the amendment at the end of this ADR) requires named check runs
  (`quality`, `contract-and-test`, `console`, `mainline-assets`, and one
  `mainline-images ` run per published image: two at v0.1.0, three once ADR 0030 slice 1b lands). Those names change; the check list is updated to the
  new job names and to require both architecture runs of each image.

### Release archives

`scripts/build-release-artifacts.sh` stops iterating a target list. It builds
only for the host (`go env GOOS`/`go env GOARCH`) and rejects `--target` values
that differ from the host, so a cross build is impossible rather than merely
unused. The `build-assets` and `mainline-assets` jobs become a matrix over the
four runners above, each uploading `dist/` slices as
`<product>-<version>-<goos>-<goarch>` artifacts. A final `assemble-release` job
downloads all slices, runs `scripts/write-release-checksums.sh` over the union,
and uploads the combined set; the checksum file is therefore produced once from
natively built archives. `make cli-build` is reduced the same way (host target,
output `dist/notarxctl_<goos>_<goarch>`), and `CLI_TARGETS` is removed from
`Makefile:14`. The Console bundle is built once in the linux/amd64 leg and
embedded in all archives, or is rebuilt per leg if `--skip-console` is not set;
the slice picks the simpler of the two without changing archive contents.

### Tests on both architectures

- `make test`, `make race`, `make vet` and `make lint` run on both Linux
  runners. Coverage floors (`scripts/coverage-check.sh`) are enforced on amd64
  only, since coverage is architecture independent; arm64 runs the same tests
  without the floor.
- `contract-lane.yml` gains an `runner` input (default `ubuntu-24.04`) and
  includes the architecture in evidence and artifact names so legs do not
  collide. The `contract-core` lane and a native-format smoke subset
  (`six-format-auth`, `oci-discovery`) run on arm64 for every pull request. The
  remaining `contract-native` lanes run on arm64 on pushes to `main` and nightly.
  This bounds pull-request wall-clock; the required-check list contains the
  PR-time arm64 legs only.
- **Release gate.** Because `verify-candidate` only inspects checks on the tagged
  SHA, a release could otherwise be tagged without arm64 native coverage on that
  commit. `release.yml` therefore runs the full arm64 `contract-native` matrix
  (via `contract-lane.yml` with the arm64 runner) as a release pre-step
  (`arm64-native-gate`), and `image-manifest` and `assemble-release` `need` it.
- `contract-benchmark.yml` stays amd64: benchmarks compare against recorded
  baselines that are hardware specific, and arm64 baselines would be recorded
  separately later.
- darwin legs run unit tests for `cmd/notarxctl` and the CLI packages plus a
  `notarxctl version` smoke test on the produced binary; they do not run the
  Docker-based contract lanes (no Docker on hosted macOS runners).

### Local development

Docker Desktop on Apple Silicon builds native linux/arm64 images, and on amd64
Linux or Windows hosts native linux/amd64, with no flags. `make build` and
`make docker-build` keep calling plain `docker build`. Producing the other
architecture locally is unsupported for release purposes; developers who need it
rely on the CI artifacts. `make cli-build` on a developer machine builds that
machine's native binary. The Oracle A1 reference deployment of ADR 0017 runs the
arm64 image built and tested on `ubuntu-24.04-arm`.

### Guard

`scripts/native-build-check.sh` (with a Node or shell self-test, run by
`make native-build-check`, included in `make preflight` and the `quality` CI
job) fails when any of the following appears outside an explicit allowlist file
`scripts/native-build-allowlist.txt`:

- `--platform=` on a `FROM` line in any `Dockerfile*`, or `TARGETARCH`,
  `TARGETOS`, `BUILDPLATFORM` references;
- `GOARCH=` or `GOOS=` assignments in `Dockerfile*`, `Makefile`, `scripts/`
  and `.github/workflows/` (the release script may reference
  `go env GOOS/GOARCH`, which is not an assignment; the allowlist exists for the
  script's own self-test fixtures);
- `docker/setup-qemu-action`, `qemu-user-static`, or `tonistiigi/binfmt`;
- the `macos-15-intel` runner label on or after 2027-09-01 (calendar guard,
  tested with an injected date);
- `self-hosted` in any `runs-on` value;
- a `platforms:` key whose value lists more than one platform in
  `docker/build-push-action` steps, and any `CGO_ENABLED=1` combined with a
  `CC=` cross compiler.

The check includes a negative fixture directory so its own tests prove each
pattern is detected.

## Security considerations

- Each image is built and executed on its native architecture before tagging,
  so the attested index never covers a binary that no pipeline ran. The
  provenance subject is the final index digest, produced by the manifest job's
  workload identity (`id-token: write`, `attestations: write`).
- The manifest job holds `packages: write` and no source checkout beyond tag
  metadata. It cannot alter layer content, only combine already attested
  digests; it verifies that each referenced digest carries the expected
  `org.opencontainers.image.revision` label before tagging.
- Hosted runners only; no self-hosted runner executes pull-request code.
- Third-party actions stay pinned by commit SHA as in the current workflows; the
  new `download-artifact` and `upload-artifact` steps follow the same pin.
- Digest artifacts are passed between jobs of one workflow run and contain only
  digests, not credentials.

## Consequences

- Every released binary and image is built and, for Linux, executed on its own
  architecture. Architecture-specific bugs surface in pull requests, not in
  production on the arm64 reference deployment.
- CI runner minutes roughly double for Linux jobs; release wall-clock is bounded
  by the slower leg, not their sum, because legs run in parallel. Free hosted
  arm64 capacity for public repositories keeps the cost at zero while the
  repository stays public.
- The workflow graph grows (matrix legs plus manifest and assemble jobs), and
  required-check names change once; branch protection must be updated at merge
  of slice 3.
- Contributors cannot produce a release-grade foreign-architecture image on
  their laptop. This is intentional.
- `scripts/build-release-artifacts.sh` loses its multi-target interface; callers
  that relied on `--target` for a foreign target (documentation in `docs/` and
  `scripts/release-readiness-check.sh`) are updated.
- The release set at v0.1.0 is two images plus archives (a third image, `notarx-scanner`, is added by ADR 0030); v0.1.0 ships this ADR
  together with ADRs 0013-0015 and 0017.
- A runner-label or capacity outage for one architecture blocks the release
  instead of silently producing a partial one.

## Implementation slices

1. **Dockerfile native build.**
   Scope: `Dockerfile`, `Dockerfile.console` (and `Dockerfile.agctl` if still
   present); confirm `Dockerfile.scanner` and `Dockerfile.apt-signer` untouched.
   - [ ] No `--platform=`, `TARGETOS`, `TARGETARCH`, `GOOS=` or `GOARCH=` remains
     in any `Dockerfile*`.
   - [ ] `make build`, `make console-docker-build` pass
     on an Apple Silicon machine and on amd64 Linux; `docker image inspect`
     shows the host architecture.
   - [ ] Image `version` subcommand output and OCI labels unchanged.
   Dependencies: ADR 0013 slice 3 (binary and directory renames); ADR 0013 slice 5
   removes `Dockerfile.agctl`.

2. **Native-build guard.**
   Scope: `scripts/native-build-check.sh`, allowlist, calendar guard for
   `macos-15-intel`, negative fixtures and
   self-test, `Makefile` target `native-build-check`, `quality` job in `ci.yml`.
   - [ ] Self-test proves each forbidden pattern in the Guard section is
     detected and the compliant tree passes.
   - [ ] The `macos-15-intel` guard fails with an injected date of 2027-09-01.
   - [ ] The guard fails on a `self-hosted` runner label (negative fixture).
   - [ ] `make native-build-check` is wired into `make preflight`.
   - [ ] `make lint` and `make docs-check` pass.
   Dependencies: slice 1 (the tree must be clean first).

3. **Per-architecture image workflows.**
   Scope: `.github/workflows/ci.yml` (`mainline-images`),
   `.github/workflows/release.yml` (`publish-images`, `verify-candidate` check
   names, `arm64-native-gate`), a reusable workflow `.github/workflows/image-build.yml`.
   - [ ] amd64 and arm64 legs build on `ubuntu-24.04` and `ubuntu-24.04-arm`,
     push by digest, and upload digests.
   - [ ] Only the images listed in the single matrix input are published (`notarx` and `notarx-console` at v0.1.0); adding an image is one matrix entry plus its release-gate check (ADR 0030 slice 1b adds `notarx-scanner`).
   - [ ] Manifest job creates the multi-arch tags; `imagetools inspect` shows
     both platforms with platform fields set; pulling `<tag>` on each
     architecture yields an attestation that `gh attestation verify` accepts.
   - [ ] Each leg runs its image pulled by digest from the registry.
   - [ ] `release.yml` requires a successful arm64 `contract-native` run
     (`arm64-native-gate`) before tagging.
   - [ ] Per-platform version and revision label checks run on the matching
     runner before tagging; a deliberately failing arm64 leg blocks tagging
     (demonstrated in the PR with a dry-run workflow dispatch).
   - [ ] Required-check names in `release.yml` match the new job names;
     `make release-version-check` and `make docs-check` pass.
   Dependencies: slice 1; ADR 0013 slice 5 (registry and image names; the two
   ADRs coordinate on `release.yml` through this slice).

4. **Matrix release archives.**
   Scope: `scripts/build-release-artifacts.sh`,
   `scripts/build-release-artifacts-test.sh`, `scripts/write-release-checksums.sh`
   (only if needed), `Makefile` (`cli-build`, remove `CLI_TARGETS`),
   `ci.yml` `mainline-assets`, `release.yml` `build-assets` and a new
   `assemble-release` job.
   - [ ] The script builds host-only and exits non-zero for a foreign `--target`
     (test added to `build-release-artifacts-test.sh`).
   - [ ] Four-leg matrix on `ubuntu-24.04`, `ubuntu-24.04-arm`, `macos-15`,
     `macos-15-intel`; each leg smoke-runs its own binary.
   - [ ] One combined checksum file covers all archives; `make release-artifacts-test`
     and `make release-readiness-check` pass.
   Dependencies: slice 2; ADR 0013 slice 3 (`notarx`, `notarxctl` names).

5. **Tests and contract lanes on arm64.**
   Scope: `ci.yml` (`contract-and-test`, `quality`), `contract-lane.yml`
   (`runner` input, architecture in evidence/artifact names, evidence-prefix
   validation), `contract-benchmark.yml` (pinned labels only).
   - [ ] `make test`, `make race`, `make vet` run on `ubuntu-24.04-arm`; coverage
     floor still enforced on amd64.
   - [ ] `contract-core` and the smoke subset run on arm64 per PR; the full
     `contract-native` matrix runs on arm64 on `main` and nightly.
   - [ ] Artifact and evidence names are unique per architecture.
   - [ ] Pinned runner labels replace every `ubuntu-latest` in
     `.github/workflows` (about five jobs in `ci.yml`, four in `release.yml`,
     plus the contract workflows); `grep ubuntu-latest` finds nothing.
   Dependencies: slice 2. Independent of slices 3 and 4.

6. **Documentation.**
   Scope: `docs/` build and release pages, `docs/adr/README` index if one
   exists, contributor guide section on local builds.
   - [ ] Documents the runner matrix, the unsupported-foreign-build rule and the
     Apple Silicon local story.
   - [ ] `make docs-check` passes.
   Dependencies: slices 3-5.

## Resolved questions

**Q1 — Keep `darwin/amd64` `notarxctl` until `macos-15-intel` retires.** Decision: ship it until the label retires (August 2027) and let the calendar guard force removal. It costs one native CI leg, honours the no-cross-compilation rule and has a hard decision date.

**Q2 — Build the Console static bundle once and share it across both architecture images.** Decision: no; build natively per architecture with no exception. CI minutes are free for a public repository and one uniform rule keeps the guard simple.

**Q3 — Self-hosted arm64 runners or single-arch PR CI if the repository becomes private.** Decision: neither applies: the repository is and stays public, and CI uses GitHub-hosted amd64 and arm64 runners on all runs (PRs, `main`, tags), with no self-hosted runners and no single-arch fallback. Hosted arm64 is free for public repositories and removes the trust and maintenance cost of self-hosting.

## Amendment (2026-11-19): required checks on main are gone

Status: accepted (2026-11-19)

Pushes to `main` run no tests (ADR 0024, amendment of the same date), so no check run of `main` is required before a release any more and the list of check names under "Release archives" and the `verify-candidate` bullet is historical. The arm64 gate is now part of the complete test suite (`test-suite.yml`) that the release `gates` job runs on the planned commit, on both architectures. `mainline-assets` and `mainline-images` still build and publish the `main` archives and images on every push and no longer wait for tests.

## Amendment (2026-11-19): darwin archives are built by the release, not on main

Status: accepted (2026-11-19)

Main CI run 44557339551 went red only because `mainline-assets / archives (darwin/arm64)` waited 16 minutes for a hosted macOS arm64 runner that GitHub never assigned (capacity constraints). Native builds stay mandatory (cross-compiling darwin on Linux stays refused), but nothing consumes the darwin archives of a `main` build: they are a build check, and the release catches real darwin build failures. So `mainline-assets` now builds only the two Linux legs (the `darwin: false` input of `release-archives.yml`), and the release pipeline, which calls the workflow with all four runners, still requires native darwin/arm64 and darwin/amd64 archives. If a release runner is never acquired, the `build` job fails with a message that explains how to tell an unassigned runner from a real failure. Consequence: a darwin-only break now first shows in the release dry run on a pull request that touches the release files or in the nightly release, not on every push to `main`; the nightly release covers every commit since the previous one.
