package repository

import (
	"context"
	"database/sql"
)

// mavenPrefixUsage measures the assets under one coordinate prefix (g/a/v/) that the
// reconciler counts: an asset counts when a visible artifact's coordinate prefix is a
// directory prefix of its path (capacity.UsageQuery("maven"), scoped to the touched
// prefix). Publishing, tombstoning, restoring, promoting, replicating or importing a
// coordinate changes only the assets under its prefix, so the writer brackets its
// transaction with this measurement under the repository's Maven capacity lock.
func mavenPrefixUsage(repositoryID, prefix string) func(context.Context, *sql.Tx) (measuredUsage, error) {
	return func(ctx context.Context, tx *sql.Tx) (measuredUsage, error) {
		var usage measuredUsage
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(a.size),0)::bigint,COUNT(*)::bigint
FROM native_maven_assets a
WHERE a.repository_id=$1::uuid AND left(a.path,length($2))=$2
  AND EXISTS (
      SELECT 1 FROM native_maven_artifacts m
      WHERE m.repository_id=a.repository_id AND m.state='visible'
        AND left(a.path, length(replace(split_part(m.coordinate, ':', 1), '.', '/') || '/' || split_part(m.coordinate, ':', 2) || '/' || split_part(m.coordinate, ':', 3) || '/')) = replace(split_part(m.coordinate, ':', 1), '.', '/') || '/' || split_part(m.coordinate, ':', 2) || '/' || split_part(m.coordinate, ':', 3) || '/'
  )`, repositoryID, prefix).Scan(&usage.bytes, &usage.objects)
		return usage, err
	}
}

// openMavenUsageBracket starts the measurement of the coordinate's prefix; call it after
// lockMavenCapacityTx and before the first change of the transaction.
func openMavenUsageBracket(ctx context.Context, tx *sql.Tx, repositoryID, coordinate string) (*usageBracket, error) {
	bracket, err := openUsageBracket(ctx, tx, repositoryID, mavenPrefixUsage(repositoryID, mavenArtifactPathPrefix(coordinate)))
	if err != nil {
		return nil, err
	}
	bracket.catalogFormat, bracket.catalogCoordinate = FormatMaven, coordinate
	return bracket, nil
}
