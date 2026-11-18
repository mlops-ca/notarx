# CI path classes and the ci-ok gate

[简体中文](ci.zh-CN.md) | [Documentation index](README.md)

CI runs only what a change needs. The `changes` job in `.github/workflows/ci.yml`
runs `scripts/ci-changes.sh` over the files a pull request touches and exposes
five classes. Heavy jobs run only when their class changed.

| Class | Paths | Jobs it enables |
| --- | --- | --- |
| `go` | `*.go`, `go.mod`, `go.sum`, `migrations/`, `internal/`, `cmd/`, `contracts/`, `examples/`, `api/`, plus the files `release-version-check` reads (`CHANGELOG*.md`, `docs/cli*.md`, `VERSION`) | `quality`, `contract-plan`, `contract`, `contract-and-test`, and `console-e2e` (it runs the real gateway) |
| `console` | `console/`, `api/` (the generated client) | `console` (unit tests, build, visual) and `console-e2e` |
| `docs` | `docs/`, `*.md`, `LICENSE`, `NOTICE` | `docs` (i18n twins, links, capability claims) |
| `images` | `Dockerfile*`, `compose*.yml`, `deploy/` | everything that executes the product (`go` and `console` too) |
| `ci` | `.github/`, `Makefile`, `scripts/` | everything |

A path that matches no class also runs everything, so a new top-level directory
cannot silently skip a job. Pushes to `main`, tags and unresolvable ranges run
everything, including the mainline asset and image publication jobs.
`pr-title` (Conventional Commit title, ADR 0024) runs on every pull request.

Code directories are matched before Markdown, so a `.md` fixture under `internal/` is Go, not docs. A docs-only pull request therefore runs `changes`, `pr-title`, `docs` and
`ci-ok`. A Go-only pull request skips `console` (unit, build, visual) but still runs `console-e2e`; a console-only pull request
skips every contract lane.

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
Jobs run on `ubuntu-24.04` with a per-lane `timeout-minutes` from the plan.
A failing lane uploads its step logs and raw reports.

The Dockerfiles no longer run `go test` while building an image: the suite runs
once in `core-checks` (`make test`) and `quality`. The tests live in an opt-in
`test` stage (`make docker-test`, run by `image-test-stages` when Dockerfiles or
compose files change) that BuildKit skips for image builds.

CI does not fetch the upstream v0.5.0 tag or run `upgrade-readiness`: ADR 0015
has no upgrade path from upstream, and
[#39](https://github.com/mlops-ca/notarx/issues/39) re-parameterises
`scripts/upgrade-readiness.sh` (kept, with its make target) on notarx release
tags. Re-add the step once a notarx release tag exists.

Run `bash scripts/ci-changes-test.sh` to check the classifier fixtures.
