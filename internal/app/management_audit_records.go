package app

import (
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

// auditRecordsWhen returns the one management audit record a handler hands to the store
// method that makes the change, completed with a resolved outcome, the current time, the
// management format and a bypassed cache, or no record when audit is not configured. The
// store writes it in the transaction of the change (ADR 0023 section 1).
func auditRecordsWhen(enabled bool, record repository.AuditRecord) []repository.AuditRecord {
	if !enabled {
		return nil
	}
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
	return []repository.AuditRecord{record}
}
