package repository

import (
	"context"
	"database/sql"
	"errors"
)

const serviceAccountColumns = `id::text, name, description, state, created_at, updated_at, version::text, COALESCE(assertion_subject, '')`

// The id-keyset listings qualify ORDER BY with the table: a bare id would resolve to the selected
// id::text output column and sort the text instead of reading the uuid primary key in order.
const (
	listServiceAccountsQuery    = `SELECT ` + serviceAccountColumns + ` FROM service_accounts WHERE ($1='' OR id>$1::uuid) ORDER BY service_accounts.id LIMIT $2`
	listServiceAccountKeysQuery = `SELECT id::text, service_account_id::text, name, array_to_json(roles), created_at, revoked_at, expires_at, last_used_at FROM api_keys WHERE service_account_id=$1::uuid AND ($2='' OR id>$2::uuid) ORDER BY api_keys.id LIMIT $3`
)

func scanServiceAccount(row interface{ Scan(...any) error }, account *ServiceAccount) error {
	return row.Scan(
		&account.ID,
		&account.Name,
		&account.Description,
		&account.State,
		&account.CreatedAt,
		&account.UpdatedAt,
		&account.Version,
		&account.AssertionSubject,
	)
}

func (s *PostgresStore) CreateServiceAccount(ctx context.Context, account ServiceAccount, audit ...AuditRecord) (ServiceAccount, error) {
	// Always one transaction, with or without an audit record: the account and the reservation of its assertion subject
	// stand or fall together, so a refused subject never leaves the account behind.
	return runTransactional(ctx, s, audit, func(q sqlRunner) (ServiceAccount, error) {
		subject := account.AssertionSubject
		err := scanServiceAccount(
			q.QueryRowContext(
				ctx,
				`INSERT INTO service_accounts (id, name, description, assertion_subject) VALUES ($1,$2,$3,NULLIF($4,'')) RETURNING `+serviceAccountColumns,
				account.ID,
				account.Name,
				account.Description,
				subject,
			),
			&account,
		)
		if isUnique(err) {
			return ServiceAccount{}, ErrNameExists
		}
		if err != nil {
			return ServiceAccount{}, err
		}
		if subject != "" {
			// The reservation outlives the account (ON DELETE SET NULL), so a later account of the same name
			// cannot inherit the trust that was built on the subject.
			_, err := q.ExecContext(ctx, `INSERT INTO service_account_assertion_subjects (subject, service_account_id) VALUES ($1,$2::uuid)`, subject, account.ID)
			if isUnique(err) {
				return ServiceAccount{}, ErrAssertionSubjectReserved
			}
			if err != nil {
				return ServiceAccount{}, err
			}
		}
		return account, nil
	})
}

