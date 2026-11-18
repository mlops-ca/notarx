-- +goose Up

-- ADR 0023 slice 7: default-on retention janitor (internal/retention).
--
-- The janitor elects one cluster-wide runner with a lease row in scheduled_tasks
-- (kind 'retention-janitor', never enabled, hidden from the task list API like the
-- partition manager's row). The audit retention policy now defaults to
-- enabled with 90 days, and the 1000 row cap on audit_cleanup_jobs.batch_size is
-- lifted so the legacy cleanup job can use the janitor batch size (2000).
-- Partial indexes serve the janitor's oldest-first terminal-row scans.
--
-- MySQL (ADR 0022): there is no migrations/mysql directory yet, so no twin is
-- added. The twin is a mechanical translation: the kind check, the policy default
-- and the batch_size check are plain DDL, and the partial indexes become indexes
-- on the time column (the janitor's MySQL form deletes in key order).

ALTER TABLE public.scheduled_tasks DROP CONSTRAINT scheduled_tasks_kind_check;
ALTER TABLE public.scheduled_tasks ADD CONSTRAINT scheduled_tasks_kind_check
    CHECK (kind = ANY (ARRAY['repository-retention'::text, 'audit-retention'::text, 'partition-manager'::text, 'retention-janitor'::text]));
ALTER TABLE public.scheduled_tasks DROP CONSTRAINT scheduled_tasks_check;
ALTER TABLE public.scheduled_tasks ADD CONSTRAINT scheduled_tasks_check
    CHECK ((kind = 'audit-retention' AND repository_id IS NULL)
        OR (kind = 'repository-retention' AND repository_id IS NOT NULL)
        OR (kind IN ('partition-manager', 'retention-janitor') AND repository_id IS NULL AND NOT enabled));

ALTER TABLE public.audit_retention_policy
    ALTER COLUMN enabled SET DEFAULT true,
    ALTER COLUMN keep_days SET DEFAULT 90;
-- A policy nobody has edited (version 1, still the old default) takes the new default.
UPDATE public.audit_retention_policy SET enabled = true, keep_days = 90
 WHERE version = 1 AND NOT enabled AND keep_days = 0;

ALTER TABLE public.audit_cleanup_jobs DROP CONSTRAINT audit_cleanup_jobs_batch_size_check;
ALTER TABLE public.audit_cleanup_jobs ADD CONSTRAINT audit_cleanup_jobs_batch_size_check CHECK (batch_size >= 1);

CREATE INDEX lifecycle_jobs_retention_idx ON public.lifecycle_jobs USING btree (completed_at)
    WHERE state IN ('completed', 'failed', 'cancelled');
CREATE INDEX webhook_deliveries_retention_idx ON public.webhook_deliveries USING btree (updated_at)
    WHERE state IN ('succeeded', 'dead');
CREATE INDEX cache_tasks_retention_idx ON public.cache_tasks USING btree (completed_at)
    WHERE completed_at IS NOT NULL;
CREATE INDEX audit_cleanup_jobs_retention_idx ON public.audit_cleanup_jobs USING btree (completed_at)
    WHERE state IN ('completed', 'failed');
CREATE INDEX scheduled_task_runs_retention_idx ON public.scheduled_task_runs USING btree (created_at);
CREATE INDEX repository_quota_alert_events_retention_idx ON public.repository_quota_alert_events USING btree (created_at);
CREATE INDEX replication_plans_retention_idx ON public.replication_plans USING btree (completed_at)
    WHERE state IN ('completed', 'failed', 'cancelled');
CREATE INDEX replication_checkpoints_retention_idx ON public.replication_checkpoints USING btree (updated_at);
CREATE INDEX runtime_node_sessions_retention_idx ON public.runtime_node_sessions USING btree (stopped_at)
    WHERE stopped_at IS NOT NULL;
CREATE INDEX email_test_deliveries_retention_idx ON public.email_test_deliveries USING btree (updated_at)
    WHERE state IN ('accepted', 'dead');
CREATE INDEX nexus_migration_entries_retention_idx ON public.nexus_migration_entries USING btree (updated_at);
CREATE INDEX repository_request_rollup_retention_idx ON public.repository_request_rollup USING btree (bucket_start);
CREATE INDEX hosted_group_idempotency_retention_idx ON public.hosted_group_idempotency USING btree (expires_at);
CREATE INDEX native_maven_commit_idempotency_retention_idx ON public.native_maven_commit_idempotency USING btree (expires_at);
