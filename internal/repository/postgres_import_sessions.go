package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const importSessionColumns = `id::text,target_repository_id::text,principal,format,coordinate,digest,format_manifest,idempotency_key,chain_path,evidence,state,bytes,created_at,updated_at,expires_at`

func scanImportSession(scanner interface{ Scan(...any) error }) (ImportSession, error) {
	var session ImportSession
	var manifest, chain, evidence []byte
	if err := scanner.Scan(&session.ID, &session.TargetRepositoryID, &session.Principal, &session.Format, &session.Coordinate, &session.Digest, &manifest, &session.IdempotencyKey,
		&chain, &evidence, &session.State, &session.Bytes, &session.CreatedAt, &session.UpdatedAt, &session.ExpiresAt); err != nil {
		return ImportSession{}, err
	}
	session.FormatManifest, session.Evidence = manifest, evidence
	if err := json.Unmarshal(chain, &session.ChainPath); err != nil {
		return ImportSession{}, err
	}
	return session, nil
}

// importCreateLock serializes the creation of import sessions: the open-session and staging-quota limits count the other
// sessions, so two creations must not both pass the count. Creation is rare next to the blob traffic it admits.
const importCreateLock = `SELECT pg_advisory_xact_lock(hashtextextended('notarx/import-session-create', 0))`

// CreateImportSession implements ImportSessionStore.
func (s *PostgresStore) CreateImportSession(ctx context.Context, session ImportSession, blobs []ImportBlob, limits ImportLimits) (ImportSession, bool, error) {
	if session.ID == "" || session.TargetRepositoryID == "" || session.IdempotencyKey == "" || session.ExpiresAt.IsZero() {
		return ImportSession{}, false, errors.New("import session needs an id, a target repository, an idempotency key and an expiry")
	}
	session = normalizeImportSession(session)
	deduped, total, err := dedupeImportBlobs(blobs)
	if err != nil {
		return ImportSession{}, false, err
	}
	chain, err := json.Marshal(session.ChainPath)
	if err != nil {
		return ImportSession{}, false, err
	}
	var evidence any
	if len(session.Evidence) > 0 {
		evidence = []byte(session.Evidence)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportSession{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, importCreateLock); err != nil {
		return ImportSession{}, false, err
	}
	existing, err := scanImportSession(tx.QueryRowContext(ctx, `SELECT `+importSessionColumns+` FROM import_sessions WHERE target_repository_id=$1::uuid AND idempotency_key=$2`, session.TargetRepositoryID, session.IdempotencyKey))
	switch {
	case err == nil:
		if !importSessionDead(existing, session.CreatedAt) {
			if !equivalentImportRequest(existing, session) {
				return ImportSession{}, false, ErrIdempotencyConflict
			}
			return existing, true, nil
		}
		// A dead session keeps its row for the reaper but gives its key to the new one, whatever the new one asks for; a
		// refused limit rolls the rename back with the rest.
		if _, err = tx.ExecContext(ctx, `UPDATE import_sessions SET idempotency_key=$2 WHERE id=$1::uuid`, existing.ID, freedImportKey(existing)); err != nil {
			return ImportSession{}, false, err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return ImportSession{}, false, err
	}
	if err = checkImportLimits(deduped, total, limits); err != nil {
		return ImportSession{}, false, err
	}
	var open int
	var staged int64
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE principal=$2),COALESCE(sum(bytes),0)::bigint FROM import_sessions WHERE state IN ('open','committing') AND expires_at > $1`,
		session.CreatedAt, session.Principal).Scan(&open, &staged); err != nil {
		return ImportSession{}, false, err
	}
	if limits.MaxOpenSessionsPerPrincipal > 0 && open >= limits.MaxOpenSessionsPerPrincipal {
		return ImportSession{}, false, ErrImportTooManySessions
	}
	if !withinStagingQuota(limits.StagingQuotaBytes, staged, total) {
		return ImportSession{}, false, ErrImportStagingQuota
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO import_sessions (id,target_repository_id,principal,format,coordinate,digest,format_manifest,idempotency_key,chain_path,evidence,state,bytes,created_at,updated_at,expires_at)
VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6,$7::jsonb,$8,$9::jsonb,$10::jsonb,'open',$11,$12,$12,$13)`,
		session.ID, session.TargetRepositoryID, session.Principal, session.Format, session.Coordinate, session.Digest, []byte(session.FormatManifest), session.IdempotencyKey, chain, evidence, total, session.CreatedAt, session.ExpiresAt); err != nil {
		return ImportSession{}, false, err
	}
	for _, blob := range deduped {
		if _, err = tx.ExecContext(ctx, `INSERT INTO import_blobs (session_id,digest,size,staging_key,created_at) VALUES ($1::uuid,$2,$3,$4,$5)`,
			session.ID, blob.Digest, blob.Size, ImportStagingKey(session.ID, blob.Digest), session.CreatedAt); err != nil {
			return ImportSession{}, false, err
		}
	}
	stored, err := scanImportSession(tx.QueryRowContext(ctx, `SELECT `+importSessionColumns+` FROM import_sessions WHERE id=$1::uuid`, session.ID))
	if err != nil {
		return ImportSession{}, false, err
	}
	return stored, false, tx.Commit()
}

