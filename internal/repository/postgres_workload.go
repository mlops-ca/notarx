package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mlops-ca/notarx/internal/ids"
)

var (
	_ WorkloadStore = (*PostgresStore)(nil)
	_ WorkloadStore = (*MemoryStore)(nil)
)

const workloadIssuerColumns = `id::text, name, kind, issuer, array_to_json(audiences), array_to_json(allowed_algs), jwks_mode, COALESCE(jwks_uri,''), static_jwks, COALESCE(ca_bundle_pem,''), allow_private_network, state, version::text, created_at, updated_at`

const workloadPolicyColumns = `id::text, name, issuer_id::text, service_account_id::text, conditions, max_token_ttl_seconds, single_use, state, version::text, expires_at, created_at, updated_at`

func scanWorkloadIssuer(row interface{ Scan(...any) error }, issuer *WorkloadIssuer) error {
	// static_jwks is nullable; scanning NULL into a json.RawMessage is unsupported, a []byte takes it.
	var audiences, algs, static []byte
	if err := row.Scan(&issuer.ID, &issuer.Name, &issuer.Kind, &issuer.Issuer, &audiences, &algs, &issuer.JWKSMode, &issuer.JWKSURI,
		&static, &issuer.CABundlePEM, &issuer.AllowPrivateNetwork, &issuer.State, &issuer.Version, &issuer.CreatedAt, &issuer.UpdatedAt); err != nil {
		return err
	}
	issuer.StaticJWKS = static
	if err := json.Unmarshal(audiences, &issuer.Audiences); err != nil {
		return err
	}
	return json.Unmarshal(algs, &issuer.AllowedAlgs)
}

func scanWorkloadPolicy(row interface{ Scan(...any) error }, policy *WorkloadTrustPolicy) error {
	var conditions []byte
	if err := row.Scan(&policy.ID, &policy.Name, &policy.IssuerID, &policy.ServiceAccountID, &conditions, &policy.MaxTokenTTLSeconds,
		&policy.SingleUse, &policy.State, &policy.Version, &policy.ExpiresAt, &policy.CreatedAt, &policy.UpdatedAt); err != nil {
		return err
	}
	policy.Conditions = conditions
	return nil
}

// nullJSON passes an empty document as SQL NULL and anything else as text for a jsonb cast.
func nullJSON(document []byte) any {
	if len(document) == 0 {
		return nil
	}
	return string(document)
}

// mapWorkloadError translates the constraint violations of the workload tables.
func mapWorkloadError(err error) error {
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) {
		return err
	}
	switch pgError.Code {
	case "23505":
		if pgError.ConstraintName == "workload_issuers_issuer_key" {
			return ErrWorkloadIssuerExists
		}
		return ErrNameExists
	case "23503":
		// Only writes of trust policies reach here; deleting an issuer maps the same violation itself.
		return ErrWorkloadReference
	case "23514":
		return workloadInvalid("%s", pgError.ConstraintName)
	}
	return err
}

func parseWorkloadVersion(version string) (int64, error) {
	value, err := strconv.ParseInt(version, 10, 64)
	if err != nil || value < 1 {
		return 0, ErrVersionConflict
	}
	return value, nil
}

func (s *PostgresStore) CreateWorkloadIssuer(ctx context.Context, issuer WorkloadIssuer, audit ...AuditRecord) (WorkloadIssuer, error) {
	issuer, err := normalizeWorkloadIssuer(issuer)
	if err != nil {
		return WorkloadIssuer{}, err
	}
	if issuer.ID == "" {
		issuer.ID = ids.New()
	}
	return runAudited(ctx, s, audit, func(q sqlRunner) (WorkloadIssuer, error) {
		var created WorkloadIssuer
		err := scanWorkloadIssuer(q.QueryRowContext(ctx, `INSERT INTO workload_issuers (id, name, kind, issuer, audiences, allowed_algs, jwks_mode, jwks_uri, static_jwks, ca_bundle_pem, allow_private_network, state)
			VALUES ($1::uuid,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9::jsonb,NULLIF($10,''),$11,$12) RETURNING `+workloadIssuerColumns,
			issuer.ID, issuer.Name, issuer.Kind, issuer.Issuer, issuer.Audiences, issuer.AllowedAlgs, issuer.JWKSMode, issuer.JWKSURI,
			nullJSON(issuer.StaticJWKS), issuer.CABundlePEM, issuer.AllowPrivateNetwork, issuer.State), &created)
		if err != nil {
			return WorkloadIssuer{}, mapWorkloadError(err)
		}
		return created, nil
	})
}

