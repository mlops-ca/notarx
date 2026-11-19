package app

import (
	"context"
	"net/http"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

// recordUnchangedAudit audits a management request that changed nothing (an idempotent
// replay, a reconcile with no work) and fails the request with 500 audit_failed when the audit
// cannot be written. There is no change for the audit to be atomic with, so the answer is
// accurate: nothing took effect. The record is raw: the helper completes it with managementAudit,
// so callers do not wrap it with auditRecordsWhen. It reports whether the handler may continue.
//
// A request that changes state never calls it. It passes the repository.AuditRecord to the
// store method that makes the change (a trailing optional argument; PostgreSQL writes it with
// insertAudit in the same transaction through runAudited, the memory store through
// commitAuditLocked), so a failed audit fails the change itself and the handler answers with
// the mutation's own error (ADR 0023 section 1). auditRecordsWhen builds those records, and
// tools/architecture rejects the retired post-commit recordMutationAudit.
func recordUnchangedAudit(w http.ResponseWriter, recorder managementAuditRecorder, ctx context.Context, record repository.AuditRecord) bool {
	if err := recorder.RecordAudit(ctx, managementAudit(record)); err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "audit_failed", "audit record could not be written")
		return false
	}
	return true
}

// managementAudit completes a management audit record: resolved outcome, the current time,
// the management format and a bypassed cache. Every management audit goes through it.
func managementAudit(record repository.AuditRecord) repository.AuditRecord {
	if record.Outcome == "" {
		record.Outcome = repository.AuditResolved
	}
	if record.OccurredAt.IsZero() {
		record.OccurredAt = time.Now().UTC()
	}
	if record.Format == "" {
		record.Format = "management"
	}
	if record.CacheDisposition == "" {
		record.CacheDisposition = "bypass"
	}
	return record
}

// auditFor returns the audit records a management handler hands to the store method that
// makes the change, or none when audit is not configured.
func (h generatedRepositoryAPIAdapter) auditFor(record repository.AuditRecord) []repository.AuditRecord {
	if h.audit == nil {
		return nil
	}
	return []repository.AuditRecord{managementAudit(record)}
}
