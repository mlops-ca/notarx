package repository

import (
	"context"
	"database/sql"
)

// sqlRunner is the part of *sql.DB and *sql.Tx the audited store methods use, so
// one body runs on the pool or inside the audit transaction.
type sqlRunner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// runAudited is the convention of ADR 0023 section 1 for management mutations: a store
// method that a management handler calls takes the AuditRecord(s) to write with the
// change as a trailing optional argument. With audit, fn and the audit inserts share
// one transaction, so a failing audit insert rolls the change back and the caller gets
// the error instead of a change that took effect. Without audit, fn runs on the pool.
func runAudited[T any](ctx context.Context, s *PostgresStore, audit []AuditRecord, fn func(sqlRunner) (T, error)) (T, error) {
	var zero T
	if len(audit) == 0 {
		return fn(s.db)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	value, err := fn(tx)
	if err != nil {
		return zero, err
	}
	if err = insertAudits(ctx, tx, audit); err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return value, nil
}

// insertAudits writes the audit records inside the caller's transaction.
func insertAudits(ctx context.Context, execer auditExecer, audit []AuditRecord) error {
	for _, record := range audit {
		if err := insertAudit(ctx, execer, record); err != nil {
			return err
		}
	}
	return nil
}

// SetAuditFault makes the memory store fail the audit of a mutation when fn returns
// an error for its record, so tests can prove a mutation does not take effect when
// its audit cannot be written (the memory twin of a failing security_audit_log insert).
func (s *MemoryStore) SetAuditFault(fn func(AuditRecord) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditFault = fn
}

// commitAuditLocked records the audit of a mutation atomically with it: call it after
// every validation and before the first state change, holding s.mu. When the audit
// cannot be written the caller returns the error and has changed nothing.
func (s *MemoryStore) commitAuditLocked(ctx context.Context, audit []AuditRecord) error {
	records := make([]AuditRecord, len(audit))
	for index := range audit {
		records[index] = withRequestContextAudit(ctx, audit[index])
	}
	for _, record := range records {
		if s.auditFault != nil {
			if err := s.auditFault(record); err != nil {
				return err
			}
		}
	}
	for _, record := range records {
		s.appendAuditLocked(record)
	}
	return nil
}
