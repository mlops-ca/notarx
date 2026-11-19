package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/mlops-ca/notarx/internal/identity"
)

const identityProviderColumns = `id::text,slug,kind,display_name,enabled,settings,COALESCE(secrets_sealed,''),attribute_mapping,provisioning_mode,email_linking_enabled,jit_default_role,version,created_at,updated_at`

type identityProviderScanner interface{ Scan(...any) error }

func scanIdentityProvider(scanner identityProviderScanner) (identity.Provider, error) {
	var provider identity.Provider
	var kind, mode string
	var settings, mapping []byte
	if err := scanner.Scan(&provider.ID, &provider.Slug, &kind, &provider.DisplayName, &provider.Enabled, &settings,
		&provider.SecretsSealed, &mapping, &mode, &provider.EmailLinkingEnabled, &provider.JITDefaultRole,
		&provider.Version, &provider.CreatedAt, &provider.UpdatedAt); err != nil {
		return identity.Provider{}, err
	}
	provider.Kind, provider.ProvisioningMode, provider.Settings = identity.Kind(kind), identity.ProvisioningMode(mode), settings
	if err := json.Unmarshal(mapping, &provider.AttributeMapping); err != nil {
		return identity.Provider{}, err
	}
	return provider, nil
}

func encodeIdentityProvider(provider identity.Provider) (settings, mapping []byte, secret any, err error) {
	settings = provider.Settings
	if len(settings) == 0 {
		settings = []byte(`{}`)
	}
	if mapping, err = json.Marshal(provider.AttributeMapping); err != nil {
		return nil, nil, nil, err
	}
	if provider.SecretsSealed != "" {
		secret = provider.SecretsSealed
	}
	return settings, mapping, secret, nil
}

func (s *PostgresStore) ListIdentityProviders(ctx context.Context) ([]identity.Provider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+identityProviderColumns+` FROM identity_providers ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	providers := make([]identity.Provider, 0)
	for rows.Next() {
		provider, err := scanIdentityProvider(rows)
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	return providers, rows.Err()
}

func (s *PostgresStore) GetIdentityProvider(ctx context.Context, id string) (identity.Provider, error) {
	provider, err := scanIdentityProvider(s.db.QueryRowContext(ctx, `SELECT `+identityProviderColumns+` FROM identity_providers WHERE id=$1::uuid`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Provider{}, ErrNotFound
	}
	return provider, err
}

func (s *PostgresStore) GetIdentityProviderBySlug(ctx context.Context, slug string) (identity.Provider, error) {
	provider, err := scanIdentityProvider(s.db.QueryRowContext(ctx, `SELECT `+identityProviderColumns+` FROM identity_providers WHERE slug=$1`, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Provider{}, ErrNotFound
	}
	return provider, err
}

func (s *PostgresStore) CreateIdentityProvider(ctx context.Context, provider identity.Provider, audit ...AuditRecord) (identity.Provider, error) {
	settings, mapping, secret, err := encodeIdentityProvider(provider)
	if err != nil {
		return identity.Provider{}, err
	}
	return runAudited(ctx, s, audit, func(q sqlRunner) (identity.Provider, error) {
		created, err := scanIdentityProvider(q.QueryRowContext(ctx, `INSERT INTO identity_providers
			(id,slug,kind,display_name,enabled,settings,secrets_sealed,attribute_mapping,provisioning_mode,email_linking_enabled,jit_default_role)
			VALUES ($1::uuid,$2,$3,$4,$5,$6::jsonb,$7,$8::jsonb,$9,$10,$11) RETURNING `+identityProviderColumns,
			provider.ID, provider.Slug, string(provider.Kind), provider.DisplayName, provider.Enabled, settings, secret, mapping,
			string(provider.ProvisioningMode), provider.EmailLinkingEnabled, provider.JITDefaultRole))
		if isUnique(err) {
			return identity.Provider{}, ErrIdentityProviderSlugExists
		}
		return created, err
	})
}

func (s *PostgresStore) ReplaceIdentityProvider(ctx context.Context, provider identity.Provider, expectedVersion int64, audit ...AuditRecord) (identity.Provider, error) {
	settings, mapping, secret, err := encodeIdentityProvider(provider)
	if err != nil {
		return identity.Provider{}, err
	}
	return runAudited(ctx, s, audit, func(q sqlRunner) (identity.Provider, error) {
		// The kind is immutable: a provider row never changes protocol.
		replaced, err := scanIdentityProvider(q.QueryRowContext(ctx, `UPDATE identity_providers SET
			slug=$2, display_name=$3, enabled=$4, settings=$5::jsonb, secrets_sealed=$6, attribute_mapping=$7::jsonb,
			provisioning_mode=$8, email_linking_enabled=$9, jit_default_role=$10, version=version+1, updated_at=now()
			WHERE id=$1::uuid AND version=$11 AND kind=$12 RETURNING `+identityProviderColumns,
			provider.ID, provider.Slug, provider.DisplayName, provider.Enabled, settings, secret, mapping,
			string(provider.ProvisioningMode), provider.EmailLinkingEnabled, provider.JITDefaultRole, expectedVersion, string(provider.Kind)))
		switch {
		case isUnique(err):
			return identity.Provider{}, ErrIdentityProviderSlugExists
		case errors.Is(err, sql.ErrNoRows):
			var exists bool
			if probe := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM identity_providers WHERE id=$1::uuid AND kind=$2)`, provider.ID, string(provider.Kind)).Scan(&exists); probe != nil {
				return identity.Provider{}, probe
			}
			if !exists {
				return identity.Provider{}, ErrNotFound
			}
			return identity.Provider{}, ErrVersionConflict
		}
		return replaced, err
	})
}

func (s *PostgresStore) DeleteIdentityProvider(ctx context.Context, id string, expectedVersion int64, audit ...AuditRecord) error {
	_, err := runAudited(ctx, s, audit, func(q sqlRunner) (struct{}, error) {
		var inUse bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM user_identities WHERE provider_id=$1::uuid)`, id).Scan(&inUse); err != nil {
			return struct{}{}, err
		}
		if inUse {
			return struct{}{}, ErrIdentityProviderInUse
		}
		result, err := q.ExecContext(ctx, `DELETE FROM identity_providers WHERE id=$1::uuid AND version=$2`, id, expectedVersion)
		if err != nil {
			return struct{}{}, err
		}
		if affected, _ := result.RowsAffected(); affected == 1 {
			return struct{}{}, nil
		}
		var exists bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM identity_providers WHERE id=$1::uuid)`, id).Scan(&exists); err != nil {
			return struct{}{}, err
		}
		if !exists {
			return struct{}{}, ErrNotFound
		}
		return struct{}{}, ErrVersionConflict
	})
	return err
}

func (s *PostgresStore) SeedIdentityProvider(ctx context.Context, provider identity.Provider) (bool, error) {
	settings, mapping, secret, err := encodeIdentityProvider(provider)
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO identity_providers
		(id,slug,kind,display_name,enabled,settings,secrets_sealed,attribute_mapping,provisioning_mode,email_linking_enabled,jit_default_role)
		SELECT $1::uuid,$2,$3,$4,$5,$6::jsonb,$7,$8::jsonb,$9,$10,$11
		WHERE NOT EXISTS (SELECT 1 FROM identity_providers)
		ON CONFLICT DO NOTHING`,
		provider.ID, provider.Slug, string(provider.Kind), provider.DisplayName, provider.Enabled, settings, secret, mapping,
		string(provider.ProvisioningMode), provider.EmailLinkingEnabled, provider.JITDefaultRole)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected == 1, nil
}
