package repository

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/mlops-ca/notarx/internal/background"
	"github.com/mlops-ca/notarx/internal/requestcontext"
)

// Hot queries are named so the integration tests can EXPLAIN the production statement.
const (
	listLifecycleJobsQuery      = `SELECT ` + lifecycleJobColumns + ` FROM lifecycle_jobs WHERE repository_id=$1::uuid ORDER BY lifecycle_jobs.created_at DESC,lifecycle_jobs.id DESC LIMIT $2`
	listLifecycleJobsAfterQuery = `SELECT ` + lifecycleJobColumns + ` FROM lifecycle_jobs WHERE repository_id=$1::uuid AND (created_at,id)<($2::timestamptz,$3::uuid) ORDER BY lifecycle_jobs.created_at DESC,lifecycle_jobs.id DESC LIMIT $4`
	getLifecycleJobQuery        = `SELECT ` + lifecycleJobColumns + ` FROM lifecycle_jobs WHERE repository_id=$1::uuid AND id=$2::uuid`
)

const allLifecycleJobsFilter = "lifecycle-jobs-all"

var (
	listAllLifecycleJobsQuery      = `SELECT h.name,` + prefixedLifecycleJobColumns("jobs") + ` FROM lifecycle_jobs jobs JOIN hosted_repositories h ON h.id=jobs.repository_id WHERE ($1::text[] IS NULL OR jobs.repository_id=ANY($1::text[]::uuid[])) ORDER BY jobs.created_at DESC,jobs.id DESC LIMIT $2`
	listAllLifecycleJobsAfterQuery = `SELECT h.name,` + prefixedLifecycleJobColumns("jobs") + ` FROM lifecycle_jobs jobs JOIN hosted_repositories h ON h.id=jobs.repository_id WHERE ($1::text[] IS NULL OR jobs.repository_id=ANY($1::text[]::uuid[])) AND (jobs.created_at,jobs.id)<($2::timestamptz,$3::uuid) ORDER BY jobs.created_at DESC,jobs.id DESC LIMIT $4`
)

// lifecycleJobColumns selects id::text as the output column id: a query on it orders by lifecycle_jobs.id, as a
// bare id would sort the text.
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

// ListLifecycleJobs returns the newest window of a repository's jobs, the first page of PageLifecycleJobs.
func (s *PostgresStore) ListLifecycleJobs(ctx context.Context, repositoryID string, limit int) ([]LifecycleJob, error) {
	page, err := s.PageLifecycleJobs(ctx, repositoryID, LifecycleJobListQuery{Limit: limit})
	return page.Items, err
}

func lifecycleJobsFilter(repositoryID string) string { return "lifecycle-jobs\x00" + repositoryID }

// PageLifecycleJobs pages a repository's jobs newest first by keyset on (created_at, id).
func (s *PostgresStore) PageLifecycleJobs(ctx context.Context, repositoryID string, query LifecycleJobListQuery) (LifecycleJobPage, error) {
	limit := pageLimit(query.Limit, 100, 100)
	cursorAt, cursorID, after, err := decodeCreatedAtToken(query.PageToken, lifecycleJobsFilter(repositoryID))
	if err != nil {
		return LifecycleJobPage{}, err
	}
	statement, args := listLifecycleJobsQuery, []any{repositoryID, limit + 1}
	if after {
		statement, args = listLifecycleJobsAfterQuery, []any{repositoryID, cursorAt, cursorID, limit + 1}
	}
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return LifecycleJobPage{}, err
	}
	defer func() { _ = rows.Close() }()
	jobs := make([]LifecycleJob, 0, limit+1)
	for rows.Next() {
		var job LifecycleJob
		if err := scanLifecycleJob(rows, &job); err != nil {
			return LifecycleJobPage{}, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return LifecycleJobPage{}, err
	}
	page := LifecycleJobPage{Items: jobs}
	if len(jobs) > limit {
		page.Items = jobs[:limit]
		last := page.Items[limit-1]
		page.NextPageToken = encodeCreatedAtToken(lifecycleJobsFilter(repositoryID), last.CreatedAt, last.ID)
	}
	return page, nil
}

