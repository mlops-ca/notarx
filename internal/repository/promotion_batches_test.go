package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/ids"
)

// exercisePromotionBatchStore is the contract both stores meet: a batch is written once per (target, key), a replay finds
// the first record, and an unknown id is ErrNotFound.
func exercisePromotionBatchStore(t *testing.T, store interface {
	PromotionBatchStore
	ArtifactPromotionStore
}) {
	t.Helper()
	ctx := context.Background()
	target, source := uuid.NewString(), uuid.NewString()
	key := "batch-" + uuid.NewString()
	batch := PromotionBatch{ID: ids.New(), SourceRepositoryID: source, TargetRepositoryID: target, RequestedBy: "alice", IdempotencyKey: key, RequestHash: "h1",
		Items: []PromotionBatchItem{{Coordinate: "a.bin", Digest: "sha256:aa"}, {Coordinate: "b.bin", Digest: "sha256:bb", AptTargetSuite: "stable"}}}
	if _, found, err := store.FindPromotionBatch(ctx, target, key); err != nil || found {
		t.Fatalf("find before create: found=%t err=%v", found, err)
	}
	stored, created, err := store.CreatePromotionBatch(ctx, batch)
	if err != nil || !created || stored.ID != batch.ID || len(stored.Items) != 2 || stored.Items[1].AptTargetSuite != "stable" || stored.CreatedAt.IsZero() {
		t.Fatalf("create: %+v created=%t err=%v", stored, created, err)
	}
	replay := batch
	replay.ID = ids.New()
	again, created, err := store.CreatePromotionBatch(ctx, replay)
	if err != nil || created || again.ID != batch.ID {
		t.Fatalf("replay must return the first batch: %+v created=%t err=%v", again, created, err)
	}
	if found, ok, err := store.FindPromotionBatch(ctx, target, key); err != nil || !ok || found.ID != batch.ID || found.RequestHash != "h1" {
		t.Fatalf("find: %+v ok=%t err=%v", found, ok, err)
	}
	got, err := store.GetPromotionBatch(ctx, batch.ID)
	if err != nil || got.ID != batch.ID || got.RequestedBy != "alice" || got.Items[0].Coordinate != "a.bin" {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if _, err = store.GetPromotionBatch(ctx, ids.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown batch err=%v", err)
	}
	if rows, err := store.GetArtifactPromotionsByKeys(ctx, []string{"manual:none"}); err != nil || len(rows) != 0 {
		t.Fatalf("keys without rows: %v err=%v", rows, err)
	}
}

func TestMemoryPromotionBatchStore(t *testing.T) {
	exercisePromotionBatchStore(t, NewMemoryStore())
}

func TestMemoryArtifactPromotionsByKeysReturnsTheNewestAttempt(t *testing.T) {
	ctx := context.Background()
	store, target, source := memoryPromotionFixture(t)
	digest := "sha256:" + "a1"
	for _, key := range []string{"k1", "k2"} {
		if _, _, err := store.EnqueueLifecycleJob(promotionProvenance(ctx, FormatRaw, "a.bin", digest, key), promotionJob(target, source, key)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.GetArtifactPromotionsByKeys(ctx, []string{"manual:k1", "manual:k2", "manual:k3"})
	if err != nil || len(rows) != 2 || rows["manual:k1"].IdempotencyKey != "manual:k1" || rows["manual:k2"].Attempt != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}
