package app

import (
	"context"
	"net/http"

	"github.com/mlops-ca/notarx/internal/repository"
)

// mutationAuditRecorder is the audit port a management handler writes through.
type mutationAuditRecorder interface {
	RecordAudit(context.Context, repository.AuditRecord) error
}

// recordMutationAudit writes the audit of a management mutation and fails the
// request with 500 audit_failed when it cannot be written (ADR 0023 section 1).
// It reports whether the handler may continue.
func recordMutationAudit(w http.ResponseWriter, recorder mutationAuditRecorder, ctx context.Context, record repository.AuditRecord) bool {
	if err := recorder.RecordAudit(ctx, record); err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "audit_failed", "audit record could not be written")
		return false
	}
	return true
}
