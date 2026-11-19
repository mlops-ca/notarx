package app

import (
	"net/http"

	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/repository"
)

func (h generatedRepositoryAPIAdapter) ReconcileRepositoryArtifactIntelligence(w http.ResponseWriter, r *http.Request, repositoryID adminopenapi.RepositoryId, params adminopenapi.ReconcileRepositoryArtifactIntelligenceParams) {
	h.withRepositoryScope(w, r, repositoryID.String(), RepositoryAdmin, func(principal Principal, repo repository.HostedRepository) {
		limit := 100
		if params.Limit != nil {
			limit = *params.Limit
		}
		jobs, err := h.lifecycleJobs.RequeueFailedLifecycleJobs(r.Context(), repo.ID, repository.LifecycleJobIntelligence, limit, auditRecordsWhen(h.audit != nil, repository.AuditRecord{
			Repository: repo.Name,
			GroupName:  repo.Name,
			Actor:      principal.Actor,
			Resource:   "repositories/" + repo.ID + "/lifecycle-jobs:reconcile-intelligence",
			Operation:  "lifecycle.intelligence_reconcile",
			Status:     http.StatusOK,
		})...)
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "reconcile artifact intelligence jobs failed")
			return
		}
		jobIDs := make([]string, 0, len(jobs))
		for _, job := range jobs {
			jobIDs = append(jobIDs, job.ID)
		}
		writeNativeMavenJSON(w, http.StatusOK, adminopenapi.LifecycleJobReconciliation{
			RepositoryId:   repositoryID,
			Requeued:       len(jobs),
			RequeuedJobIds: jobIDs,
		})
	})
}