func (s *PostgresStore) ListServiceAccounts(ctx context.Context, limit int, afterID string) ([]ServiceAccount, error) {
	rows, err := s.db.QueryContext(ctx, listServiceAccountsQuery, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	accounts := []ServiceAccount{}
	for rows.Next() {
		var account ServiceAccount
		if err := scanServiceAccount(rows, &account); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func (s *PostgresStore) GetServiceAccount(ctx context.Context, id string) (ServiceAccount, error) {
	return getServiceAccountOn(ctx, s.db, id)
}

// getServiceAccountOn reads a service account on the pool or inside a transaction (see getUserOn).
func getServiceAccountOn(ctx context.Context, q sqlRunner, id string) (ServiceAccount, error) {
	var account ServiceAccount
	err := scanServiceAccount(
		q.QueryRowContext(ctx, `SELECT `+serviceAccountColumns+` FROM service_accounts WHERE id=$1::uuid`, id),
		&account,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ServiceAccount{}, ErrNotFound
	}
	if err != nil {
		return ServiceAccount{}, err
	}
	return account, nil
}

func (s *PostgresStore) UpdateServiceAccount(ctx context.Context, update ServiceAccountUpdate, expectedVersion string, audit ...AuditRecord) (ServiceAccount, error) {
	account, err := runAudited(ctx, s, audit, func(q sqlRunner) (ServiceAccount, error) {
		var account ServiceAccount
		description := ""
		if update.Description != nil {
			description = *update.Description
		}
		state := ServiceAccountState("")
		if update.State != nil {
			state = *update.State
		}
		err := scanServiceAccount(
			q.QueryRowContext(ctx, `UPDATE service_accounts SET description=CASE WHEN $2 THEN $3 ELSE description END, state=CASE WHEN $4 THEN $5 ELSE state END, updated_at=now(), version=version+1 WHERE id=$1::uuid AND version=$6::text::bigint RETURNING `+serviceAccountColumns,
				update.ID,
				update.Description != nil,
				description,
				update.State != nil,
				state,
				expectedVersion,
			),
			&account,
		)
		if errors.Is(err, sql.ErrNoRows) {
			if _, getErr := getServiceAccountOn(ctx, q, update.ID); errors.Is(getErr, ErrNotFound) {
				return ServiceAccount{}, ErrNotFound
			}
			return ServiceAccount{}, ErrVersionConflict
		}
		if err != nil {
			return ServiceAccount{}, err
		}
		return account, notifyCache(ctx, q, CacheScopeServiceAccount, account.ID)
	})
	if err == nil {
		s.invalidateLocal(CacheScopeServiceAccount, account.ID)
	}
	return account, err
}

func (s *PostgresStore) CreateServiceAccountCredential(ctx context.Context, credential APIKey, audit ...AuditRecord) (APIKey, error) {
	return runAudited(ctx, s, audit, func(q sqlRunner) (APIKey, error) {
		err := q.QueryRowContext(
			ctx,
			`INSERT INTO api_keys (id, service_account_id, name, secret_hash, roles, expires_at)
		 SELECT $1, id, $3, $4, $5, $6 FROM service_accounts WHERE id=$2::uuid AND state='active'
		 RETURNING created_at`,
			credential.ID,
			credential.ServiceAccountID,
			credential.Name,
			credential.SecretHash,
			credential.Roles,
			credential.ExpiresAt,
		).Scan(&credential.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			account, getErr := getServiceAccountOn(ctx, q, credential.ServiceAccountID)
			if errors.Is(getErr, ErrNotFound) {
				return APIKey{}, ErrNotFound
			}
			if getErr != nil {
				return APIKey{}, getErr
			}
			if account.State != ServiceAccountActive {
				return APIKey{}, ErrServiceAccountDisabled
			}
			return APIKey{}, ErrVersionConflict
		}
		if err != nil {
			return APIKey{}, err
		}
		return credential, nil
	})
}

func (s *PostgresStore) ListServiceAccountCredentials(ctx context.Context, accountID string, limit int, afterID string) ([]APIKey, error) {
	if _, err := s.GetServiceAccount(ctx, accountID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, listServiceAccountKeysQuery, accountID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	credentials := []APIKey{}
	for rows.Next() {
		var key APIKey
		var roles []byte
		if err := rows.Scan(&key.ID, &key.ServiceAccountID, &key.Name, &roles, &key.CreatedAt, &key.RevokedAt, &key.ExpiresAt, &key.LastUsedAt); err != nil {
			return nil, err
		}
		if err := scanAPIKeyRoles(roles, &key); err != nil {
			return nil, err
		}
		credentials = append(credentials, key)
	}
	return credentials, rows.Err()
}

func (s *PostgresStore) RevokeServiceAccountCredential(ctx context.Context, accountID, credentialID string, audit ...AuditRecord) (APIKey, error) {
	key, err := runAudited(ctx, s, audit, func(q sqlRunner) (APIKey, error) {
		var key APIKey
		var roles []byte
		err := q.QueryRowContext(ctx, `UPDATE api_keys SET revoked_at=COALESCE(revoked_at, now()) WHERE id=$1::uuid AND service_account_id=$2::uuid RETURNING id::text, service_account_id::text, name, array_to_json(roles), created_at, revoked_at, expires_at, last_used_at`, credentialID, accountID).Scan(&key.ID, &key.ServiceAccountID, &key.Name, &roles, &key.CreatedAt, &key.RevokedAt, &key.ExpiresAt, &key.LastUsedAt)
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

func (s *PostgresStore) ListAssertionSubjects(ctx context.Context, limit int, afterSubject string) ([]AssertionSubject, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT subject, COALESCE(service_account_id::text, ''), created_at FROM service_account_assertion_subjects
		WHERE subject > $1 ORDER BY subject LIMIT $2`, afterSubject, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	subjects := []AssertionSubject{}
	for rows.Next() {
		var subject AssertionSubject
		if err := rows.Scan(&subject.Subject, &subject.ServiceAccountID, &subject.CreatedAt); err != nil {
			return nil, err
		}
		subjects = append(subjects, subject)
	}
	return subjects, rows.Err()
}

func (s *PostgresStore) ReleaseAssertionSubject(ctx context.Context, subject string, audit ...AuditRecord) error {
	_, err := runAudited(ctx, s, audit, func(q sqlRunner) (struct{}, error) {
		// The owner check is part of the statement, so it holds with or without a surrounding transaction.
		done, err := q.ExecContext(ctx, `DELETE FROM service_account_assertion_subjects WHERE subject=$1 AND service_account_id IS NULL`, subject)
		if err != nil {
			return struct{}{}, err
		}
		if n, err := done.RowsAffected(); err != nil {
			return struct{}{}, err
		} else if n > 0 {
			return struct{}{}, nil
		}
		var exists bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM service_account_assertion_subjects WHERE subject=$1)`, subject).Scan(&exists); err != nil {
			return struct{}{}, err
		}
		if exists {
			return struct{}{}, ErrAssertionSubjectHeld
		}
		return struct{}{}, ErrNotFound
	})
	return err
}

const assertionAudienceColumns = `service_account_id::text, audience, max_lifetime_seconds, created_at`

func scanAssertionAudience(row interface{ Scan(...any) error }, entry *AssertionAudience) error {
	return row.Scan(&entry.ServiceAccountID, &entry.Audience, &entry.MaxLifetimeSeconds, &entry.CreatedAt)
}

func (s *PostgresStore) ListAssertionAudiences(ctx context.Context, accountID string) ([]AssertionAudience, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+assertionAudienceColumns+` FROM service_account_assertion_audiences WHERE service_account_id=$1::uuid ORDER BY audience`, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	entries := []AssertionAudience{}
	for rows.Next() {
		var entry AssertionAudience
		if err := scanAssertionAudience(rows, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		// An empty list is an answer only for an account that exists.
		if _, err := s.GetServiceAccount(ctx, accountID); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func (s *PostgresStore) GetAssertionAudience(ctx context.Context, accountID, audience string) (AssertionAudience, error) {
	var entry AssertionAudience
	err := scanAssertionAudience(s.db.QueryRowContext(ctx, `SELECT `+assertionAudienceColumns+` FROM service_account_assertion_audiences WHERE service_account_id=$1::uuid AND audience=$2`, accountID, audience), &entry)
	if errors.Is(err, sql.ErrNoRows) {
		return AssertionAudience{}, ErrNotFound
	}
	return entry, err
}

func (s *PostgresStore) PutAssertionAudience(ctx context.Context, entry AssertionAudience, audit ...AuditRecord) (AssertionAudience, bool, error) {
	type result struct {
		entry   AssertionAudience
		created bool
	}
	out, err := runAudited(ctx, s, audit, func(q sqlRunner) (result, error) {
		account, err := getServiceAccountOn(ctx, q, entry.ServiceAccountID)
		if err != nil {
			return result{}, err
		}
		if account.AssertionSubject == "" {
			return result{}, ErrAssertionSubjectMissing
		}
		var created bool
		err = q.QueryRowContext(ctx, `INSERT INTO service_account_assertion_audiences (service_account_id, audience, max_lifetime_seconds) VALUES ($1::uuid,$2,$3)
			ON CONFLICT (service_account_id, audience) DO UPDATE SET max_lifetime_seconds=EXCLUDED.max_lifetime_seconds
			RETURNING `+assertionAudienceColumns+`, (xmax = 0)`, entry.ServiceAccountID, entry.Audience, entry.MaxLifetimeSeconds).Scan(&entry.ServiceAccountID, &entry.Audience, &entry.MaxLifetimeSeconds, &entry.CreatedAt, &created)
		return result{entry: entry, created: created}, err
	})
	return out.entry, out.created, err
}

func (s *PostgresStore) DeleteAssertionAudience(ctx context.Context, accountID, audience string, audit ...AuditRecord) error {
	_, err := runAudited(ctx, s, audit, func(q sqlRunner) (struct{}, error) {
		done, err := q.ExecContext(ctx, `DELETE FROM service_account_assertion_audiences WHERE service_account_id=$1::uuid AND audience=$2`, accountID, audience)
		if err != nil {
			return struct{}{}, err
		}
		if n, err := done.RowsAffected(); err != nil {
			return struct{}{}, err
		} else if n == 0 {
			return struct{}{}, ErrNotFound
		}
		return struct{}{}, nil
	})
	return err
}
