package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var _ InternalTaskLeaseStore = (*PostgresStore)(nil)

// AcquireInternalTaskLease takes or renews the lease row in one statement. The row is created on first use. The
// interval check keys on last_run_at, which a completed run sets in ReleaseInternalTaskLease; the owner of the
// lease may always renew it, so a long run keeps its lease however young the last completion was.
func (s *PostgresStore) AcquireInternalTaskLease(ctx context.Context, task InternalTaskLease, owner string, ttl, minInterval time.Duration) (bool, error) {
	var held string
	err := s.db.QueryRowContext(ctx, `INSERT INTO scheduled_tasks (id,name,description,kind,interval_seconds,enabled,next_run_at,lease_owner,lease_until)
VALUES ($1::uuid,$2,$3,$4,900,false,now(),$5,now()+$6::bigint*interval '1 millisecond')
ON CONFLICT (id) DO UPDATE SET lease_owner=EXCLUDED.lease_owner,lease_until=EXCLUDED.lease_until
WHERE scheduled_tasks.lease_owner=EXCLUDED.lease_owner
   OR ((scheduled_tasks.lease_until IS NULL OR scheduled_tasks.lease_until<=now())
       AND (scheduled_tasks.last_run_at IS NULL OR scheduled_tasks.last_run_at<=now()-$7::bigint*interval '1 millisecond'))
RETURNING lease_owner`, task.ID, task.Name, task.Description, task.Kind, owner, ttl.Milliseconds(), minInterval.Milliseconds()).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return held == owner, nil
}

func (s *PostgresStore) ReleaseInternalTaskLease(ctx context.Context, task InternalTaskLease, owner string, completed bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE scheduled_tasks SET lease_owner='',lease_until=NULL,last_run_at=CASE WHEN $3 THEN now() ELSE last_run_at END WHERE id=$1::uuid AND lease_owner=$2`, task.ID, owner, completed)
	return err
}
