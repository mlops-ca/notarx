package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/repository"
)

// PromotionBatchService promotes up to repository.MaxPromotionBatchItems artifacts of one source repository into one
// target (ADR 0021, "Batch and version sets"). Admission is all-or-nothing: every item passes the PromotionService
// admission checks before the batch is recorded and any item queued. Execution is per item through
// PromotionService.Request, under an idempotency key derived from the batch id and the item index, so an item is queued
// once however often the batch request is replayed. Callers have already authenticated the principal and authorised it
// on both repositories.
type PromotionBatchService struct {
	PromotionService
	batches repository.PromotionBatchStore
}

func newPromotionBatchService(h generatedRepositoryAPIAdapter) PromotionBatchService {
	return PromotionBatchService{PromotionService: newPromotionService(h), batches: h.promotions}
}

// PromotionBatchRequest is one validated request to promote a batch.
type PromotionBatchRequest struct {
	Principal      Principal
	Source         repository.HostedRepository
	TargetID       string
	Items          []repository.PromotionBatchItem
	IdempotencyKey string
}

// PromotionBatchItemProblem is the refusal of one item.
type PromotionBatchItemProblem struct {
	Index              int
	Coordinate, Digest string
	Status             int
	Code, Message      string
}

// PromotionBatchError is a refusal of the whole batch. Items lists the refused items of promotion_batch_rejected.
type PromotionBatchError struct {
	PromotionError
	Items []PromotionBatchItemProblem
}

// PromotionBatchItemView is one item of a batch with the state of its provenance row.
type PromotionBatchItemView struct {
	Index              int
	Coordinate, Digest string
	// State is the provenance state, or "pending" while the item has no row yet.
	State              string
	PromotionID, JobID string
}

// PromotionBatchView is a batch with its aggregate state.
type PromotionBatchView struct {
	Batch repository.PromotionBatch
	State string
	Items []PromotionBatchItemView
}

// Aggregate batch states and the item state before a provenance row exists.
const (
	promotionBatchPending         = "pending"
	promotionBatchRunning         = "running"
	promotionBatchCompleted       = "completed"
	promotionBatchPartiallyFailed = "partially_failed"
	promotionBatchFailed          = "failed"
)

func batchError(status int, code, message string) *PromotionBatchError {
	return &PromotionBatchError{PromotionError: PromotionError{Status: status, Code: code, Message: message}}
}

// PromotionBatchItemKey is the idempotency key of one item: derived from the batch id and the item index.
func PromotionBatchItemKey(batchID string, index int) string {
	return fmt.Sprintf("batch:%s:%d", batchID, index)
}

