package repository

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"github.com/mlops-ca/notarx/internal/capacity"
	"time"

	"github.com/mlops-ca/notarx/internal/jsonmeta"
)

const nuGetSessionColumns = `id::text,repository_id::text,package_id,version,digest,size,actor,state,object_key,metadata_json,nuspec,created_at,expires_at,committed_at,collected_at,source_version,source_url,registration_url`
const nuGetGovernanceColumns = `governance_version::text,listed,tombstoned,quarantined,governance_updated_at,governance_updated_by,governance_reason`
const nuGetPublicationColumns = `repository_id::text,package_id,version,session_id::text,digest,object_key,size,metadata_json,nuspec,publisher,published_at,source_version,source_url,registration_url,` + nuGetGovernanceColumns + `,body_digest,body_size,body_key`

type nuGetScanner interface{ Scan(...any) error }

func scanNuGetSession(row nuGetScanner) (NuGetSession, error) {
	var v NuGetSession
	var committed sql.NullTime
	var collected sql.NullTime
	err := row.Scan(&v.ID, &v.RepositoryID, &v.PackageID, &v.Version, &v.Digest, &v.Size, &v.Actor, &v.State, &v.ObjectKey, &v.MetadataJSON, &v.Nuspec, &v.CreatedAt, &v.ExpiresAt, &committed, &collected, &v.SourceVersion, &v.SourceURL, &v.RegistrationURL)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if committed.Valid {
		v.CommittedAt = committed.Time
	}
	if collected.Valid {
		v.CollectedAt = collected.Time
	}
	return v, err
}
func (s *PostgresStore) scanNuGetPublication(ctx context.Context, row nuGetScanner) (NuGetPublication, error) {
	v, ref, err := scanNuGetPublicationRef(row)
	if err != nil {
		return v, err
	}
	v.Nuspec, err = s.internalize(ctx, v.Nuspec, ref)
	return v, err
}

// scanNuGetPublicationRef reads a publication row. The nuspec of an externalised one is left empty; ref names it.
func scanNuGetPublicationRef(row nuGetScanner) (NuGetPublication, payloadRef, error) {
	var v NuGetPublication
	var updated sql.NullTime
	var ref payloadRef
	err := row.Scan(&v.RepositoryID, &v.PackageID, &v.Version, &v.SessionID, &v.Digest, &v.ObjectKey, &v.Size, &v.MetadataJSON, &v.Nuspec, &v.Publisher, &v.PublishedAt, &v.SourceVersion, &v.SourceURL, &v.RegistrationURL, &v.Governance.Version, &v.Governance.Listed, &v.Governance.Tombstoned, &v.Governance.Quarantined, &updated, &v.Governance.UpdatedBy, &v.Governance.Reason, &ref.digest, &ref.size, &ref.key)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ref, ErrNotFound
	}
	if updated.Valid {
		v.Governance.UpdatedAt = updated.Time
	}
	return v, ref, err
}

func (s *PostgresStore) nuGetTransaction(ctx context.Context, repo, sourceVersion string) (*sql.Tx, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	var name string
	var repoType RepositoryType
	var version string
	err = tx.QueryRowContext(ctx, `SELECT name,repo_type,version::text FROM hosted_repositories WHERE id=$1::uuid AND format='nuget' AND state='active' FOR UPDATE`, repo).Scan(&name, &repoType, &version)
	if err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrNotFound
		}
		return nil, "", err
	}
	if repoType == RepositoryTypeHosted && sourceVersion != "" || repoType == RepositoryTypeProxy && version != sourceVersion {
		_ = tx.Rollback()
		return nil, "", ErrNuGetSourceChanged
	}
	return tx, name, nil
}

