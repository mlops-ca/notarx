-- +goose Up

-- ADR 0023 slice 6: batched audit writer, usage aggregation and request rollups.
--
-- artifact_usage_stats is re-keyed from the repository name to the surrogate
-- repository id (ADR 0023 section 4): rows are backfilled by name from hosted
-- repositories and hosted groups, rows whose repository no longer exists are
-- dropped, and the primary key and both indexes move to repository_id. The name
-- is read back by joining the repository tables.
--
-- repository_request_rollup replaces the 30-day scan of the audit tables behind
-- ListRepositoryRequestStatistics and the overview. One row per repository, hour
-- and outcome; outcome is the small enum RequestOutcome (0 allowed, 1
-- unauthenticated, 2 forbidden, 3 quota, 4 quarantined, 5 rate_limited, 6 other),
-- so denied counts break down by reason. repository_id has no foreign key: the
-- id may name a hosted repository or a hosted group, and rollups outlive a
-- deletion until the 400 day retention drops them.
--
-- MySQL (ADR 0022): there is no migrations/mysql directory yet, so no twin is
-- added; the table is plain columns, so the twin is a mechanical translation.

ALTER TABLE public.artifact_usage_stats ADD COLUMN repository_id uuid;

UPDATE public.artifact_usage_stats AS usage
   SET repository_id = names.id
  FROM (
    SELECT id, name FROM public.hosted_repositories
    UNION ALL
    SELECT id, name FROM public.hosted_groups
  ) AS names
 WHERE names.name = usage.repository;

DELETE FROM public.artifact_usage_stats WHERE repository_id IS NULL;

ALTER TABLE public.artifact_usage_stats ALTER COLUMN repository_id SET NOT NULL;
ALTER TABLE public.artifact_usage_stats DROP CONSTRAINT artifact_usage_stats_pkey;
DROP INDEX public.artifact_usage_stats_repository_address_idx;
DROP INDEX public.artifact_usage_stats_repository_usage_idx;
ALTER TABLE public.artifact_usage_stats DROP COLUMN repository;
ALTER TABLE public.artifact_usage_stats
    ADD CONSTRAINT artifact_usage_stats_pkey PRIMARY KEY (repository_id, format, resource);
CREATE INDEX artifact_usage_stats_repository_address_idx
    ON public.artifact_usage_stats USING btree (repository_id, resource COLLATE "C", format COLLATE "C");
CREATE INDEX artifact_usage_stats_repository_usage_idx
    ON public.artifact_usage_stats USING btree (repository_id, download_count DESC);

CREATE TABLE public.repository_request_rollup (
    repository_id uuid NOT NULL,
    bucket_start timestamp with time zone NOT NULL,
    outcome smallint NOT NULL,
    requests bigint DEFAULT 0 NOT NULL,
    bytes bigint DEFAULT 0 NOT NULL,
    CONSTRAINT repository_request_rollup_pkey PRIMARY KEY (repository_id, bucket_start, outcome),
    CONSTRAINT repository_request_rollup_outcome_check CHECK (outcome BETWEEN 0 AND 6),
    CONSTRAINT repository_request_rollup_counts_check CHECK (requests >= 0 AND bytes >= 0)
);

-- Retention (400 days) deletes by bucket across repositories.
CREATE INDEX repository_request_rollup_bucket_idx
    ON public.repository_request_rollup USING btree (bucket_start);
