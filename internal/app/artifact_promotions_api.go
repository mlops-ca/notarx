package app

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/repository"
)

// ListArtifactPromotions pages the provenance rows of promotions, newest first (ADR 0021, Provenance and approval). A
// platform administrator sees every target repository; any other caller sees the promotions into the repositories it
// may read, so a repository it cannot read contributes nothing.
func (h generatedRepositoryAPIAdapter) ListArtifactPromotions(w http.ResponseWriter, r *http.Request, params adminopenapi.ListArtifactPromotionsParams) {
	principal, ok := h.repositoryViewPrincipal(w, r)
	if !ok {
		return
	}
	query := repository.ArtifactPromotionQuery{Limit: 100}
	if params.Limit != nil {
		query.Limit = *params.Limit
	}
	if query.Limit < 1 || query.Limit > 500 {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 500")
		return
	}
	if params.PageToken != nil {
		query.PageToken = *params.PageToken
	}
	if params.TargetRepositoryId != nil {
		query.TargetRepositoryID = params.TargetRepositoryId.String()
	}
	if params.Format != nil {
		query.Format = repository.Format(*params.Format)
	}
	if params.State != nil {
		query.State = repository.ArtifactPromotionState(*params.State)
	}
	if params.Coordinate != nil {
		query.Coordinate = *params.Coordinate
	}
	if params.Digest != nil {
		query.Digest = *params.Digest
	}
	if !principal.Admin {
		ids, all, err := h.readableRepositoryIDs(r, principal)
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list artifact promotions failed")
			return
		}
		if !all {
			query.RepositoryIDs = ids
		}
	}
	page, err := h.sessions.store.PageArtifactPromotions(r.Context(), query)
	if errors.Is(err, repository.ErrInvalidPageToken) {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "pageToken is not valid for this listing")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list artifact promotions failed")
		return
	}
	response := adminopenapi.ArtifactPromotionPage{Items: make([]adminopenapi.ArtifactPromotion, 0, len(page.Items)), NextPageToken: optionalString(page.NextPageToken)}
	for _, promotion := range page.Items {
		response.Items = append(response.Items, artifactPromotionResponse(promotion))
	}
	writeNativeMavenJSON(w, http.StatusOK, response)
}

// readableRepositoryIDs lists the repositories the principal may read through a whole-repository grant, and reports
// whether that is every repository (a role that reads them all). Each candidate is authorized, so a grant that no
// longer decides and a blocked account drop out as they do everywhere else.
func (h generatedRepositoryAPIAdapter) readableRepositoryIDs(r *http.Request, principal Principal) (ids []string, all bool, err error) {
	if principal.AccountStateReason() != "" {
		return []string{}, false, nil
	}
	if authorization.RoleAllows(principal.Role, RepositoryRead) {
		return nil, true, nil
	}
	index, ok := h.store.(repository.RepositoryGrantIndex)
	if !ok {
		return nil, false, errGrantIndexUnavailable
	}
	seen := map[string]bool{}
	ids = []string{}
	// Read is allowed by the read, write and admin scopes (authorization.grantAllows).
	for _, scope := range []string{"repositories:read", "repositories:write", authorization.GrantScopeAdmin} {
		candidates, listErr := index.ListRepositoriesGrantedToPrincipals(r.Context(), principal.GrantPrincipals(), scope)
		if listErr != nil {
			return nil, false, listErr
		}
		for _, repo := range candidates {
			if !seen[repo.ID] && h.authorizer.Authorize(r.Context(), principal, repo, RepositoryRead).Allowed {
				seen[repo.ID] = true
				ids = append(ids, repo.ID)
			}
		}
	}
	return ids, false, nil
}

func artifactPromotionResponse(promotion repository.ArtifactPromotion) adminopenapi.ArtifactPromotion {
	item := adminopenapi.ArtifactPromotion{
		Id: uuid.MustParse(promotion.ID), TargetRepositoryId: uuid.MustParse(promotion.TargetRepositoryID), Format: adminopenapi.Format(promotion.Format),
		Coordinate: promotion.Coordinate, Digest: promotion.Digest, OriginRepository: promotion.OriginRepository,
		ChainPath: make([]adminopenapi.PromotionChainHop, 0, len(promotion.ChainPath)), RequestedBy: promotion.RequestedBy,
		State: adminopenapi.ArtifactPromotionState(promotion.State), Attempt: promotion.Attempt, IdempotencyKey: promotion.IdempotencyKey,
		Approvals: make([]adminopenapi.PromotionApproval, 0, len(promotion.Approvals)), CreatedAt: promotion.CreatedAt,
	}
	for _, hop := range promotion.ChainPath {
		item.ChainPath = append(item.ChainPath, adminopenapi.PromotionChainHop{Instance: hop.Instance, Repository: hop.Repository, RuleId: hop.RuleID, At: hop.At})
	}
	for _, approval := range promotion.Approvals {
		item.Approvals = append(item.Approvals, adminopenapi.PromotionApproval{Approver: approval.Approver, At: approval.At})
	}
	if promotion.OriginInstanceID != "" {
		id := uuid.MustParse(promotion.OriginInstanceID)
		item.OriginInstanceId = &id
	}
	if promotion.RuleID != "" {
		id := uuid.MustParse(promotion.RuleID)
		item.RuleId = &id
	}
	if promotion.RuleVersion != 0 {
		item.RuleVersion = &promotion.RuleVersion
	}
	if promotion.JobID != "" {
		id := uuid.MustParse(promotion.JobID)
		item.JobId = &id
	}
	if !promotion.CompletedAt.IsZero() {
		item.CompletedAt = &promotion.CompletedAt
	}
	return item
}
