# Contributing to notarx

[Français](CONTRIBUTING.fr-CA.md) · [简体中文](CONTRIBUTING.zh-CN.md)

notarx is currently developed by its core team. This guide records
the engineering workflow used inside the repository so changes remain
reviewable and reproducible while the project is prepared for broader
contribution.

## Development prerequisites

- Docker with Compose support
- `kubectl`, `jq`, and a local Kubernetes cluster when changing deployment
  resources
- Go at the version declared in `go.mod`
- Rust/Cargo 1.96.0 when changing the staged Cargo protocol foundation
- Node.js 26 and npm
- GNU Make
- OpenSSL

Start the complete local stack from a checkout:

```sh
make dev-bootstrap
make dev
```

`make dev-bootstrap` creates a private `.env` when needed and generates only
the credentials required by the local Gateway, PostgreSQL, and RustFS stack.
It preserves existing real values and is safe to run again. See the
[getting-started guide](docs/getting-started.md) for the complete workflow.

`make dev-status` verifies the Console, its API proxy, and Gateway health.
`make dev-down` stops only the checkout-managed Console; `make down` stops the
Compose stack without deleting its PostgreSQL and RustFS volumes.

## Change workflow

1. Keep a change focused on one behaviour or engineering concern.
2. Add or update tests at the public boundary affected by the change.
3. Update the OpenAPI source before generated clients or server interfaces.
4. Add a migration for schema changes; never edit an applied migration.
5. Describe user-visible behaviour changes in the pull request title (a
   Conventional Commit subject); `CHANGELOG.md` is generated from it at release
   time and is not edited by hand.
6. Run the smallest relevant check while developing, then the required checks
   below before committing.

Generated files must not be edited by hand. The editable OpenAPI roots are
`api/openapi/native-hosted.yaml` for the native/Console contract and
`api/openapi/management-runtime.yaml` for the Go management boundary. Keep shared
management paths and schemas consistent in both roots. `make openapi-check`
regenerates and verifies both JSON bundles, the Console client, and the Go
management contract.

Install the pinned generators before running the contract check:

```sh
npm --prefix tools/openapi ci --ignore-scripts --no-audit --no-fund
npm --prefix console ci --ignore-scripts --no-audit --no-fund
```

Do not reinstall Console dependencies while `make dev` is serving Vite. Stop
the managed Console with `make dev-down`, install dependencies, then restart it
with `make dev`; replacing `node_modules` under a live Vite process invalidates
its optimized dependency URLs.

The optional Ant Design CLI lint/usage/doctor helpers have been removed with
their `fast-glob` → `micromatch` → `braces` development dependency chain.
GHSA-vfj7-8cjw-p6xm has no audit exception; reintroducing the advisory fails the
gate. Use `make console-check` for ESLint and formatting, `make console-typecheck`
for types, and `make console-test` for the full coverage gate. API generation
still uses the pinned OpenAPI tooling above. Themes use the Console theme
definitions and Ant Design `ConfigProvider`; Vite and Tailwind build the styles
through `make console-build`. None of these flows requires the Ant Design CLI.
Other audit decisions remain in `scripts/npm-audit-policy.mjs`; new advisories,
invalid reports, registry failures, and expired exceptions fail dependency audit.

## Language

Issues, pull requests, commit messages, code comments and documentation are
written in English (Canadian spelling, as set by
[ADR 0014](docs/adr/0014-locale-policy.md)). Do not file issues or PRs in other
languages. Product locales and translated documentation follow the locale
policy in ADR 0014, not this file.

## Issue-to-merge traceability

Track each user-visible change or defect in a GitHub Issue with the observed
problem, intended behaviour, explicit scope, acceptance criteria, and dependencies.
Keep each Issue small enough to validate and merge independently; split unrelated
protocol or cache semantics into linked follow-up Issues.

Use a branch such as `codex/issue-24-group-resolution`. Link the Issue in the PR
body with `Closes #24`, describe the final behaviour, and record commands and
results against the PR head SHA. Merge only after the required checks pass for
that SHA. Record the PR, merge SHA, and remaining limitations in the Issue, then
remove the merged feature branch after checking that no worktree still uses it.
Do not remove unmerged branches solely because they are old.