// GetImportSession implements ImportSessionStore.
func (s *PostgresStore) GetImportSession(ctx context.Context, id string) (ImportSession, error) {
	session, err := scanImportSession(s.db.QueryRowContext(ctx, `SELECT `+importSessionColumns+` FROM import_sessions WHERE id=$1::uuid`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ImportSession{}, ErrNotFound
	}
	return session, err
}

// MissingImportBlobs implements ImportSessionStore.
func (s *PostgresStore) MissingImportBlobs(ctx context.Context, sessionID string) ([]ImportBlob, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM import_sessions WHERE id=$1::uuid)`, sessionID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT session_id::text,digest,size,staging_key,verified FROM import_blobs WHERE session_id=$1::uuid AND NOT verified ORDER BY digest`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	missing := []ImportBlob{}
	for rows.Next() {
		var blob ImportBlob
		if err = rows.Scan(&blob.SessionID, &blob.Digest, &blob.Size, &blob.StagingKey, &blob.Verified); err != nil {
			return nil, err
		}
		missing = append(missing, blob)
	}
	return missing, rows.Err()
}

// lockImportSession reads the session state under a row lock.
func lockImportSession(ctx context.Context, tx *sql.Tx, id string) (state ImportSessionState, expiresAt time.Time, err error) {
	err = tx.QueryRowContext(ctx, `SELECT state,expires_at FROM import_sessions WHERE id=$1::uuid FOR UPDATE`, id).Scan(&state, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrNotFound
	}
	return state, expiresAt, err
}

// RecordImportBlob implements ImportSessionStore.
func (s *PostgresStore) RecordImportBlob(ctx context.Context, sessionID, digest string, size int64, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	state, expiresAt, err := lockImportSession(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	if state != ImportSessionOpen || !expiresAt.After(now) {
		return ErrImportSessionClosed
	}
	var claimed int64
	err = tx.QueryRowContext(ctx, `SELECT size FROM import_blobs WHERE session_id=$1::uuid AND digest=$2`, sessionID, digest).Scan(&claimed)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if claimed != size {
		return ErrImportBlobSizeMismatch
	}
	if _, err = tx.ExecContext(ctx, `UPDATE import_blobs SET verified=true WHERE session_id=$1::uuid AND digest=$2`, sessionID, digest); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE import_sessions SET updated_at=$2 WHERE id=$1::uuid`, sessionID, now); err != nil {
		return err
	}
	return tx.Commit()
}

// BeginImportCommit implements ImportSessionStore.
func (s *PostgresStore) BeginImportCommit(ctx context.Context, sessionID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	state, expiresAt, err := lockImportSession(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	switch {
	case state == ImportSessionCommitting:
		return nil
	case state != ImportSessionOpen || !expiresAt.After(now):
		return ErrImportSessionClosed
	}
	var unverified bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM import_blobs WHERE session_id=$1::uuid AND NOT verified)`, sessionID).Scan(&unverified); err != nil {
		return err
	}
	if unverified {
		return ErrImportBlobsMissing
	}
	if _, err = tx.ExecContext(ctx, `UPDATE import_sessions SET state='committing',updated_at=$2,expires_at=GREATEST(expires_at,$3) WHERE id=$1::uuid`, sessionID, now, now.Add(ImportCommitGrace)); err != nil {
		return err
	}
	return tx.Commit()
}

