# CI path classes and the ci-ok gate

[Documentation index](README.md)

CI runs only what a change needs. The `changes` job in `.github/workflows/ci.yml`
runs `scripts/ci-changes.sh` over the files a pull request touches and exposes
six classes. Heavy jobs run only when their class changed.

The test jobs are defined once, in the reusable workflow `.github/workflows/test-suite.yml`.
Pull request CI calls it as the `tests` job with the classes of the change. The nightly
release gates call it with every class on, on the planned commit (see
[releasing](releasing.md)). **A push to `main` runs no tests of this suite** (ADR 0024, amendment of
2026-11-19; the separate path-filtered `oci-live` workflow below still runs on its own triggers): it runs only `mainline-plan`, `mainline-assets` and `mainline-images`, which
build and publish the `main` archives and images, plus `ci-ok`. Real results come from pull
request CI and from the nightly release gates. Inside a pull request run the suite's jobs
show as `tests / <job>`.

| Class | Paths | Jobs it enables |
| --- | --- | --- |
| `go` | `*.go`, `go.mod`, `go.sum`, `migrations/`, `internal/`, `cmd/`, `contracts/`, `examples/`, `api/`, plus the files `release-version-check` reads (`CHANGELOG*.md`, `docs/cli*.md`, `VERSION`) | `lint`, `quality`, `release-checks`, `quality-arm64`, `object-store-contract`, `contract-plan`, the contract jobs (`contract-early`, `contract-amd64`, `contract-arm64`), and `console-e2e` (it runs the real gateway) |
| `console` | `console/`, `api/` (the generated client) | `console` (unit tests, build, visual) and `console-e2e` |
| `docs` | `docs/`, `*.md`, `LICENSE`, `NOTICE` | `docs` (i18n twins, links, capability claims) |
| `images` | `Dockerfile*`, `compose*.yml`, `deploy/` | everything that executes the product (`go` and `console` too) |
| `ci` | `.github/`, `Makefile`, `scripts/`, `packaging/` | everything, including the package steps of `release-checks` (native deb and rpm build, content assertions and container install test; ADR 0025). A Go change runs them too, since `ci` turns `go` on and packages hold the Go binaries and `migrations/` |
| `arm64` | the `images` and `ci` paths, `go.mod`, `go.sum`, per-architecture Go sources and assembly (`*_amd64.go`, `*_arm64.go`, their `_test.go` and `.s` files), and any changed Go file whose `//go:build` line names `amd64` or `arm64` or that reads `runtime.GOARCH` | lint on arm64 (in `quality-arm64`) and the arm64 contract lanes (ADR 0016, amendment of 2026-11-19); `quality-arm64` vet and race and the arm64 image build run for every Go change |

A path that matches no class also runs everything, so a new top-level directory
cannot silently skip a job. An unresolvable range runs everything. The release gates
call the suite with every class on. Pushes to `main` classify nothing and run no test.
The `pr-title` check (Conventional Commit title, ADR 0024, `.github/workflows/pr-title.yml`) is a separate workflow that runs when a pull request is opened, edited, synchronized or reopened, so editing a title re-checks it without re-running CI. It is not part of `ci-ok`; require it in branch protection next to `ci-ok`.

Code directories are matched before Markdown, so a `.md` fixture under `internal/` is Go, not docs. A docs-only pull request therefore runs `changes`, `docs` and
`ci-ok` (plus the separate `pr-title` workflow). A Go-only pull request skips `console` (unit, build, visual) but still runs `console-e2e`; a console-only pull request
skips every contract lane.

## Hosted runner capacity (macOS)

`mainline-assets` on a push to `main` builds only the Linux archives, packages and the Console and OpenAPI archives (the `darwin` input of `release-archives.yml` is false). Nothing on `main` consumes darwin archives, and GitHub-hosted macOS runners can go unassigned during capacity constraints (main run 44557339551), which used to turn `main` red with no code fault. The release workflow still builds and requires native darwin/arm64 and darwin/amd64 archives; its `build` job fails with an explicit message when any archive or image job did not succeed. Action: open the failing legs. If the only failure is "The job was not acquired by Runner" (no log), re-run the failed jobs of that workflow run. A leg with a failed step is a real build failure to fix first; the matrix is fail-fast, so the legs cancelled after it are collateral, not capacity problems. The pull request workflow `release-dry-run.yml` is path-filtered, is not a required check and keeps rehearsing all four runners. The darwin `make cli-test` step runs in the release. The ADR 0016 amendment of 2026-11-19 records the decision.

## Console E2E sharding and spec selection

