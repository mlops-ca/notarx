# CI path classes and the ci-ok gate

[Documentation index](README.md)

CI runs only what a change needs. The `changes` job in `.github/workflows/ci.yml`
runs `scripts/ci-changes.sh` over the files a pull request touches and exposes
five classes. Heavy jobs run only when their class changed.

| Class | Paths | Jobs it enables |
| --- | --- | --- |
| `go` | `*.go`, `go.mod`, `go.sum`, `migrations/`, `internal/`, `cmd/`, `contracts/`, `examples/`, `api/`, plus the files `release-version-check` reads (`CHANGELOG*.md`, `docs/cli*.md`, `VERSION`) | `quality`, `object-store-contract`, `contract-plan`, `contract`, `contract-and-test`, and `console-e2e` (it runs the real gateway) |
| `console` | `console/`, `api/` (the generated client) | `console` (unit tests, build, visual) and `console-e2e` |
| `docs` | `docs/`, `*.md`, `LICENSE`, `NOTICE` | `docs` (i18n twins, links, capability claims) |
| `images` | `Dockerfile*`, `compose*.yml`, `deploy/` | everything that executes the product (`go` and `console` too) |
| `ci` | `.github/`, `Makefile`, `scripts/` | everything |

A path that matches no class also runs everything, so a new top-level directory
cannot silently skip a job. Pushes to `main`, tags and unresolvable ranges run
everything, including the mainline asset and image publication jobs.
The `pr-title` check (Conventional Commit title, ADR 0024, `.github/workflows/pr-title.yml`) is a separate workflow that runs when a pull request is opened, edited, synchronized or reopened, so editing a title re-checks it without re-running CI. It is not part of `ci-ok`; require it in branch protection next to `ci-ok`.

Code directories are matched before Markdown, so a `.md` fixture under `internal/` is Go, not docs. A docs-only pull request therefore runs `changes`, `docs` and
`ci-ok` (plus the separate `pr-title` workflow). A Go-only pull request skips `console` (unit, build, visual) but still runs `console-e2e`; a console-only pull request
skips every contract lane.

## Console E2E sharding and spec selection

`console-e2e` is a matrix of four jobs. Shards `1`, `2` and `3` each start the
shared prebuilt gateway images and run `playwright test --shard=i/3`, so every
spec runs in exactly one shard; the fourth entry, `cli`, runs the `notarxctl` CLI
E2E against its own gateway instead of serializing it behind the Playwright
suite. Each matrix entry carries its own shard fraction, so add or renumber entries together. Each
shard uploads a Playwright blob report; `console-e2e-report` merges them with
`playwright merge-reports` into one HTML report artifact (`console-e2e-report`),
(it runs when every shard passed; failed shards upload their own blob report and traces) (`console-e2e-traces-<shard>`).
`ci-ok` requires the matrix result (every shard) and, when all shards passed, `console-e2e-report`.

The `changes` job also runs `scripts/ci-e2e-select.sh`. When every changed path is
an existing top-level `console/e2e/*.spec.ts` file, only those specs run (still
spread over the shards; a shard left without a spec passes and uploads no blob). Any other path (Console
sources, `e2e/support`, fixtures, a deleted spec, Go, CI files) or an
unresolvable range is uncertain and runs the full suite. No spec is skipped on
`main`, tags or any non-selected change.

The `console` job (unit tests, build, mocked visual baseline) and `console-e2e`
do not duplicate work: the visual config only matches `e2e/visual`, which the
behaviour config ignores, and the contract lanes never run Playwright.

## The ci-ok gate

`ci-ok` depends on every other job and always runs. It passes when each needed
job succeeded or was skipped by change detection, and fails on any failure or
cancellation. A job that change detection required but that was skipped (for
example because an upstream job failed) also fails the gate. Require `ci-ok` as the single branch-protection check; the set of
jobs behind it can change without editing the protection rule.

## Contract lanes

