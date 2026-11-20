package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/mlops-ca/notarx/internal/protocol/cargo"
)

func (s *PostgresStore) GetCargoProxyConfig(ctx context.Context, repositoryID string) (CargoProxyConfig, error) {
	var value CargoProxyConfig
	err := s.db.QueryRowContext(ctx, `SELECT repository_id::text,download_template,search_api,fetched_at,expires_at,source_version
		FROM native_cargo_proxy_configs WHERE repository_id=$1::uuid`, repositoryID).
		Scan(&value.RepositoryID, &value.DownloadTemplate, &value.SearchAPI, &value.FetchedAt, &value.ExpiresAt, &value.SourceVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return CargoProxyConfig{}, ErrNotFound
	}
	return value, err
}

func (s *PostgresStore) PutCargoProxyConfig(ctx context.Context, value CargoProxyConfig) error {
	if value.RepositoryID == "" || value.DownloadTemplate == "" || value.FetchedAt.IsZero() || !value.ExpiresAt.After(value.FetchedAt) {
		return ErrInvalidCargoIdentity
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkProxySourceVersion(ctx, tx, value.RepositoryID, value.SourceVersion); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO native_cargo_proxy_configs
		(repository_id,download_template,search_api,fetched_at,expires_at,source_version)
		SELECT id,$2,$3,$4,$5,$6 FROM hosted_repositories
		WHERE id=$1::uuid AND format='cargo' AND repo_type='proxy' AND state='active'
		ON CONFLICT (repository_id) DO UPDATE SET download_template=EXCLUDED.download_template,
		search_api=EXCLUDED.search_api,fetched_at=EXCLUDED.fetched_at,expires_at=EXCLUDED.expires_at,source_version=EXCLUDED.source_version`,
		value.RepositoryID, value.DownloadTemplate, value.SearchAPI, value.FetchedAt, value.ExpiresAt, value.SourceVersion)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err == nil && count == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

const cargoProxyIndexColumns = `repository_id::text,name,body,status,upstream_etag,upstream_modified,fetched_at,expires_at,source_version,body_digest,body_size,body_key`

func (s *PostgresStore) GetCargoProxyIndex(ctx context.Context, repositoryID, name string) (CargoProxyIndex, error) {
	identity, err := cargo.NormalizeIdentity(name, "0.0.0")
	if err != nil || repositoryID == "" {
		return CargoProxyIndex{}, ErrInvalidCargoIdentity
	}
	var value CargoProxyIndex
	err = s.scanCargoProxyIndex(ctx, s.db.QueryRowContext(ctx, `SELECT `+cargoProxyIndexColumns+` FROM native_cargo_proxy_indexes
		WHERE repository_id=$1::uuid AND collision_key=$2`, repositoryID, identity.CollisionKey), &value)
	if errors.Is(err, sql.ErrNoRows) {
		return CargoProxyIndex{}, ErrNotFound
	}
	return value, err
}

func (s *PostgresStore) PutCargoProxyIndex(ctx context.Context, value CargoProxyIndex) error {
	identity, err := cargo.NormalizeIdentity(value.Name, "0.0.0")
	if err != nil || value.RepositoryID == "" {
		return ErrInvalidCargoIdentity
	}
	body, ref, err := s.externalize(ctx, value.Body)
	if err != nil {
		return err
	}
	bodyDigest, bodySize, bodyKey := ref.columns()
	prefetched, err := s.prefetchCargoProxyIndex(ctx, value.RepositoryID, identity.CollisionKey)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockActiveCargoProxyRepository(ctx, tx, value.RepositoryID); err != nil {
		return err
	}
	if err := checkProxySourceVersion(ctx, tx, value.RepositoryID, value.SourceVersion); err != nil {
		return err
	}
	current, err := s.lockCargoProxyIndex(ctx, tx, value.RepositoryID, identity.CollisionKey, prefetched)
	var previous *CargoProxyIndex
	if err == nil {
		previous = &current
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if previous != nil && previous.Name != value.Name {
		return ErrUpstreamChanged
	}
	if err := validateCargoProxyIndex(value, previous); err != nil {
		return err
	}
	if value.Status == 200 {
		rows, err := tx.QueryContext(ctx, `SELECT version,checksum FROM native_cargo_proxy_crates
			WHERE repository_id=$1::uuid AND collision_key=$2`, value.RepositoryID, identity.CollisionKey)
		if err != nil {
			return err
		}
		for rows.Next() {
			var version, checksum string
			if err := rows.Scan(&version, &checksum); err != nil {
				_ = rows.Close()
				return err
			}
			want, err := cargoProxyChecksum(value, version)
			if err != nil || want != checksum {
				_ = rows.Close()
				return ErrUpstreamChanged
			}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO native_cargo_proxy_indexes
		(repository_id,collision_key,name,body,status,upstream_etag,upstream_modified,fetched_at,expires_at,source_version,body_digest,body_size,body_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (repository_id,collision_key) DO UPDATE SET body=EXCLUDED.body,status=EXCLUDED.status,
		upstream_etag=EXCLUDED.upstream_etag,upstream_modified=EXCLUDED.upstream_modified,
		fetched_at=EXCLUDED.fetched_at,expires_at=EXCLUDED.expires_at,source_version=EXCLUDED.source_version,
		body_digest=EXCLUDED.body_digest,body_size=EXCLUDED.body_size,body_key=EXCLUDED.body_key`,
		value.RepositoryID, identity.CollisionKey, value.Name, body, value.Status,
		value.ETag, value.Modified, value.FetchedAt, value.ExpiresAt, value.SourceVersion, bodyDigest, bodySize, bodyKey)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) GetCargoProxyCrate(ctx context.Context, repositoryID, name, version string) (CargoProxyCrate, error) {
	identity, err := cargo.NormalizeIdentity(name, version)
	if err != nil || repositoryID == "" {
		return CargoProxyCrate{}, ErrInvalidCargoIdentity
	}
	var value CargoProxyCrate
	err = s.db.QueryRowContext(ctx, `SELECT repository_id::text,name,version,checksum,object_key,size,cached_at,source_version
		FROM native_cargo_proxy_crates WHERE repository_id=$1::uuid AND collision_key=$2 AND version_key=$3`,
		repositoryID, identity.CollisionKey, identity.VersionKey).
		Scan(&value.RepositoryID, &value.Name, &value.Version, &value.Checksum, &value.ObjectKey, &value.Size, &value.CachedAt, &value.SourceVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return CargoProxyCrate{}, ErrNotFound
	}
	return value, err
}

