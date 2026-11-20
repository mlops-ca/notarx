package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	discovery "github.com/mlops-ca/notarx/internal/ocidiscovery"
)

var _ discovery.Store = (*PostgresStore)(nil)

func checkPostgresOCIGuards(ctx context.Context, tx *sql.Tx, guards []discovery.Guard) error {
	if err := discovery.ValidateGuards(guards); err != nil {
		return err
	}
	for _, guard := range sortedOCIGuards(guards) {
		query := ""
		switch guard.Kind {
		case "group":
			query = `SELECT version::text FROM hosted_groups WHERE id=$1 AND format='oci' FOR SHARE`
		case "repository":
			query = `SELECT version::text FROM hosted_repositories WHERE id=$1 AND format='oci' AND state='active' FOR SHARE`
		case "grants":
			// Lock a physical default row, too: an unlocked missing row cannot
			// fence a concurrent first authorization change.
			if guard.Version == "1" {
				if _, err := tx.ExecContext(ctx, `INSERT INTO repository_grant_sets(repository_id,version) VALUES($1,1) ON CONFLICT DO NOTHING`, guard.ID); err != nil {
					return err
				}
			}
			query = `SELECT version::text FROM repository_grant_sets WHERE repository_id=$1 FOR SHARE`
		}
		var version string
		err := tx.QueryRowContext(ctx, query, guard.ID).Scan(&version)
		if errors.Is(err, sql.ErrNoRows) || err == nil && version != guard.Version {
			return discovery.ErrChanged
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) SaveOCIDiscoverySnapshot(ctx context.Context, snapshot discovery.Snapshot) error {
	if err := discovery.ValidateSnapshot(snapshot, time.Now().UTC()); err != nil {
		return err
	}
	data, err := discovery.Payload(snapshot)
	if err != nil {
		return err
	}
	// A payload above the inline threshold goes to the object store once, however often the save restarts; the row then holds JSON null.
	inline, ref, err := s.externalizeJSON(ctx, data, "null")
	if err != nil {
		return err
	}
	staged := stagedOCISnapshot{data: data, inline: inline, ref: ref}
	err = s.saveOCIDiscoverySnapshotOnce(ctx, snapshot, staged, nil)
	if !errors.Is(err, errPayloadNotPrefetched) {
		return err
	}
	prefetched, err := s.prefetchOCIDiscoveryPayload(ctx, snapshot.ID)
	if err != nil {
		return err
	}
	if err = s.saveOCIDiscoverySnapshotOnce(ctx, snapshot, staged, prefetched); errors.Is(err, errPayloadNotPrefetched) {
		// The stored snapshot changed again while its payload was read: it is not the snapshot being saved.
		return discovery.ErrChanged
	}
	return err
}

// stagedOCISnapshot is a snapshot payload externalised before the save transaction opens.
type stagedOCISnapshot struct {
	data   []byte
	inline string
	ref    payloadRef
}

// A snapshot id that is already stored with different bytes needs the stored payload to decide whether it is the same
// snapshot; the transaction never reads the object store for it, so it returns errPayloadNotPrefetched, the payload is
// read outside the transaction (prefetchOCIDiscoveryPayload) and the save starts over with it.
// prefetchOCIDiscoveryPayload reads the externalised payload of the stored snapshot id, outside any transaction.
func (s *PostgresStore) prefetchOCIDiscoveryPayload(ctx context.Context, id string) (payloadPrefetch, error) {
	var ref payloadRef
	err := s.db.QueryRowContext(ctx, `SELECT body_digest,body_size,body_key FROM oci_discovery_snapshots WHERE id=$1`, id).Scan(&ref.digest, &ref.size, &ref.key)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !ref.external() {
		// Gone or inline since: the retry decides without a payload.
		return payloadPrefetch{}, nil
	}
	if err != nil {
		return nil, err
	}
	data, err := s.internalize(ctx, nil, ref)
	if err != nil {
		return nil, err
	}
	prefetched := payloadPrefetch{}
	prefetched.add(ref, data)
	return prefetched, nil
}

func (s *PostgresStore) saveOCIDiscoverySnapshotOnce(ctx context.Context, snapshot discovery.Snapshot, staged stagedOCISnapshot, prefetched payloadPrefetch) error {
	data, inline, ref := staged.data, staged.inline, staged.ref
	bodyDigest, bodySize, bodyKey := ref.columns()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkPostgresOCIGuards(ctx, tx, snapshot.Guards); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "oci-discovery-snapshots:"+snapshot.Owner); err != nil {
		return err
	}
	var prior []byte
	var priorRef payloadRef
	err = tx.QueryRowContext(ctx, `SELECT payload,body_digest,body_size,body_key FROM oci_discovery_snapshots WHERE id=$1`, snapshot.ID).Scan(&prior, &priorRef.digest, &priorRef.size, &priorRef.key)
	if err == nil {
		same, err := s.sameStoredOCISnapshot(prior, priorRef, data, ref, snapshot, prefetched)
		if err != nil {
			return err
		}
		if !same {
			return discovery.ErrChanged
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM oci_discovery_snapshots WHERE id IN (SELECT id FROM oci_discovery_snapshots WHERE expires_at<=clock_timestamp() ORDER BY expires_at LIMIT 100)`); err != nil {
		return err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM oci_discovery_snapshots WHERE owner=$1 AND expires_at>clock_timestamp()`, snapshot.Owner).Scan(&active); err != nil {
		return err
	}
	if active >= 32 {
		return discovery.ErrLimit
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO oci_discovery_snapshots(id,owner,kind,payload,expires_at,body_digest,body_size,body_key) VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7,$8)`, snapshot.ID, snapshot.Owner, snapshot.Kind, inline, snapshot.ExpiresAt, bodyDigest, bodySize, bodyKey); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, ociSnapshotAudit(snapshot)); err != nil {
		return err
	}
	return tx.Commit()
}

// sameStoredOCISnapshot reports whether the stored row (its inline payload and reference) holds the snapshot being
// saved, without reading the object store, so the save transaction never waits on it. An externalised payload is the
// exact bytes data of a snapshot (discovery.Payload), so equal digests mean the same snapshot; otherwise the stored
// payload is compared as a snapshot, which needs it prefetched (errPayloadNotPrefetched). An inline payload is jsonb,
// which normalises the text, so it is compared as a snapshot too.
func (s *PostgresStore) sameStoredOCISnapshot(prior []byte, priorRef payloadRef, data []byte, ref payloadRef, snapshot discovery.Snapshot, prefetched payloadPrefetch) (bool, error) {
	if priorRef.external() {
		if payloadsEqual(nil, priorRef, data, ref) {
			return true, nil
		}
		var err error
		if prior, err = s.internalizePrefetched(prior, priorRef, prefetched); err != nil {
			return false, err
		}
	}
	var existing discovery.Snapshot
	return json.Unmarshal(prior, &existing) == nil && sameOCISnapshot(existing, snapshot), nil
}

// GetOCIDiscoverySnapshot reads the snapshot row, resolves an externalised payload, and only then opens the
// transaction that checks the guards the payload names, so no transaction waits on the object store.
func (s *PostgresStore) GetOCIDiscoverySnapshot(ctx context.Context, id, owner string) (discovery.Snapshot, error) {
	var data []byte
	var ref payloadRef
	err := s.db.QueryRowContext(ctx, `SELECT payload,body_digest,body_size,body_key FROM oci_discovery_snapshots WHERE id=$1::uuid AND owner=$2 AND expires_at>clock_timestamp()`, id, owner).Scan(&data, &ref.digest, &ref.size, &ref.key)
	if errors.Is(err, sql.ErrNoRows) {
		return discovery.Snapshot{}, discovery.ErrNotFound
	}
	if err != nil {
		return discovery.Snapshot{}, err
	}
	if data, err = s.internalize(ctx, data, ref); err != nil {
		return discovery.Snapshot{}, err
	}
	var snapshot discovery.Snapshot
	if json.Unmarshal(data, &snapshot) != nil {
		return discovery.Snapshot{}, discovery.ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return discovery.Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// The row was selected before the object read: a snapshot that expired or was purged meanwhile is gone (database clock).
	var live bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM oci_discovery_snapshots WHERE id=$1::uuid AND owner=$2 AND expires_at>clock_timestamp())`, id, owner).Scan(&live); err != nil {
		return discovery.Snapshot{}, err
	}
	if !live {
		return discovery.Snapshot{}, discovery.ErrNotFound
	}
	if err := checkPostgresOCIGuards(ctx, tx, snapshot.Guards); err != nil {
		return discovery.Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return discovery.Snapshot{}, err
	}
	return snapshot, nil
}

func (s *PostgresStore) PublishOCISubjectBinding(ctx context.Context, binding discovery.Binding) error {
	if err := discovery.ValidateBinding(binding); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkPostgresOCIGuards(ctx, tx, binding.Guards); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "oci-discovery-binding:"+binding.Scope+":"+binding.Owner+":"+binding.Image); err != nil {
		return err
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM oci_subject_bindings WHERE scope=$1 AND owner=$2 AND image=$3 AND subject=$4 FOR UPDATE`, binding.Scope, binding.Owner, binding.Image, binding.Subject).Scan(&prior)
	if err == nil {
		var old discovery.Binding
		if json.Unmarshal(prior, &old) != nil {
			return discovery.ErrInvalid
		}
		binding, err = mergeOCIBinding(old, binding)
		if err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	data, err := discovery.Payload(binding)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO oci_subject_bindings(scope,owner,image,subject,source_id,source_version,payload) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb) ON CONFLICT(scope,owner,image,subject) DO UPDATE SET payload=EXCLUDED.payload`, binding.Scope, binding.Owner, binding.Image, binding.Subject, binding.SourceID, binding.SourceVersion, string(data)); err != nil {
		return err
	}
	for _, object := range binding.Objects {
		encoded, err := discovery.Payload(object)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO oci_bound_objects(scope,owner,image,digest,subject,source_id,source_version,payload) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb) ON CONFLICT DO NOTHING`, binding.Scope, binding.Owner, binding.Image, object.Digest, binding.Subject, binding.SourceID, binding.SourceVersion, string(encoded)); err != nil {
			return err
		}
		var sourceID, sourceVersion string
		var stored []byte
		if err := tx.QueryRowContext(ctx, `SELECT source_id::text,source_version::text,payload FROM oci_bound_objects WHERE scope=$1 AND owner=$2 AND image=$3 AND digest=$4`, binding.Scope, binding.Owner, binding.Image, object.Digest).Scan(&sourceID, &sourceVersion, &stored); err != nil {
			return err
		}
		var old discovery.Object
		if json.Unmarshal(stored, &old) != nil || sourceID != binding.SourceID || sourceVersion != binding.SourceVersion || old != object {
			return discovery.ErrChanged
		}
	}
	if err := insertAudit(ctx, tx, ociDiscoveryAudit(binding.Actor, binding.Scope, binding.Image, binding.Subject, "oci.referrers.bind")); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) GetOCISubjectBinding(ctx context.Context, scope, owner, image, subject string) (discovery.Binding, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM oci_subject_bindings WHERE scope=$1 AND owner=$2 AND image=$3 AND subject=$4`, scope, owner, image, subject).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return discovery.Binding{}, discovery.ErrNotFound
	}
	if err != nil {
		return discovery.Binding{}, err
	}
	var binding discovery.Binding
	if json.Unmarshal(data, &binding) != nil {
		return discovery.Binding{}, discovery.ErrInvalid
	}
	return binding, nil
}

func (s *PostgresStore) GetOCIBoundObject(ctx context.Context, scope, owner, image, digest string) (discovery.Binding, discovery.Object, error) {
	var bindingData, objectData []byte
	err := s.db.QueryRowContext(ctx, `SELECT b.payload,o.payload FROM oci_bound_objects o JOIN oci_subject_bindings b USING(scope,owner,image,subject) WHERE o.scope=$1 AND o.owner=$2 AND o.image=$3 AND o.digest=$4`, scope, owner, image, digest).Scan(&bindingData, &objectData)
	if errors.Is(err, sql.ErrNoRows) {
		return discovery.Binding{}, discovery.Object{}, discovery.ErrNotFound
	}
	if err != nil {
		return discovery.Binding{}, discovery.Object{}, err
	}
	var binding discovery.Binding
	var object discovery.Object
	if json.Unmarshal(bindingData, &binding) != nil || json.Unmarshal(objectData, &object) != nil {
		return discovery.Binding{}, discovery.Object{}, discovery.ErrInvalid
	}
	return binding, object, nil
}
