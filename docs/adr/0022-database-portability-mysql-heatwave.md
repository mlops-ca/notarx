# Database Portability: MySQL 8.4 (HeatWave) as Second Backend

[Documentation index](../README.md)

Status: accepted (2026-11-17)

See also: [ADR 0026](0026-debian-and-rpm-repositories.md), [ADR 0027](0027-hugging-face-repositories.md), [ADR 0028](0028-generic-versioned-packages.md), [ADR 0029](0029-repository-visibility-public-private-feeds.md), [ADR 0030](0030-sbom-generation-and-vulnerability-scanning.md).

## Context

ADR 0017 declares PostgreSQL 16 the only database and runs a self-hosted
PostgreSQL on the Oracle Cloud Always Free A1 VM. The Always Free tier also
offers managed databases, and none of them is PostgreSQL: MySQL HeatWave, Autonomous
Database and NoSQL Database. A deployment that wants a managed, backed-up
database at no cost therefore cannot use notarx today. Facts verified on
docs.oracle.com on 2026-11-17:

- MySQL HeatWave Always Free: one standalone DB system (single-node HeatWave
  cluster), home region only, 50 GB storage, shape `MySQL.Free` (1 ECPU, 8 GB RAM), automatic backups with
  1-day retention, no point-in-time recovery and a plan that cannot be changed,
  no high availability, private subnet only (no public endpoint), administrator
  account with fewer privileges than `root`. Oracle's own sample shows MySQL 9.0.1,
  so the server version is not guaranteed to be 8.4 LTS.
- Autonomous AI Database Always Free: two databases, 1 OCPU, 20 GB each, at most
  20 simultaneous sessions, Oracle 19c or 21c depending on region.
- NoSQL Database Always Free: 3 tables of 25 GB each.

**How deep the PostgreSQL binding goes** (grep over the tree at the time of
writing; counts are approximate, not a guarantee):

- `internal/repository/postgres_*.go`: 78 non-test files, 18,226 lines (25.6k
  with the 53 test files). Behind it, four `pgstore` packages:
  `internal/nexusmigration/pgstore` (5,675 lines), `internal/rubygemspublication/pgstore`
  (1,335), `internal/repositorypreset/pgstore` (297), `internal/nexussource/pgstore`
  (211). About 26k lines of SQL-bearing Go in total.
- Raw `database/sql` over pgx stdlib: `internal/database/postgres.go` opens four
  pools, 32 default, 8 coordinator, 4 artifact-lock and 2 notification
  (`DefaultPoolConfig`, `DefaultCoordinatorPoolConfig`, `DefaultArtifactLockPoolConfig`,
  `NotificationPoolConfig`), 46 connections in all. Pools are opened from
  `cmd/gateway/main.go`, `internal/repository/postgres.go`,
  `internal/cache/coordinator.go`, `internal/app/postgres_cache_task_queue.go` and
  `internal/app/postgres_cache_control_store.go`.
- Advisory locks in 28 Go files: transaction-scoped `pg_advisory_xact_lock`
  (npm, Maven, hosted, Conan, OCI discovery, Helm, NuGet and RubyGems
  replication, users, e-mail, quota, Nexus migration leases) and session-scoped
  locks held on a dedicated connection (`postgres_advisory_lock.go`, `postgres_raw.go`,
  `internal/cache/coordinator.go` via `pg_try_advisory_lock`, RubyGems publication).
- `LISTEN/NOTIFY`: `internal/database/notification.go` reaches into pgx with
  `WaitForNotification`; `internal/app/postgres_cache_task_queue.go` already
  documents a low-frequency poll as the recovery path; triggers from
  `000066`, `000067` and `000068` emit notifications.
- `RETURNING` in 50 files, `ON CONFLICT` in 45 (74 `DO NOTHING` and 36
  `DO UPDATE` statements), `jsonb` in 29, `FOR UPDATE SKIP LOCKED` claims across the
  queues, `DISTINCT ON` in 3, `ILIKE` in 4, `FILTER (WHERE ...)` in 6, about 230
  `now()` and 150 `clock_timestamp()` uses, about 930 `::` casts, numbered
  placeholders (`$1`) reused within one statement.
- Schema (173 migrations, one baseline after ADR 0015): about 165 `UUID`
  columns, 20 `TEXT[]`, 8 `BYTEA`, 29 partial indexes, 14 triggers and PL/pgSQL
  functions in 5 files (capacity enforcement in `000046` evaluates a CASE over
  per-format asset tables and raises `P0001`), `pg_trgm` GIN indexes
  (`000064`, `000145`), about 29 `TEXT` primary or unique keys.
- `scripts/run-migrations.sh` is a `psql` loop with a SHA-256 ledger; `internal/backupops/postgres.go`
  shells out to `pg_dump --format=custom` and `pg_restore`; the restore sandbox in
  `internal/backupops/docker.go` runs a `postgres` image.
- The seam exists but is shallow. `internal/repository/store.go` defines the
  `*Store` interfaces, memory implementations exist as contract fixtures
  (ADR 0010: one suite function per store, run against memory and, under
  `-tags integration`, PostgreSQL). Postgres tests need `TEST_DATABASE_URL`
  (`internal/repository/integration_main_test.go`).