func (s *PostgresStore) GetWorkloadIssuer(ctx context.Context, id string) (WorkloadIssuer, error) {
	return getWorkloadIssuerOn(ctx, s.db, `id=$1::uuid`, id)
}

func (s *PostgresStore) GetWorkloadIssuerByIssuer(ctx context.Context, issuer string) (WorkloadIssuer, error) {
	return getWorkloadIssuerOn(ctx, s.db, `issuer=$1`, issuer)
}

func getWorkloadIssuerOn(ctx context.Context, q sqlRunner, filter, arg string) (WorkloadIssuer, error) {
	var issuer WorkloadIssuer
	err := scanWorkloadIssuer(q.QueryRowContext(ctx, `SELECT `+workloadIssuerColumns+` FROM workload_issuers WHERE `+filter, arg), &issuer)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkloadIssuer{}, ErrNotFound
	}
	if err != nil {
		return WorkloadIssuer{}, mapMalformedUUID(err)
	}
	return issuer, nil
}

// mapMalformedUUID reports a malformed identifier as not found instead of a database error.
func mapMalformedUUID(err error) error {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "22P02" {
		return ErrNotFound
	}
	return err
}

func (s *PostgresStore) ListWorkloadIssuers(ctx context.Context, limit int, afterID string) ([]WorkloadIssuer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+workloadIssuerColumns+` FROM workload_issuers WHERE ($1='' OR id>$1::uuid) ORDER BY id LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, mapMalformedUUID(err)
	}
	defer func() { _ = rows.Close() }()
	issuers := []WorkloadIssuer{}
	for rows.Next() {
		var issuer WorkloadIssuer
		if err := scanWorkloadIssuer(rows, &issuer); err != nil {
			return nil, err
		}
		issuers = append(issuers, issuer)
	}
	return issuers, rows.Err()
}

func (s *PostgresStore) UpdateWorkloadIssuer(ctx context.Context, issuer WorkloadIssuer, expectedVersion string, audit ...AuditRecord) (WorkloadIssuer, error) {
	issuer, err := normalizeWorkloadIssuer(issuer)
	if err != nil {
		return WorkloadIssuer{}, err
	}
	version, err := parseWorkloadVersion(expectedVersion)
	if err != nil {
		return WorkloadIssuer{}, err
	}
	return runAudited(ctx, s, audit, func(q sqlRunner) (WorkloadIssuer, error) {
		var updated WorkloadIssuer
		err := scanWorkloadIssuer(q.QueryRowContext(ctx, `UPDATE workload_issuers SET name=$2, kind=$3, issuer=$4, audiences=$5, allowed_algs=$6, jwks_mode=$7,
			jwks_uri=NULLIF($8,''), static_jwks=$9::jsonb, ca_bundle_pem=NULLIF($10,''), allow_private_network=$11, state=$12, updated_at=now(), version=version+1
			WHERE id=$1::uuid AND version=$13 RETURNING `+workloadIssuerColumns,
			issuer.ID, issuer.Name, issuer.Kind, issuer.Issuer, issuer.Audiences, issuer.AllowedAlgs, issuer.JWKSMode, issuer.JWKSURI,
			nullJSON(issuer.StaticJWKS), issuer.CABundlePEM, issuer.AllowPrivateNetwork, issuer.State, version), &updated)
		if errors.Is(err, sql.ErrNoRows) {
			if _, getErr := getWorkloadIssuerOn(ctx, q, `id=$1::uuid`, issuer.ID); errors.Is(getErr, ErrNotFound) {
				return WorkloadIssuer{}, ErrNotFound
			}
			return WorkloadIssuer{}, ErrVersionConflict
		}
		if err != nil {
			return WorkloadIssuer{}, mapWorkloadError(mapMalformedUUID(err))
		}
		return updated, nil
	})
}

func (s *PostgresStore) DeleteWorkloadIssuer(ctx context.Context, id, expectedVersion string, audit ...AuditRecord) error {
	version, err := parseWorkloadVersion(expectedVersion)
	if err != nil {
		return err
	}
	_, err = runAudited(ctx, s, audit, func(q sqlRunner) (struct{}, error) {
		return struct{}{}, deleteWorkloadRow(ctx, q, "workload_issuers", id, version)
	})
	return err
}

// deleteWorkloadRow deletes one versioned row of table (a trusted constant).
func deleteWorkloadRow(ctx context.Context, q sqlRunner, table, id string, version int64) error {
	result, err := q.ExecContext(ctx, `DELETE FROM `+table+` WHERE id=$1::uuid AND version=$2`, id, version)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23503" {
			return ErrWorkloadIssuerInUse
		}
		return mapMalformedUUID(err)
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		return nil
	}
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id=$1::uuid)`, id).Scan(&exists); err != nil {
		return mapMalformedUUID(err)
	}
	if !exists {
		return ErrNotFound
	}
	return ErrVersionConflict
}

