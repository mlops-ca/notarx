package repository

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
)

const lifecycleJobLeaseDuration = 10 * time.Minute

func lifecycleJobKey(repositoryID string, kind LifecycleJobKind, idempotencyKey string) string {
	return repositoryID + "\x00" + string(kind) + "\x00" + idempotencyKey
}

func cloneLifecycleJob(job LifecycleJob) LifecycleJob {
	job.Payload = append([]byte(nil), job.Payload...)
	return job
}

func (s *MemoryStore) EnqueueLifecycleJob(ctx context.Context, job LifecycleJob, audit ...AuditRecord) (LifecycleJob, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := lifecycleJobKey(job.RepositoryID, job.Kind, job.IdempotencyKey)
	if existing, ok := s.lifecycleJobs[key]; ok {
		if !equivalentLifecyclePayload(existing.Payload, job.Payload) {
			return LifecycleJob{}, false, ErrIdempotencyConflict
		}
		return cloneLifecycleJob(existing), true, nil
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return LifecycleJob{}, false, err
	}
	now := time.Now().UTC()
	createdAt := now
	if !createdAt.After(s.lifecycleCreatedAt) {
		createdAt = s.lifecycleCreatedAt.Add(time.Nanosecond)
	}
	s.lifecycleCreatedAt = createdAt
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = DefaultLifecycleJobMaxAttempts
	}
	if job.ProgressTotal <= 0 && job.Kind != LifecycleJobRetention {
		job.ProgressTotal = 1
	}
	job.State, job.CreatedAt, job.NextAttemptAt, job.Payload = LifecycleJobPending, createdAt, now, append([]byte(nil), job.Payload...)
	s.lifecycleJobs[key] = job
	return cloneLifecycleJob(job), false, nil
}

func (s *MemoryStore) ListLifecycleJobs(ctx context.Context, repositoryID string, limit int) ([]LifecycleJob, error) {
	page, err := s.PageLifecycleJobs(ctx, repositoryID, LifecycleJobListQuery{Limit: limit})
	return page.Items, err
}

