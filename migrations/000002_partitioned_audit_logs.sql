-- ADR 0023 slice 5: partitioned audit tables.
--
-- Pre-tag migration (ADR 0015, ADR 0023 binding constraints): databases are
-- disposable before v0.1.0, so resolver_audit_log is dropped and recreated as a
-- range-partitioned table WITHOUT copying data, and the ADR 0015 re-squash folds
-- this file into the baseline. After the first stable tag this shape changes only
-- through forward migrations.
--
-- resolver_audit_log receives async-class records (reads, downloads, group
-- resolution) and keeps 90 days by default; security_audit_log has the same
-- shape for sync-class records and keeps 730 days by default (ADR 0023 section 1
-- and 2). Both are partitioned monthly by occurred_at, with the partition key in
-- the primary key and no foreign key pointing into them. The internal/partitions
-- manager creates the monthly partitions and drops expired ones.
--
-- Indexes shrink from six to three: the primary key serves
-- ORDER BY occurred_at DESC, id DESC cursors and replaces the former occurred_at,
-- BRIN and (occurred_at, id) indexes; (repository, occurred_at DESC) and
-- (actor, occurred_at DESC) serve the equality filters. The group_name index is
-- not kept: group filters run as a residual on the time-bounded primary-key scan
-- (the list API requires a time window), which the explain assertions cover.
-- repository_id is the nullable surrogate identity beside the historical
-- repository name; it has no foreign key so audit survives repository deletion.
--
-- The partition manager elects one cluster-wide runner with a lease row in
-- scheduled_tasks (kind 'partition-manager', never enabled, so the scheduler never
-- claims it and the task list API hides it); lease_owner and lease_until are the
-- lease. No session advisory lock is used.
--
-- MySQL (ADR 0022): plain tables with an AUTO_INCREMENT key, no partitions, and no
-- lease row; there is no migrations/mysql directory yet, so the twin is written
-- with the ADR 0022 store slices.

ALTER TABLE public.scheduled_tasks
    ADD COLUMN lease_owner text NOT NULL DEFAULT '',
    ADD COLUMN lease_until timestamp with time zone;
ALTER TABLE public.scheduled_tasks DROP CONSTRAINT scheduled_tasks_kind_check;
ALTER TABLE public.scheduled_tasks ADD CONSTRAINT scheduled_tasks_kind_check
    CHECK (kind = ANY (ARRAY['repository-retention'::text, 'audit-retention'::text, 'partition-manager'::text]));
ALTER TABLE public.scheduled_tasks DROP CONSTRAINT scheduled_tasks_check;
ALTER TABLE public.scheduled_tasks ADD CONSTRAINT scheduled_tasks_check
    CHECK ((kind = 'audit-retention' AND repository_id IS NULL)
        OR (kind = 'repository-retention' AND repository_id IS NOT NULL)
        OR (kind = 'partition-manager' AND repository_id IS NULL AND NOT enabled));

DROP TABLE public.resolver_audit_log;

CREATE SEQUENCE public.resolver_audit_log_id_seq;

CREATE TABLE public.resolver_audit_log (
    id bigint NOT NULL DEFAULT nextval('public.resolver_audit_log_id_seq'),
    group_name text NOT NULL,
    repository text NOT NULL,
    member_name text,
    outcome text NOT NULL,
    actor text NOT NULL,
    occurred_at timestamp with time zone NOT NULL DEFAULT now(),
    format text,
    resource text,
    representation text,
    member_type text,
    upstream_host text,
    operation text,
    http_status integer,
    cache_disposition text,
    bytes bigint,
    request_id text,
    trace_id text,
    authorization_source text,
    authorization_reason text,
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    repository_id uuid,
    CONSTRAINT resolver_audit_log_evidence_check CHECK (jsonb_typeof(evidence) = 'object'::text),
    CONSTRAINT resolver_audit_log_pkey PRIMARY KEY (occurred_at, id)
) PARTITION BY RANGE (occurred_at);

ALTER SEQUENCE public.resolver_audit_log_id_seq OWNED BY public.resolver_audit_log.id;

-- Rows outside every manager-created partition land here so an outage of the
-- partition manager cannot make an audit insert fail. The manager keeps it empty
-- and notarx_partition_default_rows alerts when it is not.
CREATE TABLE public.resolver_audit_log_default PARTITION OF public.resolver_audit_log DEFAULT;

CREATE INDEX resolver_audit_log_repository_occurred_at_idx ON public.resolver_audit_log USING btree (repository, occurred_at DESC);
CREATE INDEX resolver_audit_log_actor_occurred_at_idx ON public.resolver_audit_log USING btree (actor, occurred_at DESC);

CREATE SEQUENCE public.security_audit_log_id_seq;

CREATE TABLE public.security_audit_log (
    id bigint NOT NULL DEFAULT nextval('public.security_audit_log_id_seq'),
    group_name text NOT NULL,
    repository text NOT NULL,
    member_name text,
    outcome text NOT NULL,
    actor text NOT NULL,
    occurred_at timestamp with time zone NOT NULL DEFAULT now(),
    format text,
    resource text,
    representation text,
    member_type text,
    upstream_host text,
    operation text,
    http_status integer,
    cache_disposition text,
    bytes bigint,
    request_id text,
    trace_id text,
    authorization_source text,
    authorization_reason text,
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    repository_id uuid,
    CONSTRAINT security_audit_log_evidence_check CHECK (jsonb_typeof(evidence) = 'object'::text),
    CONSTRAINT security_audit_log_pkey PRIMARY KEY (occurred_at, id)
) PARTITION BY RANGE (occurred_at);

ALTER SEQUENCE public.security_audit_log_id_seq OWNED BY public.security_audit_log.id;

-- Rows outside every manager-created partition land here so an outage of the
-- partition manager cannot make an audit insert fail. The manager keeps it empty
-- and notarx_partition_default_rows alerts when it is not.
CREATE TABLE public.security_audit_log_default PARTITION OF public.security_audit_log DEFAULT;

CREATE INDEX security_audit_log_repository_occurred_at_idx ON public.security_audit_log USING btree (repository, occurred_at DESC);
CREATE INDEX security_audit_log_actor_occurred_at_idx ON public.security_audit_log USING btree (actor, occurred_at DESC);