CI, isolated protocol fixtures, local runtime checks, and release acceptance are
separate evidence. Closing an implementation Issue does not declare a release
ready; track upgrade, restore, and deployment acceptance explicitly.

## Issue classification

Every Issue carries exactly one type label and one primary area label; Issues
about a specific package format also carry that format label. The label
vocabulary is the classification: filtering on `label:"area: cli"` returns
exactly that workstream.

| Axis | Labels | Meaning |
| --- | --- | --- |
| Type | `type: feature`, `type: bug`, `type: design`, `type: docs`, `type: chore` | `feature` extends user-visible capability; `bug` is behaviour contradicting the intended outcome or the contract; `design` produces a decision recorded in an ADR or document rather than code; `docs` changes documentation only; `chore` covers CI, dependencies, refactors, and tooling with no behaviour change. |
| Area | `area: console`, `area: api`, `area: cli`, `area: auth`, `area: audit`, `area: platform`, `area: ops` | `console` is the web UI; `api` is the management contract, its OpenAPI sources, and generated clients; `cli` is `notarxctl`; `auth` covers identity, authorization, accounts, and service credentials; `audit` covers audit records, retention, and forensics; `platform` is the server-side data plane — artifacts and repositories, lifecycle and retention, promotion and replication, capacity, cache, quarantine, scanning; `ops` covers deployment, runtime, diagnostics, logs, backup, and upgrade. |
| Format | `format: maven`, `format: oci`, `format: npm`, `format: pypi`, `format: go`, `format: apt`, `format: conan`, `format: cargo`, `format: nuget`, `format: raw` | Orthogonal to area; applied to Issues about one protocol's behaviour. |

Titles start with the area: `Console:`, `API:` and `CLI:` for the technical
surfaces, and `Auth:`, `Audit:`, `Platform:` or `Ops:` for the domains,
followed by the finding or the required outcome rather than the activity. Meta work that spans areas (process, tooling) may omit the area word
and the area label.

Every Issue states its background, scope, acceptance criteria, and an explicit
out-of-scope; a series keeps a parent Issue whose body lists its children as a
task list. Keep one Issue per independently mergeable change and reference it
with `Closes #N`. The issue forms under `.github/ISSUE_TEMPLATE/` use type-specific
fields for these elements and apply the type label; add the applicable area and
format labels when filing. For bugs, expected behaviour and contract evidence
define acceptance, and impact states scope and exclusions. For designs,
problem/constraints state scope and exclusions, and the decision with its
recorded outcome defines acceptance. Feature forms name these elements directly.

## Required checks

Run the matrix locally before pushing, so a push spends CI once instead of once
per iteration:

```sh
make ci-local                        # the fast set, a few minutes
make ci-local-full                   # every pull-request job, including the native E2E suites
./scripts/ci-local.sh --job console  # one CI job
./scripts/ci-local.sh --list         # what each selection runs
```

`scripts/ci-local.sh` calls the same targets and scripts the workflow calls
rather than reimplementing them, groups its output by CI job, and prints each
check with its duration. It fails fast by default; pass `--keep-going` to collect
every failure in one run. `mainline-assets` and `mainline-images` are push-to-main
only and are reported as out of scope. CI remains the source of truth.

For a backend-only change:

```sh
go test ./path/to/changed/package
make lint
make vet
make coverage
```

For a Console change:

```sh
npm --prefix console ci
make console-check
make console-test
make console-build
```

`make lint` also runs `make spellcheck`, which checks the Console messages and
the English documentation against the Canadian English (`en-CA`) dictionary
(`colour`, `-ize`); `make architecture-check` rejects CJK text in non-test Go
files, because servers return English messages with stable codes and the
Console localizes by code (ADR 0014). Install the Console dependencies first
(`npm --prefix console ci`); project terms go in `.cspell/project-words.txt`.

`make coverage` enforces both the repository-wide Go baseline and stricter
floors for stable security, lifecycle, replication, and scanning packages.
`make console-test` measures all hand-written Console TypeScript and TSX while
excluding only the generated API client and test setup. Coverage floors are
non-regression guards: raise them with meaningful public-boundary tests and do
not lower them to make a change pass.