func (s *PostgresStore) PutCargoProxyCrate(ctx context.Context, value CargoProxyCrate) error {
	identity, err := cargo.NormalizeIdentity(value.Name, value.Version)
	if err != nil || value.RepositoryID == "" {
		return ErrInvalidCargoIdentity
	}
	prefetched, err := s.prefetchCargoProxyIndex(ctx, value.RepositoryID, identity.CollisionKey)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockActiveCargoProxyRepository(ctx, tx, value.RepositoryID); err != nil {
		return err
	}
	if err := checkProxySourceVersion(ctx, tx, value.RepositoryID, value.SourceVersion); err != nil {
		return err
	}
	index, err := s.lockCargoProxyIndex(ctx, tx, value.RepositoryID, identity.CollisionKey, prefetched)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := validateCargoProxyCrate(value, index); err != nil {
		return err
	}
	var checksum string
	var size int64
	err = tx.QueryRowContext(ctx, `SELECT checksum,size FROM native_cargo_proxy_crates
		WHERE repository_id=$1::uuid AND collision_key=$2 AND version_key=$3 FOR UPDATE`,
		value.RepositoryID, identity.CollisionKey, identity.VersionKey).Scan(&checksum, &size)
	if err == nil && (checksum != value.Checksum || size != value.Size) {
		return ErrUpstreamChanged
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO native_cargo_proxy_crates
		(repository_id,collision_key,version_key,name,version,checksum,object_key,size,cached_at,source_version)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (repository_id,collision_key,version_key) DO UPDATE SET source_version=EXCLUDED.source_version,cached_at=EXCLUDED.cached_at`,
		value.RepositoryID, identity.CollisionKey, identity.VersionKey, value.Name, value.Version,
		value.Checksum, value.ObjectKey, value.Size, value.CachedAt, value.SourceVersion)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) scanCargoProxyIndex(ctx context.Context, row interface{ Scan(...any) error }, value *CargoProxyIndex) error {
	var ref payloadRef
	if err := scanCargoProxyIndexRef(row, value, &ref); err != nil {
		return err
	}
	body, err := s.internalize(ctx, value.Body, ref)
	value.Body = body
	return err
}

// scanCargoProxyIndexRef reads an index row. The body of an externalised one is left empty; ref names it.
func scanCargoProxyIndexRef(row interface{ Scan(...any) error }, value *CargoProxyIndex, ref *payloadRef) error {
	return row.Scan(&value.RepositoryID, &value.Name, &value.Body, &value.Status, &value.ETag,
		&value.Modified, &value.FetchedAt, &value.ExpiresAt, &value.SourceVersion, &ref.digest, &ref.size, &ref.key)
}

// prefetchCargoProxyIndex reads the externalised body of an index before the transaction that locks the row opens,
// so the transaction validates against it without waiting on the object store. A missing row or an inline body
// prefetches nothing.
func (s *PostgresStore) prefetchCargoProxyIndex(ctx context.Context, repositoryID, collisionKey string) (payloadPrefetch, error) {
	var ref payloadRef
	err := s.db.QueryRowContext(ctx, `SELECT body_digest,body_size,body_key FROM native_cargo_proxy_indexes
		WHERE repository_id=$1::uuid AND collision_key=$2 AND body_digest IS NOT NULL`, repositoryID, collisionKey).Scan(&ref.digest, &ref.size, &ref.key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	body, err := s.internalize(ctx, nil, ref)
	if err != nil {
		return nil, err
	}
	prefetched := payloadPrefetch{}
	prefetched.add(ref, body)
	return prefetched, nil
}

// lockCargoProxyIndex locks and reads an index row. An externalised body comes from prefetched when it is still the
// row's body, and from the object store otherwise, the behaviour before prefetching.
func (s *PostgresStore) lockCargoProxyIndex(ctx context.Context, tx *sql.Tx, repositoryID, collisionKey string, prefetched payloadPrefetch) (CargoProxyIndex, error) {
	var value CargoProxyIndex
	var ref payloadRef
	if err := scanCargoProxyIndexRef(tx.QueryRowContext(ctx, `SELECT `+cargoProxyIndexColumns+` FROM native_cargo_proxy_indexes
		WHERE repository_id=$1::uuid AND collision_key=$2 FOR UPDATE`, repositoryID, collisionKey), &value, &ref); err != nil {
		return value, err
	}
	var err error
	value.Body, err = s.internalizeWith(ctx, value.Body, ref, prefetched)
	return value, err
}

func lockActiveCargoProxyRepository(ctx context.Context, tx *sql.Tx, repositoryID string) error {
	var format Format
	var repoType RepositoryType
	var state RepositoryState
	err := tx.QueryRowContext(ctx, `SELECT format,repo_type,state FROM hosted_repositories
		WHERE id=$1::uuid FOR UPDATE`, repositoryID).Scan(&format, &repoType, &state)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (format != FormatCargo || repoType != RepositoryTypeProxy || state != RepositoryActive) {
		return ErrNotFound
	}
	return err
}
