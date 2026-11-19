package app

import (
	"context"
	"net/http"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

// mutationAuditRecorder is the audit port a management handler writes through.
type mutationAuditRecorder interface {
	RecordAudit(context.Context, repository.AuditRecord) error
}

// recordMutationAudit writes the audit of a management mutation after it committed and
// fails the request with 500 audit_failed when it cannot be written (ADR 0023 section 1).
// It reports whether the handler may continue.
//
// It is also how a request that changed nothing (an idempotent replay, a reconcile with no
// work) is audited, since there is no change for the audit to be atomic with.
//
// This is the legacy form: the change has already taken effect when the audit fails. New
// and migrated handlers pass the repository.AuditRecord to the store method that makes the
// change (a trailing optional argument; PostgreSQL writes it with insertAudit in the same
// transaction through runAudited, the memory store through commitAuditLocked), so a failed
// audit fails the change itself. The user and service account, settings, theme, email,
// webhook and scheduled task handlers, the local login, and the repository policy, access,
// quota alert rule, lifecycle job, quarantine, scan, intelligence and tombstone handlers are migrated
// (auditRecordsWhen builds their records); the remaining handlers follow in their own slices.
func recordMutationAudit(w http.ResponseWriter, recorder mutationAuditRecorder, ctx context.Context, record repository.AuditRecord) bool {
	if err := recorder.RecordAudit(ctx, record); err != nil {
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
