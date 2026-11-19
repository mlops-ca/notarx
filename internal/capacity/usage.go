// Package capacity implements repository quota accounting (ADR 0023 section 3):
// a per-repository counter row that publishing transactions update with Reserve as
// their last statement, and a reconciler that recomputes usage from the asset
// tables and corrects drift.
package capacity

// UsageCTE computes the logical usage of every hosted repository from the asset
// tables. It is the reconciler's source of truth and the definition the
// per-format equality tests compare the counters with. It defines the CTEs
// "usage" and "totals" (repository_id, used_bytes, object_count).
const UsageCTE = `WITH usage AS (
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
	-- Bytes reserved by Helm and NuGet sessions still pending (not yet committed or expired): in the
	-- counter from admission until the session commits, expires or is aborted. They hold bytes, not objects.
	SELECT repository_id,COALESCE(SUM(size+provenance_size),0)::bigint,0::bigint
	FROM native_helm_publication_sessions WHERE state='pending' GROUP BY repository_id
	UNION ALL
	SELECT repository_id,COALESCE(SUM(size),0)::bigint,0::bigint
	FROM native_nuget_publication_sessions WHERE state IN ('reserved','staged') GROUP BY repository_id
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
	-- Bytes reserved by APT publication sessions still open or uploading and by archive restores
	-- still preparing: they are in the counter from admission until the session completes,
	-- expires or fails. They hold bytes, not objects.
	SELECT repository_id,COALESCE(SUM(declared_size),0)::bigint,0::bigint
	FROM native_apt_publication_sessions WHERE state IN ('open','uploading') GROUP BY repository_id
	UNION ALL
	SELECT repository_id,COALESCE(SUM(reserved_bytes),0)::bigint,0::bigint
	FROM native_apt_archive_restores WHERE state='preparing' GROUP BY repository_id
	UNION ALL
	SELECT repository_id,COALESCE(SUM(size),0)::bigint,COUNT(*)::bigint
	FROM native_rubygems_sessions WHERE state='committed' GROUP BY repository_id
), totals AS (
	SELECT repository_id,SUM(used_bytes)::bigint AS used_bytes,SUM(object_count)::bigint AS object_count
	FROM usage GROUP BY repository_id
)`

