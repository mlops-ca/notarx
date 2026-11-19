package repository

import (
	"context"
	"sort"
	"time"

	"github.com/mlops-ca/notarx/internal/identity"
)

func cloneProvider(provider identity.Provider) identity.Provider {
	provider.Settings = append([]byte(nil), provider.Settings...)
	return provider
}

func (s *MemoryStore) slugTakenLocked(slug, exceptID string) bool {
	for id, provider := range s.identityProviders {
		if provider.Slug == slug && id != exceptID {
			return true
		}
	}
	return false
}

func (s *MemoryStore) ListIdentityProviders(context.Context) ([]identity.Provider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	providers := make([]identity.Provider, 0, len(s.identityProviders))
	for _, provider := range s.identityProviders {
		providers = append(providers, cloneProvider(provider))
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Slug < providers[j].Slug })
	return providers, nil
}

func (s *MemoryStore) GetIdentityProvider(_ context.Context, id string) (identity.Provider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	provider, ok := s.identityProviders[id]
	if !ok {
		return identity.Provider{}, ErrNotFound
	}
	return cloneProvider(provider), nil
}

func (s *MemoryStore) GetIdentityProviderBySlug(_ context.Context, slug string) (identity.Provider, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, provider := range s.identityProviders {
		if provider.Slug == slug {
			return cloneProvider(provider), nil
		}
	}
	return identity.Provider{}, ErrNotFound
}

func (s *MemoryStore) CreateIdentityProvider(ctx context.Context, provider identity.Provider, audit ...AuditRecord) (identity.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slugTakenLocked(provider.Slug, "") {
		return identity.Provider{}, ErrIdentityProviderSlugExists
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return identity.Provider{}, err
	}
	now := time.Now().UTC()
	provider.Version, provider.CreatedAt, provider.UpdatedAt = 1, now, now
	if s.identityProviders == nil {
		s.identityProviders = map[string]identity.Provider{}
	}
	s.identityProviders[provider.ID] = cloneProvider(provider)
	return cloneProvider(provider), nil
}

func (s *MemoryStore) ReplaceIdentityProvider(ctx context.Context, provider identity.Provider, expectedVersion int64, audit ...AuditRecord) (identity.Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.identityProviders[provider.ID]
	if !ok || current.Kind != provider.Kind {
		return identity.Provider{}, ErrNotFound
	}
	if current.Version != expectedVersion {
		return identity.Provider{}, ErrVersionConflict
	}
	if s.slugTakenLocked(provider.Slug, provider.ID) {
		return identity.Provider{}, ErrIdentityProviderSlugExists
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return identity.Provider{}, err
	}
	provider.Version, provider.CreatedAt, provider.UpdatedAt = current.Version+1, current.CreatedAt, time.Now().UTC()
	s.identityProviders[provider.ID] = cloneProvider(provider)
	return cloneProvider(provider), nil
}

func (s *MemoryStore) DeleteIdentityProvider(ctx context.Context, id string, expectedVersion int64, audit ...AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.identityProviders[id]
	if !ok {
		return ErrNotFound
	}
	for _, linked := range s.userIdentities {
		if linked.ProviderID == id {
			return ErrIdentityProviderInUse
		}
	}
	if current.Version != expectedVersion {
		return ErrVersionConflict
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	delete(s.identityProviders, id)
	return nil
}

func (s *MemoryStore) SeedIdentityProvider(_ context.Context, provider identity.Provider) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.identityProviders) > 0 {
		return false, nil
	}
	now := time.Now().UTC()
	provider.Version, provider.CreatedAt, provider.UpdatedAt = 1, now, now
	s.identityProviders = map[string]identity.Provider{provider.ID: cloneProvider(provider)}
	return true, nil
}
