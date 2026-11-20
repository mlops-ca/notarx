package repository

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"
)

func (s *PostgresStore) CreateReplicationPlan(ctx context.Context, plan ReplicationPlan, checkpoints []ReplicationCheckpoint, audit ...AuditRecord) (ReplicationPlan, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReplicationPlan{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	err = scanReplicationPlan(tx.QueryRowContext(ctx, `
		INSERT INTO replication_plans (id, source_repository_id, target_repository_id, format, coordinate, digest, idempotency_key, state, next_attempt_at, max_attempts, apt_target_suite)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'pending',now(),LEAST(GREATEST(COALESCE(NULLIF($8, 0), 3), 1), 10),$9)
		ON CONFLICT (target_repository_id, idempotency_key) DO NOTHING
		RETURNING id::text,source_repository_id::text,target_repository_id::text,format,coordinate,digest,idempotency_key,state,created_at,started_at,completed_at,last_error,next_attempt_at,lease_expires_at,lease_token,attempts,max_attempts,apt_target_suite`,
		plan.ID, plan.SourceRepositoryID, plan.TargetRepositoryID, plan.Format, plan.Coordinate, plan.Digest, plan.IdempotencyKey, plan.MaxAttempts, plan.APTTargetSuite), &plan)
	if err == nil {
		for _, checkpoint := range checkpoints {
			sourceObjectKey := checkpoint.SourceObjectKey
			if sourceObjectKey == "" {
				sourceObjectKey = checkpoint.ObjectKey
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO replication_checkpoints (plan_id,source_object_key,object_key,digest,size,byte_offset,state,attempts,last_error) VALUES ($1,$2,$3,$4,$5,0,'pending',0,'')`, plan.ID, sourceObjectKey, checkpoint.ObjectKey, checkpoint.Digest, checkpoint.Size); err != nil {
				return ReplicationPlan{}, false, err
			}
		}
		if err = insertAudits(ctx, tx, audit); err != nil {
			return ReplicationPlan{}, false, err
		}
		if err = tx.Commit(); err != nil {
			return ReplicationPlan{}, false, err
		}
		return plan, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ReplicationPlan{}, false, err
	}

	var existing ReplicationPlan
	if err = scanReplicationPlan(tx.QueryRowContext(ctx, `SELECT id::text,source_repository_id::text,target_repository_id::text,format,coordinate,digest,idempotency_key,state,created_at,started_at,completed_at,last_error,next_attempt_at,lease_expires_at,lease_token,attempts,max_attempts,apt_target_suite FROM replication_plans WHERE target_repository_id=$1::uuid AND idempotency_key=$2 FOR UPDATE`, plan.TargetRepositoryID, plan.IdempotencyKey), &existing); err != nil {
		return ReplicationPlan{}, false, err
	}
	existingChecks, err := listReplicationCheckpoints(ctx, tx, existing.ID)
	if err != nil {
		return ReplicationPlan{}, false, err
	}
	samePlan := existing.SourceRepositoryID == plan.SourceRepositoryID && existing.TargetRepositoryID == plan.TargetRepositoryID && existing.Format == plan.Format && existing.Coordinate == plan.Coordinate && existing.Digest == plan.Digest && existing.APTTargetSuite == plan.APTTargetSuite
	replayablePark := existing.State == "failed" && (existing.LastError == ArtifactQuarantinedReason || (existing.Format == FormatPyPI && existing.LastError == ReplicationSnapshotChangedReason)) && existing.NextAttemptAt.IsZero() && existing.Attempts < existing.MaxAttempts
	refreshablePyPIReplay := samePlan && existing.Format == FormatPyPI && replayablePark
	if !samePlan || (!refreshablePyPIReplay && !equivalentReplicationCheckpoints(existingChecks, checkpoints)) {
		return ReplicationPlan{}, false, ErrIdempotencyConflict
	}
	if replayablePark {
		if refreshablePyPIReplay {
			if _, err = tx.ExecContext(ctx, `DELETE FROM replication_checkpoints WHERE plan_id=$1::uuid`, existing.ID); err != nil {
				return ReplicationPlan{}, false, err
			}
			for _, checkpoint := range checkpoints {
				sourceObjectKey := checkpoint.SourceObjectKey
				if sourceObjectKey == "" {
					sourceObjectKey = checkpoint.ObjectKey
				}
				if _, err = tx.ExecContext(ctx, `INSERT INTO replication_checkpoints (plan_id,source_object_key,object_key,digest,size,byte_offset,state,attempts,last_error,verified_at,updated_at) VALUES ($1,$2,$3,$4,$5,0,'pending',0,'',NULL,now())`, existing.ID, sourceObjectKey, checkpoint.ObjectKey, checkpoint.Digest, checkpoint.Size); err != nil {
					return ReplicationPlan{}, false, err
				}
			}
		}
		var requeued ReplicationPlan
		if err = scanReplicationPlan(tx.QueryRowContext(ctx, `
			UPDATE replication_plans
			SET state='pending',started_at=NULL,completed_at=NULL,last_error='',next_attempt_at=now(),lease_expires_at=NULL,lease_token=NULL
			WHERE id=$1::uuid AND state='failed' AND last_error=$2 AND next_attempt_at IS NULL AND attempts < max_attempts
			RETURNING id::text,source_repository_id::text,target_repository_id::text,format,coordinate,digest,idempotency_key,state,created_at,started_at,completed_at,last_error,next_attempt_at,lease_expires_at,lease_token,attempts,max_attempts,apt_target_suite`, existing.ID, existing.LastError), &requeued); err != nil {
			return ReplicationPlan{}, false, err
		}
		existing = requeued
		if err = insertAudits(ctx, tx, audit); err != nil {
			return ReplicationPlan{}, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return ReplicationPlan{}, false, err
	}
	return existing, true, nil
}

func (s *PostgresStore) ClaimReplicationPlans(ctx context.Context, limit int) ([]ReplicationPlan, error) {
	return s.claimReplicationPlans(ctx, nil, limit)
}

func (s *PostgresStore) ClaimReplicationPlansByFormat(ctx context.Context, format Format, limit int) ([]ReplicationPlan, error) {
	return s.claimReplicationPlans(ctx, []Format{format}, limit)
}

// ClaimReplicationPlansByFormats claims plans of any of formats in one claim (every format when empty); it is
// the call of the per-process replication claimer (lifecycle.ClaimBroker).
func (s *PostgresStore) ClaimReplicationPlansByFormats(ctx context.Context, formats []Format, limit int) ([]ReplicationPlan, error) {
	return s.claimReplicationPlans(ctx, formats, limit)
}

// claimReplicationPlans claims up to limit plans. A claim for several formats (the per-process claimer)
// follows the share plan of claimFormatShares, like the lifecycle claim, so a deep older backlog of one format
// cannot starve the others. The batches share one transaction.
func (s *PostgresStore) claimReplicationPlans(ctx context.Context, formats []Format, limit int) ([]ReplicationPlan, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if len(formats) <= 1 {
		return claimReplicationBatch(ctx, s.db, formatNames(formats), limit)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	plans := make([]ReplicationPlan, 0, limit)
	err = claimFormatShares(ctx, formats, limit, s.claimRotation.Add(1), func(ctx context.Context, _ formatClaimPhase, batch []Format, want int) ([]Format, bool, error) {
		more, err := claimReplicationBatch(ctx, tx, formatNames(batch), want)
		if err != nil {
			return nil, false, err
		}
		plans = append(plans, more...)
		claimed := make([]Format, len(more))
		for i, plan := range more {
			claimed[i] = plan.Format
		}
		return claimed, len(more) < want, nil
	})
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return plans, nil
}

func formatNames(formats []Format) []string {
	names := make([]string, len(formats))
	for i, format := range formats {
		names[i] = string(format)
	}
	return names
}

func claimReplicationBatch(ctx context.Context, q sqlRunner, names []string, limit int) ([]ReplicationPlan, error) {
	rows, err := q.QueryContext(ctx, claimReplicationPlansQuery, names, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	plans := make([]ReplicationPlan, 0, limit)
	for rows.Next() {
		var plan ReplicationPlan
		if err := scanReplicationPlan(rows, &plan); err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

func (s *PostgresStore) ListReplicationPlans(ctx context.Context, repositoryID string, limit int) ([]ReplicationPlan, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id::text,source_repository_id::text,target_repository_id::text,format,coordinate,digest,idempotency_key,state,created_at,started_at,completed_at,last_error,next_attempt_at,lease_expires_at,lease_token,attempts,max_attempts,apt_target_suite FROM replication_plans WHERE source_repository_id=$1::uuid OR target_repository_id=$1::uuid ORDER BY created_at DESC LIMIT $2`, repositoryID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	plans := make([]ReplicationPlan, 0, limit)
	for rows.Next() {
		var plan ReplicationPlan
		if err := scanReplicationPlan(rows, &plan); err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

func (s *PostgresStore) GetReplicationPlan(ctx context.Context, repositoryID, id string) (ReplicationPlan, error) {
	var plan ReplicationPlan
	err := scanReplicationPlan(s.db.QueryRowContext(ctx, `SELECT id::text,source_repository_id::text,target_repository_id::text,format,coordinate,digest,idempotency_key,state,created_at,started_at,completed_at,last_error,next_attempt_at,lease_expires_at,lease_token,attempts,max_attempts,apt_target_suite FROM replication_plans WHERE id=$1::uuid AND (source_repository_id=$2::uuid OR target_repository_id=$2::uuid)`, id, repositoryID), &plan)
	if errors.Is(err, sql.ErrNoRows) {
		return ReplicationPlan{}, ErrNotFound
	}
	return plan, err
}

func (s *PostgresStore) ListReplicationCheckpoints(ctx context.Context, planID string) ([]ReplicationCheckpoint, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM replication_plans WHERE id=$1::uuid)`, planID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	checks, err := listReplicationCheckpoints(ctx, s.db, planID)
	return checks, err
}

func (s *PostgresStore) UpdateReplicationCheckpointWithLease(ctx context.Context, checkpoint ReplicationCheckpoint, leaseToken string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE replication_checkpoints c
		SET byte_offset=$4,state=$5,attempts=$6,last_error=$7,verified_at=$8,updated_at=now()
		FROM replication_plans p
		WHERE c.plan_id=p.id AND c.plan_id=$1::uuid AND c.object_key=$2 AND p.state='running' AND p.lease_token=$3 AND p.lease_expires_at>now()`,
		checkpoint.PlanID, checkpoint.ObjectKey, leaseToken, checkpoint.ByteOffset, checkpoint.State, checkpoint.Attempts, checkpoint.LastError, nullableReplicationTime(checkpoint.VerifiedAt))
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE replication_plans SET lease_expires_at=now()+interval '10 minutes' WHERE id=$1::uuid AND lease_token=$2`, checkpoint.PlanID, leaseToken); err != nil {
		return err
	}
	return nil
}

func (s *PostgresStore) RenewReplicationPlanLease(ctx context.Context, id, leaseToken string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE replication_plans SET lease_expires_at=clock_timestamp()+interval '10 minutes' WHERE id=$1::uuid AND state='running' AND lease_token=$2 AND lease_expires_at>clock_timestamp()`, id, leaseToken)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

// ReleaseReplicationPlanLease hands a claimed plan that never ran back to the queue: it is pending and
// claimable at once, and the attempt its claim counted is returned. A lease that is no longer the caller's, or
// has expired, is ErrNotFound.
func (s *PostgresStore) ReleaseReplicationPlanLease(ctx context.Context, id, leaseToken string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE replication_plans SET state='pending',started_at=NULL,next_attempt_at=NULL,lease_expires_at=NULL,lease_token=NULL,attempts=GREATEST(attempts-1,0) WHERE id=$1::uuid AND state='running' AND lease_token=$2 AND lease_expires_at>clock_timestamp()`, id, leaseToken)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) LockReplicationPlanLease(ctx context.Context, id, leaseToken string) (func(), error) {
	_, release, err := s.lockPostgresAdvisoryKeys(ctx, []string{"replication-plan-lease:" + id})
	if err != nil {
		return nil, err
	}
	var valid bool
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM replication_plans WHERE id=$1::uuid AND state='running' AND lease_token=$2 AND lease_expires_at>clock_timestamp())`, id, leaseToken).Scan(&valid); err != nil {
		release()
		return nil, err
	}
	if !valid {
		release()
		return nil, ErrNotFound
	}
	return release, nil
}

func (s *PostgresStore) CompleteReplicationPlanWithLease(ctx context.Context, id, leaseToken string) error {
	return s.finishReplicationPlan(ctx, id, "completed", "", leaseToken)
}

func (s *PostgresStore) FailReplicationPlanWithLease(ctx context.Context, id, message, leaseToken string) error {
	return s.finishReplicationPlan(ctx, id, "failed", message, leaseToken)
}

func (s *PostgresStore) ParkReplicationPlanWithLease(ctx context.Context, id, message, leaseToken string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE replication_plans SET state='failed',completed_at=now(),last_error=$3,lease_expires_at=NULL,lease_token=NULL,next_attempt_at=NULL,attempts=GREATEST(attempts-1,0) WHERE id=$1::uuid AND state='running' AND lease_token=$2 AND lease_expires_at>now()`, id, leaseToken, message)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) finishReplicationPlan(ctx context.Context, id, state, message, leaseToken string) error {
	if state == "failed" {
		result, err := s.db.ExecContext(ctx, `UPDATE replication_plans SET state='failed',completed_at=now(),last_error=$3,lease_expires_at=NULL,lease_token=NULL,next_attempt_at=CASE WHEN attempts < max_attempts THEN now() ELSE NULL END WHERE id=$1::uuid AND state='running' AND lease_token=$2 AND lease_expires_at>now()`, id, leaseToken, message)
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return ErrNotFound
		}
		return nil
	}
	result, err := s.db.ExecContext(ctx, `UPDATE replication_plans SET state=$2,completed_at=now(),last_error=$3,lease_expires_at=NULL,lease_token=NULL,next_attempt_at=NULL WHERE id=$1::uuid AND state='running' AND lease_token=$4 AND lease_expires_at>now()`, id, state, message, leaseToken)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) RecoverExpiredReplicationPlans(ctx context.Context, before time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `WITH candidates AS (
		SELECT id FROM replication_plans
		WHERE state='running' AND lease_expires_at IS NOT NULL AND lease_expires_at <= $1
		AND pg_try_advisory_xact_lock(hashtextextended(concat('replication-plan-lease:', id), 0))
		FOR UPDATE SKIP LOCKED
	) UPDATE replication_plans p SET state='failed',last_error='replication worker lease expired',lease_expires_at=NULL,lease_token=NULL,next_attempt_at=CASE WHEN attempts < max_attempts THEN $1 ELSE NULL END
	FROM candidates WHERE p.id=candidates.id`, before)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	return int(count), nil
}

// CancelReplicationPlan stops a pending or failed plan from being claimed again.
// Running plans are not cancellable because the worker owns them mid-flight;
// the caller is expected to have checked the state, so any zero-row result
// (missing or not pending/failed) is reported as ErrNotFound.
func (s *PostgresStore) CancelReplicationPlan(ctx context.Context, repositoryID, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE replication_plans SET state='cancelled',completed_at=now(),last_error='' WHERE id=$1::uuid AND (source_repository_id=$2::uuid OR target_repository_id=$2::uuid) AND state IN ('pending','failed')`, id, repositoryID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

type replicationPlanScanner interface{ Scan(...any) error }

func scanReplicationPlan(scanner replicationPlanScanner, plan *ReplicationPlan) error {
	var startedAt, completedAt, nextAttemptAt, leaseExpiresAt sql.NullTime
	var leaseToken sql.NullString
	if err := scanner.Scan(&plan.ID, &plan.SourceRepositoryID, &plan.TargetRepositoryID, &plan.Format, &plan.Coordinate, &plan.Digest, &plan.IdempotencyKey, &plan.State, &plan.CreatedAt, &startedAt, &completedAt, &plan.LastError, &nextAttemptAt, &leaseExpiresAt, &leaseToken, &plan.Attempts, &plan.MaxAttempts, &plan.APTTargetSuite); err != nil {
		return err
	}
	if startedAt.Valid {
		plan.StartedAt = startedAt.Time
	}
	if completedAt.Valid {
		plan.CompletedAt = completedAt.Time
	}
	if nextAttemptAt.Valid {
		plan.NextAttemptAt = nextAttemptAt.Time
	}
	if leaseExpiresAt.Valid {
		plan.LeaseExpiresAt = leaseExpiresAt.Time
	}
	if leaseToken.Valid {
		plan.LeaseToken = leaseToken.String
	}
	return nil
}

type replicationCheckpointScanner interface{ Scan(...any) error }

func scanReplicationCheckpoint(scanner replicationCheckpointScanner, checkpoint *ReplicationCheckpoint) error {
	var verifiedAt sql.NullTime
	if err := scanner.Scan(&checkpoint.PlanID, &checkpoint.SourceObjectKey, &checkpoint.ObjectKey, &checkpoint.Digest, &checkpoint.Size, &checkpoint.ByteOffset, &checkpoint.State, &checkpoint.Attempts, &checkpoint.LastError, &verifiedAt, &checkpoint.UpdatedAt); err != nil {
		return err
	}
	if verifiedAt.Valid {
		checkpoint.VerifiedAt = verifiedAt.Time
	}
	return nil
}

type replicationCheckpointQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listReplicationCheckpoints(ctx context.Context, query replicationCheckpointQuery, planID string) ([]ReplicationCheckpoint, error) {
	rows, err := query.QueryContext(ctx, `SELECT plan_id::text,source_object_key,object_key,digest,size,byte_offset,state,attempts,last_error,verified_at,updated_at FROM replication_checkpoints WHERE plan_id=$1::uuid ORDER BY object_key`, planID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	checks := []ReplicationCheckpoint{}
	for rows.Next() {
		var checkpoint ReplicationCheckpoint
		if err := scanReplicationCheckpoint(rows, &checkpoint); err != nil {
			return nil, err
		}
		checks = append(checks, checkpoint)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return checks, nil
}

func nullableReplicationTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func equivalentReplicationCheckpoints(existing, requested []ReplicationCheckpoint) bool {
	if len(existing) != len(requested) {
		return false
	}
	want := append([]ReplicationCheckpoint(nil), requested...)
	sort.Slice(want, func(i, j int) bool { return want[i].ObjectKey < want[j].ObjectKey })
	for i, got := range existing {
		gotSource := got.SourceObjectKey
		if gotSource == "" {
			gotSource = got.ObjectKey
		}
		wantSource := want[i].SourceObjectKey
		if wantSource == "" {
			wantSource = want[i].ObjectKey
		}
		if gotSource != wantSource || got.ObjectKey != want[i].ObjectKey || got.Digest != want[i].Digest || got.Size != want[i].Size {
			return false
		}
	}
	return true
}

// claimReplicationPlansQuery takes due replication plans (format list, empty for any, and limit); the hot-query
// registry plans it.
const claimReplicationPlansQuery = `
		WITH candidates AS (
			SELECT id FROM replication_plans WHERE ((state='pending' AND (next_attempt_at IS NULL OR next_attempt_at <= now())) OR (state='failed' AND next_attempt_at IS NOT NULL AND next_attempt_at <= now())) AND attempts < CASE WHEN max_attempts < 1 THEN 3 ELSE max_attempts END AND (cardinality($1::text[])=0 OR format=ANY($1::text[])) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT $2
		)
		UPDATE replication_plans p SET state='running',started_at=now(),completed_at=NULL,last_error='',next_attempt_at=NULL,lease_expires_at=now()+interval '10 minutes',lease_token=md5(random()::text || clock_timestamp()::text || p.id::text),attempts=attempts+1,max_attempts=CASE WHEN max_attempts < 1 THEN 3 ELSE max_attempts END
		FROM candidates WHERE p.id=candidates.id
		RETURNING p.id::text,p.source_repository_id::text,p.target_repository_id::text,p.format,p.coordinate,p.digest,p.idempotency_key,p.state,p.created_at,p.started_at,p.completed_at,p.last_error,p.next_attempt_at,p.lease_expires_at,p.lease_token,p.attempts,p.max_attempts,p.apt_target_suite`
