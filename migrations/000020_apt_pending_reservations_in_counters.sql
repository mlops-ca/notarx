-- +goose Up

-- ADR 0023 section 3 follow-up: APT publication sessions and archive restores now reserve their
-- bytes in repository_usage_counters when they are admitted (capacity.Reserve) and release them
-- when the session completes, expires or fails, instead of summing the asset tables under a
-- FOR UPDATE on the quota row. The reconciler counts those pending bytes, so the counters of
-- repositories with sessions open at upgrade time are raised by them here (a counter row is created
-- when the repository has none yet). Without this the first
-- completion would release bytes that were never reserved and the counter would clamp at zero
-- until the next reconciler pass.
--
-- MySQL (ADR 0022): there is no migrations/mysql directory yet. The twin is INSERT ... ON DUPLICATE KEY UPDATE.

INSERT INTO repository_usage_counters(repository_id, bytes, objects, quota_bytes, updated_at)
SELECT p.repository_id, p.pending, 0, NULLIF((SELECT q.quota_bytes FROM repository_capacity_quotas q WHERE q.repository_id = p.repository_id), 0), now()
  FROM (
    SELECT repository_id, SUM(pending)::bigint AS pending FROM (
        SELECT repository_id, SUM(declared_size) AS pending
          FROM native_apt_publication_sessions WHERE state IN ('open','uploading') GROUP BY repository_id
        UNION ALL
        SELECT repository_id, SUM(reserved_bytes)
          FROM native_apt_archive_restores WHERE state='preparing' GROUP BY repository_id
    ) u GROUP BY repository_id
  ) p
  JOIN hosted_repositories h ON h.id = p.repository_id
 WHERE p.pending > 0
ON CONFLICT (repository_id) DO UPDATE
   SET bytes = repository_usage_counters.bytes + EXCLUDED.bytes, updated_at = now();
