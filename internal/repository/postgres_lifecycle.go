package repository

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/mlops-ca/notarx/internal/requestcontext"
)

// Hot queries are named so the integration tests can EXPLAIN the production statement.
const (
	listLifecycleJobsQuery = `SELECT ` + lifecycleJobColumns + ` FROM lifecycle_jobs WHERE repository_id=$1::uuid ORDER BY created_at DESC LIMIT $2`
	getLifecycleJobQuery   = `SELECT ` + lifecycleJobColumns + ` FROM lifecycle_jobs WHERE repository_id=$1::uuid AND id=$2::uuid`
)

const lifecycleJobColumns = `id::text,repository_id::text,kind,idempotency_key,payload,state,created_at,started_at,completed_at,last_error,attempts,max_attempts,next_attempt_at,lease_expires_at,lease_token,progress_current,progress_total,progress_message`

// EnqueueLifecycleJob inserts the job, or returns the job of the same idempotency key. With audit, a
// newly inserted job and its audit record commit together (ADR 0023 section 1); a replay of the key
// changes nothing and writes no audit.
func (s *PostgresStore) EnqueueLifecycleJob(ctx context.Context, job LifecycleJob, audit ...AuditRecord) (LifecycleJob, bool, error) {
	if len(audit) == 0 {
		return enqueueLifecycleJob(ctx, s.db, job)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LifecycleJob{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	stored, replayed, err := enqueueLifecycleJob(ctx, tx, job)
	if err != nil {
		return LifecycleJob{}, false, err
	}
	if !replayed {
		if err = insertAudits(ctx, tx, audit); err != nil {
			return LifecycleJob{}, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return LifecycleJob{}, false, err
	}
	return stored, replayed, nil
}

func enqueueLifecycleJob(ctx context.Context, q sqlRunner, job LifecycleJob) (LifecycleJob, bool, error) {
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = DefaultLifecycleJobMaxAttempts
	}
	if job.ProgressTotal <= 0 && job.Kind != LifecycleJobRetention {
		job.ProgressTotal = 1
	}
	err := scanLifecycleJob(q.QueryRowContext(ctx, `INSERT INTO lifecycle_jobs (id,repository_id,kind,idempotency_key,state,payload,max_attempts,next_attempt_at,progress_total) VALUES ($1,$2,$3,$4,'pending',$5,$6,now(),$7) ON CONFLICT (repository_id,kind,idempotency_key) DO NOTHING RETURNING `+lifecycleJobColumns, job.ID, job.RepositoryID, job.Kind, job.IdempotencyKey, job.Payload, job.MaxAttempts, job.ProgressTotal), &job)
	if err == nil {
		return job, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return LifecycleJob{}, false, err
	}
	var existing LifecycleJob
	err = scanLifecycleJob(q.QueryRowContext(ctx, `SELECT `+lifecycleJobColumns+` FROM lifecycle_jobs WHERE repository_id=$1::uuid AND kind=$2 AND idempotency_key=$3`, job.RepositoryID, job.Kind, job.IdempotencyKey), &existing)
	if err != nil {
		return LifecycleJob{}, false, err
	}
	if !equivalentLifecyclePayload(existing.Payload, job.Payload) {
		return LifecycleJob{}, false, ErrIdempotencyConflict
	}
	return existing, true, nil
}

func (s *PostgresStore) ListLifecycleJobs(ctx context.Context, repositoryID string, limit int) ([]LifecycleJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, listLifecycleJobsQuery, repositoryID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	jobs := make([]LifecycleJob, 0, limit)
	for rows.Next() {
		var job LifecycleJob
		if err := scanLifecycleJob(rows, &job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, job := range jobs {
		jobCtx, ids := requestcontext.WithRequest(ctx, job.ID)
		slog.LogAttrs(jobCtx, slog.LevelInfo, "lifecycle job claimed",
			slog.String("component", "lifecycle_worker"), slog.String("operation", "lifecycle.claim"),
			slog.String("requestId", ids.RequestID), slog.String("traceId", ids.TraceID),
			slog.String("jobId", job.ID), slog.String("kind", string(job.Kind)), slog.String("repositoryId", job.RepositoryID), slog.Int("attempt", job.Attempts),
		)
	}
	return jobs, nil
}

func (s *PostgresStore) ListAllLifecycleJobs(ctx context.Context, limit int) ([]RepositoryLifecycleJob, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `SELECT h.name,`+prefixedLifecycleJobColumns("jobs")+`
		FROM lifecycle_jobs jobs
		JOIN hosted_repositories h ON h.id=jobs.repository_id
		ORDER BY jobs.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	records := make([]RepositoryLifecycleJob, 0, limit)
	for rows.Next() {
		var record RepositoryLifecycleJob
		if err := scanRepositoryLifecycleJob(rows, &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *PostgresStore) GetLifecycleJob(ctx context.Context, repositoryID, id string) (LifecycleJob, error) {
	var job LifecycleJob
	err := scanLifecycleJob(s.db.QueryRowContext(ctx, getLifecycleJobQuery, repositoryID, id), &job)
	if errors.Is(err, sql.ErrNoRows) {
		return LifecycleJob{}, ErrNotFound
	}
	return job, err
}

func (s *PostgresStore) GetLatestArtifactScanJob(ctx context.Context, repositoryID string, format Format, coordinate, digest string) (LifecycleJob, error) {
	var job LifecycleJob
	err := scanLifecycleJob(s.db.QueryRowContext(ctx, `SELECT `+lifecycleJobColumns+`
		FROM lifecycle_jobs
		WHERE repository_id=$1::uuid AND kind='scan'
		  AND payload->>'format'=$2 AND payload->>'coordinate'=$3 AND payload->>'digest'=$4
		ORDER BY created_at DESC,id DESC LIMIT 1`, repositoryID, format, coordinate, digest), &job)
	if errors.Is(err, sql.ErrNoRows) {
		return LifecycleJob{}, ErrNotFound
	}
	return job, err
}

func (s *PostgresStore) ClaimLifecycleJobs(ctx context.Context, limit int) ([]LifecycleJob, error) {
	return s.claimLifecycleJobs(ctx, "", "", limit)
}

func (s *PostgresStore) ClaimLifecycleJobsByKind(ctx context.Context, kind LifecycleJobKind, limit int) ([]LifecycleJob, error) {
	return s.claimLifecycleJobs(ctx, kind, "", limit)
}

func (s *PostgresStore) ClaimLifecycleJobsByKindAndFormat(ctx context.Context, kind LifecycleJobKind, format Format, limit int) ([]LifecycleJob, error) {
	return s.claimLifecycleJobs(ctx, kind, format, limit)
}

// claimLifecycleJobs claims up to limit actionable jobs (ADR 0023 section 7). Candidates come from the
// actionable partial index in next_attempt_at order with FOR UPDATE SKIP LOCKED, so nothing is sorted and no
// DISTINCT ON runs. The per-repository advisory lock is taken after the LIMIT, only for rows already
// selected; the row is then marked running and the running set of the repository is probed. A row over the
// kind's RepoConcurrency is rolled back to its candidate state (a savepoint, so no notification and no
// attempt is consumed) and excluded from later rounds, which keeps a hot repository from starving the
// others. The scan stops at QueueClaimScanCap examined rows.
func (s *PostgresStore) claimLifecycleJobs(ctx context.Context, kind LifecycleJobKind, format Format, limit int) ([]LifecycleJob, error) {
	if limit <= 0 {
		limit = 100
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var (
		claimed  = make([]LifecycleJob, 0, limit)
		skipped  = []string{}
		scanned  int
		finished bool
	)
	for !finished && len(claimed) < limit && scanned < QueueClaimScanCap {
		want := limit - len(claimed)
		if remaining := QueueClaimScanCap - scanned; want > remaining {
			want = remaining
		}
		candidates, err := selectLifecycleCandidates(ctx, tx, kind, format, skipped, want)
		if err != nil {
			return nil, err
		}
		scanned += len(candidates)
		finished = len(candidates) < want
		for _, candidate := range candidates {
			job, ok, err := claimLifecycleCandidate(ctx, tx, candidate)
			if err != nil {
				return nil, err
			}
			if !ok {
				skipped = append(skipped, candidate.id)
				continue
			}
			claimed = append(claimed, job)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.claimObserver.record(kind, scanned)
	return claimed, nil
}

// The candidate scans of the claim. A claim for one kind (every runtime) is served by
// lifecycle_jobs_kind_actionable_idx (kind, next_attempt_at) in order; one for all kinds
// (ClaimLifecycleJobs) by lifecycle_jobs_actionable_idx. Neither sorts the pending set, which is why the
// order is next_attempt_at alone: a tie-breaker outside the index would need a sort. The integration
// test asserts both plans.
const (
	claimLifecycleCandidatesByKindQuery = `SELECT id::text,repository_id::text,kind FROM lifecycle_jobs
        WHERE state IN ('pending','retrying') AND next_attempt_at<=now() AND attempts<max_attempts
          AND kind=$1 AND ($2='' OR payload->>'format'=$2) AND id<>ALL($3::text[]::uuid[])
        ORDER BY next_attempt_at LIMIT $4 FOR UPDATE SKIP LOCKED`
	claimLifecycleCandidatesAnyKindQuery = `SELECT id::text,repository_id::text,kind FROM lifecycle_jobs
        WHERE state IN ('pending','retrying') AND next_attempt_at<=now() AND attempts<max_attempts
          AND ($1='' OR payload->>'format'=$1) AND id<>ALL($2::text[]::uuid[])
        ORDER BY next_attempt_at LIMIT $3 FOR UPDATE SKIP LOCKED`
)

type lifecycleCandidate struct {
	id           string
	repositoryID string
	kind         LifecycleJobKind
}

func selectLifecycleCandidates(ctx context.Context, tx *sql.Tx, kind LifecycleJobKind, format Format, skipped []string, limit int) ([]lifecycleCandidate, error) {
	var rows *sql.Rows
	var err error
	if kind != "" {
		rows, err = tx.QueryContext(ctx, claimLifecycleCandidatesByKindQuery, string(kind), string(format), skipped, limit)
	} else {
		rows, err = tx.QueryContext(ctx, claimLifecycleCandidatesAnyKindQuery, string(format), skipped, limit)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var candidates []lifecycleCandidate
	for rows.Next() {
		var candidate lifecycleCandidate
		if err = rows.Scan(&candidate.id, &candidate.repositoryID, &candidate.kind); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// claimLifecycleCandidate takes the repository lock, marks the row running and probes the running set. It
// reports false, with the row left exactly as selected, when the repository has no room for the kind or
// another claimer holds the repository lock.
func claimLifecycleCandidate(ctx context.Context, tx *sql.Tx, candidate lifecycleCandidate) (LifecycleJob, bool, error) {
	var locked bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))`, candidate.repositoryID).Scan(&locked); err != nil {
		return LifecycleJob{}, false, err
	}
	if !locked {
		return LifecycleJob{}, false, nil
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT lifecycle_claim`); err != nil {
		return LifecycleJob{}, false, err
	}
	var job LifecycleJob
	err := scanLifecycleJob(tx.QueryRowContext(ctx, `UPDATE lifecycle_jobs SET state='running',started_at=now(),completed_at=NULL,next_attempt_at=NULL,
        lease_expires_at=now()+interval '10 minutes',lease_token=gen_random_uuid()::text,attempts=attempts+1
        WHERE id=$1::uuid RETURNING `+lifecycleJobColumns, candidate.id), &job)
	if err != nil {
		return LifecycleJob{}, false, err
	}
	var running int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM lifecycle_jobs WHERE repository_id=$1::uuid AND state='running'`, candidate.repositoryID).Scan(&running); err != nil {
		return LifecycleJob{}, false, err
	}
	if running > LifecycleJobRepoConcurrency(candidate.kind) {
		_, err = tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT lifecycle_claim`)
		return LifecycleJob{}, false, err
	}
	_, err = tx.ExecContext(ctx, `RELEASE SAVEPOINT lifecycle_claim`)
	return job, err == nil, err
}

func prefixedLifecycleJobColumns(prefix string) string {
	return prefix + `.id::text,` + prefix + `.repository_id::text,` + prefix + `.kind,` + prefix + `.idempotency_key,` + prefix + `.payload,` + prefix + `.state,` + prefix + `.created_at,` + prefix + `.started_at,` + prefix + `.completed_at,` + prefix + `.last_error,` + prefix + `.attempts,` + prefix + `.max_attempts,` + prefix + `.next_attempt_at,` + prefix + `.lease_expires_at,` + prefix + `.lease_token,` + prefix + `.progress_current,` + prefix + `.progress_total,` + prefix + `.progress_message`
}

// lifecycleRecoveryBatch bounds one recovery statement so the scheduler task never holds a long lock set.
const lifecycleRecoveryBatch = 500

// RecoverExpiredLifecycleJobs returns jobs whose worker lease expired before before to the queue, in batches.
// It is called by the scheduler task only; claims do not recover leases (ADR 0023 section 7).
func (s *PostgresStore) RecoverExpiredLifecycleJobs(ctx context.Context, before time.Time) (int, error) {
	total := 0
	for {
		result, err := s.db.ExecContext(ctx, `WITH candidates AS (
		SELECT id FROM lifecycle_jobs
		WHERE state='running' AND lease_expires_at IS NOT NULL AND lease_expires_at<=$1
		AND pg_try_advisory_xact_lock(hashtextextended(format('lifecycle-job-lease:%s', id), 0))
		ORDER BY lease_expires_at LIMIT $2
		FOR UPDATE SKIP LOCKED
	) UPDATE lifecycle_jobs jobs SET
        state=CASE WHEN attempts>=max_attempts THEN 'failed' ELSE 'retrying' END,
        completed_at=CASE WHEN attempts>=max_attempts THEN $1 ELSE NULL END,
        next_attempt_at=CASE WHEN attempts>=max_attempts THEN NULL ELSE $1+LEAST(interval '30 minutes',interval '30 seconds'*power(2,LEAST(GREATEST(attempts-1,0),6))) END,
        lease_expires_at=NULL,lease_token='',last_error='worker lease expired before completion'
		FROM candidates WHERE jobs.id=candidates.id`, before.UTC(), lifecycleRecoveryBatch)
		if err != nil {
			return total, err
		}
		count, err := result.RowsAffected()
		total += int(count)
		if err != nil || count < lifecycleRecoveryBatch {
			return total, err
		}
	}
}

func (s *PostgresStore) RunLifecycleJobNow(ctx context.Context, repositoryID, id string, audit ...AuditRecord) (LifecycleJob, error) {
	return s.controlLifecycleJob(ctx, repositoryID, id, audit, `UPDATE lifecycle_jobs SET state='pending',next_attempt_at=now() WHERE repository_id=$1::uuid AND id=$2::uuid AND state IN ('pending','retrying') RETURNING `+lifecycleJobColumns)
}

func (s *PostgresStore) RetryLifecycleJob(ctx context.Context, repositoryID, id string, audit ...AuditRecord) (LifecycleJob, error) {
	return s.controlLifecycleJob(ctx, repositoryID, id, audit, `UPDATE lifecycle_jobs SET state='pending',attempts=0,next_attempt_at=now(),started_at=NULL,completed_at=NULL,lease_expires_at=NULL,last_error='',progress_current=0,progress_message='' WHERE repository_id=$1::uuid AND id=$2::uuid AND state IN ('failed','cancelled') RETURNING `+lifecycleJobColumns)
}

func (s *PostgresStore) RequeueFailedLifecycleJobs(ctx context.Context, repositoryID string, kind LifecycleJobKind, limit int, audit ...AuditRecord) ([]LifecycleJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	return runAudited(ctx, s, audit, func(q sqlRunner) ([]LifecycleJob, error) {
		return s.requeueFailedLifecycleJobs(ctx, q, repositoryID, kind, limit)
	})
}

func (s *PostgresStore) requeueFailedLifecycleJobs(ctx context.Context, q sqlRunner, repositoryID string, kind LifecycleJobKind, limit int) ([]LifecycleJob, error) {
	rows, err := q.QueryContext(ctx, `WITH candidates AS (
        SELECT id FROM lifecycle_jobs
        WHERE repository_id=$1::uuid AND kind=$2 AND state IN ('failed','cancelled')
        ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT $3
    ) UPDATE lifecycle_jobs jobs SET state='pending',attempts=0,next_attempt_at=now(),started_at=NULL,
        completed_at=NULL,lease_expires_at=NULL,lease_token='',last_error='',progress_current=0,progress_message=''
    FROM candidates WHERE jobs.id=candidates.id RETURNING `+prefixedLifecycleJobColumns("jobs"), repositoryID, kind, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	jobs := make([]LifecycleJob, 0, limit)
	for rows.Next() {
		var job LifecycleJob
		if err := scanLifecycleJob(rows, &job); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *PostgresStore) CancelLifecycleJob(ctx context.Context, repositoryID, id string, audit ...AuditRecord) (LifecycleJob, error) {
	return s.controlLifecycleJob(ctx, repositoryID, id, audit, `UPDATE lifecycle_jobs SET state='cancelled',completed_at=now(),next_attempt_at=NULL WHERE repository_id=$1::uuid AND id=$2::uuid AND state IN ('pending','retrying') RETURNING `+lifecycleJobColumns)
}

func (s *PostgresStore) controlLifecycleJob(ctx context.Context, repositoryID, id string, audit []AuditRecord, query string) (LifecycleJob, error) {
	return runAudited(ctx, s, audit, func(q sqlRunner) (LifecycleJob, error) {
		var job LifecycleJob
		err := scanLifecycleJob(q.QueryRowContext(ctx, query, repositoryID, id), &job)
		if err == nil {
			return job, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return LifecycleJob{}, err
		}
		var exists bool
		if queryErr := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lifecycle_jobs WHERE repository_id=$1::uuid AND id=$2::uuid)`, repositoryID, id).Scan(&exists); queryErr != nil {
			return LifecycleJob{}, queryErr
		}
		if !exists {
			return LifecycleJob{}, ErrNotFound
		}
		return LifecycleJob{}, ErrVersionConflict
	})
}

func (s *PostgresStore) UpdateLifecycleJobProgress(ctx context.Context, id, leaseToken string, current, total int, message string) error {
	if current < 0 || total < 0 || current > total {
		return ErrVersionConflict
	}
	result, err := s.db.ExecContext(ctx, `UPDATE lifecycle_jobs SET progress_current=$3,progress_total=$4,progress_message=$5,
		lease_expires_at=GREATEST(lease_expires_at+interval '1 microsecond',clock_timestamp()+interval '10 minutes')
		WHERE id=$1::uuid AND lease_token=$2 AND state='running'`, id, leaseToken, current, total, message)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) RenewLifecycleJobLease(ctx context.Context, id, leaseToken string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE lifecycle_jobs SET
		lease_expires_at=GREATEST(lease_expires_at+interval '1 microsecond',clock_timestamp()+interval '10 minutes')
		WHERE id=$1::uuid AND lease_token=$2 AND state='running' AND lease_expires_at>clock_timestamp()`, id, leaseToken)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) LockLifecycleJobLease(ctx context.Context, id, leaseToken string) (func(), error) {
	_, release, err := s.lockPostgresAdvisoryKeys(ctx, []string{"lifecycle-job-lease:" + id})
	if err != nil {
		return nil, err
	}
	var valid bool
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lifecycle_jobs WHERE id=$1::uuid AND state='running' AND lease_token=$2 AND lease_expires_at>clock_timestamp())`, id, leaseToken).Scan(&valid); err != nil {
		release()
		return nil, err
	}
	if !valid {
		release()
		return nil, ErrNotFound
	}
	return release, nil
}

