package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var _ InternalTaskLeaseStore = (*PostgresStore)(nil)

// AcquireInternalTaskLease takes or renews the lease row of an internal job. The interval check keys on last_run_at,
// which a completed run sets in ReleaseInternalTaskLease; the owner of the lease may always renew it, so a long run
// keeps its lease however young the last completion was.
//
// The steady state of a node that does not win is one UPDATE that matches no row and so locks none: only the first use
// of a lease row inserts it, and only a node that can take the lease updates it.
func (s *PostgresStore) AcquireInternalTaskLease(ctx context.Context, task InternalTaskLease, owner string, ttl, minInterval time.Duration) (bool, error) {
	var held string
	err := s.db.QueryRowContext(ctx, `UPDATE scheduled_tasks SET lease_owner=$2,lease_until=now()+$3::bigint*interval '1 millisecond'
WHERE id=$1::uuid AND (lease_owner=$2
   OR ((lease_until IS NULL OR lease_until<=now()) AND (last_run_at IS NULL OR last_run_at<=now()-$4::bigint*interval '1 millisecond')))
RETURNING lease_owner`, task.ID, owner, ttl.Milliseconds(), minInterval.Milliseconds()).Scan(&held)
	if err == nil {
		return held == owner, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	// Either another node holds the lease or the row does not exist yet. Creating it makes the creator the holder; a
	// concurrent creator loses on the primary key and is not the holder.
	result, err := s.db.ExecContext(ctx, `INSERT INTO scheduled_tasks (id,name,description,kind,interval_seconds,enabled,next_run_at,lease_owner,lease_until)
VALUES ($1::uuid,$2,$3,$4,900,false,now(),$5,now()+$6::bigint*interval '1 millisecond')
ON CONFLICT (id) DO NOTHING`, task.ID, task.Name, task.Description, task.Kind, owner, ttl.Milliseconds())
	if err != nil {
		return false, err
	}
	inserted, _ := result.RowsAffected()
	return inserted == 1, nil
}

func (s *PostgresStore) ReleaseInternalTaskLease(ctx context.Context, task InternalTaskLease, owner string, completed bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE scheduled_tasks SET lease_owner='',lease_until=NULL,last_run_at=CASE WHEN $3 THEN now() ELSE last_run_at END WHERE id=$1::uuid AND lease_owner=$2`, task.ID, owner, completed)
	return err
}
