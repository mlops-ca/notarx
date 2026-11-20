package repository

import (
	"context"
	"database/sql"
	"time"
)

func (s *PostgresStore) QueryArtifactUsage(ctx context.Context, repository string, query ArtifactUsageQuery) (ArtifactUsagePage, error) {
	var page ArtifactUsagePage
	if err := query.Validate(repository); err != nil {
		return page, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer func() { _ = tx.Rollback() }()
	afterResource, afterFormat, after, err := query.afterAddress(repository)
	if err != nil {
		return page, err
	}
	// The lifetime totals are one aggregate over the repository; the matching total is bounded.
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(download_count), 0), COALESCE(SUM(total_bytes), 0), COUNT(*)
		FROM artifact_usage_stats WHERE repository_id = (SELECT id FROM (`+repositoryNames+`) n WHERE n.name = $1)`, repository).
		Scan(&page.Totals.DownloadCount, &page.Totals.TotalBytes, &page.Totals.Resources)
	if err != nil {
		return page, err
	}
	if query.Query == "" {
		// Every address matches: the lifetime aggregate above already counted them.
		page.Total = BoundedListTotal(page.Totals.Resources)
	} else if page.Total, err = boundedTotal(ctx, tx, "", `artifact_usage_stats s JOIN (`+repositoryNames+`) n ON n.id = s.repository_id WHERE n.name = $1 AND strpos(s.resource, $2) > 0`, repository, query.Query); err != nil {
		return page, err
	}
	var cursorResource, cursorFormat sql.NullString
	if after {
		cursorResource, cursorFormat = sql.NullString{String: afterResource, Valid: true}, sql.NullString{String: afterFormat, Valid: true}
	}
	rows, err := tx.QueryContext(ctx, usageSelect+` FROM artifact_usage_stats s JOIN (`+repositoryNames+`) n ON n.id = s.repository_id
		WHERE n.name = $1 AND strpos(s.resource, $2) > 0
		  AND ($3::text IS NULL OR (s.resource COLLATE "C", s.format COLLATE "C") > ($3::text COLLATE "C", $4::text COLLATE "C"))
		ORDER BY s.resource COLLATE "C" ASC, s.format COLLATE "C" ASC LIMIT $5`, repository, query.Query, cursorResource, cursorFormat, query.Limit+1)
	if err != nil {
		return page, err
	}
	page.Items = make([]ArtifactUsageStat, 0, query.Limit+1)
	for rows.Next() {
		var stat ArtifactUsageStat
		if err := rows.Scan(&stat.Repository, &stat.Format, &stat.Resource, &stat.DownloadCount, &stat.TotalBytes, &stat.FirstDownloadedAt, &stat.LastDownloadedAt, &stat.LastActor); err != nil {
			_ = rows.Close()
			return page, err
		}
		page.Items = append(page.Items, stat)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return page, err
	}
	if len(page.Items) > query.Limit {
		page.Items = page.Items[:query.Limit]
		page.NextPageToken = query.pageToken(repository, page.Items[query.Limit-1])
	}
	return page, tx.Commit()
}

// usageSelect projects one usage row with the repository name read back from the id.
const usageSelect = `SELECT n.name, s.format, s.resource, s.download_count, s.total_bytes, s.first_downloaded_at, s.last_downloaded_at, s.last_actor`

// RecordArtifactUsage folds one download increment into the durable aggregate
// immediately. The audit writer aggregates in memory and flushes through
// FlushUsage; tests and seeds may call this directly. An unknown repository
// records nothing.
func (s *PostgresStore) RecordArtifactUsage(ctx context.Context, stat ArtifactUsageStat) error {
	if stat.LastDownloadedAt.IsZero() {
		stat.LastDownloadedAt = time.Now().UTC()
	}
	return s.FlushUsage(ctx, UsageBatch{Usage: []UsageDelta{{Repository: stat.Repository, Format: stat.Format, Resource: stat.Resource,
		Downloads: 1, Bytes: stat.TotalBytes, First: stat.LastDownloadedAt, Last: stat.LastDownloadedAt, LastActor: stat.LastActor}}})
}

func (s *PostgresStore) ListArtifactUsage(ctx context.Context, repository string, limit int) ([]ArtifactUsageStat, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, usageSelect+` FROM artifact_usage_stats s JOIN (`+repositoryNames+`) n ON n.id = s.repository_id
		WHERE ($1 = '' OR n.name = $1)
		ORDER BY s.download_count DESC, s.resource ASC
		LIMIT $2`, repository, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	stats := make([]ArtifactUsageStat, 0, limit)
	for rows.Next() {
		var stat ArtifactUsageStat
		if err := rows.Scan(&stat.Repository, &stat.Format, &stat.Resource, &stat.DownloadCount, &stat.TotalBytes, &stat.FirstDownloadedAt, &stat.LastDownloadedAt, &stat.LastActor); err != nil {
			return nil, err
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}

func (s *PostgresStore) WalkArtifactUsage(ctx context.Context, repository string, visit func(ArtifactUsageStat) error) error {
	rows, err := s.db.QueryContext(ctx, walkArtifactUsageQuery, repository)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var stat ArtifactUsageStat
		if err := rows.Scan(&stat.Repository, &stat.Format, &stat.Resource, &stat.DownloadCount, &stat.TotalBytes, &stat.FirstDownloadedAt, &stat.LastDownloadedAt, &stat.LastActor); err != nil {
			return err
		}
		if err := visit(stat); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *PostgresStore) ArtifactUsageTotals(ctx context.Context, repository string) (ArtifactUsageTotals, error) {
	var totals ArtifactUsageTotals
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(s.download_count), 0), COALESCE(SUM(s.total_bytes), 0), COUNT(*)
		FROM artifact_usage_stats s
		WHERE ($1 = '' OR s.repository_id = (SELECT id FROM (`+repositoryNames+`) n WHERE n.name = $1))`, repository).
		Scan(&totals.DownloadCount, &totals.TotalBytes, &totals.Resources)
	return totals, err
}

// walkArtifactUsageQuery reads every usage row of one repository or group.
const walkArtifactUsageQuery = usageSelect + ` FROM artifact_usage_stats s JOIN (` + repositoryNames + `) n ON n.id = s.repository_id WHERE n.name = $1`