ADR 0017 rejected "Add Oracle Autonomous Database or MySQL" as a rewrite and set
revisit criteria (funded user population, provably equivalent lock/queue/notify
semantics, a named maintainer). The Oracle free-tier constraint is the funded
population: it is the reference deployment of this fork. This ADR supplies the
design that criterion (b) asks for.

## Considered designs

### Which managed database

1. **PostgreSQL only.** Free-tier users self-host PostgreSQL on the A1 VM (ADR 0017
   variant A). Works, but gives up the managed offering that the free tier
   provides, and backups remain the operator's problem.
2. **MySQL 8.4 LTS, MySQL HeatWave compatible.** Closest feature match among
   the three: InnoDB row locks, `FOR UPDATE SKIP LOCKED` and `NOWAIT`, CTEs and
   window functions, native `JSON`, triggers, `GET_LOCK`. A single 50 GB node
   comfortably holds a control plane whose bytes live in object storage.
3. **Oracle Autonomous Database.** Rejected for now. (a) 20 simultaneous
   sessions against 46 pooled connections today, plus session-held locks and a
   listener that each pin a session; shrinking pools to fit changes concurrency
   behaviour and leaves almost no headroom for migrations, backups and operators.
   (b) Dialect: `''` is `NULL` (000046 itself compares `a.package_id=''`, and
   empty-string sentinels are a pattern in the schema), no `TEXT` that can be
   indexed or compared (CLOB), no `BOOLEAN` in 19c, `ON CONFLICT` becomes `MERGE`
   and `RETURNING` becomes `RETURNING INTO` with output binds, which the generic
   `database/sql` call sites cannot express, no `ILIKE`, no array type,
   `DBMS_LOCK` privileges restricted in Autonomous, `LISTEN/NOTIFY` becomes Advanced
   Queuing. (c) Drivers: `godror` needs CGO and Instant Client, which conflicts with
   the pure-Go native builds of ADR 0016; the pure-Go `go-ora` has a smaller user
   base. (d) 19c or 21c depending on region makes the target a moving matrix.
   Revisit only if the session cap rises above the pool budget and a maintainer
   volunteers; the abstraction below does not preclude a third dialect.
4. **OCI NoSQL Database.** Rejected. Three 25 GB tables, key-value access, no
   joins, no multi-table transactions: the control plane relies on joins,
   unique constraints, row locks and multi-statement transactions throughout.

### How to abstract the dialect

A. **sqlc per dialect.** Typed code generation, supports PostgreSQL and MySQL.
   Rejected: it would be a rewrite of 26k lines into `.sql` query files for the
   PostgreSQL path too, and the PostgreSQL queries that use dynamic predicates
   and reused numbered placeholders do not map cleanly; the PostgreSQL path
   would stop being intact.
B. **Query builder (goqu, squirrel).** Rejected. Builders hide differences only
   for CRUD; locking clauses, upserts, JSON and windowed queries still need
   per-dialect strings, and converting the PostgreSQL path risks regressions in
   the highest-value code with no gain for existing users.
C. **ORM (gorm, bun, ent).** Rejected. Different transaction and error
   semantics, generated SQL that is harder to review against locking
   requirements, and a full rewrite of the PostgreSQL path.
D. **Runtime SQL translation** (rewrite `$n` and `::` on the fly). Rejected.
   Regex translation of `ON CONFLICT`, `RETURNING` and arrays is unsound; failures
   would appear in production rather than in review.
E. **Per-dialect store implementations behind the existing `*Store` interfaces,
   plus a small `internal/dialect` package for the four non-SQL primitives
   (locks, claims, notifications, error classification).** The PostgreSQL files
   are not touched; the interfaces and the contract suites from ADR 0010 define
   behaviour; each MySQL store is hand-written MySQL SQL that a reviewer can
   read. Cost: about 18k lines of parallel code and a permanent obligation to
   implement every new store method twice. Mitigated by the contract suite
   failing for any dialect missing a method.

Choose E.

## Decision

PostgreSQL 16 stays the reference backend: it receives new features first, is
the default, and nothing in this ADR changes its code path except at the
composition root. **MySQL 8.4 LTS** is added as the second supported backend and
is the managed-database option for the Oracle free tier (MySQL HeatWave Always
Free). Oracle Autonomous Database and OCI NoSQL are not supported. A third
dialect is out of scope.

### Support claim and gating

Product documentation may say "MySQL supported" only when every store group in
the table under "Phasing" passes the full contract and integration suites against
`mysql:8.4` and against a live HeatWave Always Free instance (slice 11). Until then
`NOTARX_DATABASE_DRIVER=mysql` is refused at start-up unless
`NOTARX_EXPERIMENTAL_MYSQL=true` is set; with it set the composition root lists
the store ports that have no MySQL implementation and refuses to start if any
is missing, so a partial backend never serves traffic. Cross-dialect data
migration (PostgreSQL to MySQL) is not provided; an installation picks a dialect
at creation. Backups are dialect-specific.

### Configuration

