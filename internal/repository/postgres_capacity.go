package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/mlops-ca/notarx/internal/capacity"
)

// repositoryCapacityRecordsQuery lists every repository's quota and usage ordered by
// id. Covered formats (internal/capacity registry) read their counter row, so the
// overview is O(repositories); formats still pending aggregate the asset tables.
var repositoryCapacityRecordsQuery = buildRepositoryCapacityRecordsQuery(capacity.CoveredFormats(), capacity.AllCovered())

func buildRepositoryCapacityRecordsQuery(covered []string, all bool) string {
	const head = `SELECT h.id::text,h.name,h.format,h.repo_type,h.endpoint,h.state,
	COALESCE(q.quota_bytes,0),`
	const tail = `
FROM hosted_repositories h
LEFT JOIN repository_capacity_quotas q ON q.repository_id=h.id
LEFT JOIN repository_usage_counters c ON c.repository_id=h.id`
	if all {
		return head + `COALESCE(c.bytes,0),COALESCE(c.objects,0)` + tail + `
ORDER BY h.id`
	}
	list := "''"
	if len(covered) > 0 {
		list = "'" + strings.Join(covered, "','") + "'"
	}
	return capacity.UsageCTE + head + `
	CASE WHEN h.format IN (` + list + `) THEN COALESCE(c.bytes,0) ELSE COALESCE(t.used_bytes,0) END,
	CASE WHEN h.format IN (` + list + `) THEN COALESCE(c.objects,0) ELSE COALESCE(t.object_count,0) END` + tail + `
LEFT JOIN totals t ON t.repository_id=h.id
ORDER BY h.id`
}

func (s *PostgresStore) GetRepositoryCapacity(ctx context.Context, id string) (RepositoryCapacity, error) {
	var current RepositoryCapacity
	err := s.db.QueryRowContext(ctx, `SELECT format,COALESCE((SELECT quota_bytes FROM repository_capacity_quotas WHERE repository_id=h.id),0) FROM hosted_repositories h WHERE id=$1::uuid`, id).Scan(&current.Format, &current.QuotaBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryCapacity{}, ErrNotFound
	}
	if err != nil {
		return RepositoryCapacity{}, err
	}
	current.RepositoryID = id
	if capacity.Covered(string(current.Format)) {
		// A covered format reads its counter, like the overview and the quota alerts do; a
		// repository that never reserved has no row and no usage.
		err = s.db.QueryRowContext(ctx, `SELECT bytes,objects FROM repository_usage_counters WHERE repository_id=$1::uuid`, id).Scan(&current.UsedBytes, &current.ObjectCount)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return RepositoryCapacity{}, err
		}
		return current, nil
	}
	query, _ := capacity.UsageQuery(string(current.Format))
	if err = s.db.QueryRowContext(ctx, query, id).Scan(&current.UsedBytes, &current.ObjectCount); err != nil {
		return RepositoryCapacity{}, err
	}
	return current, nil
}

func (s *PostgresStore) ListRepositoryCapacityRecords(ctx context.Context) ([]RepositoryCapacityRecord, error) {
	rows, err := s.db.QueryContext(ctx, repositoryCapacityRecordsQuery)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	records := []RepositoryCapacityRecord{}
	for rows.Next() {
		var record RepositoryCapacityRecord
		if err := rows.Scan(
			&record.Repository.ID,
			&record.Repository.Name,
			&record.Repository.Format,
			&record.Repository.Type,
			&record.Repository.Endpoint,
			&record.Repository.State,
			&record.Capacity.QuotaBytes,
			&record.Capacity.UsedBytes,
			&record.Capacity.ObjectCount,
		); err != nil {
			return nil, err
		}
		record.Capacity.RepositoryID = record.Repository.ID
		record.Capacity.Format = record.Repository.Format
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *PostgresStore) ReplaceRepositoryCapacityQuota(ctx context.Context, id string, quota int64, audit ...AuditRecord) (RepositoryCapacity, error) {
	if quota < 0 {
		return RepositoryCapacity{}, ErrDisabled
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RepositoryCapacity{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var repositoryID string
	if err = tx.QueryRowContext(ctx, `SELECT id::text FROM hosted_repositories WHERE id=$1::uuid FOR UPDATE`, id).Scan(&repositoryID); errors.Is(err, sql.ErrNoRows) {
		return RepositoryCapacity{}, ErrNotFound
	} else if err != nil {
		return RepositoryCapacity{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO repository_capacity_quotas(repository_id,quota_bytes) VALUES ($1,$2) ON CONFLICT(repository_id) DO UPDATE SET quota_bytes=EXCLUDED.quota_bytes`, repositoryID, quota)
	if err != nil {
		return RepositoryCapacity{}, err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return RepositoryCapacity{}, ErrNotFound
	}
	if err = capacity.SetQuota(ctx, tx, repositoryID, quota); err != nil {
		return RepositoryCapacity{}, err
	}
	// The capacity triggers that rejected a quota below the current usage are gone (migration
	// 000014); the counter row SetQuota just locked holds the usage, so a concurrent Reserve waits.
	var used int64
	if err = tx.QueryRowContext(ctx, `SELECT bytes FROM repository_usage_counters WHERE repository_id=$1::uuid`, repositoryID).Scan(&used); err != nil {
		return RepositoryCapacity{}, err
	}
	if quota > 0 && used > quota {
		return RepositoryCapacity{}, ErrQuotaExceeded
	}
	if err = insertAudits(ctx, tx, audit); err != nil {
		return RepositoryCapacity{}, err
	}
	if err = tx.Commit(); err != nil {
		return RepositoryCapacity{}, err
	}
	return s.GetRepositoryCapacity(ctx, id)
}
