package repository

import (
	"context"
	"database/sql"
	"errors"
	"github.com/mlops-ca/notarx/internal/capacity"
	"time"
)

const helmSessionColumns = `id::text,repository_id::text,name,version,digest,size,provenance_digest,provenance_size,actor,state,object_key,provenance_key,metadata_json,chart_yaml,created_at,expires_at,committed_at,collected_at,source_version`
const helmPublicationColumns = `repository_id::text,name,version,session_id::text,digest,object_key,size,provenance_digest,provenance_key,provenance_size,metadata_json,chart_yaml,publisher,published_at,governance_version::text,tombstoned,quarantined,governance_updated_at,governance_updated_by,governance_reason,body_digest,body_size,body_key`

type helmScanner interface{ Scan(...any) error }

func scanHelmSession(row helmScanner) (HelmSession, error) {
	var in HelmSession
	var committed, collected sql.NullTime
	err := row.Scan(&in.ID, &in.RepositoryID, &in.Name, &in.Version, &in.Digest, &in.Size, &in.ProvenanceDigest, &in.ProvenanceSize, &in.Actor, &in.State, &in.ObjectKey, &in.ProvenanceKey, &in.MetadataJSON, &in.ChartYAML, &in.CreatedAt, &in.ExpiresAt, &committed, &collected, &in.SourceVersion)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if committed.Valid {
		in.CommittedAt = committed.Time
	}
	if collected.Valid {
		in.CollectedAt = collected.Time
	}
	return in, err
}
func (s *PostgresStore) scanHelmPublication(ctx context.Context, row helmScanner) (HelmPublication, error) {
	in, ref, err := scanHelmPublicationRef(row)
	if err != nil {
		return in, err
	}
	in.ChartYAML, err = s.internalize(ctx, in.ChartYAML, ref)
	return in, err
}