| Key | Default | Meaning |
| --- | --- | --- |
| `NOTARX_DATABASE_DRIVER` | `postgres` | `postgres` or `mysql`; anything else is a start-up error. |
| `NOTARX_DATABASE_URL` | none | PostgreSQL: unchanged. MySQL: `mysql://user:password@host:3306/notarx?tls=verify-full`, parsed by notarx and converted to a `go-sql-driver/mysql` config; the driver DSN form is not accepted so credentials cannot leak through odd quoting. |
| `NOTARX_DATABASE_TLS_CA_FILE` | system roots | CA bundle for the MySQL server certificate; `tls=` values are `required` (no verification, warns), `verify-ca` (custom `VerifyPeerCertificate`, since Go's `crypto/tls` has no CA-only mode), `verify-full` (default when the host is not loopback). They are notarx's own names, mapped to `RegisterTLSConfig` entries of `go-sql-driver/mysql`. |
| `NOTARX_DATABASE_MAX_CONNECTIONS` | derived | Total budget across the four pools; split in the 32:8:4 ratio for MySQL (the notification pool is not opened, since there is no LISTEN), minimum 1 per pool. Unset keeps today's 46 on PostgreSQL and 44 on MySQL. The free-tier profile (ADR 0017 slice 6) sets a value taken from the HeatWave `max_connections` measured in slice 11, and ships a conservative value until then; the start-up check compares `@@max_connections` with the pool budget and fails loudly rather than guessing. |
| `NOTARX_DATABASE_POLL_INTERVAL` | `500ms` | MySQL queue and notification polling cadence (see "Locks, queues and notifications"). |
| `NOTARX_DATABASE_MIGRATE_URL` | none | Optional separate MySQL URL with DDL rights for the migration runner only; when set, the serving URL needs DML only. |
| `NOTARX_EXPERIMENTAL_MYSQL` | `false` | Gate described above; removed by slice 11. |

Connection parameters set by notarx, not by the operator: `parseTime=true`,
`loc=UTC`, `charset=utf8mb4`, `collation=utf8mb4_0900_bin`, `clientFoundRows=false`
(so `RowsAffected` is 1 for insert, 0 for a no-op upsert), `multiStatements=false`,
and session variables `time_zone='+00:00'`, `transaction_isolation='READ-COMMITTED'`,
`sql_mode='STRICT_ALL_TABLES,NO_ZERO_DATE,NO_ZERO_IN_DATE,ERROR_FOR_DIVISION_BY_ZERO,ONLY_FULL_GROUP_BY,NO_ENGINE_SUBSTITUTION'`,
`innodb_lock_wait_timeout` bounded to the request deadline (default 30s). READ
COMMITTED is explicit because InnoDB defaults to REPEATABLE READ, whose gap
locks would turn upsert and queue-claim patterns into deadlocks; it also matches
PostgreSQL's default. Startup checks `SELECT VERSION()` (8.4 LTS, or a later 9.x innovation release only after the full suite has passed on it; 8.0 is
unsupported because 8.0 reached end of life in April 2026; the free tier may run a 9.x release, which slice 11 records), `@@lower_case_table_names`
(all identifiers are lower-case snake case, so the value does not matter) and
`@@max_connections` against the pool budget, and fails with a message naming the
offending value (preflight, `internal/preflight/preflight.go`).

### Package layout

- `internal/database`: `Open(driver, url, PoolConfig)` returns `*sql.DB` for
  either driver; PostgreSQL behaviour unchanged. `pgx` imports stay confined to
  PostgreSQL files; the MySQL driver is `github.com/go-sql-driver/mysql` (MPL-2.0,
  file-level copyleft, acceptable as an unmodified dependency; recorded in
  `dependency-audit`).
- `internal/dialect`: interfaces `Locker`, `Claimer`, `Notifier` and
  `ErrorClassifier` (unique violation, foreign-key violation, deadlock,
  serialization failure, lock timeout mapped to the existing sentinel errors in
  `internal/repository/store.go`), with `postgres` and `mysql` implementations.
- `internal/repository/mysql_*.go`: `MySQLStore`, same package as the PostgreSQL
  store so it can reuse unexported value types, cursor codecs and validation;
  files are named `mysql_<domain>.go` mirroring `postgres_<domain>.go`. The
  `pgstore` packages get a sibling `mysqlstore`. Shared non-SQL logic moves to
  helpers by extraction in the same PR that needs it; no PostgreSQL SQL moves.
- Composition (`cmd/gateway`, to be `cmd/notarx` by ADR 0013) selects the store
  set once, from `NOTARX_DATABASE_DRIVER`. Handlers see only the interfaces.

### Feature mapping

