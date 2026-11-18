package repository

import "context"

func defaultPublicAccessPolicy() PublicAccessPolicy {
	return PublicAccessPolicy{Version: "1"}
}

func (s *MemoryStore) GetPublicAccessPolicy(_ context.Context) (PublicAccessPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.publicAccessPolicy.Version == "" {
		return defaultPublicAccessPolicy(), nil
	}
	return s.publicAccessPolicy, nil
}

func (s *MemoryStore) ReplacePublicAccessPolicy(_ context.Context, policy PublicAccessPolicy, expectedVersion string) (PublicAccessPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.publicAccessPolicy
	if current.Version == "" {
		current = defaultPublicAccessPolicy()
	}
	if current.Version != expectedVersion {
		return PublicAccessPolicy{}, ErrVersionConflict
	}
	policy.Version = nextHostedGroupVersion(current.Version)
	s.publicAccessPolicy = policy
	return policy, nil
}
