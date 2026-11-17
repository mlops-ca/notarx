# Single Baseline Migration

[Documentation index](../README.md)

Status: proposed (2026-11-17)

## Context

notarx is a hard fork (ADR 0013) that restarts versioning at 0.1.0 and offers
no upgrade path from upstream installations. The inherited schema history no
longer serves that fork:

- `migrations/` holds 173 files totalling about 4,300 lines. Numbering runs
  from `000001_initial.sql` to `000190_native_rubygems_replication.sql` with
  gaps, and the prefixes `000045` and `000188` are each used by two files
  (`000188_helm_preview_replication.sql`, `000188_native_rubygems_quarantine.sql`).
  Order is decided by filename sort only.
- `scripts/run-migrations.sh` applies `*.sql` in sorted order, strips the
  `-- +goose Down` section with `sed`, and records `(filename, checksum)` in
  `artifact_gateway_schema_migrations`. A recorded file whose SHA-256 changed
  aborts the run ("Applied migration ... has changed"). The runner is mounted
  by `compose.yml`, `compose.integration.yml`,
  `deploy/kubernetes/overlays/local/gateway-patch.yaml` and
  `deploy/oidc-test/kubernetes.yaml`.
- The history is mostly DDL, but carries seed and data-fix statements that a
  baseline must treat deliberately:
  - `000001_initial.sql`: `INSERT INTO schema_version` (the table is not read
    by any Go or shell code).
  - Singleton rows that code expects to exist: `000049_audit_retention.sql`
    (`audit_retention_policy`), `000055_anonymous_access_policy.sql`
    (`anonymous_access_policy`), `000107_site_settings.sql` (`site_settings`),
    `000174_installation_repository_presets.sql`
    (`installation_repository_presets`, state `pending` on an empty
    installation, `skipped_existing` otherwise, catalog `standard-v1`).
  - Pure backfills of pre-existing rows, meaningless on an empty database:
    `000056_native_oci_manifest_restore.sql` (tombstones),
    `000123_member_level_and_repository_grants.sql` (derives
    `repository_grant_sets` and `repository_grants` from `users` and
    `hosted_repositories`), and `gen_random_uuid()` backfills in
    `000133_cargo_distribution_webhooks.sql`.
- Extensions are created only in `000064_postgres_observability_indexes.sql`
  (`pg_stat_statements`, `pg_trgm`). `gen_random_uuid()` is built in from
  PostgreSQL 13, so `pgcrypto` is not needed. `pg_stat_statements` additionally
  needs `shared_preload_libraries` (see `internal/backupops/docker.go`).
