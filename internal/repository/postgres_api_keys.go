package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

func (s *PostgresStore) CreateAPIKey(ctx context.Context, key APIKey) (APIKey, error) {
	err := s.db.QueryRowContext(ctx, `INSERT INTO api_keys (id, service_account_id, name, secret_hash, roles, expires_at) VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5, $6) RETURNING created_at`, key.ID, key.ServiceAccountID, key.Name, key.SecretHash, key.Roles, key.ExpiresAt).Scan(&key.CreatedAt)
	if err != nil {
		return APIKey{}, err
	}
	return key, nil
}

func (s *PostgresStore) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id::text, '', name, array_to_json(roles), created_at, revoked_at, expires_at, last_used_at FROM api_keys WHERE service_account_id IS NULL ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	keys := []APIKey{}
	for rows.Next() {
		var key APIKey
		var roles []byte
		if err := rows.Scan(&key.ID, &key.ServiceAccountID, &key.Name, &roles, &key.CreatedAt, &key.RevokedAt, &key.ExpiresAt, &key.LastUsedAt); err != nil {
			return nil, err
		}
		if err := scanAPIKeyRoles(roles, &key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// FindActiveAPIKeyByHash is a read: last_used_at is recorded by TouchAPIKeys, in
// batches, so authenticating a request writes nothing (ADR 0023 section 6).
func (s *PostgresStore) FindActiveAPIKeyByHash(ctx context.Context, hash string) (APIKey, error) {
	var key APIKey
	var roles []byte
	err := s.db.QueryRowContext(ctx, getActiveAPIKeyBySecretHashQuery, hash).Scan(&key.ID, &key.ServiceAccountID, &key.Name, &key.SecretHash, &roles, &key.CreatedAt, &key.ExpiresAt, &key.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, err
	}
	if err := scanAPIKeyRoles(roles, &key); err != nil {
		return APIKey{}, err
	}
	return key, nil
}

// RevokeAPIKey revokes a standalone key and, in the same transaction, notifies
// every node so its cached principal is evicted (ADR 0023 section 6).
func (s *PostgresStore) RevokeAPIKey(ctx context.Context, id string) (APIKey, error) {
	key, err := runTransactional(ctx, s, nil, func(q sqlRunner) (APIKey, error) {
		var key APIKey
		var roles []byte
		err := q.QueryRowContext(ctx, `UPDATE api_keys SET revoked_at = COALESCE(revoked_at, now()) WHERE id=$1::uuid AND service_account_id IS NULL RETURNING id::text, '', name, array_to_json(roles), created_at, revoked_at, expires_at, last_used_at`, id).Scan(&key.ID, &key.ServiceAccountID, &key.Name, &roles, &key.CreatedAt, &key.RevokedAt, &key.ExpiresAt, &key.LastUsedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return APIKey{}, ErrNotFound
		}
		if err != nil {
			return APIKey{}, err
		}
		if err := scanAPIKeyRoles(roles, &key); err != nil {
			return APIKey{}, err
		}
		return key, notifyCache(ctx, q, CacheScopeAPIKey, key.ID)
	})
	if err == nil {
		s.invalidateLocal(CacheScopeAPIKey, key.ID)
	}
	return key, err
}

// TouchAPIKeys writes the batched last_used_at values in key order, skipping a
// row whose stored value is not older than notBefore.
func (s *PostgresStore) TouchAPIKeys(ctx context.Context, uses []APIKeyUse, notBefore time.Time) error {
	if len(uses) == 0 {
		return nil
	}
	sort.Slice(uses, func(i, j int) bool { return uses[i].ID < uses[j].ID })
	ids := make([]string, len(uses))
	at := make([]time.Time, len(uses))
	for i, use := range uses {
		ids[i], at[i] = use.ID, use.At
	}
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at=u.at FROM (SELECT * FROM unnest($1::text[]::uuid[], $2::timestamptz[]) AS t(id, at)) u WHERE api_keys.id=u.id AND (api_keys.last_used_at IS NULL OR api_keys.last_used_at < $3)`, ids, at, notBefore)
	return err
}

func scanAPIKeyRoles(encoded []byte, key *APIKey) error {
	return json.Unmarshal(encoded, &key.Roles)
}

// getActiveAPIKeyBySecretHashQuery is the credential lookup of every API-key request, planned by the hot-query registry.
const getActiveAPIKeyBySecretHashQuery = `SELECT id::text,COALESCE(service_account_id::text,''),name,secret_hash,array_to_json(roles),created_at,expires_at,last_used_at FROM api_keys WHERE secret_hash=$1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>now()) AND (service_account_id IS NULL OR EXISTS (SELECT 1 FROM service_accounts WHERE id=api_keys.service_account_id AND state='active'))`
