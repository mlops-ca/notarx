-- +goose Up

-- ADR 0021 (slice 3a), "Rules": a promotion rule says which artifacts of a source repository are promoted, under which
-- conditions, to which target, and as which service account. The table holds the configuration only; evaluation
-- (promotion_evaluations), the chain graph check and the approval workflow belong to later slices.
--
-- The target is a local repository or a remote instance; the checks tie the columns to target_kind, so a local rule
-- never carries a remote instance and a remote rule never carries a local repository. match, conditions, approval and
-- schedule are JSON documents validated by internal/promotionrules (the scope of the rule is the "scope" member of match).
-- Every edit increments version; the audit log records the editor, which is also kept in updated_by.
--
-- A rule is meaningless without its repositories, so both repository keys cascade. A remote instance and a service
-- account that a rule still names cannot be deleted. The number of rules per source repository is capped by
-- NOTARX_PROMOTION_MAX_RULES_PER_REPOSITORY under an advisory lock of the repository (internal/repository).
--
-- Indexes (ADR 0023 section 5): the evaluator fan-out looks up the enabled rules of a source repository
-- (source_repository_id, enabled), which also serves the repository foreign key; the other three foreign keys carry a
-- leading index so a repository, instance or service account delete does not scan the table; the keyset listing
-- orders by name in byte order (COLLATE "C"), like remote_instances (migration 000023). A rule table is
-- configuration, small and rarely updated: no fillfactor or autovacuum tuning.
--
-- MySQL (ADR 0022): the twin ships with the generated baseline; the catalogue carries the table.
CREATE TABLE public.promotion_rules (
    id uuid NOT NULL,
    name text NOT NULL,
    source_repository_id uuid NOT NULL,
    target_kind text NOT NULL,
    target_repository_id uuid,
    remote_instance_id uuid,
    remote_repository text,
    match jsonb NOT NULL,
    conditions jsonb DEFAULT '{}'::jsonb NOT NULL,
    approval jsonb,
    schedule jsonb,
    run_as_service_account_id uuid NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_by text NOT NULL,
    updated_by text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT promotion_rules_pkey PRIMARY KEY (id),
    CONSTRAINT promotion_rules_name_key UNIQUE (name),
    CONSTRAINT promotion_rules_source_repository_id_fkey FOREIGN KEY (source_repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE,
    CONSTRAINT promotion_rules_target_repository_id_fkey FOREIGN KEY (target_repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE,
    CONSTRAINT promotion_rules_remote_instance_id_fkey FOREIGN KEY (remote_instance_id) REFERENCES public.remote_instances(id),
    CONSTRAINT promotion_rules_run_as_service_account_id_fkey FOREIGN KEY (run_as_service_account_id) REFERENCES public.service_accounts(id),
    CONSTRAINT promotion_rules_name_check CHECK (((length(name) >= 1) AND (length(name) <= 128))),
    CONSTRAINT promotion_rules_target_check CHECK ((((target_kind = 'local'::text) AND (target_repository_id IS NOT NULL) AND (remote_instance_id IS NULL) AND (remote_repository IS NULL) AND (target_repository_id <> source_repository_id)) OR ((target_kind = 'remote'::text) AND (target_repository_id IS NULL) AND (remote_instance_id IS NOT NULL) AND (remote_repository IS NOT NULL) AND (length(remote_repository) >= 1) AND (length(remote_repository) <= 255)))),
    CONSTRAINT promotion_rules_match_check CHECK ((jsonb_typeof(match) = 'object'::text)),
    CONSTRAINT promotion_rules_conditions_check CHECK ((jsonb_typeof(conditions) = 'object'::text)),
    CONSTRAINT promotion_rules_approval_check CHECK (((approval IS NULL) OR (jsonb_typeof(approval) = 'object'::text))),
    CONSTRAINT promotion_rules_schedule_check CHECK (((schedule IS NULL) OR (jsonb_typeof(schedule) = 'object'::text))),
    CONSTRAINT promotion_rules_version_check CHECK ((version >= 1)),
    CONSTRAINT promotion_rules_actor_check CHECK (((length(created_by) >= 1) AND (length(created_by) <= 255) AND (length(updated_by) >= 1) AND (length(updated_by) <= 255)))
);

CREATE INDEX promotion_rules_source_idx ON public.promotion_rules USING btree (source_repository_id, enabled);
CREATE INDEX promotion_rules_target_idx ON public.promotion_rules USING btree (target_repository_id);
CREATE INDEX promotion_rules_remote_instance_idx ON public.promotion_rules USING btree (remote_instance_id);
CREATE INDEX promotion_rules_run_as_idx ON public.promotion_rules USING btree (run_as_service_account_id);
CREATE INDEX promotion_rules_name_bytes_idx ON public.promotion_rules USING btree (name COLLATE "C");
