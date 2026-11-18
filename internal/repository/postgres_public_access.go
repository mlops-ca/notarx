package repository

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

func (s *PostgresStore) GetPublicAccessPolicy(ctx context.Context) (PublicAccessPolicy, error) {
	var policy PublicAccessPolicy
	var version int64
	err := s.db.QueryRowContext(ctx, `SELECT version, enabled FROM public_access_policy WHERE singleton=true`).Scan(&version, &policy.Enabled)
	if err != nil {
		return PublicAccessPolicy{}, err
	}
	policy.Version = strconv.FormatInt(version, 10)
	return policy, nil
}

func (s *PostgresStore) ReplacePublicAccessPolicy(ctx context.Context, policy PublicAccessPolicy, expectedVersion string) (PublicAccessPolicy, error) {
	version, err := strconv.ParseInt(expectedVersion, 10, 64)
	if err != nil {
		return PublicAccessPolicy{}, ErrVersionConflict
	}
	var nextVersion int64
	err = s.db.QueryRowContext(ctx, `UPDATE public_access_policy SET enabled=$1, version=version+1 WHERE singleton=true AND version=$2 RETURNING version`, policy.Enabled, version).Scan(&nextVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return PublicAccessPolicy{}, ErrVersionConflict
	}
	if err != nil {
		return PublicAccessPolicy{}, err
	}
	policy.Version = strconv.FormatInt(nextVersion, 10)
	return policy, nil
}