`scripts/ci-contract-lanes.json` is the only place that names lanes, their tool
needs and their timeouts. The `contract-plan` job turns it into a matrix
(`scripts/ci-contract.py matrix`) and `contract` runs one reusable
`contract-lane.yml` job per entry in parallel (no `max-parallel` cap; the free
hosted runner limit applies). The former serial `core` lane (about 34 minutes)
is split into four lanes by duration, so the full run is bound by its slowest
lane instead of their sum. Every step of the old lane runs in exactly one lane;
`upgrade-readiness` is the only step dropped (below). `contract-and-test` stays
as the stable aggregate named by the release gate and requires every lane.

Each lane uses the default shallow checkout, warms the Go module cache once with
retries (`GOPROXY=https://proxy.golang.org|direct`) and installs only the tools
it declares: Rust from `rust-toolchain.toml`, Node from `.nvmrc`, Go from
`go.mod`, and npm dependencies only for lanes that run OpenAPI or Console code
generation. npm timeouts live in `console/.npmrc` and `tools/openapi/.npmrc`.
Jobs run on the native runner of their leg (`ubuntu-24.04` or `ubuntu-24.04-arm`,
the `runner` input of `contract-lane.yml`) with a per-lane `timeout-minutes` from
the plan. A failing lane uploads its step logs and raw reports under a name that
carries the architecture.

Every lane runs on amd64. The plan gives each lane an `arm64` tier: `pull-request`
lanes (the four `core-*` lanes and the `six-format-auth` and `oci-discovery`
smoke subset) also run on arm64 for every pull request, and `mainline` lanes join
them on pushes to `main`. `scripts/ci-contract.py matrix --scope pull-request|mainline`
emits the matrix. A step that runs a third-party artifact published for one
architecture only (`published-native-backup-test` executes the published amd64
release binaries) declares `architectures` in the plan; other architectures skip
and report it, nothing is emulated. `quality-arm64` runs lint, vet and the race detector on
`ubuntu-24.04-arm`; the coverage floor stays on amd64. Both jobs call the local
composite action `.github/actions/go-quality` for setup, lint, vet and race, so the
steps have one definition. They stay two jobs, not a matrix, because the release gate
requires a check named exactly `quality`. See
[native builds](native-builds.md).

## Shared images

`build-images` builds the notarx and reference apt signer images once per run
(one parallel matrix leg each, BuildKit layers cached in the GitHub Actions
cache; the reference scanner has no CI consumer, so it is not built here) and uploads each as a tarball artifact named
`image-<runner arch>-<image>`. Every contract lane and `console-e2e` downloads
the artifacts for its own runner architecture and `docker load`s them, so no job
rebuilds an image from source. `compose.yml` names the images
(`notarx-gateway:local`, `notarx-reference-apt-signer:local`,
`notarx-reference-scanner:local`; override with `NOTARX_IMAGE`,
`NOTARX_APT_SIGNER_IMAGE`, `NOTARX_SCANNER_IMAGE`). Lanes set
`NOTARX_COMPOSE_BUILD_FLAG=--no-build`; the e2e scripts use it in place of
`--build`, and unset (local runs) they still build from source. Scripts that
build with their own version arguments (`backup-restore-readiness`,
`performance-baseline`) keep a per-project image name so they never touch the
shared tag. `ci-ok` requires `build-images` whenever `go` or `console`
changed. `build-images` runs each image on both native runners (`ubuntu-24.04`
and `ubuntu-24.04-arm`), and a contract lane downloads the artifacts of its own
runner architecture.