`console-e2e` is a matrix of three jobs. Shards `1`, `2` and `3` each start the
shared prebuilt gateway images and run `playwright test --shard=i/3`, so every
spec runs in exactly one shard. The entry marked `cli: true` (shard `2`) then runs the
`notarxctl` CLI E2E, also when one of its specs failed, so both verdicts show. The CLI
E2E boots its own isolated gateway from the loaded images (`scripts/cli-e2e.sh` without
`CLI_E2E_SERVER`: a Compose project of its own on free ports), so the state the specs of
the shard left behind cannot reach it. It used to be a fourth job that paid the whole
checkout, image load and gateway start for about 0.4 minutes of tests (issue #500). Each matrix entry carries its own shard fraction, so add or renumber entries together. Each
shard uploads a Playwright blob report; `console-e2e-report` merges them with
`playwright merge-reports` into one HTML report artifact (`console-e2e-report`),
(it runs when every shard passed; failed shards upload their own blob report and traces) (`console-e2e-traces-<shard>`).
`ci-ok` requires the matrix result (every shard) and, when all shards passed, `console-e2e-report`.

The `changes` job also runs `scripts/ci-e2e-select.sh`. When every changed path is
an existing top-level `console/e2e/*.spec.ts` file, only those specs run (still
spread over the shards; a shard left without a spec passes and uploads no blob). Any other path (Console
sources, `e2e/support`, fixtures, a deleted spec, Go, CI files) or an
unresolvable range is uncertain and runs the full suite. No spec is skipped on
any non-selected change, nor in the release gates.

The `console` job (unit tests, build, mocked visual baseline) and `console-e2e`
do not duplicate work: the visual config only matches `e2e/visual`, which the
behaviour config ignores, and the contract lanes never run Playwright.

## Go checks: lint, quality and one unit-test pass

The Go class runs three jobs. `lint` (amd64) is the fast signal: `make lint-checks`
(`golangci-lint` and the repository lint tools), `make vet`, the native-build rules, the
migration checks and the dependency audit. `release-checks` (amd64) runs the
architecture-independent release and packaging checks, the 60-second SAML fuzz run and,
on pull requests, the native deb and rpm package build with its content assertions and
the debian:12 and rockylinux:9 container install test (`make package-install-test`, ADR
0025; it needs Docker and the pinned nfpm, and runs also when a release check failed).
The release gates skip the package steps there, because `release-archives.yml` builds
and install-tests the packages of the release on both architectures.
Those used to follow lint in `lint` and held its verdict back by about 2.5 minutes. A
first cut of issue #500 moved them behind the unit tests in `quality`, which made that
job the slowest of the suite (13.4 minutes, run 44684290400), so they have a job of
their own. `quality` (amd64) runs the unit tests exactly once:
`make race-coverage` runs `go test -count=1 -race -coverprofile` over every package and
`scripts/coverage-check.sh` reads that one profile for the repository-wide floor and
for every per-package floor in `scripts/coverage-packages.txt` (covered statements
over all statements of the package, the figure `go test -cover` prints). Before
this, `quality` ran the race detector over `./internal/...`, then the whole suite
again with `-coverprofile`, then each floored package a third time with `-cover`.
The race detector now covers every package (`cmd/`, `contracts/`, `examples/` and
`tools/` too), on both architectures: `make race` has the same scope. `make coverage`
still runs a plain (non-race) pass with the same floors for local use. `quality-arm64`
runs `make vet` and `make race` natively on arm64 for every Go change, and `make lint`
too for an architecture-sensitive one (ADR 0016, amendment of 2026-11-19).

`make lint` is `make lint-checks` plus `make lint-tools-test` (the Go tests of
`tools/architecture` and `tools/sqllint`). The amd64 `lint` job and
`scripts/ci-local.sh` run `lint-checks`, because the unit-test pass next to them
already runs those tests.

`golangci-lint` runs in its pinned image as the calling user, against the host module
and build caches (restored by `actions/setup-go` in `lint`, and by the race build cache
below in `quality-arm64`) and its own result cache in
`GOLANGCI_LINT_CACHE` (default `~/.cache/notarx/golangci-lint`). A warm run downloads
no module and re-analyses only changed packages. The `quality` and `quality-arm64` jobs
keep their own cache of the module and build caches (`go-race-*`, one per runner
architecture), because the `actions/setup-go` cache is saved by `main` builds and never
holds the race-instrumented build. `quality-arm64` used the `actions/setup-go` cache
until issue #518, so its race pass compiled every package with the race detector on
every run.

Both caches are restored on every run and saved only by runs on `main` (the nightly
release gates), under daily keys (runner, toolchain or lint image, `go.sum`, the UTC
date): an exact hit saves nothing, so a fixed key would freeze a cache at its first
contents, and a save from every pull request would crowd the repository's cache quota.
A pull request restores the newest cache `main` saved, by key prefix. The unit-test
pass uses `-count=1`, so a restored build cache never replays an earlier test result.

The repository has a 10 GB cache quota, and beyond it GitHub evicts the least recently
used entries (11.8 GB were in use on 2026-11-20, most of it BuildKit layers and
Playwright browsers saved by pull requests; issue #518 took the runner image version
out of the browser key). Every Go pull request restores the newest race and lint caches,
so they stay recently used; a superseded daily entry is no longer restored and is the
first to go.

## Workflow linting

`make lint` runs `make actionlint-check`, which runs the pinned `rhysd/actionlint` image (actionlint, which also lints the shell in every `run` step) over every file in `.github/workflows`. Any finding fails the `lint` job (and `quality-arm64` when it lints), so a workflow warning cannot be merged.

## Playwright browser install

Both Playwright installs (the `console` job and the `console-e2e` shards) use the `.github/actions/playwright-browser` composite action. The browser download is time-boxed with `timeout` (5 minutes; composite action steps do not support `timeout-minutes`) and its directory (`~/.cache/ms-playwright`) is cached with `actions/cache`, keyed by runner image (`ImageOS`), architecture and the resolved `@playwright/test` version, so a cache hit downloads no browser. The system dependencies (`playwright install-deps`) are .deb packages from the runner's Ubuntu mirror, which was observed hanging or failing (apt exit 100) in CI. The first run for a runner image version (`ImageVersion`), architecture and Playwright version installs them with apt, pointed at a user-owned archive directory (`~/.cache/playwright-deps`); the step removes `partial` and `lock` and hands ownership back from root so tar can read the files, and the directory is saved right after the install step (when at least one .deb was downloaded), not at the end of the job. Every later run restores it and installs the packages with `dpkg --install --skip-same-version --refuse-downgrade`, so a warm runner never contacts apt; if dpkg refuses because the runner drifted from the image, the step installs through apt instead and does not save. A new runner image version changes the key, because its installed baseline can differ. The key is `playwright-deps-<ImageOS>-<arch>-<Playwright version>-<ImageVersion>`, so a runner image version that has no cache of its own restores the newest packages saved for another image version by prefix. That is not a hit: the step installs through apt, which reuses every restored package whose version still matches and downloads only the rest, then saves the exact key. Before the save the step drops every restored package that is installed at another version and, after a successful install, every package that is not installed, so the cache holds one version of each package and does not grow with every image version (a failed install keeps the rest for the next run to resume from). A pull request can restore only what `main` (the nightly release gates) or the pull request itself saved, and hosted runners of three or four image versions serve the repository at once, so with the image version in the middle of the key a pull request usually missed and fetched every package from the mirror, which took up to 7.7 minutes (run 44675064060; issue #500). There is no retry loop, fallback mirror or sleep: the cold apt install is time-boxed to 8 minutes with `timeout`. When it fails, the whole packages downloaded so far are saved under a `-partial-` key and the next run restores them by prefix (not a hit, so it still runs apt, which skips what it already holds), so a slow mirror converges instead of restarting. The cache keys read `ImageOS`, `ImageVersion` and the architecture from the environment in a step, because those are runner environment variables, not expression contexts (the earlier `env.ImageOS` key expanded to an empty string). We chose this over running the browser tests in the official Playwright container image, which would need a digest that cannot be verified from a pull request.

## The ci-ok gate

`ci-ok` depends on `changes`, `tests` and the two `oci-free-tier` jobs and
always runs. It passes when each needed job succeeded or was skipped, and fails on any
failure or cancellation. On a pull request `changes` and `tests` must have
succeeded; `changes` also runs `scripts/check-no-chinese.py` (the owner rule that no
Chinese content may be added; it was a separate `no-chinese` job); the `tests` job holds `suite-ok`, which fails a job that change detection
required but that was skipped (for example because an upstream job failed). On a push to
`main` every job behind `ci-ok` is skipped, so it passes without running a test. Require `ci-ok` as the single branch-protection check; the set of
jobs behind it can change without editing the protection rule.

## fr-CA review and the sidecar

`.github/workflows/i18n-review.yml` (ADR 0014, slice 13) reviews the changed fr-CA strings and
documents of a pull request. Its `review` job is the check: it fails when a critical key has a
finding, and it posts the findings as a pull request comment. It never commits to the pull
request branch. A push made with `GITHUB_TOKEN` starts no workflows, so a bot commit left the new
head without a CI run and `ci-ok` missing until a maintainer re-ran the checks; the pull request
head now keeps exactly the commits its author pushed, and its required checks run on them. No
personal access token and no GitHub App token is used.

The review does not write the sidecar. The committed `console/src/messages/.review/fr-CA.json` is the
input of `make i18n-release-check`, so it is updated at release time, in the release preparation
pull request: run `node scripts/i18n-review.mjs --write-sidecar` (with `I18N_REVIEW_PROVIDER` and its
key set) from the base branch, which reviews every key that is not yet reviewed for its current
text, and commit the sidecar it writes. `make i18n-release-check` then passes only when every key is
reviewed. A sidecar produced by a pull request is never copied in: its content comes from the pull
request head. Fork pull requests are reviewed by a maintainer with `workflow_dispatch`; the result
is the `i18n-review` commit status on the head.

## Contract lanes

`scripts/ci-contract-lanes.json` is the only place that names lanes, their tool
needs and their timeouts. The `contract-plan` job turns it into a matrix
(`scripts/ci-contract.py matrix`) and three jobs run one reusable
`contract-lane.yml` job per entry in parallel (no `max-parallel` cap; the free
hosted runner limit applies). The plan is split into groups by the images a job
loads (`matrix --group`), so no job waits for an image it never loads:

| Job | Group | Waits for |
| --- | --- | --- |
| `contract-early` | jobs whose lanes all declare `"images": false` (both architectures) | `contract-plan` only |
| `contract-amd64` | amd64 jobs with a lane that loads the prebuilt images | `build-images-amd64` |
| `contract-arm64` | arm64 jobs with a lane that loads the prebuilt images | `build-images-arm64` |

Every lane declares `images`. The integration shards, `core-backup`, `core-checks` and
`perf-load` declare `false`: they run `compose.integration.yml`, build their binaries
from source, stub Docker in their script tests or build their own per-project image. A job without images downloads and loads
nothing, and Compose still runs with `--no-build`, so a lane that wrongly declares
`false` fails on the missing image instead of building it. A group the plan leaves
empty (`contract-arm64` on a pull request without the `arm64` class) is skipped, and
`suite-ok` requires each group that has a job. Before issue #500 a single `contract`
job waited for all four image builds, the two arm64 ones included, about 4.9 minutes
into the run. The former serial `core` lane (about 34 minutes)
is split into four lanes by duration, so the full run is bound by its slowest
lane instead of their sum. Every step of the old lane runs in exactly one lane;
`upgrade-readiness` is the only step dropped (below). The `suite-ok` job of
`test-suite.yml` requires every lane.

### Contract lane packing

A lane is the unit of the plan; a CI job runs one lane or, for short lanes, several.
The `jobs` object of `scripts/ci-contract-lanes.json` packs lanes of a few minutes
into one job, so they share one checkout, toolchain setup and image load instead of
each paying it (about 0.6 minutes per job) and holding a runner of the 20 the
repository may use at once. Every other lane is a job named after itself.
`scripts/ci-contract.py run <lane>...` runs the lanes of a job in order, keeps going
after a failed lane, writes each lane's evidence to its own directory and lists every
lane's verdict, duration and failed steps in the job's step summary; the job fails
when any lane failed. The arm64 leg of a packed job runs only the lanes whose tier runs
on arm64 in that scope.

| Job | Lanes | Lane step minutes (run 44692652060, attempt 1) |
| --- | --- | --- |
| `packed-perf-integration` | `perf-load`, `core-integration-1` | 2.1 + 4.6 |
| `packed-checks-backup` | `core-checks`, `core-backup` | 1.9 + 4.3 |
| `packed-native-discovery` | `core-native-e2e-1`, `oci-discovery` | about 3.8 + 2.0 |
| `packed-native-auth-pypi` | `core-native-e2e-2`, `six-format-auth`, `pypi-migration` | about 4.1 + 1.1 + 0.6 |
| `packed-email-clients` | `core-email`, `migration-clients` | 6.8 (with the image rebuild described below) + 0.8 |
| `packed-nuget-migrations` | `nuget`, `oci-migration`, `npm-migration` | 4.2 + 0.5 + 0.6 |
| `packed-maven-helm` | `maven-oci-auth`, `helm` | 4.5 + 1.7 |
| `packed-inventory-rubygems` | `inventory`, `rubygems` | 3.5 + 2.7 |

The former `core-native-e2e` lane (about 8 minutes, the critical path of a Go-only pull
request after #500) is two lanes now, split by step duration: `core-native-e2e-1` runs
`cli-runtime-e2e`, `native-oci-e2e`, `native-raw-e2e`, `native-maven-e2e`,
`native-npm-e2e` and `native-go-e2e`, and `core-native-e2e-2` runs `native-pypi-e2e`,
`native-apt-e2e` and `apt-signer-rotation-e2e`. Both keep the `pull-request` arm64 tier.
`email-compose-check` (lane `core-email`) used to rebuild the notarx image from source
for its per-project fixture name; under `NOTARX_COMPOSE_BUILD_FLAG=--no-build` it now
tags the loaded image with that name and removes the tag when it cleans up (issue #518).
The six image jobs keep the job count of #500.

A job's timeout is the sum of its lanes' timeouts and it installs the union of their
tool needs. A packed job takes longer than its longest lane (`packed-nuget-migrations`
about 7 minutes against 5), so only lanes that stay well under the critical path of
the suite (the Console shards, about 9 to 11 minutes after the start) are packed. The lanes of
a job share one runner and Docker daemon: each lane's scripts own their Compose
projects and tear them down on exit, and a cancelled job stops after the lane that
was running (the lanes after it are reported as not run). A lane that crashes is
reported as an error and the next lane runs. The lanes run in the listed order, so
`perf-load` comes before `core-integration-1` and measures a daemon that no other lane
has used yet; it has no latency threshold, only a zero-failed-requests check, but its
evidence numbers stay comparable between runs. Move a lane in or out of a pack by editing `jobs` only; the workflows
name no lane and no job.

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
lanes (the six `core-*` lanes and the `six-format-auth` and `oci-discovery`
smoke subset) also run on arm64 for every pull request with the `arm64` class (an
architecture-sensitive change), and `mainline` lanes join them in the `mainline` scope,
which the release gates use. A pull request without the `arm64` class runs no arm64
contract lane (ADR 0016, amendment of 2026-11-19). `scripts/ci-contract.py matrix --scope pull-request|mainline [--no-arm64]`
emits the matrix; it refuses `--no-arm64` with the `mainline` scope. A step that runs a third-party artifact published for one
architecture only (`published-native-backup-test` executes the published amd64
release binaries) declares `architectures` in the plan; other architectures skip
and report it, nothing is emulated. `quality-arm64` runs vet and the race detector on
`ubuntu-24.04-arm` for every Go change, and lint as well for an architecture-sensitive change; the coverage floor stays on amd64. The jobs call the local
composite action `.github/actions/go-quality` for setup, lint, vet and race, so the
steps have one definition. They are separate jobs, not a matrix, so the amd64-only steps stay
out of the arm64 leg. See
[native builds](native-builds.md).

## Measured effect (issue #482)

These are Go-only pull requests, all with `ci-ok` green.

- **Before:** runs 44640058817, 44633978455, 44626976683 and 44620652504, the four the issue measured. Every arm64 lane ran on pull requests then.
- **After:** the three attempts of run 44673768958. This was draft #496, a comment-only Go change, so the `arm64` class was off. It is one change re-run three times, so the attempts share a commit and a cache state: read the numbers as indicative. The race and lint caches were cold, because `main` saves them only from the nightly release gates, which had not run yet.

How to read the columns:

- **Wall** runs from the start of the run to the end of `ci-ok`. It is the pull request verdict.
- **Queue** is the longest wait of any one job for a runner, caused by the other runs of the repository.
- **Lint** and **unit tests** give the minute each verdict arrived. Before, both came from the one `quality` job, so the two columns are equal.

| Run | Wall (min) | Longest queue (min) | Lint verdict (min) | Unit tests and coverage (min) | Runner-minutes | Jobs |
| --- | --- | --- | --- | --- | --- | --- |
| before, 44640058817 | 26.9 | 3.7 | 17.8 (`quality`) | 17.8 (`quality`) | 189 | 46 |
| before, 44633978455 | 28.7 | 13.7 | 16.1 (`quality`) | 16.1 (`quality`) | 192 | 46 |
| before, 44626976683 | 34.7 | 12.1 | 23.4 (`quality`) | 23.4 (`quality`) | 179 | 46 |
| before, 44620652504 | 26.5 | 6.5 | 17.7 (`quality`) | 17.7 (`quality`) | 187 | 46 |
| after, attempt 1 | 17.1 | 2.0 | 8.6 (`lint`) | 10.9 (`quality`) | 122 | 29 |
| after, attempt 2 | 18.8 | 6.1 | 6.5 (`lint`) | 6.9 (`quality`) | 104 | 29 |
| after, attempt 3 | 19.1 | 6.5 | 9.9 (`lint`) | 11.8 (`quality`) | 108 | 29 |

- **Wall time** fell by about a third: medians of 27.8 and 18.8 minutes. With the longest queue subtracted it still fell by about a third, from about 20 to about 13.5 minutes.
- **Runner-minutes** fell by 32 to 46 percent (122 against 179 at worst, 104 against 192 at best).
- **The `arm64` class:** a pull request with it on (run 44670328253, the branch of #491) took 14.7 minutes, with 41 jobs and 173 runner-minutes. It is faster than the Go-only attempts although its longest queue was longer (4.8 minutes), because its slowest lane, `core-integration-1` on amd64, took 6.6 minutes, against 9.4 in attempt 1. That lane's time varies from run to run, and #500 evens out the integration shards.

Four targets of the issue were left open: a verdict (wall) in 12 minutes or less, half the runner-minutes, 25 jobs or fewer, and a lint signal in 5 minutes. Issue #500 tracks them.

## Measured effect (issue #500)

Issue #500 removed the wait of every contract lane for all four image builds (the contract groups and the per-architecture image jobs above), split the integration suite into three shards, folded the CLI E2E into a Console shard and gave the release checks a job of their own. A Go-only pull request now runs 25 jobs (26 in the measurement below, which predates folding `packages` into `release-checks`). The first run of the change itself (44684290400, with the `arm64` class on, so with every arm64 lane: 39 jobs, 172 runner-minutes, the longest queue 3.9 minutes) shows the new shape:

| Signal | #482 (`arm64` class on, run 44670328253) | #500 first run (44684290400) |
| --- | --- | --- |
| First contract lane created (min) | 2.4 (after all four image legs) | 0.5 (`contract-early`) |
| amd64 images ready (min) | 2.4 (all four legs) | 2.9 (both amd64 images in one job) |
| Every integration and backup lane done (min) | 13.8 | 8.0 |
| Lint verdict (min) | 6.0 | 3.6 |
| Wall (min) | 14.0 | 15.0 (`core-native-e2e` queued 3.9 minutes; `quality` 13.4 with the release checks, since moved out) |

The Go-only measurement: the three attempts of run 44692652060 (draft #512, a comment-only Go change, `arm64` class off), with the columns of the #482 table:

| Run | Wall (min) | Longest queue (min) | Lint verdict (min) | Unit tests and coverage (min) | Runner-minutes | Jobs |
| --- | --- | --- | --- | --- | --- | --- |
| #482 after, attempts 1 to 3 (run 44673768958) | 17.1 to 19.1 | 2.0 to 6.5 | 6.5 to 9.9 | 6.9 to 11.8 | 104 to 122 | 29 |
| #500 (before the `packages` fold), attempt 1 | 16.8 | 6.5 | 4.5 | 10.7 | 116 | 26 |
| #500, attempt 2 | 13.2 | 2.9 | 5.0 | 7.2 | 112 | 26 |
| #500, attempt 3 | 12.4 | 0.1 | 3.7 | 6.9 | 108 | 26 |

Attempt 3 failed on a planner-statistics flake in `./internal/repository` that the new shard layout exposed (fixed in #515); its timing is complete. The jobs count `packages`, which ci.yml ran next to the suite for every Go change; it is now a set of steps of `release-checks`, so a Go-only pull request runs 25 jobs.

- **Lint verdict**: 3.7 to 5.0 minutes, on target.
- **Wall**: 12.4 minutes with no queue, 13.2 and 16.8 with queues of 2.9 and 6.5 minutes. The amd64 image lanes still queue for runners (the repository may use 20 at once and the other open pull requests share them), and `core-native-e2e` (about 8 minutes after the amd64 images at about 2.6) is the critical path without queue.
- **Runner-minutes**: 108 to 116, against the target of about 93. The contract lanes hold most of them (the lane steps themselves, not setup: setup is 0.3 to 0.5 minutes per job), and `quality` restores no race build cache until the nightly release gates save one on `main`.
- **Jobs**: 26 in these runs, 25 with `packages` folded into `release-checks`. Run 44670328253 had cold queues (the issue's figure of 4.9 minutes before the first lane is from the Go-only draft #496, whose image legs queued).

## Where every former check runs (issue #482)

Issue #482 reshaped the pull request suite (ADR 0016 and ADR 0024, amendments of
2026-11-19). No check was dropped; this table maps every job of the earlier layout
(run 44640058817) to where it runs now.

| Earlier job | Now (pull request) | Release gates (`mainline` scope) |
| --- | --- | --- |
| `no-chinese` | a step of `changes` that records its verdict without failing the job; `ci-ok` fails on a violation, and the rest of the suite still runs | not part of the gates (as before) |
| `changes`, `oci-free-tier`, `oci-free-tier-tofu` | unchanged | unchanged |
| `packages` | steps of `release-checks` (issue #500) | not in the suite; `release-archives.yml` builds and install-tests the release packages, as before |
| `quality`: lint, vet, native-build rules, migration, release and packaging checks, SAML fuzz, dependency audit | `lint` (lint, vet, native-build rules, migration checks, dependency audit); `release-checks` (release and packaging checks, SAML fuzz; issue #500) | the same |
| `quality`: `make race`, `make coverage` and the per-package coverage reruns | `quality`: `make race-coverage`, one pass with the race detector and every floor | `quality` |
| `quality-arm64`: lint, vet, race | `quality-arm64`: vet and race for every Go change; lint with the `arm64` class | `quality-arm64`: lint, vet, race |
| `console`, `docs`, `image-test-stages`, `console-e2e-report` | unchanged | unchanged |
| `build-images` (4: two images on two architectures) | `build-images-amd64` and `build-images-arm64`, each building both images of its architecture (issue #500) | the same |
| `console-e2e` shards `1` to `3` and `cli` | shards `1` to `3`; shard `2` runs the CLI E2E after its specs (issue #500) | the same |
| `object-store-contract` (3 jobs: RustFS, MinIO twice) | one `object-store-contract` job running the three configurations in turn | the same job |
| `contract (core-backup)` | `core-backup` (in `packed-checks-backup`), `core-integration-1` (in `packed-perf-integration`), `core-integration-2`, `core-integration-3`, all in `contract-early` | the same four lanes on both architectures |
| `contract (core-checks)`, `contract (six-format-auth)` | `core-checks` in `packed-checks-backup` (`contract-early`), `six-format-auth` in `packed-native-auth-pypi` (issue #518; `packed-rubygems-discovery-auth` after #500) | the same, both architectures |
| `contract (nuget)`, `contract (oci-migration)`, `contract (npm-migration)` | `packed-nuget-migrations` | the same, both architectures |
| `contract (maven-oci-auth)`, `contract (migration-clients)` | `maven-oci-auth` in `packed-maven-helm`, `migration-clients` in `packed-email-clients` (issue #518) | the same, both architectures |
| `contract (rubygems)`, `contract (oci-discovery)` | `rubygems` in `packed-inventory-rubygems`, `oci-discovery` in `packed-native-discovery` (issue #518) | the same, both architectures |
| `contract (inventory)`, `contract (helm)`, `contract (pypi-migration)` | `inventory` in `packed-inventory-rubygems`, `helm` in `packed-maven-helm`, `pypi-migration` in `packed-native-auth-pypi` (issue #518) | the same, both architectures |
| `contract (perf-load)` | `packed-perf-integration`, before `core-integration-1` (issue #500) | the same on both architectures |
| `contract (core-email)` | `core-email` in `packed-email-clients` (issue #518) | the same, both architectures |
| `contract (core-native-e2e)` | split into `core-native-e2e-1` (in `packed-native-discovery`) and `core-native-e2e-2` (in `packed-native-auth-pypi`), every step in exactly one of them (issue #518) | the same, both architectures |
| any lane not in `jobs` | unchanged: a job named after the lane | unchanged |
| every contract job | `contract-early` (lanes that load no prebuilt image), `contract-amd64` or `contract-arm64` (issue #500) | the same three groups |
| the seven arm64 contract legs | only with the `arm64` class (architecture-sensitive change) | always, every lane |
| (new) | `release-checks` | `release-checks` |
| `contract-plan`, `suite-ok`, `ci-ok` | unchanged (`suite-ok` also requires `lint`) | unchanged |

## The backup and integration lanes

The former `core-backup` lane ran the Conan E2E, `make backup-transfer-test`,
`make published-native-backup-test` and `make integration-test` one after another
(about 20 minutes, the longest job of a pull request). It is four lanes now. None of them
loads a prebuilt image, so they run in `contract-early` and start right after the plan:

| Lane | Steps | Lane step minutes, run 44675064060 (two shards then) |
| --- | --- | --- |
| `core-backup` (packed after `core-checks`) | `make backup-transfer-test` | 6.6 |
| `core-integration-1` (packed after `perf-load`) | `make integration-test INTEGRATION_SHARD=1` (`./internal/app` and the Cargo replay) | was 9.3 with `./internal/repository`, the small packages and the published backup test |
| `core-integration-2` | `make integration-test INTEGRATION_SHARD=2`, `make published-native-backup-test` | new; `./internal/repository` took 2.4 minutes of tests, the published backup test 1.6 |
| `core-integration-3` | `make integration-test INTEGRATION_SHARD=3`, `make conan-e2e` | was 7.3 as shard 2 with the small packages; Conan 2.1 |

The two-shard layout of #482 left `core-integration-1` at 9.3 to 11.3 minutes, the
slowest job of the suite: `./internal/app` (2.0 minutes of tests) and
`./internal/repository` (2.4) were in the same shard, after about 1.5 minutes of stack
start and compilation, followed by the Cargo replay (0.9) and the published backup
test (1.6). Issue #500 gives `./internal/app` a shard of its own and moves the published
backup test to the new middle shard.

`scripts/integration-packages.txt` holds the integration package list and assigns each
package to one of three shards balanced by measured test time; `scripts/integration-test.sh --list`
prints it. `TestRequiredIntegrationCommandCoversIntegrationTaggedPackages` (`contracts/`)
and `make test` (`scripts/integration-isolation-test.sh`) fail when a package with
integration-tagged files is missing from the list or listed twice. Each shard starts
its own PostgreSQL and RustFS stack, so the shards cannot see each other's data; inside
a shard the packages still run one at a time (`-p 1`), because they share that
database. `-p 1` used to serialize compilation too (about two minutes of the
integration step): the script now builds every test binary first with full
parallelism (`go test -exec true`, which builds and links but runs nothing); the
second `go test -p 1` finds every compiled package in the build cache, so it spends
seconds per package relinking or reusing the binary before running it. Each shard pays
the stack start and its own compilation, about 1.5 minutes, which is why there are
three shards and not more. Shard 1 holds `./internal/app` and runs the official Cargo
replay on the host. The shard that holds `./internal/migrate` (ADR 0022) also starts a
`mysql:8.4` container for the MySQL runner tests (`TEST_MYSQL_URL`); every shard runs the
runner parity check of `scripts/migration-runner-check.sh`, which applies the PostgreSQL
migrations with the Go runner and compares the ledger with `run-migrations.sh`. Without
`INTEGRATION_SHARD`, `make integration-test` runs every package in one stack, as before.

## Shared images

`build-images-amd64` and `build-images-arm64` build the notarx and reference apt signer
images once per run, natively on `ubuntu-24.04` and `ubuntu-24.04-arm`. Each job
builds both images in parallel with `docker buildx bake` (`.github/ci-images.hcl`,
through the `.github/actions/ci-images` composite action; BuildKit layers cached in
the GitHub Actions cache, one scope per image and architecture; the reference
scanner has no CI consumer, so it is not built here) and uploads both tarballs as
one artifact named `image-<runner arch>`. Every contract job that loads images and
`console-e2e` download the artifact of their own runner architecture and
`docker load` it, so no job rebuilds an image from source. `compose.yml` names the images
(`notarx:local`, `notarx-reference-apt-signer:local`,
`notarx-reference-scanner:local`; override with `NOTARX_IMAGE`,
`NOTARX_APT_SIGNER_IMAGE`, `NOTARX_SCANNER_IMAGE`). Lanes set
`NOTARX_COMPOSE_BUILD_FLAG=--no-build`; the e2e scripts use it in place of
`--build`, and unset (local runs) they still build from source. Scripts that
build with their own version arguments (`backup-restore-readiness`,
`performance-baseline`) keep a per-project image name so they never touch the
shared tag. `suite-ok` requires both image jobs whenever `go` or `console`
changed. They used to be four matrix legs (one per image and architecture) behind a
single `needs`, so an amd64 lane waited for the arm64 builds too (issue #500). The
arm64 job runs on every Go change even when no arm64 lane does (a pull request without
the `arm64` class): ADR 0016 (amendment of 2026-11-19, point 2) keeps the arm64 image
build as the per-pull-request native arm64 build check, next to `quality-arm64`.

### Go module cache for image builds

BuildKit cache mounts do not survive between hosted runners, so the image build used to fetch every module from proxy.golang.org whenever `go.mod` or `go.sum` changed, and one proxy error failed the job. The image jobs (`.github/actions/ci-images`) restore the module download directory with `actions/cache` (key: runner architecture, month, hash of `go.mod` and `go.sum`; the prefix fallback is limited to the same month, so dropped modules cannot pile up). It passes the directory to the build as the `gomod-seed` build context. Each Dockerfile runs `GOPROXY=file:///gomod-seed,https://proxy.golang.org|direct go mod download && go mod verify`, so a warm run makes no request to the proxy and a `go.sum` change fetches only the new modules. On an exact cache hit the job first runs `GOPROXY=off go mod download`, which fails instead of fetching and is the proof that the warm path is network free. `go.sum` and `go mod verify` still decide which bytes are accepted, so a poisoned cache cannot change the build, and there is no retry loop. The BuildKit `/go/pkg/mod` cache mount is gone, because the layer now holds the modules; local `make` image targets pass the host module cache as the same seed. A seed that changes content (a cache miss) also invalidates the cached download layer, which is correct since `go.mod` changed. Release image builds (`image-build.yml`) use the empty default seed and fetch from the proxy, as they did before on their fresh runners, so release digests and provenance (ADR 0016, ADR 0024) are unaffected; module contents are fixed by `go.sum`, so the compiled output does not depend on where the modules came from.

The `oci-free-tier` and `oci-free-tier-tofu` jobs run when the `images` class changed
(`deploy/` is in it). `oci-free-tier` runs on the native arm64 runner, because the
reference deployment targets the Oracle Ampere A1 shape: it checks the rendered
Compose model and resource limits (`make oci-free-tier-check`), asserts that every
third-party image publishes `linux/arm64` (`make oci-free-tier-images-check`; the
project's own images are gated by the release pipeline) and smoke-tests the stack by
pushing and pulling an OCI image and a Raw file through Caddy
(`make oci-free-tier-smoke`). `oci-free-tier-tofu` formats, validates and lints the
optional OpenTofu module (`make oci-free-tier-tofu-check`). `ci-ok` requires both
whenever `images` changed. They stay in `ci.yml` and are not part of the release gates.

The Dockerfiles no longer run `go test` while building an image: the suite runs
once in `core-checks` (`make test`) and `quality`. The tests live in an opt-in
`test` stage (`make docker-test`, run by `image-test-stages` when Dockerfiles or
compose files change) that BuildKit skips for image builds.

CI does not fetch an upstream tag or run `upgrade-readiness`: ADR 0015 has no
upgrade path from upstream. `scripts/upgrade-readiness.sh` (kept, with its make
target) is parameterized on stable notarx release tags and reports a no-op until
one exists; re-add the CI step once a second notarx release exists.
`make backup-transfer-test` (lane `core-backup`) runs the same upgrade properties
against the baseline and a synthetic `000002` migration meanwhile.

## Object store contract

`object-store-contract` (`.github/workflows/object-store-contract.yml`, called from
`test-suite.yml` with the `go` class) runs the shared object store contract
(`internal/objectstore/storetest`, ADR 0017) against the S3 driver on RustFS
and on a second S3-compatible implementation, once per checksum mode where the
service supports it. The three configurations (RustFS, and the second service with each
checksum mode) run one after another in one job that starts both services once and
lists each configuration's result in the step summary; a failed configuration does not
stop the next one. The same suite runs against the in-memory store in
`make test` and against RustFS in `make integration-test`; the
`./internal/objectstore` coverage floor in `scripts/coverage-packages.txt` is set
so it fails if the suite stops running. The second service has no published
container image, so the job builds it from a pinned upstream revision with the
repository Go toolchain, in the background while RustFS starts; bump `MINIO_VERSION` in the workflow to move it.
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
`scripts/native-maven-e2e.sh` starts the same fixture as a loopback mirror
(`cmd/maven-fixture-mirror`, URL printed at start), points Maven at it with a
settings mirror for `central`, refuses every other non-loopback host through
blocked settings proxies, and prints the unrecorded paths when Maven fails. The
Maven client Go tests do the same through `mavenfixture.MirrorXML` and
`mavenfixture.AddBlockedProxies`.
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

## Hermetic Maven and Gradle lanes

The Maven and Gradle clients and the Go tests that drive them never reach the
internet in CI. Besides the recorded fixtures and the in-client refusals above,
`scripts/egress-guard.sh` enforces it in the kernel: `contract-lane.yml` sets
`NOTARX_EGRESS_GUARD=1`, and `native-maven-e2e.sh`, `nexus-migration-client-e2e.sh`
and `private-upstream-auth-e2e.sh` start their `mvn`, `gradle` and `go test`
commands through it. The command runs with the group `notarx-egress` as its
effective group, and an iptables (and ip6tables) owner match on that group
rejects every packet except to loopback and private ranges (the fixture mirror,
the service containers and the system under test). Before running, the guard
proves it is in force by failing to connect to a public address and refuses to
run the command otherwise. Tool downloads (the pinned Maven, Gradle and ORAS
archives) happen outside the guard, before the clients run. Without the variable
the guard runs the command unchanged, so local runs are not affected.
`scripts/ci-contract.py` carries no Maven Central mirror.

Pinned tools fetched from GitHub releases (nfpm, git-cliff, ORAS, crane) go through
`github_release_download` in `scripts/lib/github-release.sh`. GitHub sometimes answers
unauthenticated release-asset downloads from shared runner IPs with a 403, which
`curl --retry` does not retry. With `GH_TOKEN` set (the workflows pass `github.token`) the
helper uses `gh release download` with retries; without a token, as in a local run, it falls back
to a plain `curl`. Every caller still verifies its pinned checksum, and the nfpm and git-cliff
binaries are also cached with `actions/cache`, keyed by runner architecture and a hash of the
version file and installer script, so most runs make no network call.

## Migration checks

The `lint` job runs `make migration-check`
(`scripts/migration-immutability-check.sh`, checkout with full history and tags).
It always enforces unique, gap-free, snake_case migration names. Once a stable
`vX.Y.Z` tag exists it also fails any diff of a released migration against the
latest stable tag; nightly tags are ignored. `make test` runs the script's bash
fixtures.

The `lint` job also runs `make schema-parity` (`scripts/schema-parity.sh`, ADR 0022): it
migrates a throwaway PostgreSQL (and a MySQL once `migrations/mysql` holds a baseline) and
compares its `information_schema` with the neutral catalogue `migrations/catalogue.yaml`.
`make test` also fails when a migration after the baseline exists in only one of
`migrations/postgres` and `migrations/mysql` without an entry (dialect and reason) in
`migrations/dialect-scope.yaml`.

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
0.x rules. The plan reads no check runs: pushes to `main` run no tests, so the nightly plans the tip of
`main` and the gates of the release run test that commit. `make release-plan-test`
runs the plan against fixture repositories and fails if git-cliff's bump behaviour
deviates from the pinned expectations. `release.yml` runs the plan every night
(see [releasing](releasing.md)); `make release-pipeline-test` covers the manifest, attestation
verification, pruning, governance and workflow-structure checks of that pipeline.

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
part of `ci-ok`. `release.yml` itself (the nightly `plan`, `gates`, `build`, `publish` and `prune`
jobs) is not callable: its structure is checked by `make release-pipeline-test` and it is first
exercised by a real nightly or a `workflow_dispatch` dry run on `main`. Its dry run runs the
full `test-suite.yml` on the planned commit.
