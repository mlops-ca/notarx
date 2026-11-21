package repository

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"
)

var (
	_ ImportSessionStore = (*MemoryStore)(nil)
	_ ImportSessionStore = (*PostgresStore)(nil)
)

func cloneImportSession(session ImportSession) ImportSession {
	session.FormatManifest = slices.Clone(session.FormatManifest)
	session.Evidence = slices.Clone(session.Evidence)
	session.ChainPath = slices.Clone(session.ChainPath)
	return session
}

// CreateImportSession implements ImportSessionStore.
func (s *MemoryStore) CreateImportSession(_ context.Context, session ImportSession, blobs []ImportBlob, limits ImportLimits) (ImportSession, bool, error) {
	if session.ID == "" || session.TargetRepositoryID == "" || session.IdempotencyKey == "" || session.ExpiresAt.IsZero() {
		return ImportSession{}, false, errors.New("import session needs an id, a target repository, an idempotency key and an expiry")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.importSessions {
		if existing.TargetRepositoryID == session.TargetRepositoryID && existing.IdempotencyKey == session.IdempotencyKey {
			if !equivalentImportRequest(existing, session) {
				return ImportSession{}, false, ErrIdempotencyConflict
			}
			return cloneImportSession(existing), true, nil
		}
	}
	deduped, total, err := dedupeImportBlobs(blobs)
	if err != nil {
		return ImportSession{}, false, err
	}
	if err = checkImportLimits(deduped, total, limits); err != nil {
		return ImportSession{}, false, err
	}
	var open int
	var staged int64
	for _, existing := range s.importSessions {
		if existing.State.Live() && existing.ExpiresAt.After(session.CreatedAt) {
			staged += existing.Bytes
			if existing.Principal == session.Principal {
				open++
			}
		}
	}
	if limits.MaxOpenSessionsPerPrincipal > 0 && open >= limits.MaxOpenSessionsPerPrincipal {
		return ImportSession{}, false, ErrImportTooManySessions
	}
	if limits.StagingQuotaBytes > 0 && staged+total > limits.StagingQuotaBytes {
		return ImportSession{}, false, ErrImportStagingQuota
	}
	session.State, session.Bytes, session.UpdatedAt = ImportSessionOpen, total, session.CreatedAt
	if session.ChainPath == nil {
		session.ChainPath = []PromotionChainHop{}
	}
	if s.importSessions == nil {
		s.importSessions, s.importBlobs = map[string]ImportSession{}, map[string]map[string]ImportBlob{}
	}
	s.importSessions[session.ID] = cloneImportSession(session)
	s.importBlobs[session.ID] = make(map[string]ImportBlob, len(deduped))
	for _, blob := range deduped {
		blob.SessionID, blob.StagingKey = session.ID, ImportStagingKey(session.ID, blob.Digest)
		s.importBlobs[session.ID][blob.Digest] = blob
	}
	return cloneImportSession(session), false, nil
}

// GetImportSession implements ImportSessionStore.
func (s *MemoryStore) GetImportSession(_ context.Context, id string) (ImportSession, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.importSessions[id]
	if !ok {
		return ImportSession{}, ErrNotFound
	}
	return cloneImportSession(session), nil
}

// MissingImportBlobs implements ImportSessionStore.
func (s *MemoryStore) MissingImportBlobs(_ context.Context, sessionID string) ([]ImportBlob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.importSessions[sessionID]; !ok {
		return nil, ErrNotFound
	}
	return missingImportBlobsLocked(s.importBlobs[sessionID]), nil
}

func missingImportBlobsLocked(blobs map[string]ImportBlob) []ImportBlob {
	missing := make([]ImportBlob, 0, len(blobs))
	for _, blob := range blobs {
		if !blob.Verified {
			missing = append(missing, blob)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].Digest < missing[j].Digest })
	return missing
}

// RecordImportBlob implements ImportSessionStore.
func (s *MemoryStore) RecordImportBlob(_ context.Context, sessionID, digest string, size int64, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.importSessions[sessionID]
	if !ok {
		return ErrNotFound
	}
	if session.State != ImportSessionOpen || !session.ExpiresAt.After(now) {
		return ErrImportSessionClosed
	}
	blob, ok := s.importBlobs[sessionID][digest]
	if !ok {
		return ErrNotFound
	}
	if blob.Size != size {
		return ErrImportBlobSizeMismatch
	}
	blob.Verified = true
	s.importBlobs[sessionID][digest] = blob
	session.UpdatedAt = now
	s.importSessions[sessionID] = session
	return nil
}

// BeginImportCommit implements ImportSessionStore.
func (s *MemoryStore) BeginImportCommit(_ context.Context, sessionID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.importSessions[sessionID]
	if !ok {
		return ErrNotFound
	}
	switch {
	case session.State == ImportSessionCommitting:
		return nil
	case session.State != ImportSessionOpen || !session.ExpiresAt.After(now):
		return ErrImportSessionClosed
	case len(missingImportBlobsLocked(s.importBlobs[sessionID])) > 0:
		return ErrImportBlobsMissing
	}
	session.State, session.UpdatedAt = ImportSessionCommitting, now
	s.importSessions[sessionID] = session
	return nil
}

// CompleteImportSession implements ImportSessionStore.
func (s *MemoryStore) CompleteImportSession(_ context.Context, sessionID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.importSessions[sessionID]
	if !ok {
		return ErrNotFound
	}
	if session.State != ImportSessionCommitting {
		return ErrImportSessionClosed
	}
	session.State, session.UpdatedAt = ImportSessionCommitted, now
	s.importSessions[sessionID] = session
	return nil
}

// AbortImportSession implements ImportSessionStore.
func (s *MemoryStore) AbortImportSession(_ context.Context, sessionID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.importSessions[sessionID]
	if !ok {
		return ErrNotFound
	}
	switch {
	case session.State == ImportSessionAborted:
		return nil
	case !session.State.Live():
		return ErrImportSessionClosed
	}
	session.State, session.UpdatedAt = ImportSessionAborted, now
	s.importSessions[sessionID] = session
	return nil
}

// ReapImportSessions implements ImportSessionStore.
func (s *MemoryStore) ReapImportSessions(ctx context.Context, now time.Time, limit int, remove func(ctx context.Context, keys []string) error) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.importSessions))
	for id, session := range s.importSessions {
		if (session.State.Live() && !session.ExpiresAt.After(now)) || (!session.State.Live() && len(stagingKeysLocked(s.importBlobs[id])) > 0) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := s.importSessions[ids[i]], s.importSessions[ids[j]]
		if !a.ExpiresAt.Equal(b.ExpiresAt) {
			return a.ExpiresAt.Before(b.ExpiresAt)
		}
		return a.ID < b.ID
	})
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	var failures []error
	finished := 0
	for _, id := range ids {
		keys := stagingKeysLocked(s.importBlobs[id])
		if len(keys) > 0 {
			if err := remove(ctx, keys); err != nil {
				failures = append(failures, err)
				continue
			}
			for digest, blob := range s.importBlobs[id] {
				blob.StagingKey = ""
				s.importBlobs[id][digest] = blob
			}
		}
		session := s.importSessions[id]
		if session.State.Live() {
			session.State = ImportSessionExpired
		}
		session.UpdatedAt = now
		s.importSessions[id] = session
		finished++
	}
	return finished, errors.Join(failures...)
}

func stagingKeysLocked(blobs map[string]ImportBlob) []string {
	var keys []string
	for _, blob := range blobs {
		if blob.StagingKey != "" {
			keys = append(keys, blob.StagingKey)
		}
	}
	sort.Strings(keys)
	return keys
}
