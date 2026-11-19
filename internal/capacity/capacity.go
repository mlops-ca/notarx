package capacity

import (
	"context"
	"database/sql"
	"errors"
)

// ErrQuotaExceeded is returned by Reserve when a positive delta would take a
// repository over its quota. The capacity triggers that once raised the same message were
// dropped before the baseline was re-squashed; Reserve is the only enforcement.
var ErrQuotaExceeded = errors.New("repository capacity quota exceeded")

// Execer is the part of *sql.Tx (or *sql.DB) Reserve needs.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ReserveStatement is the guarded counter update of ADR 0023 section 3. A negative or zero
// delta is never rejected, so deletion and tombstoning always release bytes; the
// counter never goes below zero.
const ReserveStatement = `UPDATE repository_usage_counters
   SET bytes = GREATEST(0, bytes + $2::bigint), objects = GREATEST(0, objects + $3::bigint), updated_at = now()
 WHERE repository_id = $1::uuid
   AND ($2::bigint <= 0 OR quota_bytes IS NULL OR quota_bytes = 0 OR bytes + $2::bigint <= quota_bytes)`

// ensureSQL creates the counter row of a repository that has none yet (a repository
// created after the backfill). Its usage starts at zero: a repository without a
// counter row has had no counted write. The quota comes from the quota table.
const ensureSQL = `INSERT INTO repository_usage_counters(repository_id,bytes,objects,quota_bytes,updated_at)
SELECT h.id,0,0,NULLIF((SELECT q.quota_bytes FROM repository_capacity_quotas q WHERE q.repository_id=h.id),0),now()
  FROM hosted_repositories h WHERE h.id=$1::uuid
ON CONFLICT (repository_id) DO NOTHING`

// Reserve adds deltaBytes and deltaObjects to the repository's usage counter and
// enforces the quota in the same statement. Call it as the last statement of the
// publishing transaction (short lock hold). A positive delta that would exceed
// the quota returns ErrQuotaExceeded and changes nothing. The caller owns the
// transaction and must roll it back on error.
func Reserve(ctx context.Context, tx Execer, repositoryID string, deltaBytes, deltaObjects int64) error {
	// seen is true once a SELECT has found the counter row: the UPDATE that follows it ran with
	// the row visible, so only then does zero affected rows mean the quota guard rejected the delta.
	seen := false
	for attempt := 0; attempt < 3; attempt++ {
		result, err := tx.ExecContext(ctx, ReserveStatement, repositoryID, deltaBytes, deltaObjects)
		if err != nil {
			return err
		}
		if rows, err := result.RowsAffected(); err != nil {
			return err
		} else if rows > 0 {
			return nil
		}
		var present int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM repository_usage_counters WHERE repository_id=$1::uuid`, repositoryID).Scan(&present)
		switch {
		case err == nil:
			// The row exists. The UPDATE above may have run before a concurrent writer committed it
			// (zero rows for that reason), so the guard is blamed only after an UPDATE that followed
			// a SELECT which saw the row.
			if seen {
				return ErrQuotaExceeded
			}
			seen = true
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if attempt == 2 {
			break
		}
		// No counter row yet (a repository created after the backfill). A concurrent writer may
		// create it first and DO NOTHING then affects no row, so the next attempt, not this
		// statement, decides whether the repository exists.
		if _, err = tx.ExecContext(ctx, ensureSQL, repositoryID); err != nil {
			return err
		}
	}
	return sql.ErrNoRows // the repository does not exist
}

// Headroom reports ErrQuotaExceeded when extraBytes more would take the repository over its
// quota, without changing the counter. It is the pre-flight of an operation that reserves its
// bytes later, in smaller steps (a replication job reserves each item as it runs); the
// admission itself is Reserve. The comparison mirrors ReserveStatement's guard (a NULL or zero quota
// is unlimited); change both together. A repository without a counter row or quota never fails.
func Headroom(ctx context.Context, q Execer, repositoryID string, extraBytes int64) error {
	var bytes int64
	var quota sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT bytes,quota_bytes FROM repository_usage_counters WHERE repository_id=$1::uuid`, repositoryID).Scan(&bytes, &quota)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if extraBytes > 0 && quota.Valid && quota.Int64 > 0 && bytes+extraBytes > quota.Int64 {
		return ErrQuotaExceeded
	}
	return nil
}

// SetQuota records the quota in the counter row, creating the row when needed. The
// quota API calls it in the transaction that writes repository_capacity_quotas;
// a zero quota (unlimited) is stored as NULL.
func SetQuota(ctx context.Context, tx Execer, repositoryID string, quotaBytes int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO repository_usage_counters(repository_id,bytes,objects,quota_bytes,updated_at)
VALUES ($1::uuid,0,0,NULLIF($2::bigint,0),now())
ON CONFLICT (repository_id) DO UPDATE SET quota_bytes=NULLIF($2::bigint,0), updated_at=now()`, repositoryID, quotaBytes)
	return err
}
