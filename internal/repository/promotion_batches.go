package repository

import (
	"context"
	"time"
)

// MaxPromotionBatchItems is the preview batch limit of ADR 0021 "Batch and version sets".
const MaxPromotionBatchItems = 100

// PromotionBatchItem is one (coordinate, digest) of a batch. AptTargetSuite is set only for APT.
type PromotionBatchItem struct {
	Coordinate     string `json:"coordinate"`
	Digest         string `json:"digest"`
	AptTargetSuite string `json:"aptTargetSuite,omitempty"`
}

// PromotionBatch is the durable record of an admitted promotion batch. It is written once, after every item passed
// admission, and never updated; the aggregate state is computed from the provenance rows of the items.
type PromotionBatch struct {
	ID, SourceRepositoryID, TargetRepositoryID string
	RequestedBy                                string
	// IdempotencyKey is unique per target repository; RequestHash tells a replay from another request reusing the key.
	IdempotencyKey, RequestHash string
	Items                       []PromotionBatchItem
	CreatedAt                   time.Time
}

// PromotionBatchStore persists promotion batches.
type PromotionBatchStore interface {
	// CreatePromotionBatch stores the batch unless its (target, idempotency key) exists: the stored batch is returned
	// with created=false then, so a replay finds the first request's record.
	CreatePromotionBatch(ctx context.Context, batch PromotionBatch) (stored PromotionBatch, created bool, err error)
	// FindPromotionBatch returns the batch recorded for the target and idempotency key, or found=false.
	FindPromotionBatch(ctx context.Context, targetRepositoryID, idempotencyKey string) (batch PromotionBatch, found bool, err error)
	// GetPromotionBatch returns ErrNotFound for an unknown id.
	GetPromotionBatch(ctx context.Context, id string) (PromotionBatch, error)
}
