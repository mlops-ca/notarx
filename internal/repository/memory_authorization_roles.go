package repository

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

func cloneAuthorizationRole(role AuthorizationRole) AuthorizationRole {
	role.Scopes = append([]string(nil), role.Scopes...)
	return role
}

func (s *MemoryStore) CreateAuthorizationRole(ctx context.Context, role AuthorizationRole, audit ...AuditRecord) (AuthorizationRole, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := strings.TrimSpace(role.Name)
	if name == "" {
		return AuthorizationRole{}, ErrNameExists
	}
	for _, existing := range s.authorizationRoles {
		if strings.EqualFold(existing.Name, name) {
			return AuthorizationRole{}, ErrAuthorizationRoleNameExists
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return AuthorizationRole{}, err
	}
	now := time.Now().UTC()
	if role.ID == "" {
		role.ID = ids.New()
	}
	role.Name = name
	role.Version = "1"
	role.CreatedAt = now
	role.UpdatedAt = now
	role = cloneAuthorizationRole(role)
	s.authorizationRoles[role.ID] = role
	return cloneAuthorizationRole(role), nil
}

func (s *MemoryStore) ListAuthorizationRoles(_ context.Context) ([]AuthorizationRole, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]AuthorizationRole, 0, len(s.authorizationRoles))
	for _, role := range s.authorizationRoles {
		items = append(items, cloneAuthorizationRole(role))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func (s *MemoryStore) GetAuthorizationRole(_ context.Context, id string) (AuthorizationRole, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	role, ok := s.authorizationRoles[id]
	if !ok {
		return AuthorizationRole{}, ErrNotFound
	}
	return cloneAuthorizationRole(role), nil
}

func (s *MemoryStore) UpdateAuthorizationRole(ctx context.Context, role AuthorizationRole, expectedVersion string, audit ...AuditRecord) (AuthorizationRole, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.authorizationRoles[role.ID]
	if !ok {
		return AuthorizationRole{}, ErrNotFound
	}
	if current.Version != expectedVersion {
		return AuthorizationRole{}, ErrVersionConflict
	}
	name := strings.TrimSpace(role.Name)
	if name == "" {
		return AuthorizationRole{}, ErrNameExists
	}
	for id, existing := range s.authorizationRoles {
		if id != role.ID && strings.EqualFold(existing.Name, name) {
			return AuthorizationRole{}, ErrAuthorizationRoleNameExists
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return AuthorizationRole{}, err
	}
	role.Name = name
	role.Version = nextHostedGroupVersion(current.Version)
	role.CreatedAt = current.CreatedAt
	role.UpdatedAt = time.Now().UTC()
	role = cloneAuthorizationRole(role)
	s.authorizationRoles[role.ID] = role
	return cloneAuthorizationRole(role), nil
}

func (s *MemoryStore) DeleteAuthorizationRole(ctx context.Context, id string, audit ...AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.authorizationRoles[id]; !ok {
		return ErrNotFound
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	delete(s.authorizationRoles, id)
	return nil
}