func (s *PostgresStore) CreateWorkloadTrustPolicy(ctx context.Context, policy WorkloadTrustPolicy, audit ...AuditRecord) (WorkloadTrustPolicy, error) {
	policy, err := normalizeWorkloadTrustPolicy(policy)
	if err != nil {
		return WorkloadTrustPolicy{}, err
	}
	if policy.ID == "" {
		policy.ID = ids.New()
	}
	return runAudited(ctx, s, audit, func(q sqlRunner) (WorkloadTrustPolicy, error) {
		var created WorkloadTrustPolicy
		err := scanWorkloadPolicy(q.QueryRowContext(ctx, `INSERT INTO workload_trust_policies (id, name, issuer_id, service_account_id, conditions, max_token_ttl_seconds, single_use, state, expires_at)
			VALUES ($1::uuid,$2,$3::uuid,$4::uuid,$5::jsonb,$6,$7,$8,$9) RETURNING `+workloadPolicyColumns,
			policy.ID, policy.Name, policy.IssuerID, policy.ServiceAccountID, string(policy.Conditions), policy.MaxTokenTTLSeconds, policy.SingleUse, policy.State, policy.ExpiresAt), &created)
		if err != nil {
			return WorkloadTrustPolicy{}, mapWorkloadError(err)
		}
		return created, nil
	})
}

func (s *PostgresStore) GetWorkloadTrustPolicy(ctx context.Context, id string) (WorkloadTrustPolicy, error) {
	var policy WorkloadTrustPolicy
	err := scanWorkloadPolicy(s.db.QueryRowContext(ctx, `SELECT `+workloadPolicyColumns+` FROM workload_trust_policies WHERE id=$1::uuid`, id), &policy)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkloadTrustPolicy{}, ErrNotFound
	}
	if err != nil {
		return WorkloadTrustPolicy{}, mapMalformedUUID(err)
	}
	return policy, nil
}

func (s *PostgresStore) listWorkloadPolicies(ctx context.Context, query string, args ...any) ([]WorkloadTrustPolicy, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, mapMalformedUUID(err)
	}
	defer func() { _ = rows.Close() }()
	policies := []WorkloadTrustPolicy{}
	for rows.Next() {
		var policy WorkloadTrustPolicy
		if err := scanWorkloadPolicy(rows, &policy); err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	return policies, rows.Err()
}