For contract, persistence, or protocol changes, also run the matching checks:

```sh
make openapi-check
make integration-test
make native-oci-e2e
make native-raw-e2e
make native-maven-e2e
make conan-e2e
make cargo-contract
```

For Kubernetes manifests, Console container routing, or local deployment
helpers, run the offline render gate before using a live cluster:

```sh
make kubernetes-local-check
make console-docker-build
```

When Docker Desktop Kubernetes is available, also run
`make kubernetes-local-up`, `make kubernetes-local-status`, and
`make kubernetes-local-verify`. The verification publishes a unique Raw object,
restarts Gateway, and reads both its PostgreSQL Repository record and object
bytes back. Record the live evidence and use `make kubernetes-local-down` only
when deleting the namespace and its local PVC data is intended.

Builds are native: notarx never cross-compiles and never emulates (ADR 0016).
`make build`, `make docker-build` and `make cli-build` produce the architecture of
your machine (arm64 on Apple Silicon, amd64 on Linux x86_64), and the other
architecture comes from the CI artifacts, not from a local `--platform` build.
Run `make native-build-check` after touching a Dockerfile, the Makefile, `scripts/`
or a workflow; see [native builds](docs/native-builds.md).

Run `make test` before submitting a change that affects shared backend behaviour.
Run `make docs-check` when changing Markdown entry points, capability claims,
or local assets; it validates local documentation references and the
documentation site map.
The CI workflow is the final source of truth for the complete required matrix.

### Contract lanes and performance evidence

`scripts/ci-contract-lanes.json` is the single source for the contract lanes:
their names and order, the tools each needs (`rust`, `node`, `npm`), job and
step timeouts, and the failure artefacts. `scripts/ci-contract.py matrix` turns
it into the matrix that `test-suite.yml` feeds to the reusable `contract-lane.yml`, so
neither workflow names a lane. Add or move a lane by editing the JSON only.

The contract jobs (`contract-early` for lanes that load no prebuilt image,
`contract-amd64` and `contract-arm64` for the others, each waiting only for the
image build of its architecture) run in parallel with `fail-fast: false`. The
`suite-ok` job of `test-suite.yml` requires every job of the suite to succeed or
be skipped by change detection; pull request CI and the release gates (ADR 0024) both
call that suite, and a push to `main` runs no tests. The
former serial `core` lane is split into `core-checks`, `core-email`,
`core-native-e2e`, `core-backup`, `core-integration-1`, `core-integration-2` and `core-integration-3`, followed by the native lanes (migration
clients, inventory, NuGet, OCI migration and discovery, private-upstream
authentication, npm migration, Helm, RubyGems, PyPI migration). Each lane
installs only the toolchains it declares, read from `go.mod`,
`rust-toolchain.toml` and `.nvmrc`; npm retry and timeout settings live in
`console/.npmrc` and `tools/openapi/.npmrc`.

Run one lane with `./scripts/ci-local.sh --lane inventory`; `--job contract`
runs every lane plus the local CLI stack check. Local OpenAPI freshness
regenerates and compares file hashes so uncommitted generated changes remain
reviewable. The lane runner controls run with
`python3 -B -m unittest discover -s scripts/tests -p test_ci_contract.py`.
A failing lane uploads `contract-<lane>-<attempt>` with its step logs and raw
producer reports. `upgrade-readiness` is not run in CI until a stable notarx release tag exists (ADR
0015); `make backup-transfer-test` runs the baseline-plus-synthetic-`000002`
upgrade fixture meanwhile.

## Documentation conventions

Every published Markdown document has an English (en-CA spelling) canonical
route that uses the unsuffixed `.md` filename. Localized companions follow
[ADR 0014](docs/adr/0014-locale-policy.md):

- **en-CA** is the reference locale and the only mandatory one.
- **fr-CA** (`.fr-CA.md`, route prefix `/fr-CA/`) is first-class. The core set
  `README`, `docs/README`, `docs/getting-started`, `SECURITY` and
  `CONTRIBUTING` requires a French companion; other French pages are optional.