| PostgreSQL feature (where) | MySQL 8.4 design |
| --- | --- |
| `pg_advisory_xact_lock(hashtextextended(k,0))` (npm, Maven, hosted, Conan, OCI discovery, replication creates, users, quota, e-mail, Nexus leases) | Transaction-scoped lock table `notarx_lock_buckets(bucket SMALLINT UNSIGNED PRIMARY KEY)`, 4096 rows seeded by the baseline. `Locker.LockTx(tx, keys ...string)` is variadic: it computes `bucket = fnv64(key) mod 4096` per key, de-duplicates, sorts ascending and runs `SELECT bucket FROM notarx_lock_buckets WHERE bucket=? FOR UPDATE` for each; released at commit or rollback exactly like `xact` locks. Ordering is only guaranteed within one call, so a call site that needs several keys must pass them together; a second `LockTx` in the same transaction can still deadlock and is retried. Bucket locks are for request-length critical sections only; anything held for minutes (Nexus migration leases, publication) uses `GET_LOCK` or a lease row, because a waiter would hit `innodb_lock_wait_timeout`. With 4096 buckets instead of a 64-bit hash, unrelated keys collide more often; slice 4 measures contention. Numeric two-key locks (`15112,229`) map to named keys. |
| `pg_advisory_lock` / `pg_try_advisory_lock` on a dedicated connection (`postgres_advisory_lock.go`, `postgres_raw.go`, `cache/coordinator.go`, RubyGems publication) | `GET_LOCK(name, timeout)` and `RELEASE_LOCK` on the same dedicated `sql.Conn`: session-scoped like PostgreSQL's, released if the connection dies, multiple locks per session allowed in 8.x. Lock names are server-wide, not per database, so the name is `nx:` plus the first 40 hex characters of SHA-256 of `database name + key` (limit is 64 characters). `COMMIT` does not release them; before a connection returns to the pool the code calls `RELEASE_ALL_LOCKS()` or discards the connection. Cancelling a waiter closes the driver connection, which releases the lock. `pg_try_` maps to timeout 0. Return value must be checked (1 acquired, 0 timeout, NULL error). Dedicated connections stay out of the pool as today and `wait_timeout` must exceed the lease ping interval; preflight checks it (the session value may be set at connect). |
| `LISTEN/NOTIFY` (`database/notification.go`, cache task queue, triggers 066-068) | `Notifier` implemented by polling the queue tables directly (the existing recovery poll becomes the only path); `NOTARX_DATABASE_POLL_INTERVAL` is the cadence while work is flowing; an idle poller backs off with jitter to 5s and resets on work. A same-process enqueue wakes the local poller through a channel, so single-node latency stays low; cross-replica latency is up to the backoff ceiling. Triggers 066-068 are not ported. No notification table: it would add a write per enqueue for latency polling already bounds. |
| `FOR UPDATE SKIP LOCKED` claims | Supported by InnoDB in 8.0+ with the same syntax; keep claims short, ordered by indexed columns, in READ COMMITTED. `Claimer` documents the contract (claim, lease expiry, requeue) so the suite tests it. |
| `INSERT ... ON CONFLICT (cols) DO NOTHING` (74) | `INSERT ... ON DUPLICATE KEY UPDATE pk=pk` on the table's key, never `INSERT IGNORE` (which also swallows foreign-key and truncation errors). Where a table has more than one unique key, a pre-check or a single-key design is documented in the PR, since `ON DUPLICATE KEY` fires on any unique key while `ON CONFLICT` names one. Even in READ COMMITTED, InnoDB takes shared locks on duplicate-key checks, so concurrent upserts can deadlock; the retry in `withTx` covers it. Created-or-existing is read from `RowsAffected` (1 vs 0). |
| `ON CONFLICT DO UPDATE` (36) | `ON DUPLICATE KEY UPDATE` with `VALUES()` replaced by a row alias (`AS new`), allowed in 8.0.19+; affected rows 1 insert, 2 update, 0 unchanged, which stores must not interpret numerically except through helpers. |
| `RETURNING` (50 files) | None in MySQL. Inserts use application-generated IDs (UUIDv7 in Go, per ADR 0023) and return the in-memory struct; updates run `SELECT ... FOR UPDATE` first then `UPDATE`, or `UPDATE` then `SELECT` in the same transaction. Sequences become `AUTO_INCREMENT` with `LastInsertId()`. |
| `UUID`, `gen_random_uuid()` | `CHAR(36) CHARACTER SET ascii COLLATE ascii_bin`; matches the string IDs already used by Go code and keeps queries readable. IDs are normalized to lower case in Go and generated as UUIDv7 on MySQL (random v4 keys fragment the clustered primary index; nothing may rely on ID order). `BINARY(16)` is a size optimization to revisit if the performance smoke (slice 10) shows index bloat. |
| `timestamptz`, `now()`, `clock_timestamp()` | `DATETIME(6)` in UTC (session `time_zone='+00:00'`). `now()` maps to `NOW(6)` (statement start, differs from PostgreSQL transaction start, acceptable as stores do not rely on it across statements), `clock_timestamp()` to `SYSDATE(6)`. New code passes the time from the Go clock. `DATETIME` has no zone: values outside the year range 1000-9999 are rejected by the store. |
| `jsonb` (29 files, 59 migration hits) | `JSON` (stored binary, keys sorted by length then bytes, duplicate keys last-wins as in 8.0.3 and later, like `jsonb`; numbers and key order in output differ from `jsonb`, so stores must not compare serialized text). Operators `->>` and `@>` become `JSON_UNQUOTE(JSON_EXTRACT())` and `JSON_CONTAINS`; stores query JSON only through helpers in `internal/dialect` tested on both engines. Indexes on JSON paths use generated columns. |
| `TEXT[]` (20 columns) | If no query filters by element (`ANY`, `&&`, `@>`, `unnest`), a `JSON` array; otherwise a child table. Slice 3 classifies each column. |
| `BYTEA` | `VARBINARY` or `BLOB`; digests as `BINARY(32)` where keyed. |
| Case sensitivity | PostgreSQL text is case-sensitive; MySQL's default `utf8mb4_0900_ai_ci` is not, which would make `Foo` and `foo` collide in unique keys and silently change auth lookups. The baseline sets database and table collation to `utf8mb4_0900_bin` (NO PAD, so trailing spaces are significant). Case-insensitive behaviour is explicit: `lower()` unique indexes become a stored or virtual `LOWER(col)` generated column under the bin collation. `utf8mb4_0900_ai_ci` is not used for identity (it is also accent-insensitive, so `e` equals `é`, which `lower()` is not); it is allowed only for display search columns on the parity allow-list. |
| Max index key 3072 bytes (InnoDB DYNAMIC) | `utf8mb4` is up to 4 bytes per character: an indexed `VARCHAR` is at most 768 characters alone, less in composites with 36-byte IDs. About 29 `TEXT` keys (paths, coordinates, references, names) get either a bounded `VARCHAR(n)` with a length validated by the API layer, or a stored `SHA-256` hash column `BINARY(32)` with the unique index on it and the full value in `TEXT`. Slice 3 produces the per-column decision list. |
| Partial and unique-where indexes (29) | Unique: a virtual generated column that is `NULL` outside the predicate plus a unique index (NULLs do not collide). Non-unique: a full index when cheap, otherwise dropped with the query reviewed. ADR 0023 section 5 specifies which partial indexes need a generated-column index on MySQL (queue claim `(kind, next_attempt_at)` on a generated `actionable` flag, `promotion_evaluations`, `artifact_promotions` pending states, trigram-replacement prefix indexes on live rows); the generated column is part of the index specification, not a later port step. |
| `pg_trgm` GIN (`000064`, `000145`) for substring search | No trigram indexes. FULLTEXT with the ngram parser depends on `ngram_token_size`, a read-only server start-up option that is not among the user-configurable HeatWave variables (to be confirmed in slice 11), so it is not relied on. Searches fall back to bounded `LIKE '%q%'` on top of ordered keyset pagination with a hard row-scan limit and a documented degradation (prefix and exact search stay indexed). Slice 10 measures it and records a catalogue size above which operators should use PostgreSQL. |
| Triggers and PL/pgSQL: capacity enforcement (`000046`), notify (`066-068`), `000076`, `000080`, `000081`, `000090`, `000110`, `000126`, `000131`, `000133` | Notify triggers are dropped (polling). Capacity: superseded by ADR 0023 section 3. Both engines drop the O(N) `SUM` triggers and use `capacity.Reserve(tx, repositoryID, deltaBytes, deltaObjects)`, a conditional `UPDATE` of the `repository_usage_counters` row (no trigger, no `FOR UPDATE` scan, no `SUM`), plus a periodic reconciler; the MySQL form is the same statement. Every other trigger is classified in slice 3: ported to application code by default, kept as a MySQL trigger with `SIGNAL SQLSTATE '45000'` only where it guards an invariant that raw SQL writers could violate. |
| `DISTINCT ON`, `FILTER (WHERE)`, `array_agg`, `string_agg` | `ROW_NUMBER() OVER`, `SUM(CASE ...)`, `JSON_ARRAYAGG`, `GROUP_CONCAT` (with `group_concat_max_len` raised by session). CTEs are supported unchanged. |
| `$n` reused placeholders, `::` casts | MySQL SQL is written by hand with positional `?`; reused values are passed twice or bound to session variables only inside one statement; no casts. |
| Transactional DDL | None in MySQL: every DDL statement implicitly commits. See "Migrations". |
| Deadlocks (1213) and lock wait timeout (1205) | `ErrorClassifier` marks both retryable. 1213 rolls back the whole transaction; 1205 by default rolls back only the failing statement (`innodb_rollback_on_timeout=OFF`), so the helper must roll back explicitly before retrying. `withTx` retries up to three times with jitter and only transaction closures that are safe to re-run, a PostgreSQL `40001`/`40P01` equivalent that the PostgreSQL path already needs. Lock order discipline and READ COMMITTED keep it rare. |
| `pg_stat_statements`, `information_schema`/`pg_class` catalog queries (17 uses; `backupops`, preflight) | Per-dialect catalog queries behind `dialect` functions; no Go code reads `pg_stat_statements`. |