func (s *PostgresStore) ListWorkloadTrustPolicies(ctx context.Context, limit int, afterID string) ([]WorkloadTrustPolicy, error) {
	return s.listWorkloadPolicies(ctx, `SELECT `+workloadPolicyColumns+` FROM workload_trust_policies WHERE ($1='' OR id>$1::uuid) ORDER BY id LIMIT $2`, afterID, limit)
}

func (s *PostgresStore) ListActiveWorkloadTrustPolicies(ctx context.Context, issuerID string, now time.Time) ([]WorkloadTrustPolicy, error) {
	return s.listWorkloadPolicies(ctx, `SELECT `+workloadPolicyColumns+` FROM workload_trust_policies
		WHERE issuer_id=$1::uuid AND state='active' AND (expires_at IS NULL OR expires_at>$2) ORDER BY name`, issuerID, now)
}

func (s *PostgresStore) UpdateWorkloadTrustPolicy(ctx context.Context, policy WorkloadTrustPolicy, expectedVersion string, audit ...AuditRecord) (WorkloadTrustPolicy, error) {
	policy, err := normalizeWorkloadTrustPolicy(policy)
	if err != nil {
		return WorkloadTrustPolicy{}, err
	}
	version, err := parseWorkloadVersion(expectedVersion)
	if err != nil {
		return WorkloadTrustPolicy{}, err
	}
	return runAudited(ctx, s, audit, func(q sqlRunner) (WorkloadTrustPolicy, error) {
		var updated WorkloadTrustPolicy
		err := scanWorkloadPolicy(q.QueryRowContext(ctx, `UPDATE workload_trust_policies SET name=$2, issuer_id=$3::uuid, service_account_id=$4::uuid, conditions=$5::jsonb,
			max_token_ttl_seconds=$6, single_use=$7, state=$8, expires_at=$9, updated_at=now(), version=version+1
			WHERE id=$1::uuid AND version=$10 RETURNING `+workloadPolicyColumns,
			policy.ID, policy.Name, policy.IssuerID, policy.ServiceAccountID, string(policy.Conditions), policy.MaxTokenTTLSeconds, policy.SingleUse, policy.State, policy.ExpiresAt, version), &updated)
		if errors.Is(err, sql.ErrNoRows) {
			var exists bool
			if existsErr := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM workload_trust_policies WHERE id=$1::uuid)`, policy.ID).Scan(&exists); existsErr != nil {
				return WorkloadTrustPolicy{}, mapMalformedUUID(existsErr)
			}
			if !exists {
				return WorkloadTrustPolicy{}, ErrNotFound
			}
			return WorkloadTrustPolicy{}, ErrVersionConflict
		}
		if err != nil {
			return WorkloadTrustPolicy{}, mapWorkloadError(mapMalformedUUID(err))
		}
		return updated, nil
	})
}

func (s *PostgresStore) DeleteWorkloadTrustPolicy(ctx context.Context, id, expectedVersion string, audit ...AuditRecord) error {
	version, err := parseWorkloadVersion(expectedVersion)
	if err != nil {
		return err
	}
	_, err = runAudited(ctx, s, audit, func(q sqlRunner) (struct{}, error) {
		return struct{}{}, deleteWorkloadRow(ctx, q, "workload_trust_policies", id, version)
	})
	return err
}

func (s *PostgresStore) RecordWorkloadJTI(ctx context.Context, issuerID string, jtiHash []byte, expiresAt time.Time) (bool, error) {
	if err := checkJTIArguments(issuerID, jtiHash); err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO workload_jti_seen (issuer_id, jti_hash, expires_at) VALUES ($1::uuid,$2,$3) ON CONFLICT DO NOTHING`, issuerID, jtiHash, expiresAt)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}
