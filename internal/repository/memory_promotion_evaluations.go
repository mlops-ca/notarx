package repository

import (
	"context"
	"time"

	"github.com/mlops-ca/notarx/internal/events"
	"github.com/mlops-ca/notarx/internal/ids"
)

// SetPromotionRuleMatcher installs the matcher the in-memory store uses to fan a published artifact out to rule
// evaluations, or removes it when nil. Tests that exercise the evaluator install it.
func (s *MemoryStore) SetPromotionRuleMatcher(matcher PromotionRuleMatcher) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.promotionMatcher = matcher
}

// fanOutPromotionEvaluationsLocked is events.Emit's rule fan-out for the in-memory store: one pending evaluation per
// enabled, active rule of the source repository that the matcher accepts, once per (event, rule). The caller holds s.mu.
// With no matcher installed no evaluation is written.
func (s *MemoryStore) fanOutPromotionEvaluationsLocked(event WebhookEvent) {
	if s.promotionMatcher == nil || event.Type != events.ArtifactPublished || event.RepositoryID == "" {
		return
	}
	var rules []PromotionRule
	for _, rule := range s.promotionRules {
		if rule.SourceRepositoryID == event.RepositoryID && rule.Enabled {
			rules = append(rules, clonePromotionRule(rule))
		}
	}
	matched := s.promotionMatcher.Match(event.domainEvent(), rules)
	if s.promotionEvaluations == nil {
		s.promotionEvaluations = map[string]PromotionEvaluation{}
	}
	seq := s.eventSeqs[event.ID]
	for _, ruleID := range matched {
		if _, exists := s.promotionRules[ruleID]; !exists || s.hasEvaluationLocked(seq, ruleID) {
			continue
		}
		now := time.Now().UTC()
		evaluation := PromotionEvaluation{ID: ids.New(), EventSeq: seq, EventOccurredAt: event.OccurredAt, RuleID: ruleID, RepositoryID: event.RepositoryID,
			Coordinate: event.Coordinate, Digest: event.Digest, State: PromotionEvaluationPending, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
		s.promotionEvaluations[evaluation.ID] = evaluation
	}
}

func (s *MemoryStore) hasEvaluationLocked(seq int64, ruleID string) bool {
	for _, evaluation := range s.promotionEvaluations {
		if evaluation.EventSeq == seq && evaluation.RuleID == ruleID {
			return true
		}
	}
	return false
}

// ClaimPromotionEvaluations implements PromotionEvaluationStore.
func (s *MemoryStore) ClaimPromotionEvaluations(_ context.Context, owner string, lease time.Duration, limit int) ([]PromotionEvaluation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	var due []PromotionEvaluation
	for id, evaluation := range s.promotionEvaluations {
		if (evaluation.State != PromotionEvaluationPending && evaluation.State != PromotionEvaluationWaiting) || evaluation.NextAttemptAt.After(now) {
			continue
		}
		if evaluation.Attempts >= PromotionEvaluationMaxAttempts {
			evaluation.State, evaluation.LastReason, evaluation.UpdatedAt = PromotionEvaluationFailed, PromotionEvaluationExhausted, now
			evaluation.LeaseOwner, evaluation.LeaseToken, evaluation.LeaseExpiresAt = "", "", time.Time{}
			s.promotionEvaluations[id] = evaluation
			continue
		}
		due = append(due, evaluation)
	}
	sortEvaluationsOldestDueFirst(due)
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	for i, evaluation := range due {
		evaluation.LeaseOwner, evaluation.LeaseToken, evaluation.LeaseExpiresAt = owner, ids.New(), now.Add(lease)
		evaluation.NextAttemptAt, evaluation.Attempts, evaluation.UpdatedAt = evaluation.LeaseExpiresAt, evaluation.Attempts+1, now
		s.promotionEvaluations[evaluation.ID] = evaluation
		due[i] = evaluation
	}
	return due, nil
}

// FinishPromotionEvaluation implements PromotionEvaluationStore.
func (s *MemoryStore) FinishPromotionEvaluation(_ context.Context, id, leaseToken string, outcome PromotionEvaluationOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	evaluation, ok := s.promotionEvaluations[id]
	if !ok || evaluation.LeaseToken == "" || evaluation.LeaseToken != leaseToken || !evaluation.LeaseExpiresAt.After(now) {
		return ErrPromotionEvaluationLeaseLost
	}
	evaluation.State, evaluation.LastReason, evaluation.UpdatedAt = outcome.State, truncateRunes(outcome.Reason, promotionReasonMax), now
	if outcome.State == PromotionEvaluationPending || outcome.State == PromotionEvaluationWaiting {
		evaluation.NextAttemptAt = now
		if !outcome.NextAttemptAt.IsZero() {
			evaluation.NextAttemptAt = outcome.NextAttemptAt.UTC()
		}
	}
	switch {
	case outcome.State == PromotionEvaluationWaiting:
		evaluation.Attempts = 0
	case outcome.NotAnAttempt:
		evaluation.Attempts = max(evaluation.Attempts-1, 0)
	}
	if outcome.PromotionID != "" {
		evaluation.PromotionID = outcome.PromotionID
	}
	evaluation.LeaseOwner, evaluation.LeaseToken, evaluation.LeaseExpiresAt = "", "", time.Time{}
	s.promotionEvaluations[id] = evaluation
	return nil
}

// ListPromotionEvaluations implements PromotionEvaluationStore.
func (s *MemoryStore) ListPromotionEvaluations(_ context.Context, query PromotionEvaluationQuery) ([]PromotionEvaluation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var items []PromotionEvaluation
	for _, evaluation := range s.promotionEvaluations {
		if evaluation.RuleID == query.RuleID && (query.State == "" || evaluation.State == query.State) {
			items = append(items, evaluation)
		}
	}
	sortEvaluationsNewestFirst(items)
	if limit := pageLimit(query.Limit, 100, 500); len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// ParkPromotionRule implements PromotionEvaluationStore.
func (s *MemoryStore) ParkPromotionRule(ctx context.Context, ruleID, expectedVersion, state, reason string, event events.Event, audit ...AuditRecord) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rule, ok := s.promotionRules[ruleID]
	if !ok || rule.State != PromotionRuleActive || rule.Version != expectedVersion {
		return false, nil
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return false, err
	}
	rule.State, rule.StateReason, rule.UpdatedAt = state, truncateRunes(reason, promotionReasonMax), time.Now().UTC()
	s.promotionRules[ruleID] = rule
	s.enqueueWebhookEventLocked(WebhookEvent{Type: event.Type, RepositoryID: event.RepositoryID, Format: event.Format, Coordinate: event.Coordinate, Digest: event.Digest, Version: event.Version, Data: event.Payload})
	return true, nil
}

var _ PromotionEvaluationStore = (*MemoryStore)(nil)
