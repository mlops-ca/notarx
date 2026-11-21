package repository

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/events"
	"github.com/mlops-ca/notarx/internal/ids"
)

func artifactQuarantineWebhookEvent(value ArtifactQuarantine) WebhookEvent {
	eventType := WebhookEventArtifactQuarantined
	if value.State == ArtifactQuarantineStateReleased {
		eventType = WebhookEventArtifactReleased
	}
	data, _ := json.Marshal(ArtifactQuarantineWebhookData{
		RepositoryID: value.RepositoryID, Format: value.Format, Coordinate: value.Coordinate,
		Digest: value.Digest, State: value.State, Reason: value.Reason, Actor: value.UpdatedBy, Version: value.Version,
	})
	return WebhookEvent{ID: ids.New(), Type: eventType, RepositoryID: value.RepositoryID, Format: string(value.Format), Coordinate: value.Coordinate,
		Digest: value.Digest, Version: value.Version, OccurredAt: value.UpdatedAt, Data: data}
}

func cloneWebhookSubscription(value WebhookSubscription) WebhookSubscription {
	value.EventTypes = append([]WebhookEventType(nil), value.EventTypes...)
	return value
}

func cloneWebhookEvent(value WebhookEvent) WebhookEvent {
	value.Data = append([]byte(nil), value.Data...)
	return value
}

