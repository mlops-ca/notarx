package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const userColumns = `id::text, name, display_name, email, description, secret_hash, role, state, last_login_at, password_changed_at, failed_login_attempts, locked_until, must_change_password, session_version, email_verified_by_admin, email_verified_at, email_verified_by, created_at, updated_at, version::text`

// Serializes role/state mutations so two concurrent requests cannot both
// remove an administrator after observing the other as active.
const userAdminMutationAdvisoryLock int64 = 0x757365725f61646d

func scanUser(row interface{ Scan(...any) error }, user *User) error {
	return row.Scan(
		&user.ID, &user.Name, &user.DisplayName, &user.Email, &user.Description,
		&user.SecretHash, &user.Role, &user.State, &user.LastLoginAt,
		&user.PasswordChangedAt, &user.FailedLoginAttempts, &user.LockedUntil,
		&user.MustChangePassword, &user.SessionVersion, &user.EmailVerifiedByAdmin,
		&user.EmailVerifiedAt, &user.EmailVerifiedBy, &user.CreatedAt,
		&user.UpdatedAt, &user.Version,
	)
}

func (s *PostgresStore) CreateUser(ctx context.Context, user User, audit ...AuditRecord) (User, error) {
	user, err := s.createUserMutation(ctx, user, audit...)
	if err == nil {
		s.invalidateLocal(CacheScopeUser, user.ID)
	}
	return user, err
}

