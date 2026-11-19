package repository

import (
	"context"
	"sort"
	"time"
)

func (s *MemoryStore) LockConsoleThemeCatalog(_ context.Context) (func(), error) {
	s.consoleThemeCatalogMu.Lock()
	return s.consoleThemeCatalogMu.Unlock, nil
}

func cloneConsoleThemePackage(theme ConsoleThemePackage) ConsoleThemePackage {
	theme.Payload = append([]byte(nil), theme.Payload...)
	return theme
}

func (s *MemoryStore) ListConsoleThemePackages(_ context.Context) ([]ConsoleThemePackage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	themes := make([]ConsoleThemePackage, 0, len(s.consoleThemePackages))
	for _, theme := range s.consoleThemePackages {
		themes = append(themes, cloneConsoleThemePackage(theme))
	}
	sort.Slice(themes, func(i, j int) bool { return themes[i].ID < themes[j].ID })
	return themes, nil
}

func (s *MemoryStore) GetConsoleThemePackage(_ context.Context, id string) (ConsoleThemePackage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	theme, ok := s.consoleThemePackages[id]
	if !ok {
		return ConsoleThemePackage{}, ErrConsoleThemeNotFound
	}
	return cloneConsoleThemePackage(theme), nil
}

func (s *MemoryStore) CreateConsoleThemePackage(ctx context.Context, theme ConsoleThemePackage, audit ...AuditRecord) (ConsoleThemePackage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.consoleThemePackages[theme.ID]; exists {
		return ConsoleThemePackage{}, ErrConsoleThemeExists
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return ConsoleThemePackage{}, err
	}
	now := time.Now().UTC()
	theme.Version = "1"
	theme.CreatedAt = now
	theme.UpdatedAt = now
	theme = cloneConsoleThemePackage(theme)
	s.consoleThemePackages[theme.ID] = theme
	return cloneConsoleThemePackage(theme), nil
}

func (s *MemoryStore) ReplaceConsoleThemePackage(ctx context.Context, theme ConsoleThemePackage, expectedVersion string, audit ...AuditRecord) (ConsoleThemePackage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.consoleThemePackages[theme.ID]
	if !exists {
		return ConsoleThemePackage{}, ErrConsoleThemeNotFound
	}
	if current.Version != expectedVersion {
		return ConsoleThemePackage{}, ErrVersionConflict
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return ConsoleThemePackage{}, err
	}
	theme.Version = nextHostedGroupVersion(current.Version)
	theme.CreatedAt = current.CreatedAt
	theme.UpdatedAt = time.Now().UTC()
	theme = cloneConsoleThemePackage(theme)
	s.consoleThemePackages[theme.ID] = theme
	return cloneConsoleThemePackage(theme), nil
}

func (s *MemoryStore) DeleteConsoleThemePackage(ctx context.Context, id, expectedVersion string, audit ...AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.consoleThemePackages[id]
	if !exists {
		return ErrConsoleThemeNotFound
	}
	if current.Version != expectedVersion {
		return ErrVersionConflict
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	delete(s.consoleThemePackages, id)
	return nil
}