// formatUsage holds, per hosted format, the single repository aggregation query
// ($1 is the repository id); it returns (used_bytes, object_count).
var formatUsage = map[string]string{

	"raw": `SELECT COALESCE(SUM(o.size),0),COUNT(*) FROM native_raw_assets a JOIN native_raw_objects o ON o.digest=a.digest WHERE a.repository_id=$1::uuid`,
	"maven": `SELECT COALESCE(SUM(a.size),0),COUNT(*)
            FROM native_maven_assets a
            WHERE a.repository_id=$1::uuid
              AND EXISTS (
                  SELECT 1 FROM native_maven_artifacts m
                  WHERE m.repository_id=a.repository_id AND m.state='visible'
                    AND left(a.path, length(replace(split_part(m.coordinate, ':', 1), '.', '/') || '/' || split_part(m.coordinate, ':', 2) || '/' || split_part(m.coordinate, ':', 3) || '/')) = replace(split_part(m.coordinate, ':', 1), '.', '/') || '/' || split_part(m.coordinate, ':', 2) || '/' || split_part(m.coordinate, ':', 3) || '/'
              )`,
	"oci":   `SELECT COALESCE((SELECT SUM(size) FROM native_oci_manifests WHERE repository_id=$1::uuid),0)+COALESCE((SELECT SUM(b.size) FROM native_oci_repository_blobs rb JOIN native_oci_blobs b ON b.digest=rb.digest WHERE rb.repository_id=$1::uuid),0), (SELECT COUNT(*) FROM native_oci_manifests WHERE repository_id=$1::uuid)+(SELECT COUNT(*) FROM native_oci_repository_blobs WHERE repository_id=$1::uuid)`,
	"conan": `SELECT COALESCE(SUM(a.size),0),COUNT(*) FROM native_conan_assets a JOIN native_conan_recipe_revisions r ON r.repository_id=a.repository_id AND r.reference=a.reference AND r.revision=a.recipe_revision LEFT JOIN native_conan_package_revisions p ON p.repository_id=a.repository_id AND p.reference=a.reference AND p.recipe_revision=a.recipe_revision AND p.package_id=a.package_id AND p.revision=a.package_revision WHERE a.repository_id=$1::uuid AND r.state='visible' AND (a.package_id='' OR p.state='visible')`,
	"npm":   `SELECT COALESCE(SUM(size),0),COUNT(*) FROM native_npm_versions WHERE repository_id=$1::uuid AND object_key<>''`,
	"pypi":  `SELECT COALESCE(SUM(size),0),COUNT(*) FROM native_pypi_files WHERE repository_id=$1::uuid AND object_key<>'' AND state='visible'`,
	"go":    `SELECT COALESCE(SUM(size),0),COUNT(*) FROM native_go_assets WHERE repository_id=$1::uuid AND collected_at IS NULL`,
	"cargo": `SELECT COALESCE(SUM(size),0),COUNT(*) FROM native_cargo_publications WHERE repository_id=$1::uuid AND collected_at IS NULL`,
	"helm": `SELECT
			COALESCE((SELECT SUM(size+provenance_size) FROM native_helm_publications WHERE repository_id=$1::uuid),0)+
			COALESCE((SELECT SUM(size+provenance_size) FROM native_helm_publication_sessions WHERE repository_id=$1::uuid AND state='pending'),0),
			COALESCE((SELECT SUM(1+CASE WHEN provenance_size>0 THEN 1 ELSE 0 END) FROM native_helm_publications WHERE repository_id=$1::uuid),0)`,
	"nuget": `SELECT
			COALESCE((SELECT SUM(size) FROM native_nuget_publications WHERE repository_id=$1::uuid),0)+
			COALESCE((SELECT SUM(size) FROM native_nuget_publication_sessions WHERE repository_id=$1::uuid AND state IN ('reserved','staged')),0),
			(SELECT COUNT(*) FROM native_nuget_publications WHERE repository_id=$1::uuid)`,
	"apt": `SELECT
			COALESCE((SELECT SUM(size) FROM native_apt_assets WHERE repository_id=$1::uuid),0)+
			COALESCE((SELECT SUM(size) FROM native_apt_package_revisions WHERE repository_id=$1::uuid),0)+
			COALESCE((SELECT SUM(size) FROM (SELECT MAX(a.size)::bigint AS size FROM native_apt_snapshot_assets a JOIN native_apt_repository_snapshots s ON s.id=a.snapshot_id WHERE a.repository_id=$1::uuid AND s.state='visible' AND a.path LIKE 'dists/%' GROUP BY a.object_key) generated),0)+
			COALESCE((SELECT SUM(declared_size) FROM native_apt_publication_sessions WHERE repository_id=$1::uuid AND state IN ('open','uploading')),0)+
			COALESCE((SELECT SUM(reserved_bytes) FROM native_apt_archive_restores WHERE repository_id=$1::uuid AND state='preparing'),0),
			(SELECT COUNT(*) FROM native_apt_assets WHERE repository_id=$1::uuid)+
			(SELECT COUNT(*) FROM native_apt_package_revisions WHERE repository_id=$1::uuid)+
			(SELECT COUNT(*) FROM (SELECT a.object_key FROM native_apt_snapshot_assets a JOIN native_apt_repository_snapshots s ON s.id=a.snapshot_id WHERE a.repository_id=$1::uuid AND s.state='visible' AND a.path LIKE 'dists/%' GROUP BY a.object_key) generated)`,
	"rubygems": `SELECT COALESCE(SUM(size),0),COUNT(*) FROM native_rubygems_sessions WHERE repository_id=$1::uuid AND state='committed'`,
}

// UsageQuery returns the aggregation query of one hosted format and whether the
// format is known. The query takes the repository id as $1::uuid and returns
// (used_bytes, object_count).
func UsageQuery(format string) (string, bool) {
	query, ok := formatUsage[format]
	return query, ok
}
