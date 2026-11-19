package repository

import (
	"context"
	"strconv"
	"time"
)

func (s *MemoryStore) GetOIDCSettings(_ context.Context) (OIDCSettings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.oidcSettings == nil {
		return OIDCSettings{}, ErrNotFound
	}
	return cloneOIDCSettings(*s.oidcSettings), nil
}

func (s *MemoryStore) ReplaceOIDCSettings(ctx context.Context, settings OIDCSettings, expectedVersion string, audit ...AuditRecord) (OIDCSettings, error) {
	settings = normalizeOIDCSettingsDefaults(settings)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.oidcSettings == nil && expectedVersion != "0" || s.oidcSettings != nil && s.oidcSettings.Version != expectedVersion {
		return OIDCSettings{}, ErrVersionConflict
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return OIDCSettings{}, err
	}
	if s.oidcSettings == nil {
		settings.Version = "1"
	} else {
		version, _ := strconv.ParseInt(s.oidcSettings.Version, 10, 64)
		settings.Version = strconv.FormatInt(version+1, 10)
	}
	settings.UpdatedAt = time.Now().UTC()
	stored := cloneOIDCSettings(settings)
	s.oidcSettings = &stored
	return cloneOIDCSettings(stored), nil
}

func cloneOIDCSettings(settings OIDCSettings) OIDCSettings {
	settings.Scopes = append([]string{}, settings.Scopes...)
	settings.AdminSubjects = append([]string{}, settings.AdminSubjects...)
	settings.MemberRoles = append([]string{}, settings.MemberRoles...)
	settings.AdminRoles = append([]string{}, settings.AdminRoles...)
	return settings
}
