package app

import "github.com/mlops-ca/notarx/internal/repository"

// auditRecordsWhen returns the one management audit record a handler hands to the store
// method that makes the change, completed by managementAudit, or no record when audit is not
// configured. The store writes it in the transaction of the change (ADR 0023 section 1).
func auditRecordsWhen(enabled bool, record repository.AuditRecord) []repository.AuditRecord {
	if !enabled {
		return nil
	}
	return []repository.AuditRecord{managementAudit(record)}
}
