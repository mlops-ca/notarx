# CI path classes and the ci-ok gate

[简体中文](ci.zh-CN.md) | [Documentation index](README.md)

CI runs only what a change needs. The `changes` job in `.github/workflows/ci.yml`
runs `scripts/ci-changes.sh` over the files a pull request touches and exposes
five classes. Heavy jobs run only when their class changed.

| Class | Paths | Jobs it enables |
| --- | --- | --- |
| `go` | `*.go`, `go.mod`, `go.sum`, `migrations/`, `internal/`, `cmd/`, `contracts/`, `examples/`, `api/`, plus the files `release-version-check` reads (`CHANGELOG*.md`, `docs/cli*.md`, `VERSION`) | `quality`, `contract-core`, `contract-native`, `contract-and-test`, and `console-e2e` (it runs the real gateway) |
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

`contract-core` and the eleven `contract-native` lanes run in parallel (there
is no `max-parallel` cap; the free hosted runner limit applies). Each lane warms
the Go module cache with retries and resolves modules through
`GOPROXY=https://proxy.golang.org|direct`, so one failed proxy stream no longer
fails a lane. Lane evidence and the aggregate check are unchanged.

Run `bash scripts/ci-changes-test.sh` to check the classifier fixtures.