### Scalability profile (ADR 0023 applies)

The decisions of ADR 0023 are binding on the MySQL backend where marked there: no
partitions (`resolver_audit_log` and `domain_events` are plain tables pruned by
primary-key-range batched deletes from the RetentionJanitor), the asynchronous
audit writer and usage aggregation (multi-row `INSERT` and `ON DUPLICATE KEY
UPDATE`), hourly request rollups, counters instead of capacity triggers,
UUIDv7 ids, polling `cache_epochs` for cache invalidation, lease rows instead of
`GET_LOCK` for long operations, and keyset pagination. The free-tier retention
profile (`NOTARX_RETENTION_PROFILE=freetier`: raw audit 7 d, domain events 3 d,
terminal jobs 1 d) is the default for variant C; it is what keeps the 50 GB
budget (data, indexes, binary logs) from filling, and raises a storage alert at about 70% of the fixed 50 GB (data, indexes and
logs); slice 10 measures actual growth and tunes the defaults, and operators may
raise the retention.

### Migrations

- Layout: `migrations/postgres/000001_baseline.sql` (the ADR 0015 file, moved) and
  `migrations/mysql/000001_baseline.sql`; the dialect directory is chosen by
  `NOTARX_DATABASE_DRIVER`. ADR 0015's generator emits both; the MySQL file is
  produced from a dialect-neutral schema description (YAML catalogue of tables,
  columns, logical types, keys, generated columns, seeds) plus hand-written
  per-dialect fragments for what has no neutral form. Naming stays
  `NNNNNN_name.sql`, so `migrationName` in `internal/backupmanifest/verify.go` and
  `migrationFilename` in `internal/backupops/release.go` do not change; the
  manifest records the dialect.