func (s *PostgresStore) createUserMutation(ctx context.Context, user User, audit ...AuditRecord) (User, error) {
	var passwordChangedAt *time.Time
	if user.SecretHash != "" {
		now := time.Now().UTC()
		passwordChangedAt = &now
	}
	return runAudited(ctx, s, audit, func(q sqlRunner) (User, error) {
		err := scanUser(q.QueryRowContext(ctx, `INSERT INTO users (id, name, display_name, email, description, secret_hash, role, must_change_password, password_changed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+userColumns, user.ID, user.Name, user.DisplayName, user.Email, user.Description, user.SecretHash, user.Role, user.MustChangePassword, passwordChangedAt), &user)
		if isUnique(err) {
			return User{}, ErrNameExists
		}
		if err != nil {
			return User{}, err
		}
		return user, nil
	})
}

func (s *PostgresStore) ListUsers(ctx context.Context, query UserListQuery) (UserPage, error) {
	query.Search = strings.TrimSpace(query.Search)
	if query.Limit <= 0 || query.Limit > 500 {
		query.Limit = 100
	}
	afterAt, afterID, after, err := query.afterCursor()
	if err != nil {
		return UserPage{}, err
	}
	const filter = ` WHERE ($1='' OR name ILIKE '%'||$1||'%' OR display_name ILIKE '%'||$1||'%' OR email ILIKE '%'||$1||'%') AND ($2='' OR role=$2) AND ($3='' OR state=$3)`
	unfiltered := query.Search == "" && query.Role == "" && query.State == ""
	estimateTable := ""
	if unfiltered {
		estimateTable = "users"
	}
	total, err := boundedTotal(ctx, s.db, estimateTable, `users`+filter, query.Search, query.Role, query.State)
	if err != nil {
		return UserPage{}, err
	}
	var cursorAt sql.NullTime
	var cursorID sql.NullString
	if after {
		cursorAt, cursorID = sql.NullTime{Time: afterAt, Valid: true}, sql.NullString{String: afterID, Valid: true}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users`+filter+` AND ($4::timestamptz IS NULL OR (created_at,id)>($4::timestamptz,$5::uuid)) ORDER BY created_at, id LIMIT $6`,
		query.Search, query.Role, query.State, cursorAt, cursorID, query.Limit+1)
	if err != nil {
		return UserPage{}, err
	}
	defer func() { _ = rows.Close() }()
	users := []User{}
	for rows.Next() {
		var user User
		if err := scanUser(rows, &user); err != nil {
			return UserPage{}, err
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return UserPage{}, err
	}
	page := UserPage{Items: users, Total: total, Limit: query.Limit}
	if len(users) > query.Limit {
		page.Items = users[:query.Limit]
		page.NextPageToken = userPageToken(query, page.Items[query.Limit-1])
	}
	return page, nil
}

func (s *PostgresStore) GetUser(ctx context.Context, id string) (User, error) {
	return getUserOn(ctx, s.db, id)
}

// getUserOn reads a user on the pool or inside a transaction, so a lookup that runs inside an
// audited mutation does not take a second pooled connection while the transaction holds one.
func getUserOn(ctx context.Context, q sqlRunner, id string) (User, error) {
	var user User
	err := scanUser(q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1::uuid`, id), &user)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *PostgresStore) GetUserByName(ctx context.Context, name string) (User, error) {
	var user User
	err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE lower(name)=lower($1)`, name), &user)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *PostgresStore) UpdateUser(ctx context.Context, update UserUpdate, expectedVersion string, audit ...AuditRecord) (User, error) {
	user, err := s.updateUserMutation(ctx, update, expectedVersion, audit...)
	if err == nil {
		s.invalidateLocal(CacheScopeUser, user.ID)
	}
	return user, err
}

func (s *PostgresStore) updateUserMutation(ctx context.Context, update UserUpdate, expectedVersion string, audit ...AuditRecord) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, userAdminMutationAdvisoryLock); err != nil {
		return User{}, err
	}
	var current User
	err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1::uuid FOR UPDATE`, update.ID), &current)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	if current.Version != expectedVersion {
		return User{}, ErrVersionConflict
	}
	if removesLastActiveAdmin(current, update.Role, update.State) {
		var others int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id<>$1 AND role='admin' AND state=$2`, update.ID, UserActive).Scan(&others); err != nil {
			return User{}, err
		}
		if others == 0 {
			return User{}, ErrLastActiveAdmin
		}
	}
	// The email verification follows the same rules as the in-memory store (applyEmailVerification);
	// the verification columns are computed in Go from the locked row so both stores stay in step.
	verified, err := applyEmailVerification(current, update, time.Now().UTC())
	if err != nil {
		return User{}, err
	}
	var updated User
	err = scanUser(tx.QueryRowContext(ctx, `UPDATE users SET role=CASE WHEN $2 THEN $3 ELSE role END, state=CASE WHEN $4 THEN $5 ELSE state END, disabled_reason=CASE WHEN $4 THEN (CASE WHEN $5='disabled' THEN 'admin' END) ELSE disabled_reason END, display_name=CASE WHEN $6 THEN $7 ELSE display_name END, email=CASE WHEN $8 THEN $9 ELSE email END, description=CASE WHEN $10 THEN $11 ELSE description END, email_verified_by_admin=$13, email_verified_at=$14, email_verified_by=$15, version=version+1, updated_at=now() WHERE id=$1::uuid AND version=$12::text::bigint RETURNING `+userColumns,
		update.ID,
		update.Role != nil, stringValue(update.Role),
		update.State != nil, stringValue(update.State),
		update.DisplayName != nil, stringValue(update.DisplayName),
		update.Email != nil, stringValue(update.Email),
		update.Description != nil, stringValue(update.Description),
		expectedVersion,
		verified.EmailVerifiedByAdmin, verified.EmailVerifiedAt, verified.EmailVerifiedBy,
	), &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrVersionConflict
	}
	if err != nil {
		return User{}, err
	}
	if err = insertAudits(ctx, tx, audit); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return updated, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *PostgresStore) DeleteUser(ctx context.Context, id string, audit ...AuditRecord) error {
	err := s.deleteUserMutation(ctx, id, audit...)
	if err == nil {
		s.invalidateLocal(CacheScopeUser, id)
	}
	return err
}