// CompleteImportSession implements ImportSessionStore.
func (s *PostgresStore) CompleteImportSession(ctx context.Context, sessionID string, now time.Time) error {
	return s.transitionImportSession(ctx, sessionID, now, ImportSessionCommitted, ImportSessionCommitting)
}

// AbortImportSession implements ImportSessionStore. Aborting an aborted session changes nothing.
func (s *PostgresStore) AbortImportSession(ctx context.Context, sessionID string, now time.Time) error {
	err := s.transitionImportSession(ctx, sessionID, now, ImportSessionAborted, ImportSessionOpen)
	if !errors.Is(err, ErrImportSessionClosed) {
		return err
	}
	session, getErr := s.GetImportSession(ctx, sessionID)
	if getErr == nil && session.State == ImportSessionAborted {
		return nil
	}
	return err
}

func (s *PostgresStore) transitionImportSession(ctx context.Context, sessionID string, now time.Time, to ImportSessionState, from ...ImportSessionState) error {
	allowed := make([]string, len(from))
	for i, state := range from {
		allowed[i] = string(state)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE import_sessions SET state=$2,updated_at=$3 WHERE id=$1::uuid AND state=ANY($4::text[])`, sessionID, string(to), now, allowed)
	if err != nil {
		return err
	}
	if n, rowsErr := result.RowsAffected(); rowsErr != nil || n == 1 {
		return rowsErr
	}
	var exists bool
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM import_sessions WHERE id=$1::uuid)`, sessionID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return ErrImportSessionClosed
}

// reapCandidate selects the next session that needs reaping, not already tried in this pass, under a row lock that other nodes
// skip.
const reapCandidateQuery = `SELECT s.id::text FROM import_sessions s
WHERE ((s.state IN ('open','committing') AND s.expires_at <= $1)
   OR (s.state IN ('committed','aborted','expired') AND EXISTS (SELECT 1 FROM import_blobs b WHERE b.session_id = s.id AND b.staging_key <> '')))
  AND s.id <> ALL($2::text[]::uuid[])
ORDER BY s.expires_at,s.id LIMIT 1 FOR UPDATE OF s SKIP LOCKED`

// ReapImportSessions implements ImportSessionStore. Each session is finished in its own transaction, under a row lock that
// the passes of other nodes skip, so a slow object store holds one session and not the batch. The staging objects are
// deleted before their keys are emptied, so a crash leaves keys that the next pass deletes again (deleting a missing
// object is not an error). A session whose objects cannot be deleted is skipped for the rest of the pass.
func (s *PostgresStore) ReapImportSessions(ctx context.Context, now time.Time, limit int, remove func(ctx context.Context, keys []string) error) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	var failures []error
	tried := []string{}
	finished := 0
	for finished < limit {
		done, id, err := s.reapOne(ctx, now, tried, remove)
		if err != nil {
			failures = append(failures, err)
		}
		if id == "" {
			break
		}
		tried = append(tried, id)
		if done {
			finished++
		}
		if err != nil && ctx.Err() != nil {
			break
		}
	}
	return finished, errors.Join(failures...)
}

// reapOne takes the next candidate in a transaction of its own. id is empty when none is left; done reports whether the
// session was finished.
func (s *PostgresStore) reapOne(ctx context.Context, now time.Time, tried []string, remove func(ctx context.Context, keys []string) error) (done bool, id string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, "", err
	}
	defer func() { _ = tx.Rollback() }()
	err = tx.QueryRowContext(ctx, reapCandidateQuery, now, tried).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	keys, err := importStagingKeys(ctx, tx, id)
	if err != nil {
		return false, id, err
	}
	if len(keys) > 0 {
		if err = remove(ctx, keys); err != nil {
			return false, id, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE import_blobs SET staging_key='' WHERE session_id=$1::uuid AND staging_key <> ''`, id); err != nil {
			return false, id, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE import_sessions SET state=CASE WHEN state IN ('open','committing') THEN 'expired' ELSE state END,updated_at=$2 WHERE id=$1::uuid`, id, now); err != nil {
		return false, id, err
	}
	return true, id, tx.Commit()
}

func importStagingKeys(ctx context.Context, tx *sql.Tx, sessionID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT staging_key FROM import_blobs WHERE session_id=$1::uuid AND staging_key <> '' ORDER BY digest`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var keys []string
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}
