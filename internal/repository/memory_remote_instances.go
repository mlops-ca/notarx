package repository

import (
	"context"
	"sort"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

const remoteInstancesFilter = "remote-instances"

func (s *MemoryStore) CreateRemoteInstance(ctx context.Context, value RemoteInstance, audit ...AuditRecord) (RemoteInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.remoteInstances {
		if existing.Name == value.Name {
			return RemoteInstance{}, ErrRemoteInstanceNameExists
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return RemoteInstance{}, err
	}
	if s.remoteInstances == nil {
		s.remoteInstances = make(map[string]RemoteInstance)
	}
	now := time.Now().UTC()
	value.Version, value.CreatedAt, value.UpdatedAt = "1", now, now
	s.remoteInstances[value.ID] = value
	return value, nil
}

func (s *MemoryStore) PageRemoteInstances(_ context.Context, query RemoteInstanceQuery) (RemoteInstancePage, error) {
	limit := pageLimit(query.Limit, 500, 500)
	afterName, after, err := decodeNameToken(query.PageToken, remoteInstancesFilter)
	if err != nil {
		return RemoteInstancePage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]RemoteInstance, 0, len(s.remoteInstances))
	for _, value := range s.remoteInstances {
		if !after || value.Name > afterName {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	page := RemoteInstancePage{Items: values}
	if len(values) > limit {
		page.Items = values[:limit]
		page.NextPageToken = EncodeKeysetToken(remoteInstancesFilter, page.Items[limit-1].Name)
	}
	return page, nil
}

func (s *MemoryStore) GetRemoteInstance(_ context.Context, id string) (RemoteInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.remoteInstances[id]
	if !ok {
		return RemoteInstance{}, ErrNotFound
	}
	return value, nil
}

func (s *MemoryStore) UpdateRemoteInstance(ctx context.Context, value RemoteInstance, expectedVersion string, audit ...AuditRecord) (RemoteInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.remoteInstances[value.ID]
	if !ok {
		return RemoteInstance{}, ErrNotFound
	}
	if current.Version != expectedVersion {
		return RemoteInstance{}, ErrVersionConflict
	}
	for id, existing := range s.remoteInstances {
		if id != value.ID && existing.Name == value.Name {
			return RemoteInstance{}, ErrRemoteInstanceNameExists
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return RemoteInstance{}, err
	}
	value.Version = nextHostedGroupVersion(current.Version)
	value.CreatedAt, value.UpdatedAt = current.CreatedAt, time.Now().UTC()
	s.remoteInstances[value.ID] = value
	return value, nil
}

func (s *MemoryStore) DeleteRemoteInstance(ctx context.Context, id string, audit ...AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.remoteInstances[id]; !ok {
		return ErrNotFound
	}
	for _, rule := range s.promotionRules {
		if rule.RemoteInstanceID == id {
			return ErrRemoteInstanceInUse
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	delete(s.remoteInstances, id)
	return nil
}

func (s *MemoryStore) RecordRemoteInstanceCheck(_ context.Context, id, state string, checkedAt time.Time) (RemoteInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.remoteInstances[id]
	if !ok {
		return RemoteInstance{}, ErrNotFound
	}
	value.State, value.LastCheckedAt, value.UpdatedAt = state, checkedAt.UTC(), time.Now().UTC()
	s.remoteInstances[id] = value
	return value, nil
}

func (s *MemoryStore) LocalInstanceID(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.localInstanceID == "" {
		s.localInstanceID = ids.New()
	}
	return s.localInstanceID, nil
}