- **zh-CN** (`.zh-CN.md`, route prefix `/zh-CN/`) is community-maintained. It is
  frozen at the upstream snapshot: existing files stay and are structure-checked,
  missing ones are silent, nobody is assigned, the Console falls back to en-CA,
  and the prune tooling is kept. New zh-CN content is welcome from a community
  contributor but is never required and never blocks a change.

A companion links to its English page and to the documentation index, and the
English page links back. The language switcher lists only the locales that
exist for the page.

fr-CA changes are reviewed by an automated LLM step rather than a Quebec-based
human ([#257](https://github.com/mlops-ca/notarx/issues/257)). It checks changed
fr-CA strings against the OQLF-aligned glossary and writing rules and flags
issues on the pull request; the provider is set with `I18N_REVIEW_PROVIDER`
(Claude, Mistral or Cohere). A maintainer still approves the pull request
(CODEOWNERS routes `*.fr-CA.md` and `*.fr-CA.ts` to the maintainers). Drafts
are marked in `console/src/messages/.review/fr-CA.json`, and a release candidate
fails `make i18n-release-check` while any fr-CA key is `draft` or a `critical`
key has not passed the review. zh-CN has no release gate.

The review is `.github/workflows/i18n-review.yml`, which runs
`scripts/i18n-review.mjs` on pull requests that touch `*.fr-CA.ts`,
`*.fr-CA.md` or `console/src/messages/glossary.ts`. Only the changed strings
(with their en-CA text) or the prose of the changed fr-CA documents, the
glossary and the writing rules are sent to the provider, never repository data.
Set the repository variable `I18N_REVIEW_PROVIDER` to `anthropic`, `mistral`
or `cohere` and add its key as the secret `ANTHROPIC_API_KEY`,
`MISTRAL_API_KEY` or `COHERE_API_KEY`; an unknown value fails with a clear
error. Findings (glossary avoid-terms, France-French wording, typography, meaning
drift from en-CA) appear as check annotations and a pull request comment, and the
check fails when a `critical` key has a finding. Keys that pass are recorded as
`reviewed` in the sidecar at release time, not on the pull request: the
workflow never pushes to the pull request branch (see
[the CI guide](docs/ci.md#fr-ca-review-and-the-sidecar)). Fork pull requests get no secrets, so a maintainer runs the workflow with
`workflow_dispatch` and the pull request number; the result is posted as the
`i18n-review` commit status. The script that holds the key always comes from the
base revision, and the head is only read as data.

Add every English document, and each companion that exists, to
`docs/site-map.json` under one of the six stable sections. The map is the framework-neutral navigation contract for a future
Docusaurus, VitePress, or MkDocs site; do not maintain framework-specific
frontmatter as a second navigation authority.

Keep translated pages behaviorally equivalent (fr-CA and any existing zh-CN). Configuration keys, routes,
status codes, commands, compatibility limits, preview status, and security
boundaries must not disappear in one locale. A short language stub or a link
to the other language is not a translated body.

Run `make docs-check` after adding, renaming, or moving a page. It validates
local links, language pairs, reciprocal links, per-locale body checks, localized
titles, and complete site-map coverage. See the
[documentation site guide](docs/documentation-site-guide.md).

## Test expectations

- Test externally observable behaviour through an HTTP, storage, protocol, or
  exported package interface.
- Prefer one focused behaviour per test and use known literal expectations.
- Add a regression test before fixing a defect when a stable public boundary is
  available.
- Do not couple tests to private helper calls merely to increase coverage.
- Do not narrow coverage collection to a small allowlist of well-tested files;
  exclusions must be generated code, fixtures, or test infrastructure.
- Use integration tests for PostgreSQL transaction, migration, locking, and
  object-store behaviour that the in-memory stores cannot prove.

## Typed keys and identifiers

Rules (ADR 0023 section 4), enforced by `make lint` (`sqllint-check`, which runs
`go run ./tools/sqllint/cmd`) and by `TestSQLTypedKeyLint` in `make test`:

- Never compare a uuid column cast to text with a parameter
  (`col::text = $1`, `$1::text = col`, `id::text` after `WHERE`): it defeats the
  btree index. Bind the parameter as `uuid.UUID` or `$n::uuid` and scan uuid
  columns into `uuid.UUID` or select them with `col::text` only in the select list.
- Create new identifiers with `ids.New()` (`internal/ids`, UUIDv7), never with
  `uuid.New()`. The lint rejects `uuid.New()` and `uuid.NewRandom()` (also under an
  import alias) anywhere in `internal/` outside `internal/ids`. `uuid.NewString()`, also a
  random UUIDv4, is counted per file in non-test code in `tools/sqllint/allowlist-uuid.txt`
  and ratcheted the same way: a new call in a file without an allowance fails the lint,
  so use `ids.New()` for row identifiers. Calls that are not row identifiers (lease
  tokens, request and trace ids, temporary names) may stay and keep their count. Nothing may
  depend on identifier order.
- `$n::uuid` raises SQLSTATE 22P02 for a string that is not a uuid, where
  `col::text = $n` used to match nothing. Parse an identifier that comes from a
  client (a path segment, a query parameter or a request body) with
  `uuid.Parse` at the API boundary and answer 400 or 404 there; do not let it
  reach a store query as a free-form string. Identifiers the server issued or
  read from the database are already valid.
- The text-cast rule has no allowlist: any violation fails the lint. Existing
  `uuid.NewString()` calls are counted per file in `tools/sqllint/allowlist-uuid.txt`.
  Those counts may only decrease: a higher count fails the lint, and a count that is now too
  high fails it too, so run `go run ./tools/sqllint/cmd -update` after converting calls.

## Database migrations

Migrations are forward-only and are applied before Gateway nodes start. The
database records both migration filenames and SHA-256 checksums in
`notarx_schema_migrations`; a recorded file whose checksum changed aborts the
run. Validate a migration with:

```sh
make migration-check
make integration-test
```

The integration suite applies all migrations to a clean PostgreSQL database
and verifies that a second run is a no-op.

The files live in one directory per database dialect (ADR 0022): today only
`migrations/postgres/`. Release archives, the compose bundle and the container
mounts keep a flat `migrations/` directory that holds the files of the one
dialect they serve.

Rules (ADR 0015):

- The schema starts from `migrations/postgres/000001_baseline.sql`, a normalized
  `pg_dump`. Never edit it in place; schema changes are new files
  `000002_<snake_case>.sql`, `000003_...`, with a unique, gap-free 6-digit
  prefix.
- Forward-only, expand/contract: a release may add objects and backfill. It may
  drop or rename only objects that the previous minor release no longer reads.
  There is no Down section.
- Data backfills live in numbered migrations, never in the baseline. The
  baseline seeds only the singleton rows the code expects.
- `pg_trgm` is required (trigram indexes). `make preflight` and the migration
  runner check that the extension is installed or creatable before the first
  statement.
- Before the first stable release (`vX.Y.Z`, ADR 0024) databases are disposable:
  the baseline was re-squashed from the whole chain just before the tag (ADR
  0015 slice 7), so a database created earlier must be recreated. After that
  tag, released migrations are immutable.

`notarx migrate` (`internal/migrate`, ADR 0022) is the Go runner. On PostgreSQL it
applies each file in one transaction and writes the same `notarx_schema_migrations`
ledger as `scripts/run-migrations.sh`, which stays the deployed runner; the integration
job compares the two ledgers on every run (`scripts/migration-runner-check.sh`). On
MySQL, which has no transactional DDL, it holds `GET_LOCK("notarx-migrate:<db>")`,
records each file as `applying` with the number of statements done, and marks it
`applied` at the end. After a crash `notarx migrate --repair` shows where the file
stopped; confirm whether the next statement took effect, then resume with
`notarx migrate --repair --completed N`. `notarx migrate lint` (also a unit test over
`migrations/mysql`) requires every MySQL file after the baseline to be re-runnable: one
specification per `ALTER TABLE`, `INSERT ... ON DUPLICATE KEY UPDATE` instead of
`INSERT IGNORE` or `REPLACE`, `WHERE` on every `UPDATE` and `DELETE`, and the
`IF [NOT] EXISTS` guards MySQL offers.

The neutral schema catalogue `migrations/catalogue.yaml` (ADR 0022) lists every table with
its logical column types, nullability, keys, unique sets, generated columns and seed rows.
It is generated from a migrated PostgreSQL, never edited by hand: after a schema change run
`go run ./tools/schemaparity generate` against a migrated database (`TEST_DATABASE_URL`) and
commit the result. `make schema-parity` compares it with a migrated PostgreSQL, and with a
migrated MySQL once `migrations/mysql` exists, through the logical-type mapping in
`internal/schemaparity/types.go`, and fails on drift. Every migration after the baseline ships in
both `migrations/postgres` and `migrations/mysql`, or is named with its one dialect and the
reason in `migrations/dialect-scope.yaml`; `make test` fails otherwise.

`make migration-check` (`scripts/migration-immutability-check.sh`) runs in CI
and in `make preflight`. It always rejects duplicate, out-of-sequence or gapped
prefixes and names that do not match `NNNNNN_<snake_case>.sql`. Once a stable
tag exists it also fails when a file that exists at the latest stable tag (the
highest tag matching `^v[0-9]+\.[0-9]+\.[0-9]+$`; nightly pre-releases are
ignored) differs from that tag. It needs the tags, so use a full clone.
`bash scripts/migration-immutability-check-test.sh` runs its fixtures.

Integration lifecycle commands explicitly use the `notarx-integration`
Compose project and `/dev/null` as the environment file. They ignore the checkout
`.env` and an exported `COMPOSE_PROJECT_NAME`, so test cleanup cannot select the
normal development stack. Use `make integration-down` to clean that test project.

## Commit messages

Pull requests are squash-merged, so the pull request title becomes the commit
subject. Use an imperative Conventional Commit subject,
`type(scope)!: subject`:

```text
feat(oci): add range reads for blobs
fix(console): preserve selected artifact version
test(storage): cover expired worker leases
docs: describe clustered runtime roles
```

The types, the releasing types (`feat`, `fix`, `perf`, `revert`; a `!` or a
`BREAKING CHANGE:` footer also releases) and the allowed scopes live in
`.github/commit-policy.json`, the single source of the policy
([ADR 0024](docs/adr/0024-release-versioning-and-conventional-commits.md)). A scope is
optional, several may be listed separated by commas (`feat(oci,npm): ...`), and
adding one is a one-line change to that file. The `pr-title` workflow checks the
title when a pull request is opened, edited or updated; run
`TITLE='feat(oci): x' make commit-policy-check` to check one locally.
`fix(security)` and `fix(deps)` release a patch; Dependabot titles follow the
policy through `.github/dependabot.yml`.

Choose the narrowest useful scope. Keep generated output in the same commit as
the contract or source change that produced it. A change to the Trivy pin or the
scanner image is `build(scanner)` and requires a reviewer from `CODEOWNERS`
([ADR 0030](docs/adr/0030-sbom-generation-and-vulnerability-scanning.md)).

A commit that cherry-picks work from the upstream project carries an
`Upstream:` trailer naming the upstream commit, for example
`Upstream: ccsert/artifact-gateway@<sha>`; see
[docs/upstream.md](docs/upstream.md).

`make lint` runs `scripts/identity-check.sh`, the identity gate of
[ADR 0013](docs/adr/0013-notarx-fork-charter.md). It fails when the upstream
owner or product name, the old environment prefix, the old CLI name or the old
metric and ledger prefix, or the short `ag-` / `AG_` abbreviation of the old
product name, appears in a tracked file. The Console keeps its internal `ag-` CSS
classes, `--ag-` custom properties and `ag.console.*` localStorage keys (a rename
would touch thousands of selectors and break stored preferences); the gate allows
`ag-` only under `console/` and in the few design and tooling files named by
`scripts/identity-check.sh --list-ag`, each with its reason. The allow-list is the one in the
ADR plus a short documented list of legacy cases (the legacy environment refusal
list and the recovery code that verifies pre-fork releases); each entry in the
script carries its reason. Lines with frozen zh-CN text are not checked.
`scripts/identity-check-test.sh` (part of `make test`) holds the fixtures.

## Contribution license

By submitting a contribution to notarx, you agree that it is
licensed under the project's [MIT License](LICENSE), unless explicitly stated
otherwise.
