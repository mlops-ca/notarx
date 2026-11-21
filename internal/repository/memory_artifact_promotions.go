package repository

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

func cloneArtifactPromotion(promotion ArtifactPromotion) ArtifactPromotion {
	promotion.ChainPath = append([]PromotionChainHop{}, promotion.ChainPath...)
	promotion.Approvals = append([]PromotionApproval{}, promotion.Approvals...)
	return promotion
}

// insertArtifactPromotionLocked stores the provenance row of a promotion job; the caller holds s.mu. A key already
// used for the same attempt is an idempotency conflict.
func (s *MemoryStore) insertArtifactPromotionLocked(promotion ArtifactPromotion, createdAt time.Time) error {
	for _, existing := range s.artifactPromotions {
		if existing.IdempotencyKey == promotion.IdempotencyKey && existing.Attempt == promotion.Attempt {
			return ErrIdempotencyConflict
		}
	}
	if promotion.ID == "" {
		return errors.New("artifact promotion has no id")
	}
	promotion.CreatedAt, promotion.CompletedAt = createdAt, time.Time{}
	if s.artifactPromotions == nil {
		s.artifactPromotions = map[string]ArtifactPromotion{}
	}
	s.artifactPromotions[promotion.ID] = cloneArtifactPromotion(promotion)
	return nil
}

// latestArtifactPromotionLocked returns the newest attempt of the promotion owning the job.
func (s *MemoryStore) latestArtifactPromotionLocked(jobID string) (ArtifactPromotion, bool) {
	var latest ArtifactPromotion
	found := false
	for _, promotion := range s.artifactPromotions {
		if promotion.JobID == jobID && (!found || promotion.Attempt > latest.Attempt) {
			latest, found = promotion, true
		}
	}
	return latest, found
}

// syncPromotionLocked is the in-memory form of the promotion_job_state_change trigger: it mirrors the job's state into
// its provenance row and, when the job reaches completed or failed, emits artifact.promoted or promotion.failed. The
// caller holds s.mu.
func (s *MemoryStore) syncPromotionLocked(job LifecycleJob) {
	if job.Kind != LifecycleJobPromotion {
		return
	}
	promotion, ok := s.latestArtifactPromotionLocked(job.ID)
	if !ok {
		return
	}
	state := promotionStateForJob(job.State)
	if state == promotion.State {
		return
	}
	promotion.State, promotion.CompletedAt = state, time.Time{}
	if state == ArtifactPromotionCompleted || state == ArtifactPromotionFailed || state == ArtifactPromotionCancelled {
		promotion.CompletedAt = time.Now().UTC()
	}
	s.artifactPromotions[promotion.ID] = promotion
	if state != ArtifactPromotionCompleted && state != ArtifactPromotionFailed {
		return
	}
	eventType := WebhookEventArtifactPromoted
	if state == ArtifactPromotionFailed {
		eventType = WebhookEventPromotionFailed
	}
	var payload struct {
		SourceRepositoryID string `json:"sourceRepositoryId"`
	}
	_ = json.Unmarshal(job.Payload, &payload)
	data, _ := json.Marshal(map[string]any{
		"operationId": job.ID, "promotionId": promotion.ID, "kind": "promotion", "format": promotion.Format,
		"sourceRepositoryId": payload.SourceRepositoryID, "targetRepositoryId": promotion.TargetRepositoryID,
		"coordinate": promotion.Coordinate, "digest": promotion.Digest, "state": state, "attempts": job.Attempts, "attempt": promotion.Attempt,
	})
	s.enqueueWebhookEventLocked(WebhookEvent{ID: ids.New(), Type: eventType, RepositoryID: promotion.TargetRepositoryID, Format: string(promotion.Format),
		Coordinate: promotion.Coordinate, Digest: promotion.Digest, Version: promotion.ID, Data: data})
}

