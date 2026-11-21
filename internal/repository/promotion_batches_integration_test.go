//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestPostgresPromotionBatchStore(t *testing.T) {
	store := promotionIntegrationStore(t)
	exercisePromotionBatchStore(t, store)
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM promotion_batches WHERE requested_by='alice' AND idempotency_key LIKE 'batch-%'`)
	})
}

func TestPostgresPromotionBatchRefusesMoreThanOneHundredItems(t *testing.T) {
	store := promotionIntegrationStore(t)
	items := make([]PromotionBatchItem, MaxPromotionBatchItems+1)
	for index := range items {
		items[index] = PromotionBatchItem{Coordinate: "a.bin", Digest: "sha256:aa"}
	}
	_, _, err := store.CreatePromotionBatch(context.Background(), PromotionBatch{ID: uuid.NewString(), SourceRepositoryID: uuid.NewString(), TargetRepositoryID: uuid.NewString(),
		RequestedBy: "alice", IdempotencyKey: "batch-over", RequestHash: "h", Items: items})
	if err == nil {
		t.Fatal("the items CHECK must refuse a 101-item batch")
	}
}