func (s *PostgresStore) CompleteLifecycleJob(ctx context.Context, id, leaseToken string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE lifecycle_jobs SET state='completed',completed_at=now(),lease_expires_at=NULL,lease_token='',next_attempt_at=NULL,last_error='',progress_current=CASE WHEN progress_total>0 THEN progress_total ELSE progress_current END WHERE id=$1::uuid AND lease_token=$2 AND state='running' AND lease_expires_at>clock_timestamp()`, id, leaseToken)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	jobCtx, ids := requestcontext.WithRequest(ctx, id)
	slog.LogAttrs(jobCtx, slog.LevelInfo, "lifecycle job completed",
		slog.String("component", "lifecycle_worker"), slog.String("operation", "lifecycle.complete"),
		slog.String("requestId", ids.RequestID), slog.String("traceId", ids.TraceID), slog.String("jobId", id),
	)
	return nil
}

func (s *PostgresStore) FailLifecycleJob(ctx context.Context, id, leaseToken, message string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE lifecycle_jobs SET
        state=CASE WHEN attempts>=max_attempts THEN 'failed' ELSE 'retrying' END,
        completed_at=CASE WHEN attempts>=max_attempts THEN now() ELSE NULL END,
        next_attempt_at=CASE WHEN attempts>=max_attempts THEN NULL ELSE now()+LEAST(interval '30 minutes',interval '30 seconds'*power(2,LEAST(GREATEST(attempts-1,0),6))) END,
        lease_expires_at=NULL,lease_token='',last_error=$3
        WHERE id=$1::uuid AND lease_token=$2 AND state='running'`, id, leaseToken, message)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	jobCtx, ids := requestcontext.WithRequest(ctx, id)
	slog.LogAttrs(jobCtx, slog.LevelWarn, "lifecycle job failed or queued for retry",
		slog.String("component", "lifecycle_worker"), slog.String("operation", "lifecycle.fail"),
		slog.String("requestId", ids.RequestID), slog.String("traceId", ids.TraceID), slog.String("jobId", id),
	)
	return nil
}

