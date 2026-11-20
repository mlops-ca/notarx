package reseal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

// Actor is the system actor of a reseal run in security_audit_log.
const Actor = "cli:secrets-reseal"

// Operation is the audit operation of a reseal run.
const Operation = "secrets.reseal"

// ErrAuditMissing reports that sealed values may have been rewritten but the
// security audit record of the run could not be written.
var ErrAuditMissing = errors.New("the security audit record of this reseal run is missing")

// RunAudited is Run followed by one security_audit_log record for the run. A
// dry run writes nothing. When the record cannot be written, rows that were
// already resealed stay resealed (each row is its own compare-and-set) and the
// returned error wraps ErrAuditMissing, so the caller can say the audit is
// missing.
func RunAudited(ctx context.Context, db *sql.DB, dryRun bool) ([]Result, error) {
	return RunAuditedScoped(ctx, db, dryRun, Everything)
}

// RunAuditedScoped is RunAudited limited to the columns of scope; the audit record carries the scope's actor and operation.
func RunAuditedScoped(ctx context.Context, db *sql.DB, dryRun bool, scope Scope) ([]Result, error) {
	results, runErr := RunScoped(ctx, db, dryRun, scope)
	if dryRun {
		return results, runErr
	}
	if err := writeAudit(ctx, db, scope, results, runErr); err != nil {
		return results, errors.Join(runErr, fmt.Errorf("%w: %w", ErrAuditMissing, err))
	}
	return results, runErr
}

// auditRecord builds the record: key ids (never key material), the operator
// and the per-table counts of resealed and skipped rows. Skipped rows are the
// ones left as they were: already current or unreadable.
func auditRecord(results []Result, runErr error) (repository.AuditRecord, error) {
	return scopedAuditRecord(Everything, results, runErr)
}

func scopedAuditRecord(scope Scope, results []Result, runErr error) (repository.AuditRecord, error) {
	current, previous, err := secrets.KeyIDs()
	if err != nil {
		return repository.AuditRecord{}, err
	}
	osUser := "unknown"
	if account, err := user.Current(); err == nil && account.Username != "" {
		osUser = account.Username
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	status := "complete"
	switch {
	case errors.Is(runErr, ErrRowsFailed):
		status = "rows_failed"
	case runErr != nil:
		status = "aborted"
	}
	evidence := map[string]string{
		"os_user":          osUser,
		"host":             host,
		"current_key_id":   current,
		"previous_key_ids": strings.Join(previous, ","),
		"status":           status,
	}
	for _, result := range results {
		evidence["table:"+result.Target] = "resealed=" + strconv.Itoa(result.Resealed) +
			" skipped=" + strconv.Itoa(result.Unchanged+result.Failed) + " failed=" + strconv.Itoa(result.Failed)
	}
	audit := repository.ManagementAudit(scope.Actor, scope.Operation, scope.Resource, 200)
	audit.Evidence = evidence
	return audit, nil
}

func writeAudit(ctx context.Context, db *sql.DB, scope Scope, results []Result, runErr error) error {
	audit, err := scopedAuditRecord(scope, results, runErr)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := repository.InsertAuditsTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}
