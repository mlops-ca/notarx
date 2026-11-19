package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mlops-ca/notarx/internal/tokenissuer"
)

var _ tokenissuer.Store = (*PostgresStore)(nil)

// ErrSettingNotFound reports an unset key of the settings table.
var ErrSettingNotFound = errors.New("setting not found")

const signingKeyColumns = `kid, alg, state, public_jwk::text, private_sealed, sealing_ref, integrity_mac, created_at, published_at, activated_at, retired_at, expires_at`

type signingKeyQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func scanSigningKeys(rows *sql.Rows) ([]tokenissuer.KeyRecord, error) {
	defer func() { _ = rows.Close() }()
	var records []tokenissuer.KeyRecord
	for rows.Next() {
		var (
			record                                 tokenissuer.KeyRecord
			state, jwk                             string
			published, activated, retired, expires sql.NullTime
		)
		if err := rows.Scan(&record.KID, &record.Alg, &state, &jwk, &record.PrivateSealed, &record.SealingRef, &record.IntegrityMAC,
			&record.CreatedAt, &published, &activated, &retired, &expires); err != nil {
			return nil, err
		}
		record.State, record.PublicJWK = tokenissuer.State(state), []byte(jwk)
		record.PublishedAt, record.ActivatedAt, record.RetiredAt, record.ExpiresAt = published.Time, activated.Time, retired.Time, expires.Time
		records = append(records, record)
	}
	return records, rows.Err()
}

// ListKeys returns every signing key without taking locks.
func (s *PostgresStore) ListKeys(ctx context.Context) ([]tokenissuer.KeyRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+signingKeyColumns+` FROM token_signing_keys ORDER BY created_at, kid`)
	if err != nil {
		return nil, err
	}
	return scanSigningKeys(rows)
}

// WithRotationLock runs fn in one transaction that holds the issuer rotation advisory lock.
func (s *PostgresStore) WithRotationLock(ctx context.Context, fn func(context.Context, tokenissuer.KeyTx) error) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var acquired bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('notarx:issuer-rotation', 0))`).Scan(&acquired); err != nil {
		return false, err
	}
	if !acquired {
		return false, nil
	}
	if err := fn(ctx, signingKeyTx{tx: tx}); err != nil {
		return true, err
	}
	return true, tx.Commit()
}

type signingKeyTx struct{ tx *sql.Tx }

// List re-reads every row and locks it for the rest of the transaction.
func (t signingKeyTx) List(ctx context.Context) ([]tokenissuer.KeyRecord, error) {
	rows, err := t.tx.QueryContext(ctx, `SELECT `+signingKeyColumns+` FROM token_signing_keys ORDER BY created_at, kid FOR UPDATE`)
	if err != nil {
		return nil, err
	}
	return scanSigningKeys(rows)
}

func (t signingKeyTx) Insert(ctx context.Context, record tokenissuer.KeyRecord) error {
	_, err := t.tx.ExecContext(ctx, `INSERT INTO token_signing_keys (kid, alg, state, public_jwk, private_sealed, sealing_ref, integrity_mac, created_at, published_at, activated_at, retired_at, expires_at)
VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, $9, $10, $11, $12)`,
		record.KID, record.Alg, string(record.State), string(record.PublicJWK), record.PrivateSealed, record.SealingRef, record.IntegrityMAC,
		record.CreatedAt.UTC(), nullTime(record.PublishedAt), nullTime(record.ActivatedAt), nullTime(record.RetiredAt), nullTime(record.ExpiresAt))
	return mapSigningKeyError(err)
}

func (t signingKeyTx) Update(ctx context.Context, record tokenissuer.KeyRecord) error {
	result, err := t.tx.ExecContext(ctx, `UPDATE token_signing_keys SET state=$2, integrity_mac=$3, published_at=$4, activated_at=$5, retired_at=$6, expires_at=$7 WHERE kid=$1`,
		record.KID, string(record.State), record.IntegrityMAC, nullTime(record.PublishedAt), nullTime(record.ActivatedAt), nullTime(record.RetiredAt), nullTime(record.ExpiresAt))
	if err != nil {
		return mapSigningKeyError(err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return tokenissuer.ErrKeyNotFound
	}
	return nil
}

func (t signingKeyTx) Delete(ctx context.Context, kid string) error {
	_, err := t.tx.ExecContext(ctx, `DELETE FROM token_signing_keys WHERE kid=$1`, kid)
	return err
}

func mapSigningKeyError(err error) error {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "23505" {
		return fmt.Errorf("%w: %s", tokenissuer.ErrUniqueState, pgError.ConstraintName)
	}
	return err
}

// Setting returns the value of a settings key, or ErrSettingNotFound.
func (s *PostgresStore) Setting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSettingNotFound
	}
	return value, err
}

// SetSetting upserts a settings key.
func (s *PostgresStore) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value)
	return err
}

// Setting keys of the settings table.
const (
	SettingDeploymentID = "deployment_id"
	SettingIssuerURL    = "issuer_url"
)

// DeploymentID returns the stable id shared by every node of this deployment. It is created
// by migration 000016; if the row is missing (restored partial schema) it is created here,
// race free, so every node converges on one value.
func (s *PostgresStore) DeploymentID(ctx context.Context) (string, error) {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('deployment_id', gen_random_uuid()::text) ON CONFLICT (key) DO NOTHING`); err != nil {
		return "", err
	}
	return s.Setting(ctx, SettingDeploymentID)
}
