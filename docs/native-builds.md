# Native builds, runner matrix and local builds

[Documentation index](README.md)

notarx performs no cross-compilation and no emulation anywhere in build, test or
release ([ADR 0016](adr/0016-native-architecture-builds.md)). Every binary and
image is built, and for Linux executed, on hardware of its own architecture. This
page covers native builds only. The release runbook (nightly and stable channels,
version rules, verification) lives in the release documentation owned by
[ADR 0024](adr/0024-release-versioning-and-conventional-commits.md), and install
and verification steps in the distribution documentation owned by
[ADR 0025](adr/0025-distribution-channels.md); this page does not repeat them.

## Runner matrix

Labels are pinned to explicit OS versions, never `ubuntu-latest`, so a runner image
changes only through a reviewed edit.

| Target | Runner label | Produces |
| --- | --- | --- |
| linux/amd64 | `ubuntu-24.04` | images, archives, tests |
| linux/arm64 | `ubuntu-24.04-arm` | images, archives, tests |
| darwin/arm64 | `macos-15` | `notarxctl` and server archives |
| darwin/amd64 | `macos-15-intel` | `notarxctl` and server archives, until the label retires |

The repository is public, so every run (pull requests, `main`, tags) uses
GitHub-hosted amd64 and arm64 runners. There are no self-hosted runners and no
single-architecture fallback.

### Reusable workflows

Two reusable `workflow_call` workflows hold the build mechanics. `ci.yml`
(`mainline-images`, `mainline-assets`) calls them on every push to `main`, and
the release pipeline calls the same workflows rather than carrying its own copy.
Pushes to `main` build only the two Linux archive legs (`darwin: false`); the
release builds all four, so a macOS runner capacity outage cannot turn `main` red.

- `.github/workflows/image-build.yml` takes `version`, `sha`, the `tags` to create
  and the `images` list (the only place that defines which images are
  published). Each image builds on a native amd64 leg and a native arm64 leg,
  pushes by digest, pulls the digest back from the registry, runs it, and checks
  the version and revision labels. A second job per image merges the two digests
  into the requested tags, checks the revision label of each digest, and attests
  the final index (provenance and SBOM). A failing leg of either architecture
  prevents every tag. Adding an image is one entry in `images` plus its
  release-gate check.
- `.github/workflows/release-archives.yml` takes `version`, `sha` and the name of
  the combined artifact. Four native legs (the table above) build the archives,
  smoke-run their own `notarxctl` and `notarx` binaries, and upload one
  `<product>-<version>-<goos>-<goarch>` artifact per archive. The darwin legs also
  run the CLI unit tests (hosted macOS has no Docker). `assemble-release` then
  downloads every slice and runs `scripts/write-release-checksums.sh` once over
  the union, so one `SHA256SUMS` covers every natively built archive.

### Tests on both architectures

`lint` and `quality` (amd64) and `quality-arm64` run lint, vet and the race detector (on amd64,
`quality` runs the race detector and the coverage floors in one pass); the
coverage floor is enforced on amd64 only because coverage is architecture
independent. Contract lanes take a `runner` input. Every lane runs on amd64. On
arm64 the four core lanes and the native-format smoke subset (`six-format-auth`,
`oci-discovery`) run on every pull request, and the remaining lanes join on pushes
to `main`. The tiers live in `scripts/ci-contract-lanes.json`; evidence and
artifact names carry the architecture so legs never collide. See
[CI path classes](ci.md#contract-lanes).

## Foreign builds are unsupported

Producing the other architecture is not a supported way to build, and the tools
refuse it:

- `scripts/build-release-artifacts.sh` builds for the host only (`go env GOOS` and
  `go env GOARCH`) and exits non-zero when `--target` names a different platform.
  There is no Windows target.
- The Dockerfiles carry no `--platform` pin and no `TARGETOS`/`TARGETARCH`; the
  build stage runs on the platform it produces, so `go test` and the produced
  binary share one architecture.
- `make cli-build` builds the machine's own binary to
  `dist/notarxctl_<goos>_<goarch>`.

Developers who need an artifact of the other architecture take it from the CI
artifacts of a pull request or of `main`.

## Local development and Apple Silicon

Docker Desktop on Apple Silicon builds native linux/arm64 images, and on amd64 Linux
or Windows hosts native linux/amd64, with no flags. `make build`, `make docker-build`
and `make console-docker-build` call plain `docker build`. A developer on Apple
Silicon therefore tests the same architecture as the Oracle Cloud Always Free
Ampere A1 reference deployment ([ADR 0017](adr/0017-runtime-backends-and-oci-free-tier.md)),
which runs the arm64 image built and tested on `ubuntu-24.04-arm`. Do not pass
`--platform` for the other architecture and do not install QEMU or binfmt
emulation for release purposes.

## The native-build guard

`make native-build-check` runs `scripts/native-build-check.sh` after its self-test
(`scripts/native-build-check-test.sh`, which proves each rule against the negative
fixtures in `scripts/testdata/native-build/`). It is part of `make preflight`, of
`make ci-local` and of the `quality` job. It fails on:

- `--platform=` on a `FROM` line, or `TARGETARCH`, `TARGETOS`, `BUILDPLATFORM` in a
  `Dockerfile*`;
- `GOOS=` or `GOARCH=` assignments in `Dockerfile*`, `Makefile`, `scripts/` and
  `.github/workflows/` (reading them with `go env` is allowed);
- `docker/setup-qemu-action`, `qemu-user-static` and `tonistiigi/binfmt`;
- `self-hosted` in any `runs-on` value and the floating `ubuntu-latest` label;
- a `platforms:` value that lists more than one platform, and `CGO_ENABLED=1`
  combined with a `CC=` cross compiler;
- the `macos-15-intel` label on or after 2027-09-01.

`scripts/native-build-allowlist.txt` skips paths and exists only for the guard, its
self-test and the fixtures, which have to spell the forbidden patterns.

## Decision point: retiring macos-15-intel

GitHub states that `macos-15-intel` is the last x86_64 hosted macOS image and is
available until August 2027. `darwin/amd64` archives ship until then. On
2027-09-01 the guard starts failing while the label is still referenced, which
forces the decision: remove the darwin/amd64 leg from
`.github/workflows/release-archives.yml` and stop shipping that archive. The
archive is never cross-built to keep it alive.