The `oci-free-tier` and `oci-free-tier-tofu` jobs run when the `images` class changed
(`deploy/` is in it). `oci-free-tier` runs on the native arm64 runner, because the
reference deployment targets the Oracle Ampere A1 shape: it checks the rendered
Compose model and resource limits (`make oci-free-tier-check`), asserts that every
third-party image publishes `linux/arm64` (`make oci-free-tier-images-check`; the
project's own images are gated by the release pipeline) and smoke-tests the stack by
pushing and pulling an OCI image and a Raw file through Caddy
(`make oci-free-tier-smoke`). `oci-free-tier-tofu` formats, validates and lints the
optional OpenTofu module (`make oci-free-tier-tofu-check`). `ci-ok` requires both
whenever `images` changed.

The Dockerfiles no longer run `go test` while building an image: the suite runs
once in `core-checks` (`make test`) and `quality`. The tests live in an opt-in
`test` stage (`make docker-test`, run by `image-test-stages` when Dockerfiles or
compose files change) that BuildKit skips for image builds.

CI does not fetch an upstream tag or run `upgrade-readiness`: ADR 0015 has no
upgrade path from upstream. `scripts/upgrade-readiness.sh` (kept, with its make
target) is parameterized on stable notarx release tags and reports a no-op until
one exists; re-add the CI step once a second notarx release exists.
`make backup-transfer-test` (lane `core-backup`) runs the same upgrade properties
against the real migrations and a synthetic `000008` migration meanwhile.

## Object store contract

`object-store-contract` (`.github/workflows/object-store-contract.yml`, called from
`ci.yml` with the `go` class) runs the shared object store contract
(`internal/objectstore/storetest`, ADR 0017) against the S3 driver on RustFS
and on a second S3-compatible implementation, once per checksum mode where the
service supports it. The same suite runs against the in-memory store in
`make test` and against RustFS in `make integration-test`; the
`./internal/objectstore` coverage floor in `scripts/coverage-packages.txt` is set
so it fails if the suite stops running. The second service has no published
container image, so the job builds it from a pinned upstream revision with the
repository Go toolchain; bump `MINIO_VERSION` in the workflow to move it.
`scripts/rustfs-only-contract-test.sh` exempts that one workflow file.

## OCI live run

`oci-live` (`.github/workflows/oci-live.yml`) is separate from `ci.yml` and is not
part of `ci-ok`. It runs the object store contract against Oracle Cloud Object
Storage with the `ocilive` build tag (see [object storage](object-storage.md)).
Triggers are pushes to `main` that touch `internal/objectstore/`, a weekly schedule
and a manual dispatch, in this repository only; never `pull_request` or
`pull_request_target`, so untrusted code cannot reach the secrets. The job detects
the `OCI_OBJECT_STORE_*` secrets in a first step and skips every other step, with a
notice, when they are absent.

## Re-record the Maven and Gradle fixtures

Maven and Gradle client tests resolve plugins and dependencies from recorded
Maven Central and Gradle plugin portal responses, never from the network. The
recordings live in `internal/mavenfixture/testdata/maven-central-fixture`: the
`maven2/` directory is Maven Central and `gradle-plugins/` is the Gradle plugin
portal. `internal/mavenfixture` serves them (Go tests start it with
`mavenfixturetest.Start`; `cmd/maven-fixture-mirror` serves it to scripts). A
path that was not recorded answers 404 and the log names the re-record hint.
`go test ./internal/mavenfixture` checks every recorded artifact against the
SHA-1 sidecar Central published next to it.

Recording needs `mvn`, `gradle`, `go` and outbound network, which the agent
machines lack, so it runs in CI as a manual workflow that normal CI never starts:

1. Run **Record Maven fixtures** (`.github/workflows/record-maven-fixtures.yml`)
   from the Actions tab (`gh workflow run record-maven-fixtures.yml`). It builds
   `.github/maven-fixture-recorder/Dockerfile` (Maven and Gradle, both source
   images pinned by digest; bump the digests together), runs
   `scripts/record-maven-fixtures.sh` in it with
   `MAVEN_CENTRAL_FIXTURE_RECORD=1`, and uploads `maven-fixtures.tar`.
2. Download the artifact, extract it at the repository root, review the new files
   and commit them in a pull request.

In record mode a miss is fetched from the upstream, stored and served, and an
existing file is never replaced. To refresh a recording delete the file first.

## Migration checks

The `quality` job runs `make migration-check`
(`scripts/migration-immutability-check.sh`, checkout with full history and tags).
It always enforces unique, gap-free, snake_case migration names. Once a stable
`vX.Y.Z` tag exists it also fails any diff of a released migration against the
latest stable tag; nightly tags are ignored. `make test` runs the script's bash
fixtures.

The temporary `baseline-equivalence` workflow
(`.github/workflows/baseline-equivalence.yml`, ADR 0015 slice 3) runs on pull
requests that touch `migrations/000001_baseline.sql` or the baseline tooling
(an ordinary `000002+` migration does not trigger it; the script refuses a source
chain that is only the baseline itself). On linux/amd64
and linux/arm64 native runners it proves `migrations/000001_baseline.sql`
equivalent to the migration chain at the pull request base (schema dump, data dump,
catalogs, no PUBLIC privileges), then runs `make integration-test` and the
`core-native-e2e`, `nuget`, `helm`, `rubygems` and `oci-discovery` lanes against
the baseline. It is not part of `ci-ok` and is removed with the re-squash.

Run `bash scripts/ci-changes-test.sh` to check the classifier fixtures.

## Dependency updates

Dependabot (`.github/dependabot.yml`) runs weekly, on Mondays, for six
ecosystems: `gomod` (`/`), `npm` (`/console` and `/tools/openapi`), `docker`
(`/`, the Dockerfiles), `docker-compose` (`/`, the compose files) and
`github-actions` (`/`). Each ecosystem opens at most two pull requests
at a time, one grouped pull request for minor and patch updates and one for major
updates, so a sweep cannot starve batch pull requests of CI runners. Titles carry
the `build(deps)` or `ci(deps)` prefix so the `pr-title` check passes.

Rules for every update, whether Dependabot or a person lands it:

- Docker base images stay pinned by digest (`image:tag@sha256:...`) and GitHub
  Actions stay pinned by commit SHA with the version in a trailing comment.
- Toolchain versions must agree: `go.mod`, every `golang:` image (Dockerfiles,
  `compose.integration.yml`, `Makefile`) and the Node major in `.nvmrc` and
  `Dockerfile.console`. `scripts/tests/test_ci_contract.py` fails on drift, and
  Dependabot cannot edit the Makefile, so bump it by hand in the same pull request.
- Fix the code, tests and configuration an update breaks instead of pinning back.
  A truly incompatible update is deferred with an `ignore` entry in
  `dependabot.yml` that carries a comment and a link to the follow-up issue.
- Validate `dependabot.yml` against the SchemaStore `dependabot-2.0` schema before
  merging a change to it.
- Close superseded individual Dependabot pull requests with a comment linking the
  pull request that replaced them.

## Release plan

`scripts/release-plan.sh` is the one place a release version is computed (ADR 0024).
It is read-only and prints `skip`, `channel`, `version`, `core`, `sha` and `previous`.
The bump comes from git-cliff, pinned in `scripts/git-cliff.version` and installed
with a checksum check by `scripts/install-git-cliff.sh`; `cliff.toml` holds the
0.x rules. `scripts/release-required-checks.txt` is the single list of check names
that must be green on a commit before it is released. `make release-plan-test`
runs the plan against fixture repositories and fails if git-cliff's bump behaviour
deviates from the pinned expectations. Wiring the plan into the release workflow is
a later slice of ADR 0024.

## Release dry run

`.github/workflows/release-dry-run.yml` rehearses the publication path on a pull
request, so a major bump of `actions/checkout`, `download-artifact`,
`upload-artifact` or a Docker action is tested before a tag is cut. It runs when
a file it exercises changes (the release and image workflows, the artifact scripts,
the Dockerfiles) and on demand through `workflow_dispatch`. It calls
`release-archives.yml` (four native runners, artifact download, checksum union) and
`image-build.yml` with `dry-run: true`: each image is built natively and loaded
locally, the labels and version are checked, the SBOM is scanned, and the digest and
SBOM artifacts are handed to `image-manifest`, which validates them. Nothing signs in,
pushes, tags or attests, so push-by-digest, the pull back by digest, the tag merge and
the attestations are not rehearsed; they first run on `main` and on a release. It is path filtered, so it is not a required check and not
part of `ci-ok`. `release.yml` itself (tag verification and `publish-release`) is not
callable and is still first exercised by a real release.
