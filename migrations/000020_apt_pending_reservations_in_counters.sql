-- +goose Up

-- ADR 0023 section 3 follow-up: APT publication sessions and archive restores now reserve their
-- bytes in repository_usage_counters when they are admitted (capacity.Reserve) and release them
-- when the session completes, expires or fails, instead of summing the asset tables under a
-- FOR UPDATE on the quota row. The reconciler counts those pending bytes, so the counters of
-- repositories with sessions open at upgrade time are raised by them here. Without this the first
-- completion would release bytes that were never reserved and the counter would clamp at zero
-- until the next reconciler pass.
--
-- MySQL (ADR 0022): there is no migrations/mysql directory yet. The twin is an UPDATE with a
-- derived table join.

UPDATE repository_usage_counters c
   SET bytes = c.bytes + p.pending, updated_at = now()
  FROM (
    SELECT repository_id, SUM(pending)::bigint AS pending FROM (
        SELECT repository_id, SUM(declared_size) AS pending
          FROM native_apt_publication_sessions WHERE state IN ('open','uploading') GROUP BY repository_id
        UNION ALL
        SELECT repository_id, SUM(reserved_bytes)
          FROM native_apt_archive_restores WHERE state='preparing' GROUP BY repository_id
    ) u GROUP BY repository_id
  ) p
 WHERE c.repository_id = p.repository_id AND p.pending > 0;
