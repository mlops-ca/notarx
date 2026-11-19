package repository

import (
	"context"
	"database/sql"
)

// aptGeneratedUsage measures the generated objects (dists/ files) of the repository's
// visible snapshots, each distinct object once at its largest size: the same
// definition as the reconciler's aggregation. A repository has one visible snapshot per
// suite, so the scan is bounded by suites and generated files, not by packages. Callers
// hold the repository row lock, which serialises every visibility change.
func aptGeneratedUsage(ctx context.Context, tx *sql.Tx, repositoryID string) (measuredUsage, error) {
	var usage measuredUsage
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size),0)::bigint,COUNT(*)::bigint FROM (
		SELECT MAX(a.size)::bigint AS size
		FROM native_apt_snapshot_assets a JOIN native_apt_repository_snapshots s ON s.id=a.snapshot_id
		WHERE a.repository_id=$1::uuid AND s.state='visible' AND a.path LIKE 'dists/%'
		GROUP BY a.object_key) generated`, repositoryID).Scan(&usage.bytes, &usage.objects)
	return usage, err
}