func (s *PostgresStore) deleteUserMutation(ctx context.Context, id string, audit ...AuditRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, userAdminMutationAdvisoryLock); err != nil {
		return err
	}
	var current User
	err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1::uuid FOR UPDATE`, id), &current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current.Role == "admin" && current.State == UserActive {
		var others int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id<>$1 AND role='admin' AND state=$2`, id, UserActive).Scan(&others); err != nil {
			return err
		}
		if others == 0 {
			return ErrLastActiveAdmin
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM users WHERE id=$1::uuid`, id); err != nil {
		return err
	}
	if err = insertAudits(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) RecordUserLoginSuccess(ctx context.Context, id string, occurredAt time.Time, audit ...AuditRecord) (User, error) {
	user, err := s.recordUserLoginSuccessMutation(ctx, id, occurredAt, audit...)
	if err == nil {
		s.invalidateLocal(CacheScopeUser, user.ID)
	}
	return user, err
}

func (s *PostgresStore) recordUserLoginSuccessMutation(ctx context.Context, id string, occurredAt time.Time, audit ...AuditRecord) (User, error) {
	return runAudited(ctx, s, audit, func(q sqlRunner) (User, error) {
		var user User
		err := scanUser(q.QueryRowContext(ctx, `UPDATE users SET last_login_at=$2, failed_login_attempts=0, locked_until=NULL, version=version+1, updated_at=$2 WHERE id=$1::uuid RETURNING `+userColumns, id, occurredAt.UTC()), &user)
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return user, err
	})
}

func (s *PostgresStore) RecordUserLoginFailure(ctx context.Context, id string, occurredAt time.Time, maxAttempts int, lockout time.Duration, audit ...AuditRecord) (User, error) {
	user, err := s.recordUserLoginFailureMutation(ctx, id, occurredAt, maxAttempts, lockout, audit...)
	if err == nil {
		s.invalidateLocal(CacheScopeUser, user.ID)
	}
	return user, err
}

func (s *PostgresStore) recordUserLoginFailureMutation(ctx context.Context, id string, occurredAt time.Time, maxAttempts int, lockout time.Duration, audit ...AuditRecord) (User, error) {
	return runAudited(ctx, s, audit, func(q sqlRunner) (User, error) {
		var user User
		lockedUntil := occurredAt.UTC().Add(lockout)
		err := scanUser(q.QueryRowContext(ctx, `UPDATE users SET failed_login_attempts=failed_login_attempts+1, locked_until=CASE WHEN $3>0 AND failed_login_attempts+1 >= $3 AND $4 THEN $5 ELSE locked_until END, version=version+1, updated_at=$2 WHERE id=$1::uuid RETURNING `+userColumns, id, occurredAt.UTC(), maxAttempts, lockout > 0, lockedUntil), &user)
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return user, err
	})
}

func (s *PostgresStore) UpdateUserPassword(ctx context.Context, id, secretHash, expectedVersion string, mustChange bool, audit ...AuditRecord) (User, error) {
	user, err := s.updateUserPasswordMutation(ctx, id, secretHash, expectedVersion, mustChange, audit...)
	if err == nil {
		s.invalidateLocal(CacheScopeUser, user.ID)
	}
	return user, err
}

func (s *PostgresStore) updateUserPasswordMutation(ctx context.Context, id, secretHash, expectedVersion string, mustChange bool, audit ...AuditRecord) (User, error) {
	return runAudited(ctx, s, audit, func(q sqlRunner) (User, error) {
		var user User
		err := scanUser(q.QueryRowContext(ctx, `WITH updated_user AS (
		UPDATE users SET secret_hash=$2, password_changed_at=now(), must_change_password=$3, session_version=session_version+1, failed_login_attempts=0, locked_until=NULL, version=version+1, updated_at=now() WHERE id=$1::uuid AND version=$4::text::bigint RETURNING *
	), revoked_sessions AS (
		UPDATE user_sessions SET revoked_at=now() WHERE user_id=(SELECT id FROM updated_user) AND revoked_at IS NULL
	) SELECT `+userColumns+` FROM updated_user`, id, secretHash, mustChange, expectedVersion), &user)
		if errors.Is(err, sql.ErrNoRows) {
			if _, getErr := getUserOn(ctx, q, id); errors.Is(getErr, ErrNotFound) {
				return User{}, ErrNotFound
			}
			return User{}, ErrVersionConflict
		}
		return user, err
	})
}

func (s *PostgresStore) RevokeUserSessions(ctx context.Context, id, expectedVersion string, audit ...AuditRecord) (User, error) {
	user, err := s.revokeUserSessionsMutation(ctx, id, expectedVersion, audit...)
	if err == nil {
		s.invalidateLocal(CacheScopeUser, user.ID)
	}
	return user, err
}

func (s *PostgresStore) revokeUserSessionsMutation(ctx context.Context, id, expectedVersion string, audit ...AuditRecord) (User, error) {
	return runAudited(ctx, s, audit, func(q sqlRunner) (User, error) {
		var user User
		err := scanUser(q.QueryRowContext(ctx, `WITH updated_user AS (
		UPDATE users SET session_version=session_version+1, version=version+1, updated_at=now() WHERE id=$1::uuid AND version=$2::text::bigint RETURNING *
	), revoked_sessions AS (
		UPDATE user_sessions SET revoked_at=now() WHERE user_id=(SELECT id FROM updated_user) AND revoked_at IS NULL
	) SELECT `+userColumns+` FROM updated_user`, id, expectedVersion), &user)
		if errors.Is(err, sql.ErrNoRows) {
			if _, getErr := getUserOn(ctx, q, id); errors.Is(getErr, ErrNotFound) {
				return User{}, ErrNotFound
			}
			return User{}, ErrVersionConflict
		}
		return user, err
	})
}
