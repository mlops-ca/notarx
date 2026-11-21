package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

var _ PromotionBatchStore = (*PostgresStore)(nil)

const promotionBatchColumns = `id::text,source_repository_id::text,target_repository_id::text,requested_by,idempotency_key,request_hash,items,created_at`

func scanPromotionBatch(scanner interface{ Scan(...any) error }) (PromotionBatch, error) {
	var batch PromotionBatch
	var items []byte
	if err := scanner.Scan(&batch.ID, &batch.SourceRepositoryID, &batch.TargetRepositoryID, &batch.RequestedBy, &batch.IdempotencyKey, &batch.RequestHash, &items, &batch.CreatedAt); err != nil {
		return PromotionBatch{}, err
	}
	if err := json.Unmarshal(items, &batch.Items); err != nil {
		return PromotionBatch{}, err
	}
	return batch, nil
}

// CreatePromotionBatch implements PromotionBatchStore. The insert is a no-op on the (target, idempotency key) unique
// constraint, then the existing row is read, so concurrent replays of one request converge on one batch.
func (s *PostgresStore) CreatePromotionBatch(ctx context.Context, batch PromotionBatch) (PromotionBatch, bool, error) {
	if batch.ID == "" {
		return PromotionBatch{}, false, errors.New("promotion batch has no id")
	}
	items, err := json.Marshal(batch.Items)
	if err != nil {
		return PromotionBatch{}, false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO promotion_batches (id,source_repository_id,target_repository_id,requested_by,idempotency_key,request_hash,items)
VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7::jsonb) ON CONFLICT (target_repository_id,idempotency_key) DO NOTHING`,
		batch.ID, batch.SourceRepositoryID, batch.TargetRepositoryID, batch.RequestedBy, batch.IdempotencyKey, batch.RequestHash, items)
	if err != nil {
		return PromotionBatch{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return PromotionBatch{}, false, err
	}
	stored, err := scanPromotionBatch(s.db.QueryRowContext(ctx, `SELECT `+promotionBatchColumns+` FROM promotion_batches WHERE target_repository_id=$1::uuid AND idempotency_key=$2`, batch.TargetRepositoryID, batch.IdempotencyKey))
	return stored, inserted == 1, err
}

// FindPromotionBatch implements PromotionBatchStore.
func (s *PostgresStore) FindPromotionBatch(ctx context.Context, targetRepositoryID, idempotencyKey string) (PromotionBatch, bool, error) {
	batch, err := scanPromotionBatch(s.db.QueryRowContext(ctx, `SELECT `+promotionBatchColumns+` FROM promotion_batches WHERE target_repository_id=$1::uuid AND idempotency_key=$2`, targetRepositoryID, idempotencyKey))
	if errors.Is(err, sql.ErrNoRows) {
		return PromotionBatch{}, false, nil
	}
	return batch, err == nil, err
}

// GetPromotionBatch implements PromotionBatchStore.
func (s *PostgresStore) GetPromotionBatch(ctx context.Context, id string) (PromotionBatch, error) {
	batch, err := scanPromotionBatch(s.db.QueryRowContext(ctx, `SELECT `+promotionBatchColumns+` FROM promotion_batches WHERE id=$1::uuid`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return PromotionBatch{}, ErrNotFound
	}
	return batch, err
}

// GetArtifactPromotionsByKeys implements ArtifactPromotionStore: the newest attempt of each idempotency key that has a
// provenance row, served by the (idempotency_key, attempt) unique index.
func (s *PostgresStore) GetArtifactPromotionsByKeys(ctx context.Context, keys []string) (map[string]ArtifactPromotion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT ON (idempotency_key) `+artifactPromotionColumns+` FROM artifact_promotions WHERE idempotency_key=ANY($1::text[]) ORDER BY idempotency_key, attempt DESC`, keys)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	found := make(map[string]ArtifactPromotion, len(keys))
	for rows.Next() {
		var promotion ArtifactPromotion
		if err := scanArtifactPromotion(rows, &promotion); err != nil {
			return nil, err
		}
		found[promotion.IdempotencyKey] = promotion
	}
	return found, rows.Err()
}