- Runner: a Go runner (`internal/migrate`) replaces the `psql` loop for MySQL,
  because the image used on OCI should not need a `psql`/`mysql` client. It reads
  the same ledger on PostgreSQL, `notarx_schema_migrations(filename, checksum, applied_at)` (unchanged); the MySQL ledger adds `state` and `statements_done` columns,
  applies statements split by a MySQL-aware splitter (no `DELIMITER`; routines are
  avoided), aborts on a changed checksum and takes `GET_LOCK` on a database-qualified name (`notarx-migrate:<db>`)
  so two replicas do not race. The PostgreSQL path keeps `scripts/run-migrations.sh`
  until the Go runner has run in parity mode against PostgreSQL for one release
  (`scripts/migration-runner-check.sh` with a byte-identical ledger on every
  release); after that release it replaces `run-migrations.sh` and the four ADR 0015
  manifests are updated in a follow-up change, not in this ADR's slices.
- No transactional DDL: before each file the runner inserts a ledger row in state
  `applying` and sets it to `applied` afterwards. A crash leaves `applying`, and start-up
  refuses to run until `notarx migrate --repair` shows the statement offset and the
  operator re-creates the database from backup or empty (acceptable for a baseline
  fresh start; a post-baseline migration must be written to be re-runnable with
  guards, enforced by a lint in slice 2). MySQL has no `ADD COLUMN IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS` or `DROP COLUMN IF EXISTS` (MariaDB only), so guards are not available. Instead the MySQL ledger also records `statements_done`; `--repair` resumes from that offset after the operator confirms the last statement's effect, and the lint requires one DDL statement per `;`-separated unit and idempotent data backfills.
- Schema parity: the PostgreSQL baseline is a normalized `pg_dump` (ADR 0015), not generated from the catalogue, so slice 3 first introspects a migrated PostgreSQL via `information_schema` to produce the checked-in neutral catalogue, and the MySQL baseline is generated from it. A check (`make schema-parity`) then compares that catalogue with
  `information_schema` of a migrated PostgreSQL and a migrated MySQL, through an explicit logical-type mapping table (tables,
  columns, nullability, logical types, key and unique sets, generated-column
  equivalents) and fails on drift; it runs in `make lint`'s CI job set. After
  the ADR 0015 cut-over, every `000002+` migration ships in both directories or
  names the dialect it applies to and why; `make test` fails otherwise. The ADR 0015
  re-squash before `v0.1.0` is unaffected; the MySQL baseline is generated after it (slice 3 depends on ADR 0015 slice 7).

### Backup and restore

`internal/backupops/postgres.go` and `docker.go` become a `Dump`/`Restore` pair
per dialect. MySQL uses `mysqldump --single-transaction --routines --triggers
--set-gtid-purged=OFF --no-tablespaces` (the HeatWave administrator is assumed to lack `PROCESS`, and no code may depend on `SUPER`-class, `PROCESS` or `LOCK TABLES` privileges; slice 11 records the actual grants). Dumps and restores must not depend on `DEFINER` privileges: triggers and routines are created without `SUPER`/`SET_USER_ID`-class rights, and the runner strips `DEFINER` clauses on restore
streamed through the existing manifest, checksum and object-reference
verification; the restore sandbox runs a `mysql:8.4` container with no network,
as the PostgreSQL one does. The backup manifest and `backup-drill`/`restore-drill`
gain a `dialect` field and refuse a cross-dialect restore. HeatWave's automatic
backups (1-day retention, no point-in-time recovery on the free tier) are a weak second layer for the database only; the notarx
backup stays authoritative because it carries the object references that tie the
control plane to the byte plane (`backup-restore-readiness`).

### OCI free-tier variant (extends ADR 0017)

