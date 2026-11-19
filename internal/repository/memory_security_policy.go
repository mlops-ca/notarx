package repository

import "context"

func (s *MemoryStore) GetRepositorySecurityPolicy(_ context.Context, repositoryID string) (RepositorySecurityPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.hostedRepositories[repositoryID]; !ok {
		return RepositorySecurityPolicy{}, ErrNotFound
	}
	policy, ok := s.securityPolicies[repositoryID]
	if !ok {
		return DefaultRepositorySecurityPolicy(), nil
	}
	return CloneRepositorySecurityPolicy(policy), nil
}

func (s *MemoryStore) ReplaceRepositorySecurityPolicy(ctx context.Context, repositoryID string, policy RepositorySecurityPolicy, expectedVersion string, audit ...AuditRecord) (RepositorySecurityPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hostedRepositories[repositoryID]; !ok {
		return RepositorySecurityPolicy{}, ErrNotFound
	}
	current, ok := s.securityPolicies[repositoryID]
	if !ok {
		current = DefaultRepositorySecurityPolicy()
	}
	if current.Version != expectedVersion {
		return RepositorySecurityPolicy{}, ErrVersionConflict
	}
	if policy.MaxAllowedSeverity == "" {
		policy.MaxAllowedSeverity = current.MaxAllowedSeverity
	}
	if policy.AllowedLicenses == nil {
		policy.AllowedLicenses = []string{}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return RepositorySecurityPolicy{}, err
	}
	policy.Version = nextHostedGroupVersion(current.Version)
	policy = CloneRepositorySecurityPolicy(policy)
	s.securityPolicies[repositoryID] = policy
	return CloneRepositorySecurityPolicy(policy), nil
}
