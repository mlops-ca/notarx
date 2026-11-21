package repository

import (
	"context"
	"slices"
	"sort"
	"time"
)

func (s *MemoryStore) promotionRuleReferencesLocked(rule PromotionRule) bool {
	if _, ok := s.hostedRepositories[rule.SourceRepositoryID]; !ok {
		return false
	}
	if _, ok := s.serviceAccounts[rule.RunAsServiceAccountID]; !ok {
		return false
	}
	switch rule.TargetKind {
	case PromotionTargetLocal:
		_, ok := s.hostedRepositories[rule.TargetRepositoryID]
		return ok
	case PromotionTargetRemote:
		_, ok := s.remoteInstances[rule.RemoteInstanceID]
		return ok
	}
	return false
}

func (s *MemoryStore) CreatePromotionRule(ctx context.Context, rule PromotionRule, maxPerSource int, audit ...AuditRecord) (PromotionRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.promotionRuleReferencesLocked(rule) {
		return PromotionRule{}, ErrPromotionRuleReference
	}
	inSource := 0
	for _, existing := range s.promotionRules {
		if existing.Name == rule.Name {
			return PromotionRule{}, ErrPromotionRuleNameExists
		}
		if existing.SourceRepositoryID == rule.SourceRepositoryID {
			inSource++
		}
	}
	if maxPerSource > 0 && inSource >= maxPerSource {
		return PromotionRule{}, ErrPromotionRuleLimit
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return PromotionRule{}, err
	}
	if s.promotionRules == nil {
		s.promotionRules = make(map[string]PromotionRule)
	}
	now := time.Now().UTC()
	rule.Version, rule.CreatedAt, rule.UpdatedAt = "1", now, now
	rule.UpdatedBy = rule.CreatedBy
	rule.State, rule.StateReason = PromotionRuleActive, ""
	s.promotionRules[rule.ID] = clonePromotionRule(rule)
	return clonePromotionRule(rule), nil
}

func (s *MemoryStore) PagePromotionRules(_ context.Context, query PromotionRuleQuery) (PromotionRulePage, error) {
	limit := pageLimit(query.Limit, 100, 500)
	filter := promotionRulesFilter(query)
	afterName, after, err := decodeNameToken(query.PageToken, filter)
	if err != nil {
		return PromotionRulePage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]PromotionRule, 0, len(s.promotionRules))
	for _, rule := range s.promotionRules {
		if (after && rule.Name <= afterName) || (query.SourceRepositoryIDs != nil && !slices.Contains(query.SourceRepositoryIDs, rule.SourceRepositoryID)) {
			continue
		}
		items = append(items, clonePromotionRule(rule))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	page := PromotionRulePage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextPageToken = EncodeKeysetToken(filter, page.Items[limit-1].Name)
	}
	return page, nil
}

func (s *MemoryStore) GetPromotionRule(_ context.Context, id string) (PromotionRule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rule, ok := s.promotionRules[id]
	if !ok {
		return PromotionRule{}, ErrNotFound
	}
	return clonePromotionRule(rule), nil
}

func (s *MemoryStore) UpdatePromotionRule(ctx context.Context, rule PromotionRule, expectedVersion string, audit ...AuditRecord) (PromotionRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.promotionRules[rule.ID]
	if !ok {
		return PromotionRule{}, ErrNotFound
	}
	if current.Version != expectedVersion {
		return PromotionRule{}, ErrVersionConflict
	}
	rule.SourceRepositoryID, rule.CreatedBy, rule.CreatedAt = current.SourceRepositoryID, current.CreatedBy, current.CreatedAt
	// An edit returns a rule whose run-as account lost its authority to active; any other parked state stays.
	rule.State, rule.StateReason = current.State, current.StateReason
	if current.State == PromotionRuleAuthorizationLost {
		rule.State, rule.StateReason = PromotionRuleActive, ""
	}
	if !s.promotionRuleReferencesLocked(rule) {
		return PromotionRule{}, ErrPromotionRuleReference
	}
	for id, existing := range s.promotionRules {
		if id != rule.ID && existing.Name == rule.Name {
			return PromotionRule{}, ErrPromotionRuleNameExists
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return PromotionRule{}, err
	}
	rule.Version, rule.UpdatedAt = nextHostedGroupVersion(current.Version), time.Now().UTC()
	s.promotionRules[rule.ID] = clonePromotionRule(rule)
	if rule.State == PromotionRuleActive && current.State != PromotionRuleActive {
		// An edit is the operator's statement that the rule is ready again; the evaluator parks it again if it is not. What
		// waited for the rule is due now, except a row a worker holds.
		for id, evaluation := range s.promotionEvaluations {
			if evaluation.RuleID == rule.ID && evaluation.State == PromotionEvaluationWaiting && evaluation.LeaseToken == "" {
				evaluation.NextAttemptAt, evaluation.UpdatedAt = rule.UpdatedAt, rule.UpdatedAt
				s.promotionEvaluations[id] = evaluation
			}
		}
	}
	return clonePromotionRule(rule), nil
}

func (s *MemoryStore) DeletePromotionRule(ctx context.Context, id string, audit ...AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.promotionRules[id]; !ok {
		return ErrNotFound
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	delete(s.promotionRules, id)
	for evaluationID, evaluation := range s.promotionEvaluations {
		if evaluation.RuleID == id {
			delete(s.promotionEvaluations, evaluationID)
		}
	}
	return nil
}

var _ PromotionRuleStore = (*MemoryStore)(nil)