Variant C: the A1 VM runs notarx, Caddy and the console; the database is a
HeatWave Always Free DB system with a private endpoint in the same VCN, reachable
only from inside the VCN (security list or network security group allows TCP 3306 from the VM address; the DB system is placed in an availability domain Oracle chooses, which may differ from the VM's, and that is fine inside one VCN). Operators reach it through the VM (SSH tunnel), never directly. No
PostgreSQL container and no database volume, which frees about 3 GB of the VM's
12 GB budget. The HeatWave analytics cluster stays off. Standalone, so database
maintenance windows cause downtime that variant A does not have; the runbook says
so. The service keeps one region (home region), which constrains object storage
and VM region choice.

## Security considerations

- TLS to HeatWave is mandatory in the free-tier profile (`verify-full` with the OCI
  CA bundle); `required` without verification is allowed only with an explicit
  warning, never for non-loopback hosts in the default profile.
- Credentials are parsed from `NOTARX_DATABASE_URL` and redacted in logs, preflight
  output and the backup manifest. DDL rights belong to the runner's optional
  `NOTARX_DATABASE_MIGRATE_URL`; the serving user then needs DML only.
- Case-collation mistakes are a security issue (token, user name and path
  identity lookups): the bin-collation baseline is covered by a contract test that
  inserts `Admin` and `admin`, and the parity check fails on any column outside
  the documented allow-list of `ai_ci` columns.
- `sql_mode` is strict so truncation cannot silently shorten digests or keys.
- App-side capacity enforcement can be bypassed by direct SQL; the database user
  is the only writer, and the restriction is stated in the operations guide.
- Lost session locks: a connection that dies releases `GET_LOCK` locks like
  PostgreSQL; the existing lease code treats a failed ping as lost ownership, and
  the contract suite kills a connection mid-lease to prove it.

## Consequences

- Positive: a managed, free database for the reference deployment; a clean
  dialect seam with the contract suites as the arbiter; the PostgreSQL path is
  untouched and shipped independently of the MySQL schedule.
- Cost: about 18k lines of non-test PostgreSQL store code plus 7.5k lines in the
  `pgstore` packages gain MySQL twins, roughly 26k lines hand-ported, with
  `RETURNING` expansion adding lines and shared validation removing some. Every new store method is written twice. CI time and cost roughly
  double for the integration job.
- Behavioural differences that remain visible: substring search is slower at
  scale, notification latency is the poll interval (default 500 ms, up to 5 s when idle across replicas), no
  transactional DDL, MySQL support is single-node HeatWave without HA on the free tier,
  with 1-day backup retention there, so the notarx backup is mandatory.
- Effort, honestly: about 28 PRs of the sizes below. With autonomous agents and
  one maintainer reviewing, two to four months elapsed; the long poles are the
  Nexus migration store (5.7k lines), the lock and queue contract, and live HeatWave
  verification. Calling the backend supported before slice 11 would be wrong.
- ADR 0017's "Absent these, MySQL requests are declined" and its option 3 rejection
  are superseded for MySQL only; Oracle and SQLite stay declined.

## Implementation slices

Sizing rule for every store slice (6-9): one PR per package group of at most about 1.5k lines of ported SQL, so a slice below expands into several PRs (this is where the 28 PRs come from); an agent PR that exceeds it is split before review. Every slice runs `make test`, `make lint` (includes `architecture-check`) and
`make coverage` at the repository floors; the integration suite runs on both
dialects from slice 4 on. No slice changes `api/openapi` (storage is below the API
contract), so `make openapi-check` and `make console-test` must stay green
unchanged. New packages are registered in `architecture-check` in the slice that
creates them.

1. **Dialect seam and configuration.**
   Scope: `internal/config`, `internal/database` (`Open`, driver switch), new
   `internal/dialect` interfaces and PostgreSQL implementations, composition root,
   `internal/preflight`.
   - [ ] `NOTARX_DATABASE_DRIVER`, TLS, pool budget and gate keys parsed; unit tests for every error path and URL form
   - [ ] PostgreSQL behaviour byte-identical: existing integration suite passes unmodified
   - [ ] `make test`, `make lint`, `make coverage`
   Depends on: ADR 0013 slice 4 (env prefix), ADR 0017 slice 1.
2. **Go migration runner.**
   Scope: new `internal/migrate`, `cmd` subcommand, `scripts/migration-runner-check.sh`
   extension.
   - [ ] runs the PostgreSQL baseline with a ledger byte-identical to `run-migrations.sh`, wired as a parity check (not the deployed runner) that runs on every release for one release before unification is considered; `run-migrations.sh` stays the PostgreSQL runner in all four manifests
   - [ ] MySQL: `applying`/`applied` states, `--repair`, `GET_LOCK` exclusion tested with two concurrent runners
   - [ ] lint for re-runnable post-baseline MySQL migrations
   Depends on: slice 1, ADR 0015 slice 4.
3. **Neutral schema catalogue, MySQL baseline and parity check.**
   Split into PRs: (3a) move `migrations/` to `migrations/postgres/` with zero schema diff, updating `backupops/release.go`, scripts, Dockerfiles and the four manifests ADR 0015 lists; (3b) catalogue introspection and `make schema-parity`; (3c) MySQL baseline generator plus decision lists for arrays, long keys, partial indexes and each trigger.
   Scope: ADR 0015 generator, `migrations/postgres`, `migrations/mysql`.
   - [ ] MySQL baseline applies to an empty `mysql:8.4` and seeds the 4096 lock buckets and the singleton rows ADR 0015 lists
   - [ ] `make schema-parity` fails on a deliberately drifted column in a test
   - [ ] bin-collation contract test (`Admin` vs `admin`)
   Depends on: ADR 0015 slice 7 (re-squash), slice 2.
4. **Lock, claim and notification primitives.**
   Scope: `internal/dialect/mysql`, `internal/cache/coordinator.go` MySQL twin, `internal/database/notification.go` abstraction, `internal/app/postgres_cache_task_queue.go` use of `Notifier`.
   - [ ] shared `dialect` contract suite on both engines: mutual exclusion, bucket ordering without deadlock, context cancellation of waiters, lock released on connection kill, SKIP LOCKED claim and lease expiry, poll latency bound
   - [ ] race tests (`make race`) for the coordinator
   Depends on: slices 1, 3 and 5 (the dialect suite needs the MySQL harness, so slice 5 lands first).
5. **Test harness on both dialects.**
   Scope: `scripts/integration-test.sh`, `compose.integration.yml`, `.github/workflows/ci.yml`
   matrix (`mysql:8.4` on ubuntu-24.04 and ubuntu-24.04-arm per ADR 0016),
   `internal/testsupport`, contract suites parametrized by driver, per-package
   coverage reporting for dialect files.
   - [ ] `TEST_DATABASE_DRIVER` selects the engine; isolation checks accept a MySQL URL
   - [ ] CI job per dialect and architecture; MySQL job allowed to fail until slice 11 is merged, listed in the PR
   Depends on: slice 3, ADR 0016 slice 3.
6. **Store group A: foundation.** Hosted repositories, groups, settings, repository visibility (ADR 0029: `visibility` is a short text column with a `CHECK` and a plain composite index on `(visibility, name)`, so no partial-index mapping is needed and the neutral catalogue records it as is; this replaces the anonymous policy), themes, users, service accounts, API keys, sessions, audit, authorization templates. `mysql_*.go` twins and `mysqlstore` registration.
   - [ ] every store contract in this group runs and passes on MySQL; ported `RETURNING` sites listed in the PR
   - [ ] last-active-admin and idempotency-key tests under concurrency
   Depends on: slices 4 and 5.
7. **Store group B: raw, generic hosted, versioned generic packages, Maven, capacity.** The versioned-Raw stores of ADR 0028 (`native_raw_versions`, `native_raw_version_properties`) are included: `VARCHAR(128)` keys and a binary collation so the path range scan stays on the primary key. Capacity uses `capacity.Reserve` from ADR 0023 slice 8 (no `AssertRepositoryCapacity`).
   - [ ] raw contract (ADR 0010) passes; capacity quota test proves rejection under concurrent uploads
   Depends on: slice 6; ADR 0023 slice 8 (`capacity.Reserve`, pre-tag).
8. **Store group C: npm, PyPI, Go, Cargo, Conan, OCI plus cache control store.**
   - [ ] per-format contract and e2e (`make native-*-e2e`) pass with `NOTARX_DATABASE_DRIVER=mysql`
   Depends on: slice 7.
9. **Store group D: APT, RPM (`rpmpublication`, ADR 0026 slice 7), Helm, NuGet, RubyGems (`rubygemspublication/mysqlstore`), lifecycle, replication, webhooks, e-mail and notification queues, `repositorypreset`, `nexussource`, `nexusmigration/mysqlstore`.** One PR per package if over 1.5k lines.
   - [ ] each package's contract and integration tests pass; queue claim fairness test
   Depends on: slice 8. Stores added by ADRs 0018-0021 (identity providers, SCIM, issuer keys, promotion rules) and by ADRs 0026-0030 (`rpmpublication`, Hugging Face `hf_*`, scan and SBOM tables, `cdn_purge_requests`) join this list as they land; the start-up gate lists any missing port, so none can be forgotten.
10. **Backup, restore and performance.**
    Scope: `internal/backupops` dialect split, manifest `dialect` field, drills, performance smoke (`make performance-baseline` against both dialects: upload, resolve, list, substring search at 1k, 100k and 1M rows).
    - [ ] `make backup-drill` and `make restore-drill` pass on MySQL; cross-dialect restore refused
    - [ ] recorded numbers and the search degradation threshold committed to `docs/`
    - [ ] free-tier profile storage measured against the 50 GB budget (data, indexes, binary logs); a storage metric and alert at about 70% is defined and the `freetier` retention defaults are tuned from the result
    Depends on: slice 9, ADR 0017 slice 4 (backup on both object drivers).
11. **HeatWave verification, OCI variant C and the support claim.**
    Scope: `deploy/oci-free-tier` variant, runbook, `docs/postgresql-capabilities.md` neighbour doc for MySQL, remove `NOTARX_EXPERIMENTAL_MYSQL`.
    - [ ] live run of the full integration suite against a HeatWave Always Free instance; recorded `max_connections`, `sql_mode`, version, TLS CA, and the granted privileges (`PROCESS`, `SUPER`-class, `LOCK TABLES`); the free-tier profile pool budget is set from the measured `max_connections` and the 70% storage alert is verified
    - [ ] compose or OpenTofu variant starts with notarx only, passes `make evidence` subset
    - [ ] the gate lists zero missing stores; docs claim MySQL only now
    Depends on: slices 9 and 10, ADR 0017 slice 6.

## Resolved questions

- **Q1 — Real `max_connections` and privileges on HeatWave Always Free?** Decision: measure in slice 11, ship the start-up check of `@@max_connections` against the pool budget, keep the conservative default meanwhile, and never depend on `SUPER`-class, `PROCESS` or `LOCK TABLES`. Oracle publishes neither a connection limit nor a privilege matrix and restricts SUPER-class rights on the managed service.
- **Q2 — Should the Go runner replace `run-migrations.sh` for PostgreSQL?** Decision: keep the deferral; replace only after the Go runner has run in parity mode for one release. The change touches four manifests and the baseline is new, so stability beats convergence, and MySQL is not blocked.
- **Q3 — Is 50 GB enough for audit retention at the default policy?** Decision: ship the free-tier profile with short raw audit retention and a storage alert at about 70%, then tune with slice 10 measurements. Storage is fixed at 50 GB including logs, so a conservative default protects the instance.