func promotionBatchRequestHash(source, target string, items []repository.PromotionBatchItem) string {
	payload, _ := json.Marshal(struct {
		Source, Target string
		Items          []repository.PromotionBatchItem
	}{source, target, items})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Create admits and queues a batch. A replay of the same Idempotency-Key with the same request returns the batch of
// the first request and queues any item that was not queued yet; the same key with another request is a conflict.
func (s PromotionBatchService) Create(ctx context.Context, in PromotionBatchRequest) (PromotionBatchView, *PromotionBatchError) {
	if len(in.Items) == 0 || len(in.Items) > repository.MaxPromotionBatchItems {
		return PromotionBatchView{}, batchError(http.StatusBadRequest, "invalid_request", fmt.Sprintf("a batch holds 1 to %d items", repository.MaxPromotionBatchItems))
	}
	hash := promotionBatchRequestHash(in.Source.ID, in.TargetID, in.Items)
	existing, found, err := s.lookup(ctx, in)
	if err != nil {
		return PromotionBatchView{}, batchError(http.StatusInternalServerError, "internal_error", "get promotion batch failed")
	}
	batch := existing
	if found && existing.RequestHash != hash {
		return PromotionBatchView{}, batchError(http.StatusConflict, "idempotency_conflict", "Idempotency-Key conflicts with an existing promotion batch")
	}
	target := s.resolveTarget(ctx, in.Source, in.TargetID)
	if !found {
		if problems := s.admitAll(ctx, in, target); len(problems) > 0 {
			refusal := batchError(http.StatusUnprocessableEntity, "promotion_batch_rejected", fmt.Sprintf("%d of %d items were refused; nothing was queued", len(problems), len(in.Items)))
			refusal.Items = problems
			return PromotionBatchView{}, refusal
		}
		var created bool
		batch, created, err = s.batches.CreatePromotionBatch(ctx, repository.PromotionBatch{
			ID: ids.New(), SourceRepositoryID: in.Source.ID, TargetRepositoryID: in.TargetID, RequestedBy: in.Principal.Actor,
			IdempotencyKey: in.IdempotencyKey, RequestHash: hash, Items: in.Items,
		})
		if err != nil {
			return PromotionBatchView{}, batchError(http.StatusInternalServerError, "internal_error", "create promotion batch failed")
		}
		if !created && batch.RequestHash != hash {
			return PromotionBatchView{}, batchError(http.StatusConflict, "idempotency_conflict", "Idempotency-Key conflicts with an existing promotion batch")
		}
	}
	if perr := s.queue(ctx, in, batch, target, !found); perr != nil {
		return PromotionBatchView{}, perr
	}
	view, err := s.view(ctx, batch)
	if err != nil {
		return PromotionBatchView{}, batchError(http.StatusInternalServerError, "internal_error", "get promotion provenance failed")
	}
	return view, nil
}

// Get returns a batch with its aggregate state, or repository.ErrNotFound.
func (s PromotionBatchService) Get(ctx context.Context, id string) (PromotionBatchView, error) {
	batch, err := s.batches.GetPromotionBatch(ctx, id)
	if err != nil {
		return PromotionBatchView{}, err
	}
	return s.view(ctx, batch)
}

// lookup finds the batch of an earlier request with the same target and Idempotency-Key.
func (s PromotionBatchService) lookup(ctx context.Context, in PromotionBatchRequest) (repository.PromotionBatch, bool, error) {
	return s.batches.FindPromotionBatch(ctx, in.TargetID, in.IdempotencyKey)
}

// admitAll runs the admission checks of every item against the resolved target and returns the refusals; none means the
// whole batch is admitted.
func (s PromotionBatchService) admitAll(ctx context.Context, in PromotionBatchRequest, target *promotionTarget) []PromotionBatchItemProblem {
	var problems []PromotionBatchItemProblem
	seen := map[[2]string]int{}
	for index, item := range in.Items {
		problem := func(status int, code, message string) {
			problems = append(problems, itemProblem(index, item, status, code, message))
		}
		key := [2]string{item.Coordinate, item.Digest}
		if first, duplicate := seen[key]; duplicate {
			problem(http.StatusBadRequest, "invalid_request", fmt.Sprintf("duplicates item %d", first))
			continue
		}
		seen[key] = index
		if !validPromotionItem(in.Source.Format, item.Coordinate, item.Digest, optionalSuite(item)) {
			problem(http.StatusBadRequest, "invalid_request", "a valid immutable artifact coordinate and sha256 digest are required")
			continue
		}
		if _, perr := s.admit(ctx, s.itemRequest(in, item, ""), target); perr != nil {
			problem(perr.Status, perr.Code, perr.Message)
		}
	}
	return problems
}

func itemProblem(index int, item repository.PromotionBatchItem, status int, code, message string) PromotionBatchItemProblem {
	return PromotionBatchItemProblem{Index: index, Coordinate: item.Coordinate, Digest: item.Digest, Status: status, Code: code, Message: message}
}

func optionalSuite(item repository.PromotionBatchItem) *string {
	if item.AptTargetSuite == "" {
		return nil
	}
	return &item.AptTargetSuite
}

func (s PromotionBatchService) itemRequest(in PromotionBatchRequest, item repository.PromotionBatchItem, key string) PromotionRequest {
	return PromotionRequest{Principal: in.Principal, Source: in.Source, TargetID: in.TargetID, Coordinate: item.Coordinate, Digest: item.Digest, AptTargetSuite: optionalSuite(item), IdempotencyKey: key}
}

// queue enqueues every item that has no provenance row yet, in order. An item that already has a row is not admitted
// again, so a replay never flips on a policy tightened since the first request. admitted says the caller admitted the
// whole batch against target a moment ago (a fresh batch); a replay is not, so its unqueued items are admitted first and
// one refusal fails the whole replay before any of them is queued. The items are then queued as admitted, never a
// second time.
func (s PromotionBatchService) queue(ctx context.Context, in PromotionBatchRequest, batch repository.PromotionBatch, target *promotionTarget, admitted bool) *PromotionBatchError {
	rows, err := s.promotions.GetArtifactPromotionsByKeys(ctx, s.provenanceKeys(batch))
	if err != nil {
		return batchError(http.StatusInternalServerError, "internal_error", "get promotion provenance failed")
	}
	var pending []int
	for index := range batch.Items {
		if _, queued := rows[promotionIdempotencyKey(batch.TargetRepositoryID, PromotionBatchItemKey(batch.ID, index))]; !queued {
			pending = append(pending, index)
		}
	}
	if !admitted {
		var problems []PromotionBatchItemProblem
		for _, index := range pending {
			item := batch.Items[index]
			if _, perr := s.admit(ctx, s.itemRequest(in, item, ""), target); perr != nil {
				problems = append(problems, itemProblem(index, item, perr.Status, perr.Code, perr.Message))
			}
		}
		if len(problems) > 0 {
			refusal := batchError(http.StatusUnprocessableEntity, "promotion_batch_rejected", fmt.Sprintf("%d of %d items were refused; nothing was queued", len(problems), len(pending)))
			refusal.Items = problems
			return refusal
		}
	}
	for _, index := range pending {
		item := batch.Items[index]
		request := s.itemRequest(in, item, PromotionBatchItemKey(batch.ID, index))
		request.admitted = true
		if _, perr := s.request(ctx, request, target); perr != nil {
			refusal := &PromotionBatchError{PromotionError: *perr}
			refusal.Items = []PromotionBatchItemProblem{itemProblem(index, item, perr.Status, perr.Code, perr.Message)}
			return refusal
		}
	}
	return nil
}

func (s PromotionBatchService) provenanceKeys(batch repository.PromotionBatch) []string {
	keys := make([]string, len(batch.Items))
	for index := range batch.Items {
		keys[index] = promotionIdempotencyKey(batch.TargetRepositoryID, PromotionBatchItemKey(batch.ID, index))
	}
	return keys
}

// view computes the item states and the aggregate state from the provenance rows of the items.
func (s PromotionBatchService) view(ctx context.Context, batch repository.PromotionBatch) (PromotionBatchView, error) {
	rows, err := s.promotions.GetArtifactPromotionsByKeys(ctx, s.provenanceKeys(batch))
	if err != nil {
		return PromotionBatchView{}, err
	}
	view := PromotionBatchView{Batch: batch, Items: make([]PromotionBatchItemView, len(batch.Items))}
	states := make([]string, len(batch.Items))
	for index, item := range batch.Items {
		entry := PromotionBatchItemView{Index: index, Coordinate: item.Coordinate, Digest: item.Digest, State: promotionBatchPending}
		if row, ok := rows[promotionIdempotencyKey(batch.TargetRepositoryID, PromotionBatchItemKey(batch.ID, index))]; ok {
			entry.State, entry.PromotionID, entry.JobID = string(row.State), row.ID, row.JobID
		}
		view.Items[index], states[index] = entry, entry.State
	}
	view.State = aggregatePromotionBatchState(states)
	return view, nil
}

// aggregatePromotionBatchState folds item states into the batch state. An item is finished once it completed or ended
// without completing (failed, rejected, expired, cancelled); the others are unfinished. No item started (pending or
// queued) is pending; unfinished items with some progress are running; all finished is completed, failed or
// partially_failed.
func aggregatePromotionBatchState(states []string) string {
	completed, ended, started := 0, 0, 0
	for _, state := range states {
		switch repository.ArtifactPromotionState(state) {
		case repository.ArtifactPromotionCompleted:
			completed++
		case repository.ArtifactPromotionFailed, repository.ArtifactPromotionRejected, repository.ArtifactPromotionExpired, repository.ArtifactPromotionCancelled:
			ended++
		case repository.ArtifactPromotionRunning:
			started++
		}
	}
	switch {
	case completed == len(states):
		return promotionBatchCompleted
	case completed+ended == len(states) && completed == 0:
		return promotionBatchFailed
	case completed+ended == len(states):
		return promotionBatchPartiallyFailed
	case completed+ended+started == 0:
		return promotionBatchPending
	}
	return promotionBatchRunning
}