type lifecycleJobScanner interface {
	Scan(...any) error
}

func scanLifecycleJob(scanner lifecycleJobScanner, job *LifecycleJob) error {
	var startedAt, completedAt, nextAttemptAt, leaseExpiresAt sql.NullTime
	if err := scanner.Scan(&job.ID, &job.RepositoryID, &job.Kind, &job.IdempotencyKey, &job.Payload, &job.State, &job.CreatedAt, &startedAt, &completedAt, &job.LastError, &job.Attempts, &job.MaxAttempts, &nextAttemptAt, &leaseExpiresAt, &job.LeaseToken, &job.ProgressCurrent, &job.ProgressTotal, &job.ProgressMessage); err != nil {
		return err
	}
	if startedAt.Valid {
		job.StartedAt = startedAt.Time
	}
	if completedAt.Valid {
		job.CompletedAt = completedAt.Time
	}
	if nextAttemptAt.Valid {
		job.NextAttemptAt = nextAttemptAt.Time
	}
	if leaseExpiresAt.Valid {
		job.LeaseExpiresAt = leaseExpiresAt.Time
	}
	return nil
}

func scanRepositoryLifecycleJob(scanner lifecycleJobScanner, record *RepositoryLifecycleJob) error {
	job := &record.Job
	var startedAt, completedAt, nextAttemptAt, leaseExpiresAt sql.NullTime
	if err := scanner.Scan(&record.RepositoryName, &job.ID, &job.RepositoryID, &job.Kind, &job.IdempotencyKey, &job.Payload, &job.State, &job.CreatedAt, &startedAt, &completedAt, &job.LastError, &job.Attempts, &job.MaxAttempts, &nextAttemptAt, &leaseExpiresAt, &job.LeaseToken, &job.ProgressCurrent, &job.ProgressTotal, &job.ProgressMessage); err != nil {
		return err
	}
	if startedAt.Valid {
		job.StartedAt = startedAt.Time
	}
	if completedAt.Valid {
		job.CompletedAt = completedAt.Time
	}
	if nextAttemptAt.Valid {
		job.NextAttemptAt = nextAttemptAt.Time
	}
	if leaseExpiresAt.Valid {
		job.LeaseExpiresAt = leaseExpiresAt.Time
	}
	return nil
}