// PageAllLifecycleJobs pages the jobs of every repository newest first by keyset on (created_at, id).
func (s *PostgresStore) PageAllLifecycleJobs(ctx context.Context, query LifecycleJobListQuery) (RepositoryLifecycleJobPage, error) {
	limit := pageLimit(query.Limit, 500, 500)
	cursorAt, cursorID, after, err := decodeCreatedAtToken(query.PageToken, allLifecycleJobsFilter)
	if err != nil {
		return RepositoryLifecycleJobPage{}, err
	}
	// A nil set (every repository) is a SQL NULL array; an empty one matches nothing.
	var repositories any
	if query.RepositoryIDs != nil {
		repositories = query.RepositoryIDs
	}
	statement, args := listAllLifecycleJobsQuery, []any{repositories, limit + 1}
	if after {
		statement, args = listAllLifecycleJobsAfterQuery, []any{repositories, cursorAt, cursorID, limit + 1}
	}
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return RepositoryLifecycleJobPage{}, err
	}
	defer func() { _ = rows.Close() }()
	records := make([]RepositoryLifecycleJob, 0, limit+1)
	for rows.Next() {
		var record RepositoryLifecycleJob
		if err := scanRepositoryLifecycleJob(rows, &record); err != nil {
			return RepositoryLifecycleJobPage{}, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return RepositoryLifecycleJobPage{}, err
	}
	page := RepositoryLifecycleJobPage{Items: records}
	if len(records) > limit {
		page.Items = records[:limit]
		last := page.Items[limit-1].Job
		page.NextPageToken = encodeCreatedAtToken(allLifecycleJobsFilter, last.CreatedAt, last.ID)
	}
	return page, nil
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
		ORDER BY lifecycle_jobs.created_at DESC,lifecycle_jobs.id DESC LIMIT 1`, repositoryID, format, coordinate, digest), &job)
	if errors.Is(err, sql.ErrNoRows) {
		return LifecycleJob{}, ErrNotFound
	}
	return job, err
}

func (s *PostgresStore) ClaimLifecycleJobs(ctx context.Context, limit int) ([]LifecycleJob, error) {
	return s.claimLifecycleJobs(ctx, "", nil, limit)
}

func (s *PostgresStore) ClaimLifecycleJobsByKind(ctx context.Context, kind LifecycleJobKind, limit int) ([]LifecycleJob, error) {
	return s.claimLifecycleJobs(ctx, kind, nil, limit)
}

func (s *PostgresStore) ClaimLifecycleJobsByKindAndFormat(ctx context.Context, kind LifecycleJobKind, format Format, limit int) ([]LifecycleJob, error) {
	return s.claimLifecycleJobs(ctx, kind, formatList(format), limit)
}

// ClaimLifecycleJobsByKindAndFormats claims jobs of one kind for any of formats in a single claim; an empty
// formats list means every format. It is the call of the per-process claimer (lifecycle.ClaimBroker).
func (s *PostgresStore) ClaimLifecycleJobsByKindAndFormats(ctx context.Context, kind LifecycleJobKind, formats []Format, limit int) ([]LifecycleJob, error) {
	return s.claimLifecycleJobs(ctx, kind, formats, limit)
}

// claimLifecycleJobs claims up to limit actionable jobs (ADR 0023 section 7). Candidates come from the
// actionable partial index in next_attempt_at order with FOR UPDATE SKIP LOCKED, so nothing is sorted and no
// DISTINCT ON runs. The per-repository advisory lock is taken after the LIMIT, only for rows already
// selected; the row is then marked running and the running set of the repository is probed. A row over the
// kind's RepoConcurrency is rolled back to its candidate state (a savepoint, so no notification and no
// attempt is consumed) and excluded from later rounds, which keeps a hot repository from starving the
// others. The scan stops at QueueClaimScanCap examined rows. A claim across all kinds that hits the cap
// continues kind by kind in rotating order (claimKindsRoundRobin), so one kind cannot consume the budget.
func (s *PostgresStore) claimLifecycleJobs(ctx context.Context, kind LifecycleJobKind, formats []Format, limit int) ([]LifecycleJob, error) {
	if limit <= 0 {
		limit = 100
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	scan := lifecycleClaimScan{tx: tx, formats: formats, limit: limit, claimed: make([]LifecycleJob, 0, limit), skipped: []string{}}
	if kind != "" && len(formats) > 1 {
		err = scan.claimFormatsRoundRobin(ctx, kind, formats, s.claimRotation.Add(1))
	} else {
		err = scan.run(ctx, kind, QueueClaimScanCap)
	}
	if err != nil {
		return nil, err
	}
	if kind == "" && scan.scanned >= QueueClaimScanCap && len(scan.claimed) < limit {
		if err = scan.claimKindsRoundRobin(ctx, s.claimRotation.Add(1)); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.claimObserver.record(kind, scan.scanned)
	for _, job := range scan.claimed {
		jobCtx, ids := requestcontext.WithRequest(ctx, job.ID)
		slog.LogAttrs(jobCtx, slog.LevelInfo, "lifecycle job claimed",
			slog.String("component", "lifecycle_worker"), slog.String("operation", "lifecycle.claim"),
			slog.String("requestId", ids.RequestID), slog.String("traceId", ids.TraceID),
			slog.String("jobId", job.ID), slog.String("kind", string(job.Kind)), slog.String("repositoryId", job.RepositoryID), slog.Int("attempt", job.Attempts),
		)
	}
	return scan.claimed, nil
}

// lifecycleClaimScan is the state of one claim transaction.
type lifecycleClaimScan struct {
	tx      *sql.Tx
	formats []Format
	limit   int
	claimed []LifecycleJob
	skipped []string
	scanned int
	// exhausted is true when the last run saw fewer candidates than it asked for: nothing more is pending.
	exhausted bool
}

// run examines candidates of kind (every kind when empty) until the claim is full, the candidates run out or
// budget rows were examined.
func (c *lifecycleClaimScan) run(ctx context.Context, kind LifecycleJobKind, budget int) error {
	var used int
	for len(c.claimed) < c.limit && used < budget {
		want := min(c.limit-len(c.claimed), budget-used)
		candidates, err := selectLifecycleCandidates(ctx, c.tx, kind, c.formats, c.skipped, want)
		if err != nil {
			return err
		}
		used += len(candidates)
		c.scanned += len(candidates)
		for _, candidate := range candidates {
			job, ok, err := claimLifecycleCandidate(ctx, c.tx, candidate)
			if err != nil {
				return err
			}
			if !ok {
				c.skipped = append(c.skipped, candidate.id)
				continue
			}
			c.claimed = append(c.claimed, job)
		}
		c.exhausted = len(candidates) < want
		if c.exhausted {
			return nil
		}
	}
	return nil
}

// claimFormatsRoundRobin serves a claim for several formats of one kind with the share plan of
// claimFormatShares. The whole claim examines at most QueueClaimScanCap rows. Each format beyond the first
// has an equal part of the cap reserved for its share, so a deep backlog the opening scan meets cannot use the
// shares' budget; the opening scan has the rest, and the remainder what the earlier batches left.
func (c *lifecycleClaimScan) claimFormatsRoundRobin(ctx context.Context, kind LifecycleJobKind, formats []Format, rotation uint64) error {
	limit := c.limit
	defer func() { c.formats, c.limit = formats, limit }()
	return claimFormatShares(ctx, formats, limit, rotation, func(ctx context.Context, phase formatClaimPhase, batch []Format, want int) ([]Format, bool, error) {
		budget := claimPhaseBudget(phase, len(formats), c.scanned)
		if budget <= 0 {
			return nil, false, nil
		}
		c.exhausted = false
		before := len(c.claimed)
		c.formats, c.limit = batch, before+want
		if err := c.run(ctx, kind, budget); err != nil {
			return nil, false, err
		}
		claimed := make([]Format, 0, len(c.claimed)-before)
		for _, job := range c.claimed[before:] {
			claimed = append(claimed, LifecycleJobFormat(job))
		}
		return claimed, c.exhausted, nil
	})
}

// claimPhaseBudget is how many rows a phase of a claim for formats formats may examine once scanned rows were:
// a share is QueueClaimScanCap/(formats+1), the opening scan the cap less a share for every other format but
// never less than one share, and a later phase at most what is left of the cap. The opening scan always runs
// first, with nothing scanned yet. Formats are the few the server knows, far below the cap; past it every phase
// is one row.
func claimPhaseBudget(phase formatClaimPhase, formats, scanned int) int {
	perFormat := max(QueueClaimScanCap/(formats+1), 1)
	left := QueueClaimScanCap - scanned
	switch phase {
	case formatClaimOpening:
		return max(QueueClaimScanCap-perFormat*(formats-1), perFormat)
	case formatClaimShare:
		return min(perFormat, left)
	default:
		return left
	}
}

// claimKindsRoundRobin spreads what is left of the claim over the kinds in rotating order, each kind within an
// equal share of the scan cap, so a kind whose backlog heads the all-kinds index cannot starve the others.
func (c *lifecycleClaimScan) claimKindsRoundRobin(ctx context.Context, rotation uint64) error {
	kinds := LifecycleJobKinds()
	share := max(QueueClaimScanCap/len(kinds), 1)
	for i := 0; i < len(kinds) && len(c.claimed) < c.limit; i++ {
		if err := c.run(ctx, kinds[(int(rotation%uint64(len(kinds)))+i)%len(kinds)], share); err != nil {
			return err
		}
	}
	return nil
}

// The candidate scans of the claim. A claim for one kind (every runtime) is served by
// lifecycle_jobs_kind_actionable_idx (kind, next_attempt_at) in order; one for all kinds
// (ClaimLifecycleJobs) by lifecycle_jobs_actionable_idx. Neither sorts the pending set, which is why the
// order is next_attempt_at alone: a tie-breaker outside the index would need a sort. The integration
// test asserts both plans.
const (
	claimLifecycleCandidatesByKindQuery = `SELECT id::text,repository_id::text,kind FROM lifecycle_jobs
        WHERE state IN ('pending','retrying') AND next_attempt_at<=now() AND attempts<max_attempts
          AND kind=$1 AND (cardinality($2::text[])=0 OR payload->>'format'=ANY($2::text[])) AND id<>ALL($3::text[]::uuid[])
        ORDER BY next_attempt_at LIMIT $4 FOR UPDATE SKIP LOCKED`
	claimLifecycleCandidatesAnyKindQuery = `SELECT id::text,repository_id::text,kind FROM lifecycle_jobs
        WHERE state IN ('pending','retrying') AND next_attempt_at<=now() AND attempts<max_attempts
          AND (cardinality($1::text[])=0 OR payload->>'format'=ANY($1::text[])) AND id<>ALL($2::text[]::uuid[])
        ORDER BY next_attempt_at LIMIT $3 FOR UPDATE SKIP LOCKED`
)

type lifecycleCandidate struct {
	id           string
	repositoryID string
	kind         LifecycleJobKind
}

func selectLifecycleCandidates(ctx context.Context, tx *sql.Tx, kind LifecycleJobKind, formats []Format, skipped []string, limit int) ([]lifecycleCandidate, error) {
	names := make([]string, len(formats))
	for i, format := range formats {
		names[i] = string(format)
	}
	var rows *sql.Rows
	var err error
	if kind != "" {
		rows, err = tx.QueryContext(ctx, claimLifecycleCandidatesByKindQuery, string(kind), names, skipped, limit)
	} else {
		rows, err = tx.QueryContext(ctx, claimLifecycleCandidatesAnyKindQuery, names, skipped, limit)
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

// ReleaseLifecycleJobLease hands a claimed job that never ran back to the queue: it is pending and actionable
// at once, the state "run now" gives, and the attempt its claim counted is returned; its last error stays. A
// lease that is no longer the caller's, or has expired, is ErrNotFound.
func (s *PostgresStore) ReleaseLifecycleJobLease(ctx context.Context, id, leaseToken string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE lifecycle_jobs SET state='pending',
		started_at=NULL,next_attempt_at=clock_timestamp(),lease_expires_at=NULL,lease_token='',attempts=GREATEST(attempts-1,0)
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

// LifecycleTerminalWriteTimeout bounds the write that ends a claimed job: complete or fail.
const LifecycleTerminalWriteTimeout = 5 * time.Second

// terminalWriteContext detaches a job's final write from the cancellation of the context its worker ran
// under. A worker that finishes a job as the process stops must still record the outcome: the stores close
// after the background loops return (ADR 0023 section 7), and a write that failed on the ended context would
// leave the job leased until its lease expired. The lease token fences the write, so a job another worker
// owns by then is never touched; the timeout bounds the wait on a database that does not answer.
func terminalWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), LifecycleTerminalWriteTimeout)
}

func (s *PostgresStore) CompleteLifecycleJob(ctx context.Context, id, leaseToken string) error {
	ctx, cancel := terminalWriteContext(ctx)
	defer cancel()
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
	// A caller whose context carries the shutdown cause was stopped with the process: the job did not fail by its
	// own fault. It goes back to the queue with the attempt its claim counted returned, instead of consuming an
	// attempt (and, at the last one, failing for good) because of a deploy. A context cancelled for any other
	// reason, a lost lease for one, is a failure and counts its attempt.
	interrupted := errors.Is(context.Cause(ctx), background.ErrShutdown)
	ctx, cancel := terminalWriteContext(ctx)
	defer cancel()
	if interrupted {
		return s.requeueInterruptedLifecycleJob(ctx, id, leaseToken, message)
	}
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

// requeueInterruptedLifecycleJob hands a job whose worker was stopped back to the queue, pending and actionable
// at once, with the attempt its claim counted returned and message kept as its last error.
func (s *PostgresStore) requeueInterruptedLifecycleJob(ctx context.Context, id, leaseToken, message string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE lifecycle_jobs SET state='pending',
		started_at=NULL,next_attempt_at=clock_timestamp(),lease_expires_at=NULL,lease_token='',attempts=GREATEST(attempts-1,0),last_error=$3
		WHERE id=$1::uuid AND lease_token=$2 AND state='running'`, id, leaseToken, message)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	jobCtx, ids := requestcontext.WithRequest(ctx, id)
	slog.LogAttrs(jobCtx, slog.LevelInfo, "lifecycle job returned to the queue by a stopping worker",
		slog.String("component", "lifecycle_worker"), slog.String("operation", "lifecycle.requeue"),
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