func (s *MemoryStore) CreateWebhookSubscription(ctx context.Context, value WebhookSubscription, audit ...AuditRecord) (WebhookSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.webhookSubscriptions {
		if existing.Name == value.Name {
			return WebhookSubscription{}, ErrWebhookSubscriptionNameExists
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return WebhookSubscription{}, err
	}
	now := time.Now().UTC()
	value.Version, value.CreatedAt, value.UpdatedAt = "1", now, now
	s.webhookSubscriptions[value.ID] = cloneWebhookSubscription(value)
	return cloneWebhookSubscription(value), nil
}

func (s *MemoryStore) PageWebhookSubscriptions(_ context.Context, query WebhookSubscriptionQuery) (WebhookSubscriptionPage, error) {
	limit := pageLimit(query.Limit, 500, 500)
	afterName, after, err := decodeNameToken(query.PageToken, webhookSubscriptionsFilter)
	if err != nil {
		return WebhookSubscriptionPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]WebhookSubscription, 0, len(s.webhookSubscriptions))
	for _, value := range s.webhookSubscriptions {
		if !after || value.Name > afterName {
			values = append(values, cloneWebhookSubscription(value))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	page := WebhookSubscriptionPage{Items: values}
	if len(values) > limit {
		page.Items = values[:limit]
		page.NextPageToken = EncodeKeysetToken(webhookSubscriptionsFilter, page.Items[limit-1].Name)
	}
	return page, nil
}

func (s *MemoryStore) GetWebhookSubscription(_ context.Context, id string) (WebhookSubscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.webhookSubscriptions[id]
	if !ok {
		return WebhookSubscription{}, ErrNotFound
	}
	return cloneWebhookSubscription(value), nil
}

func (s *MemoryStore) UpdateWebhookSubscription(ctx context.Context, value WebhookSubscription, expectedVersion string, audit ...AuditRecord) (WebhookSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.webhookSubscriptions[value.ID]
	if !ok {
		return WebhookSubscription{}, ErrNotFound
	}
	if current.Version != expectedVersion {
		return WebhookSubscription{}, ErrVersionConflict
	}
	for id, existing := range s.webhookSubscriptions {
		if id != value.ID && existing.Name == value.Name {
			return WebhookSubscription{}, ErrWebhookSubscriptionNameExists
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return WebhookSubscription{}, err
	}
	value.Version = nextHostedGroupVersion(current.Version)
	value.CreatedAt, value.UpdatedAt = current.CreatedAt, time.Now().UTC()
	s.webhookSubscriptions[value.ID] = cloneWebhookSubscription(value)
	return cloneWebhookSubscription(value), nil
}

func webhookSubscriptionAccepts(value WebhookSubscription, eventType WebhookEventType) bool {
	if !value.Enabled {
		return false
	}
	for _, candidate := range value.EventTypes {
		if candidate == eventType {
			return true
		}
	}
	return false
}

func (s *MemoryStore) EnqueueWebhookEvent(_ context.Context, event WebhookEvent) (WebhookEvent, error) {
	if err := event.domainEvent().Validate(); err != nil {
		return WebhookEvent{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enqueueWebhookEventLocked(event), nil
}

func (s *MemoryStore) enqueueWebhookEventLocked(event WebhookEvent) WebhookEvent {
	if event.ID == "" {
		event.ID = ids.New()
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	if len(event.Data) == 0 {
		event.Data = []byte("{}")
	}
	subscribed := false
	for _, subscription := range s.webhookSubscriptions {
		subscribed = subscribed || webhookSubscriptionAccepts(subscription, event.Type)
	}
	if !subscribed && !events.HasInternalConsumer(event.Type) {
		return cloneWebhookEvent(event)
	}
	s.webhookEvents[event.ID] = cloneWebhookEvent(event)
	s.eventSeq++
	if s.eventSeqs == nil {
		s.eventSeqs = map[string]int64{}
	}
	s.eventSeqs[event.ID] = s.eventSeq
	for _, subscription := range s.webhookSubscriptions {
		if !webhookSubscriptionAccepts(subscription, event.Type) {
			continue
		}
		now := event.OccurredAt
		delivery := WebhookDelivery{ID: ids.New(), EventID: event.ID, EventType: event.Type, SubscriptionID: subscription.ID, State: WebhookDeliveryPending, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
		s.webhookDeliveries[delivery.ID] = delivery
	}
	return cloneWebhookEvent(event)
}

func (s *MemoryStore) PageWebhookDeliveries(_ context.Context, query WebhookDeliveryQuery) (WebhookDeliveryPage, error) {
	limit := pageLimit(query.Limit, 100, 500)
	cursorAt, cursorID, after, err := decodeCreatedAtToken(query.PageToken, webhookDeliveriesFilter(query))
	if err != nil {
		return WebhookDeliveryPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]WebhookDelivery, 0, len(s.webhookDeliveries))
	for _, value := range s.webhookDeliveries {
		if query.SubscriptionID != "" && value.SubscriptionID != query.SubscriptionID || query.State != "" && value.State != query.State {
			continue
		}
		if after && !createdAtBefore(value.CreatedAt, value.ID, cursorAt, cursorID) {
			continue
		}
		value.SubscriptionName = s.webhookSubscriptions[value.SubscriptionID].Name
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		return newerFirst(values[i].CreatedAt, values[i].ID, values[j].CreatedAt, values[j].ID)
	})
	page := WebhookDeliveryPage{Items: values}
	if len(values) > limit {
		page.Items = values[:limit]
		last := page.Items[limit-1]
		page.NextPageToken = encodeCreatedAtToken(webhookDeliveriesFilter(query), last.CreatedAt, last.ID)
	}
	return page, nil
}

func (s *MemoryStore) GetWebhookDelivery(_ context.Context, id string) (WebhookDelivery, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.webhookDeliveries[id]
	if !ok {
		return WebhookDelivery{}, ErrNotFound
	}
	return value, nil
}

func (s *MemoryStore) ClaimWebhookDeliveries(_ context.Context, owner string, now time.Time, lease time.Duration, limit int) ([]WebhookDeliveryClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	candidates := make([]WebhookDelivery, 0)
	for _, value := range s.webhookDeliveries {
		due := (value.State == WebhookDeliveryPending || value.State == WebhookDeliveryRetrying) && !value.NextAttemptAt.After(now)
		expired := value.State == WebhookDeliveryDelivering && !value.LeaseExpiresAt.After(now)
		if expired && value.Attempts >= 8 {
			value.State, value.LeaseOwner, value.LeaseToken, value.LeaseExpiresAt, value.UpdatedAt = WebhookDeliveryDead, "", "", time.Time{}, now
			value.NextAttemptAt = time.Time{}
			s.webhookDeliveries[value.ID] = value
			continue
		}
		if due || expired {
			candidates = append(candidates, value)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].NextAttemptAt.Equal(candidates[j].NextAttemptAt) {
			return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
		}
		return candidates[i].NextAttemptAt.Before(candidates[j].NextAttemptAt)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	claims := make([]WebhookDeliveryClaim, 0, len(candidates))
	for _, delivery := range candidates {
		delivery.State, delivery.Attempts, delivery.LeaseOwner, delivery.LeaseToken, delivery.LeaseExpiresAt, delivery.UpdatedAt = WebhookDeliveryDelivering, delivery.Attempts+1, owner, uuid.NewString(), now.Add(lease), now
		delivery.NextAttemptAt = time.Time{}
		s.webhookDeliveries[delivery.ID] = delivery
		event, eventOK := s.webhookEvents[delivery.EventID]
		subscription, subscriptionOK := s.webhookSubscriptions[delivery.SubscriptionID]
		if eventOK && subscriptionOK {
			claims = append(claims, WebhookDeliveryClaim{Delivery: delivery, Event: cloneWebhookEvent(event), Subscription: cloneWebhookSubscription(subscription)})
		}
	}
	return claims, nil
}

func (s *MemoryStore) CompleteWebhookDelivery(_ context.Context, id, leaseToken string, status int, completedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.webhookDeliveries[id]
	if !ok {
		return ErrNotFound
	}
	if value.State != WebhookDeliveryDelivering || value.LeaseToken != leaseToken || !value.LeaseExpiresAt.After(completedAt) {
		return ErrVersionConflict
	}
	value.State, value.LastStatus, value.DeliveredAt, value.UpdatedAt = WebhookDeliverySucceeded, status, completedAt, completedAt
	value.NextAttemptAt, value.LeaseExpiresAt, value.LeaseOwner, value.LeaseToken, value.LastError = time.Time{}, time.Time{}, "", "", ""
	s.webhookDeliveries[id] = value
	return nil
}

func (s *MemoryStore) FailWebhookDelivery(_ context.Context, id, leaseToken string, failedAt, retryAt time.Time, status int, message string, dead bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.webhookDeliveries[id]
	if !ok {
		return ErrNotFound
	}
	if value.State != WebhookDeliveryDelivering || value.LeaseToken != leaseToken || !value.LeaseExpiresAt.After(failedAt) {
		return ErrVersionConflict
	}
	value.State, value.NextAttemptAt = WebhookDeliveryRetrying, retryAt
	if dead || value.Attempts >= 8 {
		value.State, value.NextAttemptAt = WebhookDeliveryDead, time.Time{}
	}
	value.LastStatus, value.LastError, value.LeaseOwner, value.LeaseToken, value.LeaseExpiresAt, value.UpdatedAt = status, message, "", "", time.Time{}, failedAt
	s.webhookDeliveries[id] = value
	return nil
}

func (s *MemoryStore) ReplayWebhookDelivery(ctx context.Context, id string, now time.Time, audit ...AuditRecord) (WebhookDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.webhookDeliveries[id]
	if !ok {
		return WebhookDelivery{}, ErrNotFound
	}
	if value.State != WebhookDeliveryDead {
		return WebhookDelivery{}, ErrInvalidWebhookDeliveryState
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return WebhookDelivery{}, err
	}
	value.State, value.Attempts, value.NextAttemptAt, value.LastStatus, value.LastError, value.UpdatedAt = WebhookDeliveryPending, 0, now, 0, "", now
	value.LeaseOwner, value.LeaseToken, value.LeaseExpiresAt = "", "", time.Time{}
	s.webhookDeliveries[id] = value
	return value, nil
}