func (s *PostgresStore) ReserveNuGetPublication(ctx context.Context, in NuGetSession) (NuGetSession, bool, error) {
	if !validNuGetSession(in) || !in.ExpiresAt.After(time.Now()) || !in.CreatedAt.IsZero() || in.State != "" || in.ObjectKey != "" {
		return NuGetSession{}, false, ErrInvalidNuGetPublication
	}
	tx, _, err := s.nuGetTransaction(ctx, in.RepositoryID, in.SourceVersion)
	if err != nil {
		return NuGetSession{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	// A lazily expired session releases the bytes it reserved at admission.
	if err = releaseExpiredNuGetSessions(ctx, tx, `SELECT id FROM native_nuget_publication_sessions WHERE repository_id=$1::uuid AND state IN ('reserved','staged') AND expires_at<=now()`, in.RepositoryID); err != nil {
		return NuGetSession{}, false, err
	}
	prior, err := scanNuGetSession(tx.QueryRowContext(ctx, `SELECT `+nuGetSessionColumns+` FROM native_nuget_publication_sessions WHERE id=$1::uuid`, in.ID))
	if err == nil {
		if !nuGetSessionSame(prior, in) || prior.Actor != in.Actor {
			return NuGetSession{}, false, ErrIdempotencyConflict
		}
		if prior.State == "expired" {
			return NuGetSession{}, false, ErrNuGetSessionExpired
		}
		return prior, true, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return NuGetSession{}, false, err
	}
	prior, err = scanNuGetSession(tx.QueryRowContext(ctx, `SELECT `+nuGetSessionColumns+` FROM native_nuget_publication_sessions WHERE repository_id=$1::uuid AND package_id=$2 AND version=$3 AND source_version=$4 AND state IN ('reserved','staged','committed')`, in.RepositoryID, in.PackageID, in.Version, in.SourceVersion))
	if err == nil {
		if !nuGetSessionSame(prior, in) || prior.State != "committed" && prior.Actor != in.Actor {
			return NuGetSession{}, false, ErrNuGetPublicationConflict
		}
		return prior, true, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return NuGetSession{}, false, err
	}
	// The reserved session holds its bytes in the counter until it commits or expires (ADR 0023
	// section 3); this is the quota admission, taken before the row is inserted.
	if err = capacity.Reserve(ctx, tx, in.RepositoryID, in.Size, 0); err != nil {
		return NuGetSession{}, false, err
	}
	in.ObjectKey = nuGetSessionObjectKey(in)
	in, err = scanNuGetSession(tx.QueryRowContext(ctx, `INSERT INTO native_nuget_publication_sessions(id,repository_id,package_id,version,digest,size,actor,state,object_key,expires_at,source_version)VALUES($1,$2,$3,$4,$5,$6,$7,'reserved',$8,$9,$10) RETURNING `+nuGetSessionColumns, in.ID, in.RepositoryID, in.PackageID, in.Version, in.Digest, in.Size, in.Actor, in.ObjectKey, in.ExpiresAt, in.SourceVersion))
	if err != nil {
		return NuGetSession{}, false, err
	}
	return in, false, tx.Commit()
}

// releaseExpiredNuGetSessions expires the reserved or staged sessions the candidates query selects
// and releases the bytes each reserved at admission.
func releaseExpiredNuGetSessions(ctx context.Context, tx *sql.Tx, candidates string, args ...any) error {
	return releaseExpiredSessions(ctx, tx, "native_nuget_publication_sessions", "s.size", "'reserved','staged'", candidates, args...)
}

func (s *PostgresStore) GetNuGetSession(ctx context.Context, id string) (NuGetSession, error) {
	return scanNuGetSession(s.db.QueryRowContext(ctx, `SELECT `+nuGetSessionColumns+` FROM native_nuget_publication_sessions WHERE id=$1::uuid AND EXISTS(SELECT 1 FROM hosted_repositories h WHERE h.id=repository_id AND h.format='nuget' AND h.state='active')`, id))
}

func (s *PostgresStore) StageNuGetPublication(ctx context.Context, in NuGetSession) (NuGetSession, error) {
	if !validNuGetStaging(in) {
		return NuGetSession{}, ErrInvalidNuGetPublication
	}
	tx, _, err := s.nuGetTransaction(ctx, in.RepositoryID, in.SourceVersion)
	if err != nil {
		return NuGetSession{}, err
	}
	defer func() { _ = tx.Rollback() }()
	prior, err := scanNuGetSession(tx.QueryRowContext(ctx, `SELECT `+nuGetSessionColumns+` FROM native_nuget_publication_sessions WHERE id=$1::uuid FOR UPDATE`, in.ID))
	if err != nil {
		return NuGetSession{}, err
	}
	if !nuGetSessionSame(prior, in) || prior.Actor != in.Actor {
		return NuGetSession{}, ErrNuGetPublicationConflict
	}
	if prior.State == "expired" || !prior.ExpiresAt.After(time.Now()) {
		return NuGetSession{}, ErrNuGetSessionExpired
	}
	if prior.State != "reserved" && prior.State != "staged" {
		return NuGetSession{}, ErrNuGetSessionState
	}
	if prior.State == "staged" && (!jsonmeta.Equal(prior.MetadataJSON, in.MetadataJSON) || !bytes.Equal(prior.Nuspec, in.Nuspec) || prior.SourceURL != in.SourceURL || prior.RegistrationURL != in.RegistrationURL) {
		return NuGetSession{}, ErrNuGetPublicationConflict
	}
	prior, err = scanNuGetSession(tx.QueryRowContext(ctx, `UPDATE native_nuget_publication_sessions SET state='staged',metadata_json=$2,nuspec=$3,source_url=$4,registration_url=$5 WHERE id=$1::uuid RETURNING `+nuGetSessionColumns, in.ID, string(in.MetadataJSON), in.Nuspec, in.SourceURL, in.RegistrationURL))
	if err != nil {
		return NuGetSession{}, err
	}
	return prior, tx.Commit()
}

func (s *PostgresStore) CommitNuGetPublication(ctx context.Context, id, actor string) (NuGetPublication, bool, error) {
	before, err := s.GetNuGetSession(ctx, id)
	if err != nil {
		return NuGetPublication{}, false, err
	}
	// A large nuspec goes to the object store before the transaction opens, so its locks and its connection never
	// wait on the store. The transaction re-reads the session and stores again only if the payload changed meanwhile.
	staged, err := s.stageNuGetNuspec(ctx, before, actor)
	if err != nil {
		return NuGetPublication{}, false, err
	}
	tx, name, err := s.nuGetTransaction(ctx, before.RepositoryID, before.SourceVersion)
	if err != nil {
		return NuGetPublication{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	session, err := scanNuGetSession(tx.QueryRowContext(ctx, `SELECT `+nuGetSessionColumns+` FROM native_nuget_publication_sessions WHERE id=$1::uuid FOR UPDATE`, id))
	if err != nil {
		return NuGetPublication{}, false, err
	}
	if session.Actor != actor {
		return NuGetPublication{}, false, ErrNuGetPublicationConflict
	}
	if session.State == "committed" {
		p, ref, err := scanNuGetPublicationRef(tx.QueryRowContext(ctx, `SELECT `+nuGetPublicationColumns+` FROM native_nuget_publications WHERE session_id=$1::uuid`, id))
		if err != nil {
			return NuGetPublication{}, false, err
		}
		if err = tx.Commit(); err != nil {
			return NuGetPublication{}, false, err
		}
		// The nuspec is read after the commit: the transaction holds no lock while the object store answers.
		p.Nuspec, err = s.internalize(ctx, p.Nuspec, ref)
		return p, true, err
	}
	if session.State == "expired" || !session.ExpiresAt.After(time.Now()) {
		return NuGetPublication{}, false, ErrNuGetSessionExpired
	}
	if session.State != "staged" {
		return NuGetPublication{}, false, ErrNuGetSessionState
	}
	var published time.Time
	if err = tx.QueryRowContext(ctx, `UPDATE native_nuget_publication_sessions SET state='committed',committed_at=now() WHERE id=$1::uuid RETURNING committed_at`, id).Scan(&published); err != nil {
		return NuGetPublication{}, false, err
	}
	p, err := s.insertNuGetPublicationFromSession(ctx, tx, session, staged)
	if err != nil {
		return NuGetPublication{}, false, err
	}
	if err = insertAudit(ctx, tx, nuGetPublicationAudit(p, name)); err != nil {
		return NuGetPublication{}, false, err
	}
	// A committed package counts once: its bytes were reserved with the session, so only the object
	// is added (ADR 0023 section 3).
	if err = capacity.Reserve(ctx, tx, p.RepositoryID, 0, 1); err != nil {
		return NuGetPublication{}, false, err
	}
	return p, false, tx.Commit()
}

func (s *PostgresStore) GetNuGetPublication(ctx context.Context, repo, id, version string) (NuGetPublication, error) {
	return s.scanNuGetPublication(ctx, s.db.QueryRowContext(ctx, `SELECT `+nuGetPublicationColumns+` FROM native_nuget_publications WHERE repository_id=$1::uuid AND package_id=$2 AND version=$3 AND EXISTS(SELECT 1 FROM hosted_repositories h WHERE h.id=repository_id AND h.format='nuget' AND h.state='active' AND source_version=CASE WHEN h.repo_type='proxy' THEN h.version::text ELSE '' END)`, repo, id, version))
}

func (s *PostgresStore) ListNuGetPublications(ctx context.Context, repo, id string, limit int, after string) ([]NuGetPublication, error) {
	if limit <= 0 || limit > 1000 {
		limit = 64
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosted_repositories WHERE id=$1::uuid AND format='nuget' AND state='active')`, repo).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT repository_id::text,package_id,version,session_id::text,digest,object_key,size,metadata_json,''::bytea,publisher,published_at,source_version,source_url,registration_url,`+nuGetGovernanceColumns+`,NULL::text,NULL::bigint,NULL::text FROM native_nuget_publications WHERE repository_id=$1::uuid AND ($2='' OR package_id=$2) AND (package_id||'@'||version) COLLATE "C">$3 AND EXISTS(SELECT 1 FROM hosted_repositories h WHERE h.id=repository_id AND h.state='active' AND source_version=CASE WHEN h.repo_type='proxy' THEN h.version::text ELSE '' END) ORDER BY (package_id||'@'||version) COLLATE "C" LIMIT $4`, repo, id, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []NuGetPublication{}
	for rows.Next() {
		p, err := s.scanNuGetPublication(ctx, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ExpireNuGetSessions(ctx context.Context, now time.Time, limit int) ([]NuGetSession, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Live sessions past their deadline expire first and release their reservation; the sessions
	// already expired lazily by a reservation are then collected with them.
	if err = releaseExpiredNuGetSessions(ctx, tx, `SELECT id FROM native_nuget_publication_sessions WHERE state IN ('reserved','staged') AND expires_at<=$1 ORDER BY expires_at,id FOR UPDATE SKIP LOCKED LIMIT $2`, now, limit); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `WITH expired AS(SELECT id FROM native_nuget_publication_sessions WHERE collected_at IS NULL AND state='expired' ORDER BY expires_at,id FOR UPDATE SKIP LOCKED LIMIT $1) SELECT `+nuGetSessionColumns+` FROM native_nuget_publication_sessions WHERE id IN(SELECT id FROM expired) ORDER BY expires_at,id`, limit)
	if err != nil {
		return nil, err
	}
	out := []NuGetSession{}
	for rows.Next() {
		in, err := scanNuGetSession(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, in)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func (s *PostgresStore) MarkNuGetSessionCollected(ctx context.Context, id, key string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE native_nuget_publication_sessions SET collected_at=COALESCE(collected_at,now()) WHERE id=$1::uuid AND object_key=$2 AND state='expired'`, id, key)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return ErrNuGetSessionState
	}
	return err
}

func (s *PostgresStore) LockNuGetPublication(ctx context.Context, repo, id, version string) (context.Context, func(), error) {
	return s.lockPostgresAdvisoryKeys(ctx, []string{"native-nuget-publication:" + repo + ":" + id + ":" + version})
}

// insertNuGetPublicationFromSession publishes a staged session: the row copies the session's
// metadata and keeps the nuspec in the object store when it is above the inline threshold. The caller stages the
// object before its transaction opens; a payload staged for other bytes is stored here, the behaviour before
// staging. The returned publication carries the session's nuspec, so no object is read.
func (s *PostgresStore) insertNuGetPublicationFromSession(ctx context.Context, tx *sql.Tx, session NuGetSession, staged stagedPayload) (NuGetPublication, error) {
	nuspec, ref, err := staged.externalizeIn(ctx, s, tx, session.Nuspec)
	if err != nil {
		return NuGetPublication{}, err
	}
	bodyDigest, bodySize, bodyKey := ref.columns()
	p, _, err := scanNuGetPublicationRef(tx.QueryRowContext(ctx, `INSERT INTO native_nuget_publications(repository_id,package_id,version,session_id,digest,object_key,size,metadata_json,nuspec,publisher,published_at,source_version,source_url,registration_url,listed,body_digest,body_size,body_key)SELECT repository_id,package_id,version,id,digest,object_key,size,metadata_json,$2::bytea,actor,committed_at,source_version,source_url,registration_url,COALESCE((metadata_json::jsonb->>'listed')::boolean,true),$3::text,$4::bigint,$5::text FROM native_nuget_publication_sessions WHERE id=$1::uuid RETURNING `+nuGetPublicationColumns, session.ID, nuspec, bodyDigest, bodySize, bodyKey))
	if err == nil && ref.external() {
		p.Nuspec = session.Nuspec
	}
	return p, err
}

// stageNuGetNuspec externalises the nuspec of a session before the transaction that commits it opens. A commit the
// transaction would refuse (another actor, a session that is not staged or has expired) stages nothing, so a
// rejected request never writes an object.
func (s *PostgresStore) stageNuGetNuspec(ctx context.Context, session NuGetSession, actor string) (stagedPayload, error) {
	if session.State != "staged" || session.Actor != actor || !session.ExpiresAt.After(time.Now()) {
		return stagedPayload{}, nil
	}
	return s.stagedFor(ctx, session.Nuspec)
}
