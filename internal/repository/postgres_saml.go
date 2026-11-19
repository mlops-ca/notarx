package repository

import (
	"context"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

var (
	_ SAMLStateStore = (*PostgresStore)(nil)
	_ SAMLStateStore = (*MemoryStore)(nil)
)

func (s *PostgresStore) CreateSAMLRequestState(ctx context.Context, providerID string, expiresAt time.Time) (string, error) {
	provider, err := samlStateArguments(providerID)
	if err != nil {
		return "", err
	}
	id := ids.New()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO saml_request_state (id, provider_id, expires_at) VALUES ($1::uuid,$2::uuid,$3)`, id, provider, expiresAt); err != nil {
		return "", err
	}
	return id, nil
}

func (s *PostgresStore) ConsumeSAMLRequestState(ctx context.Context, providerID, id string, now time.Time) (bool, error) {
	provider, state, ok := samlRequestID(providerID, id)
	if !ok {
		return false, nil
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM saml_request_state WHERE id = $1::uuid AND provider_id = $2::uuid AND expires_at > $3`, state, provider, now)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (s *PostgresStore) RecordSAMLAssertionID(ctx context.Context, providerID, assertionID string, expiresAt time.Time) (bool, error) {
	provider, err := samlAssertionArguments(providerID, assertionID)
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO saml_assertion_ids (provider_id, assertion_id, expires_at) VALUES ($1::uuid,$2,$3) ON CONFLICT DO NOTHING`, provider, assertionID, expiresAt)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}
