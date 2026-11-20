-- +goose Up

-- ADR 0023 section 8 (slice 14, continuation): the lifecycle job and webhook listings page newest first by
-- keyset on (created_at, id), so each listing is served by a btree in exactly that order and never sorts.
--
--   lifecycle_jobs      per repository (repository_id, created_at DESC, id DESC) and across repositories
--                       (created_at DESC, id DESC) for the console job table
--   webhook_deliveries  across subscriptions (created_at DESC, id DESC); the per-subscription index gains the id
--   webhook_subscriptions by name in byte order (COLLATE "C"), so the pages do not depend on the database
--                       collation and a token stays valid across a restore into another one
--
-- The migration runs as its own job before the application replicas start (docs/distributed-deployment.md,
-- deployment constraint 2), so the table locks of CREATE INDEX block no request, and the replaced
-- webhook_deliveries_subscription_idx is dropped and rebuilt in one transaction: no session sees the table
-- without an index. Before the first stable tag, index builds in this directory are plain CREATE INDEX statements.
--
-- MySQL (ADR 0022): the twin creates the same composite indexes (descending key parts are native in 8.4).

CREATE INDEX lifecycle_jobs_repository_created_idx ON public.lifecycle_jobs USING btree (repository_id, created_at DESC, id DESC);
CREATE INDEX lifecycle_jobs_created_idx ON public.lifecycle_jobs USING btree (created_at DESC, id DESC);
CREATE INDEX webhook_deliveries_created_idx ON public.webhook_deliveries USING btree (created_at DESC, id DESC);
DROP INDEX public.webhook_deliveries_subscription_idx;
CREATE INDEX webhook_deliveries_subscription_idx ON public.webhook_deliveries USING btree (subscription_id, created_at DESC, id DESC);
CREATE INDEX webhook_subscriptions_name_bytes_idx ON public.webhook_subscriptions USING btree (name COLLATE "C");
