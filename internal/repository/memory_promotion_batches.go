package repository

import (
	"context"
	"errors"
	"slices"
	"time"
)

var _ PromotionBatchStore = (*MemoryStore)(nil)

func clonePromotionBatch(batch PromotionBatch) PromotionBatch {
	batch.Items = slices.Clone(batch.Items)
	return batch
}

// CreatePromotionBatch implements PromotionBatchStore.
func (s *MemoryStore) CreatePromotionBatch(_ context.Context, batch PromotionBatch) (PromotionBatch, bool, error) {
	if batch.ID == "" {
		return PromotionBatch{}, false, errors.New("promotion batch has no id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.promotionBatches {
		if existing.TargetRepositoryID == batch.TargetRepositoryID && existing.IdempotencyKey == batch.IdempotencyKey {
			return clonePromotionBatch(existing), false, nil
		}
	}
	batch.CreatedAt = time.Now().UTC()
	if s.promotionBatches == nil {
		s.promotionBatches = map[string]PromotionBatch{}
	}
	s.promotionBatches[batch.ID] = clonePromotionBatch(batch)
	return clonePromotionBatch(batch), true, nil
}

// FindPromotionBatch implements PromotionBatchStore.
func (s *MemoryStore) FindPromotionBatch(_ context.Context, targetRepositoryID, idempotencyKey string) (PromotionBatch, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, batch := range s.promotionBatches {
		if batch.TargetRepositoryID == targetRepositoryID && batch.IdempotencyKey == idempotencyKey {
			return clonePromotionBatch(batch), true, nil
		}
	}
	return PromotionBatch{}, false, nil
}

// GetPromotionBatch implements PromotionBatchStore.
func (s *MemoryStore) GetPromotionBatch(_ context.Context, id string) (PromotionBatch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	batch, ok := s.promotionBatches[id]
	if !ok {
		return PromotionBatch{}, ErrNotFound
	}
	return clonePromotionBatch(batch), nil
}

// GetArtifactPromotionsByKeys implements ArtifactPromotionStore.
func (s *MemoryStore) GetArtifactPromotionsByKeys(_ context.Context, keys []string) (map[string]ArtifactPromotion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	found := make(map[string]ArtifactPromotion, len(keys))
	for _, promotion := range s.artifactPromotions {
		if !slices.Contains(keys, promotion.IdempotencyKey) {
			continue
		}
		if latest, ok := found[promotion.IdempotencyKey]; !ok || promotion.Attempt > latest.Attempt {
			found[promotion.IdempotencyKey] = cloneArtifactPromotion(promotion)
		}
	}
	return found, nil
}