// scanHelmPublicationRef reads a publication row. The chart.yaml of an externalised one is left empty; ref names it.
func scanHelmPublicationRef(row helmScanner) (HelmPublication, payloadRef, error) {
	var in HelmPublication
	var updated sql.NullTime
	var ref payloadRef
	err := row.Scan(&in.RepositoryID, &in.Name, &in.Version, &in.SessionID, &in.Digest, &in.ObjectKey, &in.Size, &in.ProvenanceDigest, &in.ProvenanceKey, &in.ProvenanceSize, &in.MetadataJSON, &in.ChartYAML, &in.Publisher, &in.PublishedAt, &in.Governance.Version, &in.Governance.Tombstoned, &in.Governance.Quarantined, &updated, &in.Governance.UpdatedBy, &in.Governance.Reason, &ref.digest, &ref.size, &ref.key)
	if updated.Valid {
		in.Governance.UpdatedAt = updated.Time
	}
	if errors.Is(err, sql.ErrNoRows) {
		return in, ref, ErrNotFound
	}
	return in, ref, err
}
func (s *PostgresStore) helmTransaction(ctx context.Context, repo string) (*sql.Tx, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	var name string
	err = tx.QueryRowContext(ctx, `SELECT name FROM hosted_repositories WHERE id=$1::uuid AND format='helm' AND repo_type='hosted' AND state='active' FOR UPDATE`, repo).Scan(&name)
	if err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrNotFound
		}
		return nil, "", err
	}
	return tx, name, nil
}
func (s *PostgresStore) ReserveHelmPublication(ctx context.Context, in HelmSession) (HelmSession, bool, error) {
	if !validHelmSession(in) || !in.ExpiresAt.After(time.Now()) || !in.CreatedAt.IsZero() || in.State != "" || in.ObjectKey != "" || in.ProvenanceKey != "" || !in.CommittedAt.IsZero() || !in.CollectedAt.IsZero() {
		return HelmSession{}, false, ErrInvalidHelmPublication
	}
	tx, _, err := s.helmWriteTransaction(ctx, in.RepositoryID, in.SourceVersion)
	if err != nil {
		return HelmSession{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	// A lazily expired session releases the bytes it reserved at admission.
	if err = releaseExpiredHelmSessions(ctx, tx, `SELECT id FROM native_helm_publication_sessions WHERE repository_id=$1::uuid AND state='pending' AND (expires_at<=now() OR source_version<>$2)`, in.RepositoryID, in.SourceVersion); err != nil {
		return HelmSession{}, false, err
	}
	prior, err := scanHelmSession(tx.QueryRowContext(ctx, `SELECT `+helmSessionColumns+` FROM native_helm_publication_sessions WHERE id=$1::uuid`, in.ID))
	if err == nil {
		if !helmSessionSame(prior, in) || prior.Actor != in.Actor {
			return HelmSession{}, false, ErrIdempotencyConflict
		}
		if prior.State == "expired" {
			return HelmSession{}, false, ErrHelmSessionExpired
		}
		return prior, true, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return HelmSession{}, false, err
	}
	prior, err = scanHelmSession(tx.QueryRowContext(ctx, `SELECT `+helmSessionColumns+` FROM native_helm_publication_sessions WHERE repository_id=$1::uuid AND name=$2 AND version=$3 AND state IN ('pending','committed')`, in.RepositoryID, in.Name, in.Version))
	if err == nil {
		if !helmSessionSame(prior, in) || prior.State != "committed" && prior.Actor != in.Actor {
			return HelmSession{}, false, ErrHelmPublicationConflict
		}
		return prior, true, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return HelmSession{}, false, err
	}
	// The pending session holds its bytes in the counter until it commits or expires (ADR 0023
	// section 3); this is the quota admission, taken before the row is inserted.
	if err = capacity.Reserve(ctx, tx, in.RepositoryID, in.Size+in.ProvenanceSize, 0); err != nil {
		return HelmSession{}, false, err
	}
	in.ObjectKey, in.ProvenanceKey = helmObjectKeys(in)
	in, err = scanHelmSession(tx.QueryRowContext(ctx, `INSERT INTO native_helm_publication_sessions(id,repository_id,name,version,digest,size,provenance_digest,provenance_size,actor,state,object_key,provenance_key,metadata_json,chart_yaml,expires_at,source_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10,$11,$12,$13,$14,$15) RETURNING `+helmSessionColumns, in.ID, in.RepositoryID, in.Name, in.Version, in.Digest, in.Size, in.ProvenanceDigest, in.ProvenanceSize, in.Actor, in.ObjectKey, in.ProvenanceKey, string(in.MetadataJSON), in.ChartYAML, in.ExpiresAt, in.SourceVersion))
	if err != nil {
		return HelmSession{}, false, err
	}
	return in, false, tx.Commit()
}
func (s *PostgresStore) GetHelmSession(ctx context.Context, id string) (HelmSession, error) {
	return scanHelmSession(s.db.QueryRowContext(ctx, `SELECT `+helmSessionColumns+` FROM native_helm_publication_sessions WHERE id=$1::uuid`, id))
}
func (s *PostgresStore) CommitHelmPublication(ctx context.Context, id, actor string) (HelmPublication, bool, error) {
	before, err := s.GetHelmSession(ctx, id)
	if err != nil {
		return HelmPublication{}, false, err
	}
	tx, name, err := s.helmWriteTransaction(ctx, before.RepositoryID, before.SourceVersion)
	if err != nil {
		return HelmPublication{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	in, err := scanHelmSession(tx.QueryRowContext(ctx, `SELECT `+helmSessionColumns+` FROM native_helm_publication_sessions WHERE id=$1::uuid FOR UPDATE`, id))
	if err != nil {
		return HelmPublication{}, false, err
	}
	if in.Actor != actor {
		return HelmPublication{}, false, ErrHelmPublicationConflict
	}
	if in.State == "committed" {
		p, e := s.scanHelmPublication(ctx, tx.QueryRowContext(ctx, `SELECT `+helmPublicationColumns+` FROM native_helm_publications WHERE session_id=$1::uuid`, id))
		if e != nil {
			return HelmPublication{}, false, e
		}
		return p, true, tx.Commit()
	}
	if in.State == "expired" || !in.ExpiresAt.After(time.Now()) {
		return HelmPublication{}, false, ErrHelmSessionExpired
	}
	if in.State != "pending" {
		return HelmPublication{}, false, ErrHelmSessionState
	}
	if _, err = tx.ExecContext(ctx, `UPDATE native_helm_publication_sessions SET state='committed',committed_at=now() WHERE id=$1::uuid`, id); err != nil {
		return HelmPublication{}, false, err
	}
	p, err := s.insertHelmPublicationFromSession(ctx, tx, in)
	if err != nil {
		return HelmPublication{}, false, err
	}
	if err = insertAudit(ctx, tx, helmPublicationAudit(p, name)); err != nil {
		return HelmPublication{}, false, err
	}
	if err = reserveHelmPublication(ctx, tx, p); err != nil {
		return HelmPublication{}, false, err
	}
	return p, false, tx.Commit()
}

// reserveHelmPublication counts a committed publication in the repository's usage counter
// (ADR 0023 section 3). Its bytes were reserved when the session was admitted and stay in the
// counter, so only the objects are added: the chart object, plus the provenance object when
// there is one. Zero bytes are never rejected.
func reserveHelmPublication(ctx context.Context, tx *sql.Tx, p HelmPublication) error {
	objects := int64(1)
	if p.ProvenanceSize > 0 {
		objects++
	}
	return capacity.Reserve(ctx, tx, p.RepositoryID, 0, objects)
}

// releaseExpiredHelmSessions expires the pending sessions the candidates query selects and releases
// the bytes (chart and provenance) each reserved at admission.
func releaseExpiredHelmSessions(ctx context.Context, tx *sql.Tx, candidates string, args ...any) error {
	return releaseExpiredSessions(ctx, tx, "native_helm_publication_sessions", "s.size+s.provenance_size", "'pending'", candidates, args...)
}
func (s *PostgresStore) GetHelmPublication(ctx context.Context, repo, name, version string) (HelmPublication, error) {
	return s.scanHelmPublication(ctx, s.db.QueryRowContext(ctx, `SELECT `+helmPublicationColumns+` FROM native_helm_publications WHERE repository_id=$1::uuid AND name=$2 AND version=$3 AND EXISTS(SELECT 1 FROM hosted_repositories h WHERE h.id=repository_id AND h.format='helm' AND h.repo_type IN ('hosted','proxy') AND h.state='active')`, repo, name, version))
}
func (s *PostgresStore) ListHelmPublications(ctx context.Context, repo string, limit int) ([]HelmPublication, error) {
	if limit < 1 || limit > 4097 {
		limit = 4097
	}
	// One statement owns the complete bounded index snapshot. No page-by-page
	// reads can mix concurrent commits into a partially constructed index.
	rows, err := s.db.QueryContext(ctx, `SELECT `+helmPublicationColumns+` FROM native_helm_publications WHERE repository_id=$1::uuid AND EXISTS(SELECT 1 FROM hosted_repositories h WHERE h.id=repository_id AND h.format='helm' AND h.repo_type IN ('hosted','proxy') AND h.state='active') ORDER BY name COLLATE "C",version COLLATE "C" LIMIT $2`, repo, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []HelmPublication{}
	var refs []payloadRef
	for rows.Next() {
		p, ref, e := scanHelmPublicationRef(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
		refs = append(refs, ref)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// The chart.yaml objects are read after the rows are closed, so the cursor does not wait on the object store.
	_ = rows.Close()
	slots := make([]payloadSlot, len(out))
	for i := range out {
		slots[i] = payloadSlot{ref: refs[i], set: func(data []byte) { out[i].ChartYAML = data }}
	}
	if err = s.resolvePayloads(ctx, slots); err != nil {
		return nil, err
	}
	return out, nil
}
func (s *PostgresStore) ExpireHelmSessions(ctx context.Context, now time.Time, limit int) ([]HelmSession, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Pending sessions past their deadline expire first and release their reservation; the sessions
	// already expired lazily by a reservation are then collected with them.
	if err = releaseExpiredHelmSessions(ctx, tx, `SELECT id FROM native_helm_publication_sessions WHERE state='pending' AND expires_at<=$1 ORDER BY expires_at,id FOR UPDATE SKIP LOCKED LIMIT $2`, now, limit); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `WITH expired AS(SELECT id FROM native_helm_publication_sessions WHERE collected_at IS NULL AND state='expired' ORDER BY expires_at,id FOR UPDATE SKIP LOCKED LIMIT $1) SELECT `+helmSessionColumns+` FROM native_helm_publication_sessions WHERE id IN(SELECT id FROM expired) ORDER BY expires_at,id`, limit)
	if err != nil {
		return nil, err
	}
	out := []HelmSession{}
	for rows.Next() {
		in, e := scanHelmSession(rows)
		if e != nil {
			_ = rows.Close()
			return nil, e
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
func (s *PostgresStore) MarkHelmSessionCollected(ctx context.Context, id, key string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE native_helm_publication_sessions SET collected_at=COALESCE(collected_at,now()) WHERE id=$1::uuid AND object_key=$2 AND state='expired'`, id, key)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrHelmSessionState
	}
	return err
}
func (s *PostgresStore) LockHelmPublication(ctx context.Context, repo, name, version string) (context.Context, func(), error) {
	return s.lockPostgresAdvisoryKeys(ctx, []string{"native-helm-publication:" + repo + ":" + name + ":" + version})
}

func (s *PostgresStore) helmWriteTransaction(ctx context.Context, id, version string) (*sql.Tx, string, error) {
	if version != "" {
		return s.helmSourceTransaction(ctx, id, version)
	}
	return s.helmTransaction(ctx, id)
}

// insertHelmPublicationFromSession publishes a committed session: the row copies the session's
// metadata and moves chart.yaml to the object store when it is above the inline threshold.
func (s *PostgresStore) insertHelmPublicationFromSession(ctx context.Context, tx *sql.Tx, session HelmSession) (HelmPublication, error) {
	chart, ref, err := s.externalize(ctx, session.ChartYAML)
	if err != nil {
		return HelmPublication{}, err
	}
	bodyDigest, bodySize, bodyKey := ref.columns()
	return s.scanHelmPublication(ctx, tx.QueryRowContext(ctx, `INSERT INTO native_helm_publications(repository_id,name,version,session_id,digest,object_key,size,provenance_digest,provenance_key,provenance_size,metadata_json,chart_yaml,publisher,published_at,body_digest,body_size,body_key) SELECT repository_id,name,version,id,digest,object_key,size,provenance_digest,provenance_key,provenance_size,metadata_json,$2::bytea,actor,committed_at,$3::text,$4::bigint,$5::text FROM native_helm_publication_sessions WHERE id=$1::uuid RETURNING `+helmPublicationColumns, session.ID, chart, bodyDigest, bodySize, bodyKey))
}