- Code and tests bind to migration names and the ledger table:
  - `internal/backupops/postgres.go` reads
    `SELECT filename,checksum FROM artifact_gateway_schema_migrations`;
    `MigrationLedger` in `internal/backupops/release.go` renders the TSV
    `filename<TAB>sha256` and validates names against `migrationFilename`.
  - `internal/backupmanifest/verify.go` accepts a ledger of
    `^[0-9]{6}_[A-Za-z0-9_]+\.sql$` rows, strictly increasing, under the schema
    format `gateway-migrations-tsv-v1`.
  - `internal/backupops/release_upgrade_integration_test.go` proves a formal
    v0.5.0 to candidate upgrade and asserts the 136 old checksums survive;
    `recovery_integration_test.go` (around line 532) recreates the ledger table
    by hand; `internal/backupops/release_test.go`, `release_module_test.go`,
    `native_archive_test.go`, `published_native_test.go`,
    `internal/backupmanifest/verify_test.go`, `internal/preflight/backup_test.go`
    and `internal/repositorypreset/pgstore/store_integration_test.go` reference
    migration files.
  - Upgrade scripts: `scripts/upgrade-readiness.sh`,
    `scripts/historical-upgrade-readiness.sh` (base ref `v0.1.0` of upstream),
    `scripts/cargo-upgrade-readiness.sh` (queries ledger rows
    `000131`/`000132`), `scripts/apt-lifecycle-upgrade-check.sh`,
    `scripts/member-role-upgrade-check.sh`, `scripts/member-role-migration-report.sh`,
    `scripts/migration-runner-check.sh` (expects one "Skipping applied
    migration" line per file) and
    `internal/repository/postgres_npm_metadata_upgrade_integration_test.go`.
  - Docs that cite migration numbers: `docs/helm-replication-preview.md`,
    `docs/rubygems-hosted-preview.md` (and `.zh-CN` twins), `CONTRIBUTING.md`
    (section "Database migrations").

## Considered designs

1. **Keep the 173 files.** Zero risk to schema fidelity, but ships forever a
   history whose first 136 steps exist only to upgrade installations that will
   never exist in this fork, with duplicate prefixes and data fixes that no
   fresh install exercises. Every test above keeps its upstream coupling.
2. **Hand-write one baseline from the Go store code.** Smallest file, but the
   schema is the product of 173 steps; hand reconstruction will drift silently
   (defaults, partial indexes, check constraints, trigger functions).
3. **Generate the baseline from the old chain with `pg_dump`, normalise, then
   review and prove equivalence once.** Fidelity comes from PostgreSQL itself;
   reviewability comes from a deterministic normaliser and a curated seed
   section. The one-off proof removes the old chain afterwards.
4. **Squash but keep the old ledger table name and filenames** to stay
   restore-compatible with upstream backups. Rejected: ADR 0013 states there is
   no upgrade path and no compatibility aliases; carrying the name would invite
   exactly the cross-fork restores that cannot work, because the baseline rows
   differ from upstream rows.

## Decision

Option 3.

### Layout and naming

- ADR 0022 later moves this file to `migrations/postgres/` and adds a MySQL baseline in `migrations/mysql/`, generated from a neutral catalogue introspected from this baseline after the re-squash (slice 7); the PostgreSQL baseline stays the normalised `pg_dump`.
- `migrations/` contains exactly one file at the cut-over:
  `000001_baseline.sql`. After the cut-over, new schema changes are ordinary
  numbered migrations (`000002` onwards); the baseline is never edited in
  place. Immediately before the `v0.1.0` tag the generator re-squashes the
  chain into a fresh `000001_baseline.sql` once (see "Migrations after the
  cut-over"). The file matches `migrationName` in
  `internal/backupmanifest/verify.go` and `migrationFilename` in
  `internal/backupops/release.go`, so neither parser changes.
- The `-- +goose Up` marker is kept for editor tooling; no Down section is
  written (the runner already discards it and migrations are forward-only).
- Ledger table renamed to `notarx_schema_migrations`
  (`filename TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ`).
  The runner creates it with `CREATE TABLE IF NOT EXISTS`; it is not part of the
  baseline file. No view, alias or fallback to the old name exists.
- The backup schema format string `gateway-migrations-tsv-v1` becomes
  `notarx-migrations-tsv-v1`; the verifier rejects the old string.
- The runner's defaults `PGUSER=gateway` and `PGDATABASE=gateway` follow the
  env prefix change of ADR 0013 and are set by the deployment slices, not here.
- The unused `schema_version` table and its seed row are dropped.

### Generation procedure

`scripts/baseline-generate.sh` (kept in the repository until 0.1.0 is tagged,
then removed):

1. Start PostgreSQL 16 (`postgres:16-alpine`, same as `compose.integration.yml`)
   and apply the source chain with the existing runner. The source is a git
   ref plus directory argument: at the cut-over it is the 173 files at the
   last upstream commit imported into the fork; at the re-squash it is the
   current `migrations/` directory. The generator is run from a container
   whose `pg_dump` image is pinned by digest in the script, and the same
   pinned image performs the dumps of the equivalence proof.
2. `pg_dump --schema-only --no-owner --no-privileges --no-comments
   --no-tablespaces --no-publications --no-subscriptions` into one file.
   The equivalence proof uses exactly the same flags.
3. Normalise deterministically (a Go program under `internal/tools/baselinefmt`
   or an awk script with tests): strip `SET`/`SELECT pg_catalog.set_config`
   preamble, the `\restrict` and `\unrestrict` psql meta-commands (recent
   `pg_dump` releases emit both with a random key; the runner pipes the file into
   `psql`, which would otherwise execute a key that differs per run), `CREATE SCHEMA public`, extension `COMMENT`s and
   the `artifact_gateway_schema_migrations` and `schema_version` objects; order
   statements by dependency as emitted by `pg_dump`; add `IF NOT EXISTS` only to
   `CREATE EXTENSION`; keep every table, type, function, trigger and index
   statement unaltered.
4. Prepend the extensions with explicit comments.
   `CREATE EXTENSION IF NOT EXISTS pg_trgm;` is required: 12 GIN
   indexes (`000064`, `000145`) use `gin_trgm_ops`. `pg_trgm` is a trusted
   extension from PostgreSQL 13, so a non-superuser migration role with
   `CREATE` on the database can install it. `pg_stat_statements` is
   optional: no Go code reads it and it is only preloaded by
   `internal/backupops/docker.go` and the compose files. It is created inside a
   guarded block (`DO $$ BEGIN CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
   EXCEPTION WHEN OTHERS THEN RAISE NOTICE '...'; END $$;`), so a server
   without it, or a role that may not create it, still migrates. Operators who
   want statement statistics set `shared_preload_libraries` themselves; ADR 0017
   documents the flag in its Compose files. No claim is made here that any
   managed PostgreSQL offering ships either extension; ADR 0017 owns the managed-PostgreSQL
   support matrix and must confirm `pg_trgm` on each backend it supports; this
   ADR neither owns nor assumes one.
5. Append a hand-written `-- seed` section with idempotent
   `INSERT ... ON CONFLICT DO NOTHING` for exactly these rows:
   `audit_retention_policy`, `anonymous_access_policy`, `site_settings`
   (singleton `true`) and `installation_repository_presets`
   (`singleton true, catalog_version 'standard-v1', state 'pending'`). The
   `skipped_existing` branch of `000174` and the backfills of `000056`,
   `000123` and `000133` are dropped because a fresh install has no prior rows.
   Each seed row carries a comment naming the originating migration. At the
   re-squash the section is regenerated by hand from the same rule: singleton
   rows that code expects stay, backfills of pre-existing rows go, and the
   proof's data comparison (below) catches omissions.
6. A human reviews the diff of the generated file against the columns each
   store package reads (see slice 2).

### Equivalence proof (run at the cut-over and again at the re-squash)

`scripts/baseline-equivalence-check.sh` runs in a temporary CI job on the
migration PR. Because the baseline is a normalised dump of the source chain, the
proof mainly validates the normaliser and the hand-written seed section; schema
fidelity itself comes from `pg_dump`, and the independent check is the existing
Go integration suite and format e2e lanes running against B.

- Database A: apply the source chain. Database B: apply `000001_baseline.sql`.
- Compare `pg_dump --schema-only --no-owner --no-privileges --no-comments` of A
  and B (same flags and pinned image as the generator) after removing the ledger
  and `schema_version` objects; the diff must be empty. There is no column-order
  exemption: `pg_dump` emits columns in attribute order and omits dropped
  columns, so a real ordering difference is drift.
- Compare `pg_dump --data-only` of A and B for every table except the ledger;
  this covers all seeded rows, including any inserted by migrations added after
  the cut-over, and fails if a seed row or a column default differs.
- Compare catalog extras that `pg_dump` can miss or render differently:
  `pg_extension`, `pg_indexes.indexdef`, `pg_get_functiondef` for every
  function, `pg_trigger`, `pg_constraint` (`pg_get_constraintdef`), sequences
  (`pg_sequences`) and `pg_class.reloptions`.
- The existing Go integration suite and the format e2e lanes then run against B.

The script and its workflow are parameterised on the source chain and kept
until the re-squash PR has merged; the re-squash slice deletes them. The old
173-file chain remains reachable in git history (the last upstream-derived commit
before the cut-over, to be recorded in the PR description).

### Migrations after the cut-over

- Release scope (ADR 0013): `v0.1.0` is the foundation ADRs 0013 to 0017.
  Schema work of ADRs 0018 to 0021 ships in 0.2.x and later as additive
  migrations. Any schema change needed earlier (for example by ADR 0017) is a
  normal numbered migration `000002+`, never an edit of the baseline. Dependent
  ADRs cite "ADR 0015 slice 4" (cut-over), not "ADR 0015".
- Re-squash once before the tag: immediately before tagging `v0.1.0`, the same
  generator and equivalence proof turn `000001_baseline.sql` plus every
  `000002+` file into a fresh `000001_baseline.sql`. Pre-tag databases are
  disposable and must be recreated; the pre-release docs say so. Because no
  baseline edits happen between cut-over and re-squash, every development and CI
  database either recreates cleanly or applies the new numbered files, and the
  runner's checksum guard never fires on a changed released file.
- The first-release gate is implemented by ADR 0024: its stable promotion refuses to create the first
  stable tag unless `migrations/` holds exactly the single re-squashed baseline (`scripts/baseline-gate.sh`, ADR 0024 slice 5).
- After the tag, released migrations are immutable. A CI check
  (`scripts/migration-immutability-check.sh`) fails when a file in
  `migrations/` that exists at the latest release tag has any diff against that
  tag. It is a no-op until the first tag exists.
- New migrations are `NNNNNN_<snake_case>.sql` with a strictly increasing,
  gap-free, unique 6-digit prefix; the same check rejects duplicate or
  out-of-sequence prefixes (the `000045` and `000188` collisions must not recur).
- Forward-only and expand/contract: a release may add objects and backfill; it
  may drop or rename only objects that the previous minor release no longer
  reads. Data backfills live in migrations, never in the baseline.
- `make migrate` and `scripts/migration-runner-check.sh` (reworked to count
  files dynamically, already the case) remain the verification entry points.

### Backup, restore and release modules

- Backups carry the ledger from `notarx_schema_migrations`. A restore requires
  the target release to ship every ledger filename with the identical checksum
  (existing `VerifyRelease` semantics, unchanged). Backups from upstream
  artifact-gateway installations, whose ledger lists 136+ upstream names, fail
  with the existing `migration_ledger_invalid` or release mismatch error and are
  documented as unsupported (see ADR 0013).
- Right after the cut-over and after the re-squash the ledger has one row. `verify.go` already requires `migrations > 0`;
  nothing assumes a minimum count. Tests that assert 136 entries are rewritten.
- `internal/backupops/release_upgrade_integration_test.go` is rewritten once a
  second notarx release exists: until then it is replaced by a fixture that
  applies the baseline and a synthetic `000002` test migration, preserving the
  properties it proves today (old checksums preserved, replay no-op, snapshot
  rollback).
- Upgrade scripts that name upstream refs (`historical-upgrade-readiness.sh`
  default `v0.1.0`, `cargo-upgrade-readiness.sh` ledger names,
  `member-role-*`, `apt-lifecycle-upgrade-check.sh`) are deleted or
  re-pointed; `upgrade-readiness.sh` is kept, parameterised on notarx release
  tags, and becomes meaningful at 0.2.0.

## Security considerations

- The baseline is executable SQL that runs as the migration role. Review is
  therefore a supply-chain control: the generated file is committed (never
  generated at build time), checksummed by the runner, and covered by the
  immutability check after the tag. The pinned `pg_dump` image digest keeps the
  generator reproducible.
- Normalisation drops `ALTER ... OWNER` and `GRANT`. The deployment keeps its
  separate migration and runtime roles; the baseline must not grant to
  `PUBLIC`. The equivalence script asserts `has_table_privilege('public', ...)`
  is false for all tables.
- The seeded `anonymous_access_policy` row must keep today's default
  (anonymous read disabled). The seed review checks the column defaults, not
  only the row's existence.
- No secret or tenant data may be in the dump; schema-only dumps contain none,
  and the normaliser fails if it sees `COPY` or `INSERT` outside the seed block.

## Consequences

- Fresh installs apply one file; `make integration-test` startup is faster and
  the "workflow reads only one file" cognitive load drops.
- No path from any upstream database or backup. This is intentional and
  already announced by ADR 0013.
- Blame and rationale of individual upstream columns are lost from `migrations/`
  and survive only in git history prior to the fork point.
- Concurrent ADR work adds new numbered files rather than editing one hot file,
  so there are no baseline merge conflicts; the cost is one more re-squash PR
  (and one disposable-database reset) just before the tag.
- The `000188` duplicate and other history oddities disappear.

## Implementation slices

1. **Ledger rename and runner hardening**
   - Scope: `scripts/run-migrations.sh`, `internal/backupops/postgres.go`,
     `internal/backupops/recovery_integration_test.go`,
     `internal/backupmanifest/verify.go` (+ tests), `CONTRIBUTING.md`, API
     fixtures referencing `gateway-migrations-tsv-v1`. `scripts/cargo-upgrade-readiness.sh`
     also names the table; it is deleted in slice 5, so slice 1 only keeps it
     compiling or skips it.
   - Rename `artifact_gateway_schema_migrations` to `notarx_schema_migrations`
     and the schema format to `notarx-migrations-tsv-v1`; reject the old values.
   - [ ] Unit tests cover accept new / reject old format string.
   - [ ] `make test`, `make lint`, `make integration-test`,
     `make backup-transfer-test` pass.
   - [ ] `grep -r artifact_gateway_schema_migrations` returns nothing outside
     history notes.
   - Depends on: ADR 0013 slice 4 (environment prefix).
2. **Baseline generator and normaliser**
   - Scope: `scripts/baseline-generate.sh` (source chain and pinned
     `pg_dump` image digest as parameters), normaliser (Go package with tests
     under `internal/tools/`, subject to the repository's architecture-check rules),
     generated `migrations/000001_baseline.sql` staged alongside the old files
     under a non-applied directory `migrations-next/`.
   - [ ] Normaliser unit tests with golden pg_dump fixtures (preamble, comments,
     ownership, `COPY` rejection).
   - [ ] Normaliser strips `\restrict` and `\unrestrict`; `pg_trgm` is
     required and `pg_stat_statements` is created in a guarded block.
   - [ ] Seed section contains only the four singleton rows; column defaults
     reviewed in the PR description against `internal/repositorypreset/pgstore`,
     the audit retention store and the site settings store.
   - [ ] `make test`, `make lint` pass.
   - Depends on: slice 1.
3. **Equivalence proof**
   - Scope: `scripts/baseline-equivalence-check.sh`, a temporary workflow
     `.github/workflows/baseline-equivalence.yml`, both parameterised on the
     source chain.
   - [ ] Job passes on `linux/amd64` and `linux/arm64` native runners (the temporary workflow names the runner labels itself; no dependency on ADR 0016) with
     an empty schema diff (no column-order exemption), equal data dumps of all
     non-ledger tables, equal function, trigger, constraint, sequence and
     `reloptions` catalogs, and no `PUBLIC` privileges.
   - [ ] Job output (diff size 0) attached to the PR.
   - Depends on: slice 2.
4. **Cut-over**
   - Scope: delete the 173 files, move `migrations-next/000001_baseline.sql` to
     `migrations/` (generator and equivalence job stay for the re-squash), rewrite
     `scripts/migration-runner-check.sh` expectations, update tests listed in
     Context that name migration files or count 136 ledger entries
     (`internal/backupops/*_test.go`, `internal/backupmanifest/verify_test.go`,
     `internal/preflight/backup_test.go`,
     `internal/repositorypreset/pgstore/store_integration_test.go`).
   - [ ] `make test`, `make integration-test`, `make backup-drill`,
     `make restore-drill`, `make preflight` pass on a fresh database.
   - [ ] The second-run no-op check reports exactly one skip.
   - Depends on: slice 3. This is the "ADR 0015 slice 4" that other ADRs cite.
5. **Retire upstream upgrade tooling**
   - Scope: delete or re-point `scripts/historical-upgrade-readiness.sh`,
     `cargo-upgrade-readiness.sh`, `apt-lifecycle-upgrade-check.sh`,
     `member-role-upgrade-check.sh`, `member-role-migration-report.sh`,
     `internal/repository/postgres_npm_metadata_upgrade_integration_test.go`,
     `release_upgrade_integration_test.go` (replace per Decision), the matching
     Makefile targets and help text, and docs that cite migration numbers
     (`docs/helm-replication-preview.md`, `docs/rubygems-hosted-preview.md`).
   - [ ] `make docs-check`, `make release-readiness-check` pass; no script
     references a deleted file.
   - Depends on: slice 4.
6. **Immutability and sequencing guard**
   - Scope: `scripts/migration-immutability-check.sh`, `Makefile` target
     `migration-check` wired into CI and `make preflight`, `CONTRIBUTING.md`
     "Database migrations" section.
   - [ ] Tests (bash fixtures) cover changed released file, duplicate prefix,
     gap, bad name.
   - [ ] Check is a no-op before the first tag and enforcing after.
   - Depends on: slice 4. Must land before the `v0.1.0` tag.
7. **Re-squash before tag**
   - Scope: `migrations/` (replace `000001_baseline.sql` and `000002+` with a
     fresh `000001_baseline.sql`), tests naming migration files, deletion of
     `scripts/baseline-generate.sh`, `scripts/baseline-equivalence-check.sh`,
     `.github/workflows/baseline-equivalence.yml` and `internal/tools/baselinefmt`
     if no longer needed.
   - [ ] Generator run against the current `migrations/` chain; equivalence proof
     passes on both architectures; output attached to the PR.
   - [ ] `make test`, `make integration-test`, `make backup-drill`,
     `make restore-drill` pass on a fresh database.
   - [ ] Ledger has one row; `make migration-check` passes.
   - Depends on: ADR 0015 slices 4 and 6, and ADR 0013 remaining slices plus
     ADRs 0014, 0016 and 0017 and ADR 0023 slices 1-10 (all foundation work merged). It is the last
     change before the `v0.1.0` tag.

## Open questions

1. Which backends (ADR 0017) lack `pg_trgm`, if any? The baseline requires it;
   a backend without it would need the trigram indexes moved into a guarded
   block with plain B-tree fallbacks, which is not designed here.
