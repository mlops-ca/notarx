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
	session = normalizeImportSession(session)
	deduped, total, err := dedupeImportBlobs(blobs)
	if err != nil {
		return ImportSession{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A dead session gives its key to the new one, whatever the new one asks for, but only if the new one is created.
	var freed string
	for id, existing := range s.importSessions {
		if existing.TargetRepositoryID == session.TargetRepositoryID && existing.IdempotencyKey == session.IdempotencyKey {
			if importSessionDead(existing, session.CreatedAt) {
				freed = id
				continue
			}
			if !equivalentImportRequest(existing, session) {
				return ImportSession{}, false, ErrIdempotencyConflict
			}
			return cloneImportSession(existing), true, nil
		}
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
	if !withinStagingQuota(limits.StagingQuotaBytes, staged, total) {
		return ImportSession{}, false, ErrImportStagingQuota
	}
	if freed != "" {
		dead := s.importSessions[freed]
		dead.IdempotencyKey = freedImportKey(dead)
		s.importSessions[freed] = dead
	}
	session.State, session.Bytes, session.UpdatedAt = ImportSessionOpen, total, session.CreatedAt
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

// GetImportBlob implements ImportSessionStore.
func (s *MemoryStore) GetImportBlob(_ context.Context, sessionID, digest string) (ImportBlob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	blob, ok := s.importBlobs[sessionID][digest]
	if !ok {
		return ImportBlob{}, ErrNotFound
	}
	return blob, nil
}

// ListImportBlobs implements ImportSessionStore.
func (s *MemoryStore) ListImportBlobs(_ context.Context, sessionID string) ([]ImportBlob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.importSessions[sessionID]; !ok {
		return nil, ErrNotFound
	}
	blobs := make([]ImportBlob, 0, len(s.importBlobs[sessionID]))
	for _, blob := range s.importBlobs[sessionID] {
		blobs = append(blobs, blob)
	}
	sort.Slice(blobs, func(i, j int) bool { return blobs[i].Digest < blobs[j].Digest })
	return blobs, nil
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
	if grace := now.Add(ImportCommitGrace); grace.After(session.ExpiresAt) {
		session.ExpiresAt = grace
	}
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
	case session.State != ImportSessionOpen:
		return ErrImportSessionClosed
	}
	session.State, session.UpdatedAt = ImportSessionAborted, now
	s.importSessions[sessionID] = session
	return nil
}

// ReapImportSessions implements ImportSessionStore. A session being reaped is marked, so overlapping passes divide the work
// as the SKIP LOCKED passes of the PostgreSQL store do, and the store is not locked while the staging objects are deleted.
func (s *MemoryStore) ReapImportSessions(ctx context.Context, now time.Time, limit int, remove func(ctx context.Context, keys []string) error) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	var failures []error
	tried := map[string]bool{}
	finished := 0
	for finished < limit {
		id, keys, ok := s.claimImportSessionToReap(now, tried)
		if !ok {
			break
		}
		tried[id] = true
		if len(keys) > 0 {
			if err := remove(ctx, keys); err != nil {
				failures = append(failures, err)
				s.releaseImportSessionReap(id)
				continue
			}
		}
		s.finishImportSessionReap(id, now, len(keys) > 0)
		finished++
	}
	return finished, errors.Join(failures...)
}

// claimImportSessionToReap marks the oldest session that needs reaping and that no pass has tried or holds, and returns its
// staging keys.
func (s *MemoryStore) claimImportSessionToReap(now time.Time, tried map[string]bool) (id string, keys []string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *ImportSession
	for candidate, session := range s.importSessions {
		if tried[candidate] || s.importReaping[candidate] {
			continue
		}
		if !importSessionNeedsReaping(session, len(stagingKeysLocked(s.importBlobs[candidate])) > 0, now) {
			continue
		}
		if best == nil || session.ExpiresAt.Before(best.ExpiresAt) || (session.ExpiresAt.Equal(best.ExpiresAt) && session.ID < best.ID) {
			picked := session
			best = &picked
		}
	}
	if best == nil {
		return "", nil, false
	}
	if s.importReaping == nil {
		s.importReaping = map[string]bool{}
	}
	s.importReaping[best.ID] = true
	return best.ID, stagingKeysLocked(s.importBlobs[best.ID]), true
}

func (s *MemoryStore) releaseImportSessionReap(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.importReaping, id)
}

// finishImportSessionReap empties the staging keys of a reaped session and expires it when it was still live.
func (s *MemoryStore) finishImportSessionReap(id string, now time.Time, hadKeys bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.importReaping, id)
	if hadKeys {
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
