package repository

import (
	"context"
	"sort"
	"time"
)

func (s *MemoryStore) CreateAPIKey(_ context.Context, key APIKey) (APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key.CreatedAt = time.Now().UTC()
	s.apiKeys[key.ID] = key
	return key, nil
}

func (s *MemoryStore) ListAPIKeys(_ context.Context) ([]APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]APIKey, 0, len(s.apiKeys))
	for _, key := range s.apiKeys {
		if key.ServiceAccountID != "" {
			continue
		}
		key.Roles = append([]string(nil), key.Roles...)
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].CreatedAt.Equal(keys[j].CreatedAt) {
			return keys[i].ID < keys[j].ID
		}
		return keys[i].CreatedAt.Before(keys[j].CreatedAt)
	})
	return keys, nil
}

func (s *MemoryStore) FindActiveAPIKeyByHash(_ context.Context, hash string) (APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	for _, key := range s.apiKeys {
		if key.SecretHash == hash && key.RevokedAt == nil && (key.ExpiresAt == nil || now.Before(*key.ExpiresAt)) {
			if key.ServiceAccountID != "" {
				account, ok := s.serviceAccounts[key.ServiceAccountID]
				if !ok || account.State != ServiceAccountActive {
					return APIKey{}, ErrNotFound
				}
			}
			key.Roles = append([]string(nil), key.Roles...)
			return key, nil
		}
	}
	return APIKey{}, ErrNotFound
}

func (s *MemoryStore) RevokeAPIKey(_ context.Context, id string) (APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.apiKeys[id]
	if !ok || key.ServiceAccountID != "" {
		return APIKey{}, ErrNotFound
	}
	if key.RevokedAt == nil {
		now := time.Now().UTC()
		key.RevokedAt = &now
		s.apiKeys[id] = key
	}
	return key, nil
}

// TouchAPIKeys records batched last-use times, skipping a key whose stored value
// is not older than notBefore.
func (s *MemoryStore) TouchAPIKeys(_ context.Context, uses []APIKeyUse, notBefore time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, use := range uses {
		key, ok := s.apiKeys[use.ID]
		if !ok || (key.LastUsedAt != nil && !key.LastUsedAt.Before(notBefore)) {
			continue
		}
		at := use.At
		key.LastUsedAt = &at
		s.apiKeys[use.ID] = key
	}
	return nil
}
