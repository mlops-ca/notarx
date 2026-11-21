package repository

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *MemoryStore) CreateServiceAccount(ctx context.Context, account ServiceAccount, audit ...AuditRecord) (ServiceAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.serviceAccounts {
		if strings.EqualFold(existing.Name, account.Name) {
			return ServiceAccount{}, ErrNameExists
		}
	}
	if account.AssertionSubject != "" {
		if _, taken := s.assertions.subjects[account.AssertionSubject]; taken {
			return ServiceAccount{}, ErrAssertionSubjectReserved
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return ServiceAccount{}, err
	}
	if account.AssertionSubject != "" {
		if s.assertions.subjects == nil {
			s.assertions.subjects = map[string]memoryAssertionSubject{}
		}
		s.assertions.subjects[account.AssertionSubject] = memoryAssertionSubject{accountID: account.ID, createdAt: time.Now().UTC()}
	}
	now := time.Now().UTC()
	account.State = ServiceAccountActive
	account.CreatedAt = now
	account.UpdatedAt = now
	account.Version = "1"
	s.serviceAccounts[account.ID] = account
	return account, nil
}

func (s *MemoryStore) CreateServiceAccountCredential(ctx context.Context, credential APIKey, audit ...AuditRecord) (APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.serviceAccounts[credential.ServiceAccountID]
	if !ok {
		return APIKey{}, ErrNotFound
	}
	if account.State != ServiceAccountActive {
		return APIKey{}, ErrServiceAccountDisabled
	}
	if _, exists := s.apiKeys[credential.ID]; exists {
		return APIKey{}, ErrNameExists
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return APIKey{}, err
	}
	credential.CreatedAt = time.Now().UTC()
	credential.Roles = append([]string(nil), credential.Roles...)
	s.apiKeys[credential.ID] = credential
	return credential, nil
}

func (s *MemoryStore) ListServiceAccounts(_ context.Context, limit int, afterID string) ([]ServiceAccount, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	accounts := make([]ServiceAccount, 0, len(s.serviceAccounts))
	for _, account := range s.serviceAccounts {
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
	start := sort.Search(len(accounts), func(i int) bool { return accounts[i].ID > afterID })
	accounts = accounts[start:]
	if len(accounts) > limit {
		accounts = accounts[:limit]
	}
	return accounts, nil
}

func (s *MemoryStore) GetServiceAccount(_ context.Context, id string) (ServiceAccount, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	account, ok := s.serviceAccounts[id]
	if !ok {
		return ServiceAccount{}, ErrNotFound
	}
	return account, nil
}

func (s *MemoryStore) UpdateServiceAccount(ctx context.Context, update ServiceAccountUpdate, expectedVersion string, audit ...AuditRecord) (ServiceAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.serviceAccounts[update.ID]
	if !ok {
		return ServiceAccount{}, ErrNotFound
	}
	if account.Version != expectedVersion {
		return ServiceAccount{}, ErrVersionConflict
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return ServiceAccount{}, err
	}
	if update.Description != nil {
		account.Description = *update.Description
	}
	if update.State != nil {
		account.State = *update.State
	}
	version, err := strconv.Atoi(account.Version)
	if err != nil {
		return ServiceAccount{}, err
	}
	account.Version = strconv.Itoa(version + 1)
	account.UpdatedAt = time.Now().UTC()
	s.serviceAccounts[account.ID] = account
	return account, nil
}

func (s *MemoryStore) ListServiceAccountCredentials(_ context.Context, accountID string, limit int, afterID string) ([]APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.serviceAccounts[accountID]; !ok {
		return nil, ErrNotFound
	}
	credentials := make([]APIKey, 0)
	for _, key := range s.apiKeys {
		if key.ServiceAccountID == accountID {
			key.Roles = append([]string(nil), key.Roles...)
			credentials = append(credentials, key)
		}
	}
	sort.Slice(credentials, func(i, j int) bool { return credentials[i].ID < credentials[j].ID })
	start := sort.Search(len(credentials), func(i int) bool { return credentials[i].ID > afterID })
	credentials = credentials[start:]
	if len(credentials) > limit {
		credentials = credentials[:limit]
	}
	return credentials, nil
}

func (s *MemoryStore) RevokeServiceAccountCredential(ctx context.Context, accountID, credentialID string, audit ...AuditRecord) (APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.apiKeys[credentialID]
	if !ok || key.ServiceAccountID != accountID {
		return APIKey{}, ErrNotFound
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return APIKey{}, err
	}
	if key.RevokedAt == nil {
		now := time.Now().UTC()
		key.RevokedAt = &now
		s.apiKeys[credentialID] = key
	}
	return key, nil
}

// memoryAssertionState holds the assertion allow-lists and the reserved subjects (ADR 0020).
type memoryAssertionState struct {
	// audiences maps an account id to its allow-list keyed by audience.
	audiences map[string]map[string]AssertionAudience
	// subjects maps a reserved subject to its reservation; an empty account id is a subject whose account was deleted.
	subjects map[string]memoryAssertionSubject
}

type memoryAssertionSubject struct {
	accountID string
	createdAt time.Time
}

func (s *MemoryStore) ListAssertionSubjects(_ context.Context) ([]AssertionSubject, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	subjects := make([]AssertionSubject, 0, len(s.assertions.subjects))
	for subject, reservation := range s.assertions.subjects {
		subjects = append(subjects, AssertionSubject{Subject: subject, ServiceAccountID: reservation.accountID, CreatedAt: reservation.createdAt})
	}
	sort.Slice(subjects, func(i, j int) bool { return subjects[i].Subject < subjects[j].Subject })
	return subjects, nil
}

func (s *MemoryStore) ReleaseAssertionSubject(ctx context.Context, subject string, audit ...AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	reservation, ok := s.assertions.subjects[subject]
	if !ok {
		return ErrNotFound
	}
	if reservation.accountID != "" {
		return ErrAssertionSubjectHeld
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	delete(s.assertions.subjects, subject)
	return nil
}

func (s *MemoryStore) ListAssertionAudiences(_ context.Context, accountID string) ([]AssertionAudience, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.serviceAccounts[accountID]; !ok {
		return nil, ErrNotFound
	}
	entries := make([]AssertionAudience, 0, len(s.assertions.audiences[accountID]))
	for _, entry := range s.assertions.audiences[accountID] {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Audience < entries[j].Audience })
	return entries, nil
}

func (s *MemoryStore) GetAssertionAudience(_ context.Context, accountID, audience string) (AssertionAudience, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.assertions.audiences[accountID][audience]
	if !ok {
		return AssertionAudience{}, ErrNotFound
	}
	return entry, nil
}

func (s *MemoryStore) PutAssertionAudience(ctx context.Context, entry AssertionAudience, audit ...AuditRecord) (AssertionAudience, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.serviceAccounts[entry.ServiceAccountID]
	if !ok {
		return AssertionAudience{}, false, ErrNotFound
	}
	if account.AssertionSubject == "" {
		return AssertionAudience{}, false, ErrAssertionSubjectMissing
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return AssertionAudience{}, false, err
	}
	if s.assertions.audiences == nil {
		s.assertions.audiences = map[string]map[string]AssertionAudience{}
	}
	if s.assertions.audiences[entry.ServiceAccountID] == nil {
		s.assertions.audiences[entry.ServiceAccountID] = map[string]AssertionAudience{}
	}
	existing, found := s.assertions.audiences[entry.ServiceAccountID][entry.Audience]
	if found {
		entry.CreatedAt = existing.CreatedAt
	} else {
		entry.CreatedAt = time.Now().UTC()
	}
	s.assertions.audiences[entry.ServiceAccountID][entry.Audience] = entry
	return entry, !found, nil
}

func (s *MemoryStore) DeleteAssertionAudience(ctx context.Context, accountID, audience string, audit ...AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.assertions.audiences[accountID][audience]; !ok {
		return ErrNotFound
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	delete(s.assertions.audiences[accountID], audience)
	return nil
}
