package app

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/repository"
)

func (h generatedRepositoryAPIAdapter) GetRepositoryArtifactScanStatus(w http.ResponseWriter, r *http.Request, repositoryID adminopenapi.RepositoryId, params adminopenapi.GetRepositoryArtifactScanStatusParams) {
	h.withRepositoryScope(w, r, repositoryID.String(), RepositoryIntelligence, func(_ Principal, repo repository.HostedRepository) {
		if !validArtifactIntelligenceIdentity(repo.Format, params.Coordinate, params.Digest) {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "coordinate and sha256 digest must identify an immutable artifact")
			return
		}
		response := adminopenapi.ArtifactScanStatus{
			Coordinate: params.Coordinate,
			Digest:     params.Digest,
			State:      adminopenapi.ArtifactScanStatusStateNever,
		}
		job, err := h.lifecycleJobs.GetLatestArtifactScanJob(r.Context(), repo.ID, repo.Format, params.Coordinate, params.Digest)
		if errors.Is(err, repository.ErrNotFound) {
			writeNativeMavenJSON(w, http.StatusOK, response)
			return
		}
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "read artifact scan status failed")
			return
		}
		item := lifecycleJobResponse(job)
		response.State = adminopenapi.ArtifactScanStatusState(job.State)
		response.Job = &item
		writeNativeMavenJSON(w, http.StatusOK, response)
	})
}

func (h generatedRepositoryAPIAdapter) ReconcileRepositoryArtifactScans(w http.ResponseWriter, r *http.Request, repositoryID adminopenapi.RepositoryId, params adminopenapi.ReconcileRepositoryArtifactScansParams) {
	h.withRepositoryScope(w, r, repositoryID.String(), RepositoryIntelligence, func(principal Principal, repo repository.HostedRepository) {
		if h.artifactScanner == nil || !scanFormatEnabled(h.artifactScanFormats, repo.Format) || !publicationScanSupported(repo) {
			writeHostedProblem(w, http.StatusServiceUnavailable, "scanner_unavailable", "publication scanning is not configured for this repository format and type")
			return
		}
		candidatesStore, ok := h.lifecycleJobs.(repository.ArtifactScanCandidateStore)
		if !ok {
			writeHostedProblem(w, http.StatusNotImplemented, "not_supported", "artifact scan reconciliation is unavailable")
			return
		}
		limit := 500
		if params.Limit != nil {
			limit = *params.Limit
		}
		candidates, err := candidatesStore.ListArtifactScanCandidates(r.Context(), repo.ID, repo.Format, limit)
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list artifact scan candidates failed")
			return
		}
		result := adminopenapi.ArtifactScanReconciliation{
			RepositoryId: uuid.MustParse(repo.ID),
			Inspected:    len(candidates),
			JobIds:       make([]string, 0, len(candidates)),
		}
		for _, candidate := range candidates {
			// Every job it creates or requeues carries its own audit record, written in the
			// transaction of that change; a reconciliation that changes nothing is audited once below.
			audit := auditRecordsWhen(h.audit != nil, repository.AuditRecord{
				Repository: repo.Name, GroupName: repo.Name, Actor: principal.Actor,
				Resource: candidate.Coordinate, Representation: candidate.Digest, Operation: "artifact.scan.reconcile", Status: http.StatusOK,
			})
			process := func() error {
				job, getErr := h.lifecycleJobs.GetLatestArtifactScanJob(r.Context(), repo.ID, repo.Format, candidate.Coordinate, candidate.Digest)
				switch {
				case errors.Is(getErr, repository.ErrNotFound):
					var enqueueErr error
					job, _, enqueueErr = repository.EnqueueArtifactScanJobLocked(r.Context(), h.lifecycleJobs, repo.ID, publicationScanIdempotencyKey(repo.ID, repo.Format, candidate.Coordinate, candidate.Digest), repository.ArtifactScanPayload{
						Format: repo.Format, Coordinate: candidate.Coordinate, Digest: candidate.Digest,
					}, audit...)
					if enqueueErr == nil {
						result.Enqueued++
						result.JobIds = append(result.JobIds, job.ID)
					}
					return enqueueErr
				case getErr != nil:
					return getErr
				case job.State == repository.LifecycleJobFailed || job.State == repository.LifecycleJobCancelled:
					var retryErr error
					job, retryErr = h.lifecycleJobs.RetryLifecycleJob(r.Context(), repo.ID, job.ID, audit...)
					if errors.Is(retryErr, repository.ErrVersionConflict) {
						result.Skipped++
						return nil
					}
					if retryErr == nil {
						result.Retried++
						result.JobIds = append(result.JobIds, job.ID)
					}
					return retryErr
				default:
					result.Skipped++
					return nil
				}
			}
			if locker, ok := h.lifecycleJobs.(repository.ArtifactScanIdentityLockStore); ok {
				unlock, lockErr := locker.LockArtifactScanIdentity(r.Context(), repo.ID, repo.Format, candidate.Coordinate, candidate.Digest)
				if lockErr != nil {
					err = lockErr
				} else {
					err = process()
					unlock()
				}
			} else {
				err = process()
			}
			if err != nil {
				writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "reconcile artifact scan queue failed")
				return
			}
		}
		if result.Enqueued+result.Retried == 0 && h.audit != nil {
			// Nothing changed, so no store call carried an audit; the reconcile is still audited, and
			// a failure here is accurate because no change took effect.
			if !recordUnchangedAudit(w, h.audit, r.Context(), repository.AuditRecord{
				Repository: repo.Name, GroupName: repo.Name, Actor: principal.Actor,
				Resource: repo.Name, Operation: "artifact.scan.reconcile", Status: http.StatusOK,
			}) {
				return
			}
		}
		writeNativeMavenJSON(w, http.StatusOK, result)
	})
}
