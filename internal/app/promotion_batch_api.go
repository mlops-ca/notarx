package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/repository"
)

// validPromotionItem reports whether coordinate and digest are a well-formed immutable artifact of the format: the same
// rules a single promotion applies.
func validPromotionItem(format repository.Format, coordinate, digest string, aptTargetSuite *string) bool {
	if !validAPTDistributionScope(format, coordinate, aptTargetSuite) || !validRepositoryDigest(digest) || !validMigratedCoordinate(format, coordinate) {
		return false
	}
	switch format {
	case repository.FormatMaven:
		return validMavenCoordinate(coordinate)
	case repository.FormatOCI:
		return coordinate != "" && !strings.Contains(coordinate, "@")
	case repository.FormatConan:
		return validConanReplicationCoordinate(coordinate)
	case repository.FormatNPM:
		return validNPMVersionCoordinate(coordinate)
	case repository.FormatPyPI:
		return validPyPIVersionCoordinate(coordinate)
	case repository.FormatGo:
		return validGoModuleVersionCoordinate(coordinate)
	case repository.FormatCargo:
		return validCargoVersionCoordinate(coordinate)
	}
	return true
}

// CreatePromotionBatch promotes up to 100 artifacts of one repository into another, all or nothing at admission
// (ADR 0021, "Batch and version sets"). The caller administers both repositories, as for a single promotion.
func (h generatedRepositoryAPIAdapter) CreatePromotionBatch(w http.ResponseWriter, r *http.Request, params adminopenapi.CreatePromotionBatchParams) {
	principal, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var request adminopenapi.PromotionBatchRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || len(request.Items) == 0 || len(request.Items) > repository.MaxPromotionBatchItems {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "sourceRepositoryId, targetRepositoryId and 1 to 100 items are required")
		return
	}
	h.withRepositoryScopeForPrincipal(w, r, principal, request.SourceRepositoryId.String(), RepositoryAdmin, func(_ Principal, source repository.HostedRepository) {
		service := newPromotionBatchService(h)
		if !service.SupportsFormat(source.Format) {
			writeHostedProblem(w, errUnsupportedPromotionFormat.Status, errUnsupportedPromotionFormat.Code, errUnsupportedPromotionFormat.Message)
			return
		}
		if !supportsAPTDistributionPreview(source, h.aptSnapshotPublisher, repository.RepositoryOperationPromote) {
			writeHostedProblem(w, http.StatusConflict, "unsupported_operation", "promotion is not supported for this repository format")
			return
		}
		h.withRepositoryScopeForPrincipal(w, r, principal, request.TargetRepositoryId.String(), RepositoryAdmin, func(Principal, repository.HostedRepository) {
			items := make([]repository.PromotionBatchItem, len(request.Items))
			for index, item := range request.Items {
				items[index] = repository.PromotionBatchItem{Coordinate: item.Coordinate, Digest: item.Digest}
				if item.AptTargetSuite != nil {
					items[index].AptTargetSuite = *item.AptTargetSuite
				}
			}
			view, perr := service.Create(r.Context(), PromotionBatchRequest{Principal: principal, Source: source, TargetID: request.TargetRepositoryId.String(), Items: items, IdempotencyKey: string(params.IdempotencyKey)})
			if perr != nil {
				writePromotionBatchProblem(w, perr)
				return
			}
			writeNativeMavenJSON(w, http.StatusAccepted, promotionBatchResponse(view))
		})
	})
}

// GetPromotionBatch shows a batch with its aggregate state to a caller that may read the target repository.
func (h generatedRepositoryAPIAdapter) GetPromotionBatch(w http.ResponseWriter, r *http.Request, batchID adminopenapi.PromotionBatchId) {
	service := newPromotionBatchService(h)
	if service.batches == nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "promotion batches are unavailable")
		return
	}
	principal, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	batch, err := service.batches.GetPromotionBatch(r.Context(), batchID.String())
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "promotion batch not found")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get promotion batch failed")
		return
	}
	h.withRepositoryScopeForPrincipal(w, r, principal, batch.TargetRepositoryID, RepositoryRead, func(Principal, repository.HostedRepository) {
		view, err := service.Get(r.Context(), batch.ID)
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get promotion batch failed")
			return
		}
		writeNativeMavenJSON(w, http.StatusOK, promotionBatchResponse(view))
	})
}

func writePromotionBatchProblem(w http.ResponseWriter, perr *PromotionBatchError) {
	if len(perr.Items) == 0 || perr.Code != "promotion_batch_rejected" {
		writeHostedProblem(w, perr.Status, perr.Code, perr.Message)
		return
	}
	items := make([]adminopenapi.PromotionBatchItemProblem, len(perr.Items))
	for index, item := range perr.Items {
		items[index] = adminopenapi.PromotionBatchItemProblem{Index: item.Index, Coordinate: item.Coordinate, Digest: item.Digest, Status: item.Status, Code: item.Code, Message: item.Message}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(perr.Status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(perr.Status), "status": perr.Status, "code": perr.Code, "message": perr.Message, "requestId": "", "items": items})
}

func promotionBatchResponse(view PromotionBatchView) adminopenapi.PromotionBatch {
	response := adminopenapi.PromotionBatch{
		Id: uuid.MustParse(view.Batch.ID), SourceRepositoryId: uuid.MustParse(view.Batch.SourceRepositoryID), TargetRepositoryId: uuid.MustParse(view.Batch.TargetRepositoryID),
		RequestedBy: view.Batch.RequestedBy, State: adminopenapi.PromotionBatchState(view.State), CreatedAt: view.Batch.CreatedAt,
		Items: make([]adminopenapi.PromotionBatchItem, 0, len(view.Items)),
	}
	for _, item := range view.Items {
		entry := adminopenapi.PromotionBatchItem{Index: item.Index, Coordinate: item.Coordinate, Digest: item.Digest, State: adminopenapi.PromotionBatchItemState(item.State)}
		if item.PromotionID != "" {
			id := uuid.MustParse(item.PromotionID)
			entry.PromotionId = &id
		}
		if item.JobID != "" {
			id := uuid.MustParse(item.JobID)
			entry.JobId = &id
		}
		response.Items = append(response.Items, entry)
	}
	return response
}