func (s *MemoryStore) PageLifecycleJobs(_ context.Context, repositoryID string, query LifecycleJobListQuery) (LifecycleJobPage, error) {
	limit := pageLimit(query.Limit, 100, 100)
	cursorAt, cursorID, after, err := decodeCreatedAtToken(query.PageToken, lifecycleJobsFilter(repositoryID))
	if err != nil {
		return LifecycleJobPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	jobs := make([]LifecycleJob, 0, limit)
	for _, job := range s.lifecycleJobs {
		if job.RepositoryID == repositoryID && (!after || createdAtBefore(job.CreatedAt, job.ID, cursorAt, cursorID)) {
			jobs = append(jobs, cloneLifecycleJob(job))
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return newerFirst(jobs[i].CreatedAt, jobs[i].ID, jobs[j].CreatedAt, jobs[j].ID) })
	page := LifecycleJobPage{Items: jobs}
	if len(jobs) > limit {
		page.Items = jobs[:limit]
		last := page.Items[limit-1]
		page.NextPageToken = encodeCreatedAtToken(lifecycleJobsFilter(repositoryID), last.CreatedAt, last.ID)
	}
	return page, nil
}

func (s *MemoryStore) PageAllLifecycleJobs(_ context.Context, query LifecycleJobListQuery) (RepositoryLifecycleJobPage, error) {
	limit := pageLimit(query.Limit, 500, 500)
	cursorAt, cursorID, after, err := decodeCreatedAtToken(query.PageToken, allLifecycleJobsFilter)
	if err != nil {
		return RepositoryLifecycleJobPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := make([]RepositoryLifecycleJob, 0, len(s.lifecycleJobs))
	for _, job := range s.lifecycleJobs {
		repo, ok := s.hostedRepositories[job.RepositoryID]
		if !ok || (after && !createdAtBefore(job.CreatedAt, job.ID, cursorAt, cursorID)) ||
			(query.RepositoryIDs != nil && !slices.Contains(query.RepositoryIDs, job.RepositoryID)) {
			continue
		}
		records = append(records, RepositoryLifecycleJob{RepositoryName: repo.Name, Job: cloneLifecycleJob(job)})
	}
	sort.Slice(records, func(i, j int) bool {
		return newerFirst(records[i].Job.CreatedAt, records[i].Job.ID, records[j].Job.CreatedAt, records[j].Job.ID)
	})
	page := RepositoryLifecycleJobPage{Items: records}
	if len(records) > limit {
		page.Items = records[:limit]
		last := page.Items[limit-1].Job
		page.NextPageToken = encodeCreatedAtToken(allLifecycleJobsFilter, last.CreatedAt, last.ID)
	}
	return page, nil
}

func (s *MemoryStore) GetLifecycleJob(_ context.Context, repositoryID, id string) (LifecycleJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, job := range s.lifecycleJobs {
		if job.RepositoryID == repositoryID && job.ID == id {
			return cloneLifecycleJob(job), nil
		}
	}
	return LifecycleJob{}, ErrNotFound
}

func (s *MemoryStore) GetLatestArtifactScanJob(_ context.Context, repositoryID string, format Format, coordinate, digest string) (LifecycleJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest LifecycleJob
	for _, job := range s.lifecycleJobs {
		if job.RepositoryID != repositoryID || job.Kind != LifecycleJobScan {
			continue
		}
		var payload ArtifactScanPayload
		if json.Unmarshal(job.Payload, &payload) != nil || payload.Format != format || payload.Coordinate != coordinate || payload.Digest != digest {
			continue
		}
		if latest.ID == "" || job.CreatedAt.After(latest.CreatedAt) || job.CreatedAt.Equal(latest.CreatedAt) && job.ID > latest.ID {
			latest = job
		}
	}
	if latest.ID == "" {
		return LifecycleJob{}, ErrNotFound
	}
	return cloneLifecycleJob(latest), nil
}

func (s *MemoryStore) ClaimLifecycleJobs(_ context.Context, limit int) ([]LifecycleJob, error) {
	return s.claimLifecycleJobs("", nil, limit)
}

func (s *MemoryStore) ClaimLifecycleJobsByKind(_ context.Context, kind LifecycleJobKind, limit int) ([]LifecycleJob, error) {
	return s.claimLifecycleJobs(kind, nil, limit)
}

func (s *MemoryStore) ClaimLifecycleJobsByKindAndFormat(_ context.Context, kind LifecycleJobKind, format Format, limit int) ([]LifecycleJob, error) {
	return s.claimLifecycleJobs(kind, []Format{format}, limit)
}

func (s *MemoryStore) ClaimLifecycleJobsByKindAndFormats(_ context.Context, kind LifecycleJobKind, formats []Format, limit int) ([]LifecycleJob, error) {
	return s.claimLifecycleJobs(kind, formats, limit)
}

func (s *MemoryStore) claimLifecycleJobs(kind LifecycleJobKind, formats []Format, limit int) ([]LifecycleJob, error) {
	s.lifecycleLeaseFence.Lock()
	defer s.lifecycleLeaseFence.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if limit <= 0 {
		limit = 100
	}
	keys := make([]string, 0, len(s.lifecycleJobs))
	runningByRepository := make(map[string]int)
	for key, job := range s.lifecycleJobs {
		if job.State == LifecycleJobRunning {
			runningByRepository[job.RepositoryID]++
		}
		if (job.State == LifecycleJobPending || job.State == LifecycleJobRetrying) && (job.NextAttemptAt.IsZero() || !job.NextAttemptAt.After(now)) && (kind == "" || job.Kind == kind) && lifecycleJobMatchesFormats(job.Payload, formats) {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return s.lifecycleJobs[keys[i]].CreatedAt.Before(s.lifecycleJobs[keys[j]].CreatedAt)
	})
	jobs := make([]LifecycleJob, 0, limit)
	for _, key := range keys {
		if len(jobs) == limit {
			break
		}
		job := s.lifecycleJobs[key]
		if runningByRepository[job.RepositoryID] >= LifecycleJobRepoConcurrency(job.Kind) {
			continue
		}
		job.State, job.StartedAt, job.CompletedAt = LifecycleJobRunning, now, time.Time{}
		job.NextAttemptAt, job.LeaseExpiresAt, job.LeaseToken = time.Time{}, now.Add(lifecycleJobLeaseDuration), uuid.NewString()
		job.Attempts++
		s.lifecycleJobs[key] = job
		runningByRepository[job.RepositoryID]++
		jobs = append(jobs, cloneLifecycleJob(job))
	}
	return jobs, nil
}

func (s *MemoryStore) RecoverExpiredLifecycleJobs(_ context.Context, before time.Time) (int, error) {
	s.lifecycleLeaseFence.Lock()
	defer s.lifecycleLeaseFence.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recoverExpiredLifecycleJobsLocked(before.UTC()), nil
}

func (s *MemoryStore) recoverExpiredLifecycleJobsLocked(before time.Time) int {
	recovered := 0
	for key, job := range s.lifecycleJobs {
		if job.State != LifecycleJobRunning || job.LeaseExpiresAt.IsZero() || job.LeaseExpiresAt.After(before) {
			continue
		}
		job.LeaseExpiresAt, job.LeaseToken = time.Time{}, ""
		job.LastError = "worker lease expired before completion"
		if job.Attempts >= job.MaxAttempts {
			job.State, job.CompletedAt = LifecycleJobFailed, before
		} else {
			job.State, job.NextAttemptAt, job.CompletedAt = LifecycleJobRetrying, before.Add(lifecycleRetryDelay(job.Attempts)), time.Time{}
		}
		s.lifecycleJobs[key] = job
		if event, ok := cargoPromotionWebhookEvent(job); ok {
			s.enqueueWebhookEventLocked(event)
		}
		recovered++
	}
	return recovered
}

func (s *MemoryStore) RunLifecycleJobNow(ctx context.Context, repositoryID, id string, audit ...AuditRecord) (LifecycleJob, error) {
	return s.updateLifecycleJobControl(ctx, repositoryID, id, audit, func(job *LifecycleJob, now time.Time) error {
		if job.State != LifecycleJobPending && job.State != LifecycleJobRetrying {
			return ErrVersionConflict
		}
		job.State, job.NextAttemptAt = LifecycleJobPending, now
		return nil
	})
}

func (s *MemoryStore) RetryLifecycleJob(ctx context.Context, repositoryID, id string, audit ...AuditRecord) (LifecycleJob, error) {
	return s.updateLifecycleJobControl(ctx, repositoryID, id, audit, func(job *LifecycleJob, now time.Time) error {
		if job.State != LifecycleJobFailed && job.State != LifecycleJobCancelled {
			return ErrVersionConflict
		}
		resetLifecycleJobForRetry(job, now)
		return nil
	})
}

func (s *MemoryStore) RequeueFailedLifecycleJobs(ctx context.Context, repositoryID string, kind LifecycleJobKind, limit int, audit ...AuditRecord) ([]LifecycleJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	keys := make([]string, 0, limit)
	for key, job := range s.lifecycleJobs {
		if job.RepositoryID == repositoryID && job.Kind == kind && (job.State == LifecycleJobFailed || job.State == LifecycleJobCancelled) {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return s.lifecycleJobs[keys[i]].CreatedAt.Before(s.lifecycleJobs[keys[j]].CreatedAt)
	})
	if len(keys) > limit {
		keys = keys[:limit]
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	jobs := make([]LifecycleJob, 0, len(keys))
	for _, key := range keys {
		job := s.lifecycleJobs[key]
		resetLifecycleJobForRetry(&job, now)
		s.lifecycleJobs[key] = job
		jobs = append(jobs, cloneLifecycleJob(job))
	}
	return jobs, nil
}

func resetLifecycleJobForRetry(job *LifecycleJob, now time.Time) {
	job.State, job.NextAttemptAt = LifecycleJobPending, now
	job.Attempts, job.LastError = 0, ""
	job.StartedAt, job.CompletedAt, job.LeaseExpiresAt, job.LeaseToken = time.Time{}, time.Time{}, time.Time{}, ""
	job.ProgressCurrent, job.ProgressMessage = 0, ""
}

func (s *MemoryStore) CancelLifecycleJob(ctx context.Context, repositoryID, id string, audit ...AuditRecord) (LifecycleJob, error) {
	return s.updateLifecycleJobControl(ctx, repositoryID, id, audit, func(job *LifecycleJob, now time.Time) error {
		if job.State != LifecycleJobPending && job.State != LifecycleJobRetrying {
			return ErrVersionConflict
		}
		job.State, job.CompletedAt, job.NextAttemptAt = LifecycleJobCancelled, now, time.Time{}
		return nil
	})
}

func (s *MemoryStore) updateLifecycleJobControl(ctx context.Context, repositoryID, id string, audit []AuditRecord, update func(*LifecycleJob, time.Time) error) (LifecycleJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, job := range s.lifecycleJobs {
		if job.RepositoryID != repositoryID || job.ID != id {
			continue
		}
		if err := update(&job, time.Now().UTC()); err != nil {
			return LifecycleJob{}, err
		}
		if err := s.commitAuditLocked(ctx, audit); err != nil {
			return LifecycleJob{}, err
		}
		s.lifecycleJobs[key] = job
		return cloneLifecycleJob(job), nil
	}
	return LifecycleJob{}, ErrNotFound
}

func (s *MemoryStore) UpdateLifecycleJobProgress(_ context.Context, id, leaseToken string, current, total int, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current < 0 || total < 0 || current > total {
		return ErrVersionConflict
	}
	for key, job := range s.lifecycleJobs {
		if job.ID != id || job.State != LifecycleJobRunning || job.LeaseToken != leaseToken {
			continue
		}
		job.ProgressCurrent, job.ProgressTotal, job.ProgressMessage = current, total, message
		job.LeaseExpiresAt = renewedLifecycleLeaseExpiry(job.LeaseExpiresAt, time.Now().UTC())
		s.lifecycleJobs[key] = job
		return nil
	}
	return ErrNotFound
}

func (s *MemoryStore) RenewLifecycleJobLease(_ context.Context, id, leaseToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for key, job := range s.lifecycleJobs {
		if job.ID != id || job.State != LifecycleJobRunning || job.LeaseToken != leaseToken || (!job.LeaseExpiresAt.IsZero() && !now.Before(job.LeaseExpiresAt)) {
			continue
		}
		job.LeaseExpiresAt = renewedLifecycleLeaseExpiry(job.LeaseExpiresAt, now)
		s.lifecycleJobs[key] = job
		return nil
	}
	return ErrNotFound
}

func (s *MemoryStore) LockLifecycleJobLease(_ context.Context, id, leaseToken string) (func(), error) {
	s.lifecycleLeaseFence.RLock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	for _, job := range s.lifecycleJobs {
		if job.ID == id && job.State == LifecycleJobRunning && job.LeaseToken == leaseToken && (job.LeaseExpiresAt.IsZero() || now.Before(job.LeaseExpiresAt)) {
			return s.lifecycleLeaseFence.RUnlock, nil
		}
	}
	s.lifecycleLeaseFence.RUnlock()
	return nil, ErrNotFound
}

func renewedLifecycleLeaseExpiry(current, now time.Time) time.Time {
	next := now.Add(lifecycleJobLeaseDuration)
	if !next.After(current) {
		return current.Add(time.Microsecond)
	}
	return next
}

// lifecycleJobMatchesFormats reports whether the job payload names one of formats; an empty list matches all.
func lifecycleJobMatchesFormats(payload []byte, formats []Format) bool {
	if len(formats) == 0 {
		return true
	}
	format, ok := lifecyclePayloadFormat(payload)
	return ok && slices.Contains(formats, format)
}

func (s *MemoryStore) CompleteLifecycleJob(_ context.Context, id, leaseToken string) error {
	return s.finishLifecycleJob(id, leaseToken, LifecycleJobCompleted, "")
}

func (s *MemoryStore) FailLifecycleJob(_ context.Context, id, leaseToken, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for key, job := range s.lifecycleJobs {
		if job.ID != id || job.State != LifecycleJobRunning || job.LeaseToken != leaseToken {
			continue
		}
		job.LastError, job.LeaseExpiresAt, job.LeaseToken = message, time.Time{}, ""
		if job.Attempts >= job.MaxAttempts {
			job.State, job.CompletedAt = LifecycleJobFailed, now
		} else {
			job.State, job.NextAttemptAt, job.CompletedAt = LifecycleJobRetrying, now.Add(lifecycleRetryDelay(job.Attempts)), time.Time{}
		}
		s.lifecycleJobs[key] = job
		if event, ok := cargoPromotionWebhookEvent(job); ok {
			s.enqueueWebhookEventLocked(event)
		}
		return nil
	}
	return ErrNotFound
}

func (s *MemoryStore) finishLifecycleJob(id, leaseToken string, state LifecycleJobState, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, job := range s.lifecycleJobs {
		if job.ID != id {
			continue
		}
		if job.State != LifecycleJobRunning || job.LeaseToken != leaseToken || (!job.LeaseExpiresAt.IsZero() && !time.Now().UTC().Before(job.LeaseExpiresAt)) {
			return ErrNotFound
		}
		job.State, job.CompletedAt, job.LastError, job.LeaseExpiresAt, job.LeaseToken = state, time.Now().UTC(), message, time.Time{}, ""
		if state == LifecycleJobCompleted && job.ProgressTotal > 0 {
			job.ProgressCurrent = job.ProgressTotal
		}
		s.lifecycleJobs[key] = job
		if event, ok := cargoPromotionWebhookEvent(job); ok {
			s.enqueueWebhookEventLocked(event)
		}
		return nil
	}
	return ErrNotFound
}

func lifecycleRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 30 * time.Second * time.Duration(1<<min(attempt-1, 6))
	if delay > 30*time.Minute {
		return 30 * time.Minute
	}
	return delay
}
