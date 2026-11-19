-- +goose Up

-- ADR 0023 section 3 follow-up (#451): Helm and NuGet publication sessions now reserve their bytes in
-- repository_usage_counters when they are admitted (capacity.Reserve) and release them when they
-- commit, expire or are aborted, instead of summing the publication and session tables under a
-- FOR UPDATE on the quota row. The reconciler counts those pending bytes, so the counters of
-- repositories with sessions pending at upgrade time are raised by them here (a counter row is
-- created when the repository has none yet). Without this the first commit or expiry would release
-- bytes that were never reserved and the counter would clamp at zero until the next reconciler pass.
--
-- MySQL (ADR 0022): there is no migrations/mysql directory yet. The twin is INSERT ... ON DUPLICATE KEY UPDATE.

INSERT INTO repository_usage_counters(repository_id, bytes, objects, quota_bytes, updated_at)
SELECT p.repository_id, p.pending, 0, NULLIF((SELECT q.quota_bytes FROM repository_capacity_quotas q WHERE q.repository_id = p.repository_id), 0), now()
  FROM (
    SELECT repository_id, SUM(pending)::bigint AS pending FROM (
        SELECT repository_id, SUM(size + provenance_size) AS pending
          FROM native_helm_publication_sessions WHERE state = 'pending' GROUP BY repository_id
        UNION ALL
        SELECT repository_id, SUM(size)
          FROM native_nuget_publication_sessions WHERE state IN ('reserved','staged') GROUP BY repository_id
    ) u GROUP BY repository_id
  ) p
  JOIN hosted_repositories h ON h.id = p.repository_id
 WHERE p.pending > 0
ON CONFLICT (repository_id) DO UPDATE
   SET bytes = repository_usage_counters.bytes + EXCLUDED.bytes, updated_at = now();
