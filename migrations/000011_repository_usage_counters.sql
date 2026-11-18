-- +goose Up

-- ADR 0023 section 3 (slice 8): per-repository usage counters.
--
-- Publishing transactions call capacity.Reserve (internal/capacity) as their last
-- statement; a reconciler (NOTARX_QUOTA_RECONCILE_INTERVAL) recomputes usage and
-- corrects drift. quota_bytes is a copy of repository_capacity_quotas written by the
-- quota API in the same transaction; NULL or 0 means unlimited. The capacity triggers
-- stay in place until every writer is covered (a later slice drops them).
--
-- MySQL (ADR 0022): there is no migrations/mysql directory yet. The twin is plain
-- DDL (drop the WITH clause) and the backfill is the same INSERT ... SELECT.

CREATE TABLE public.repository_usage_counters (
    repository_id uuid PRIMARY KEY REFERENCES public.hosted_repositories(id) ON DELETE CASCADE,
    bytes bigint NOT NULL DEFAULT 0,
    objects bigint NOT NULL DEFAULT 0,
    quota_bytes bigint,
    reconciled_at timestamp with time zone,
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT repository_usage_counters_quota_check CHECK (quota_bytes IS NULL OR quota_bytes >= 0)
) WITH (fillfactor = 70);

-- Backfill from the aggregation query (internal/capacity.UsageCTE).
INSERT INTO public.repository_usage_counters(repository_id, bytes, objects, quota_bytes, reconciled_at, updated_at)
WITH usage AS (
	SELECT a.repository_id,COALESCE(SUM(o.size),0)::bigint AS used_bytes,COUNT(*)::bigint AS object_count
	FROM native_raw_assets a JOIN native_raw_objects o ON o.digest=a.digest GROUP BY a.repository_id
	UNION ALL
	SELECT repository_id,COALESCE(SUM(size),0)::bigint,COUNT(*)::bigint
	FROM (
		-- A path may match multiple visible coordinates/builds; count each asset once.
		SELECT DISTINCT a.repository_id,a.path,a.size
		FROM native_maven_assets a
		CROSS JOIN LATERAL unnest(string_to_array(a.path,'/')) WITH ORDINALITY AS segments(part,depth)
		JOIN (
			SELECT DISTINCT repository_id,
				replace(split_part(coordinate,':',1),'.','/') || '/' || split_part(coordinate,':',2) || '/' || split_part(coordinate,':',3) || '/' AS prefix
			FROM native_maven_artifacts WHERE state='visible'
		) m ON m.repository_id=a.repository_id
			AND m.prefix=array_to_string((string_to_array(a.path,'/'))[1:depth::int],'/') || '/'
		WHERE depth<cardinality(string_to_array(a.path,'/'))
	) maven_objects GROUP BY repository_id
	UNION ALL
	SELECT repository_id,COALESCE(SUM(size),0)::bigint,COUNT(*)::bigint FROM (
		SELECT repository_id,size FROM native_oci_manifests
		UNION ALL
		SELECT rb.repository_id,b.size FROM native_oci_repository_blobs rb JOIN native_oci_blobs b ON b.digest=rb.digest
	) oci_objects GROUP BY repository_id
	UNION ALL
	SELECT a.repository_id,COALESCE(SUM(a.size),0)::bigint,COUNT(*)::bigint
	FROM native_conan_assets a
	JOIN native_conan_recipe_revisions r ON r.repository_id=a.repository_id AND r.reference=a.reference AND r.revision=a.recipe_revision
	LEFT JOIN native_conan_package_revisions p ON p.repository_id=a.repository_id AND p.reference=a.reference AND p.recipe_revision=a.recipe_revision AND p.package_id=a.package_id AND p.revision=a.package_revision
	WHERE r.state='visible' AND (a.package_id='' OR p.state='visible') GROUP BY a.repository_id
	UNION ALL
	SELECT repository_id,COALESCE(SUM(size),0)::bigint,COUNT(*)::bigint
	FROM native_npm_versions WHERE object_key<>'' GROUP BY repository_id
	UNION ALL
	SELECT repository_id,COALESCE(SUM(size),0)::bigint,COUNT(*)::bigint
	FROM native_pypi_files WHERE object_key<>'' AND state='visible' GROUP BY repository_id
	UNION ALL
	SELECT repository_id,COALESCE(SUM(size),0)::bigint,COUNT(*)::bigint
	FROM native_go_assets WHERE collected_at IS NULL GROUP BY repository_id
	UNION ALL
	SELECT repository_id,COALESCE(SUM(size),0)::bigint,COUNT(*)::bigint
	FROM native_cargo_publications WHERE collected_at IS NULL GROUP BY repository_id
	UNION ALL
	SELECT repository_id,COALESCE(SUM(size),0)::bigint,COUNT(*)::bigint
	FROM native_nuget_publications GROUP BY repository_id
	UNION ALL
	SELECT repository_id,COALESCE(SUM(size+provenance_size),0)::bigint,SUM(1+CASE WHEN provenance_size>0 THEN 1 ELSE 0 END)::bigint
	FROM native_helm_publications GROUP BY repository_id
	UNION ALL
	SELECT repository_id,COALESCE(SUM(size),0)::bigint,COUNT(*)::bigint
	FROM native_apt_assets GROUP BY repository_id
	UNION ALL
	SELECT repository_id,COALESCE(SUM(size),0)::bigint,COUNT(*)::bigint
	FROM native_apt_package_revisions GROUP BY repository_id
	UNION ALL
	SELECT repository_id,COALESCE(SUM(size),0)::bigint,COUNT(*)::bigint FROM (
		SELECT s.repository_id,a.object_key,MAX(a.size)::bigint AS size
		FROM native_apt_repository_snapshots s
		JOIN native_apt_snapshot_assets a ON a.snapshot_id=s.id
		WHERE s.state='visible' AND a.path LIKE 'dists/%'
		GROUP BY s.repository_id,a.object_key
	) apt_snapshot_objects GROUP BY repository_id
	UNION ALL
	SELECT repository_id,COALESCE(SUM(size),0)::bigint,COUNT(*)::bigint
	FROM native_rubygems_sessions WHERE state='committed' GROUP BY repository_id
), totals AS (
	SELECT repository_id,SUM(used_bytes)::bigint AS used_bytes,SUM(object_count)::bigint AS object_count
	FROM usage GROUP BY repository_id
)
SELECT h.id, COALESCE(t.used_bytes, 0), COALESCE(t.object_count, 0), NULLIF(q.quota_bytes, 0), now(), now()
  FROM public.hosted_repositories h
  LEFT JOIN totals t ON t.repository_id = h.id
  LEFT JOIN public.repository_capacity_quotas q ON q.repository_id = h.id;