// GetArtifactPromotionByJob implements ArtifactPromotionStore.
func (s *MemoryStore) GetArtifactPromotionByJob(_ context.Context, jobID string) (ArtifactPromotion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	promotion, ok := s.latestArtifactPromotionLocked(jobID)
	if !ok {
		return ArtifactPromotion{}, ErrNotFound
	}
	return cloneArtifactPromotion(promotion), nil
}

// PageArtifactPromotions implements ArtifactPromotionStore.
func (s *MemoryStore) PageArtifactPromotions(_ context.Context, query ArtifactPromotionQuery) (ArtifactPromotionPage, error) {
	limit := pageLimit(query.Limit, 100, 500)
	filter := artifactPromotionsFilter(query)
	cursorAt, cursorID, after, err := decodeCreatedAtToken(query.PageToken, filter)
	if err != nil {
		return ArtifactPromotionPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]ArtifactPromotion, 0, len(s.artifactPromotions))
	for _, promotion := range s.artifactPromotions {
		if (query.TargetRepositoryID != "" && promotion.TargetRepositoryID != query.TargetRepositoryID) ||
			(query.Format != "" && promotion.Format != query.Format) || (query.State != "" && promotion.State != query.State) ||
			(query.Coordinate != "" && promotion.Coordinate != query.Coordinate) || (query.Digest != "" && promotion.Digest != query.Digest) ||
			(query.RepositoryIDs != nil && !slices.Contains(query.RepositoryIDs, promotion.TargetRepositoryID)) ||
			(after && !createdAtBefore(promotion.CreatedAt, promotion.ID, cursorAt, cursorID)) {
			continue
		}
		items = append(items, cloneArtifactPromotion(promotion))
	}
	sort.Slice(items, func(i, j int) bool {
		return newerFirst(items[i].CreatedAt, items[i].ID, items[j].CreatedAt, items[j].ID)
	})
	page := ArtifactPromotionPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		page.NextPageToken = encodeCreatedAtToken(filter, last.CreatedAt, last.ID)
	}
	return page, nil
}

// refuseClaimedPromotionsLocked is refuseClaimedPromotions for the in-memory store; the caller holds s.mu. A refused job
// fails as FailLifecycleJob does.
func (s *MemoryStore) refuseClaimedPromotionsLocked(ctx context.Context, claimed []LifecycleJob) ([]LifecycleJob, error) {
	kept := claimed[:0:0]
	for _, job := range claimed {
		promotion, ok := s.latestArtifactPromotionLocked(job.ID)
		if job.Kind != LifecycleJobPromotion || !ok {
			kept = append(kept, job)
			continue
		}
		reason, err := promotionRefusal(ctx, memoryPolicyReader{s}, promotion, job)
		if err != nil {
			reason = "target admission policy could not be evaluated"
		}
		if reason == "" {
			kept = append(kept, job)
			continue
		}
		if failErr := s.failLifecycleJobLocked(job.ID, job.LeaseToken, reason); failErr != nil {
			return nil, failErr
		}
	}
	return kept, nil
}

// memoryPolicyReader reads policy and intelligence while the store lock is already held.
type memoryPolicyReader struct{ s *MemoryStore }

func (r memoryPolicyReader) GetRepositorySecurityPolicy(_ context.Context, repositoryID string) (RepositorySecurityPolicy, error) {
	if _, ok := r.s.hostedRepositories[repositoryID]; !ok {
		return RepositorySecurityPolicy{}, ErrNotFound
	}
	policy, ok := r.s.securityPolicies[repositoryID]
	if !ok {
		return DefaultRepositorySecurityPolicy(), nil
	}
	return CloneRepositorySecurityPolicy(policy), nil
}

func (r memoryPolicyReader) GetArtifactIntelligence(_ context.Context, repositoryID string, format Format, coordinate, digest string) (ArtifactIntelligence, error) {
	value, ok := r.s.artifactIntelligence[artifactIntelligenceKey(repositoryID, format, coordinate, digest)]
	if !ok {
		return ArtifactIntelligence{}, ErrNotFound
	}
	return cloneArtifactIntelligence(value), nil
}
