package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

const userIdentityColumns = `id::text,user_id::text,kind,provider_id::text,issuer,COALESCE(subject,''),COALESCE(scim_external_id,''),email,display_name,email_verified,last_login_at,created_at,updated_at`

type userIdentityScanner interface {
	Scan(...any) error
}

func scanUserIdentity(scanner userIdentityScanner, identity *UserIdentity) error {
	return scanner.Scan(
		&identity.ID, &identity.UserID, &identity.Kind, &identity.ProviderID, &identity.Issuer,
		&identity.Subject, &identity.SCIMExternalID, &identity.Email, &identity.DisplayName,
		&identity.EmailVerified, &identity.LastLoginAt, &identity.CreatedAt,
		&identity.UpdatedAt,
	)
}

func (s *PostgresStore) ListUserIdentities(ctx context.Context, userID string) ([]UserIdentity, error) {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+userIdentityColumns+` FROM user_identities WHERE user_id=$1::uuid ORDER BY created_at,id`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]UserIdentity, 0)
	for rows.Next() {
		var identity UserIdentity
		if err := scanUserIdentity(rows, &identity); err != nil {
			return nil, err
		}
		items = append(items, identity)
	}
	return items, rows.Err()
}

func (s *PostgresStore) CreateUserIdentity(ctx context.Context, identity UserIdentity, audit ...AuditRecord) (UserIdentity, error) {
	return runAudited(ctx, s, audit, func(q sqlRunner) (UserIdentity, error) {
		if _, err := getUserOn(ctx, q, identity.UserID); err != nil {
			return UserIdentity{}, err
		}
		identity = normalizeUserIdentity(identity)
		if identity.ID == "" {
			identity.ID = ids.New()
		}
		err := scanUserIdentity(q.QueryRowContext(ctx, `INSERT INTO user_identities (id,user_id,kind,provider_id,issuer,subject,email,display_name,email_verified) VALUES ($1,$2,$3,$4::uuid,$5,$6,$7,$8,$9) RETURNING `+userIdentityColumns,
			identity.ID, identity.UserID, identity.Kind, identity.ProviderID, identity.Issuer, identity.Subject,
			identity.Email, identity.DisplayName, identity.EmailVerified,
		), &identity)
		if isUnique(err) {
			return UserIdentity{}, ErrIdentityExists
		}
		return identity, err
	})
}

func (s *PostgresStore) DeleteUserIdentity(ctx context.Context, userID, identityID string, audit ...AuditRecord) error {
	_, err := runAudited(ctx, s, audit, func(q sqlRunner) (struct{}, error) {
		result, err := q.ExecContext(ctx, `DELETE FROM user_identities WHERE id=$1::uuid AND user_id=$2::uuid`, identityID, userID)
		if err != nil {
			return struct{}{}, err
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return struct{}{}, ErrNotFound
		}
		return struct{}{}, nil
	})
	return err
}

func (s *PostgresStore) GetUserByProviderIdentity(ctx context.Context, providerID, subject string) (User, UserIdentity, error) {
	var identity UserIdentity
	err := scanUserIdentity(s.db.QueryRowContext(ctx, `SELECT `+userIdentityColumns+` FROM user_identities WHERE provider_id=$1::uuid AND subject=$2`, providerID, strings.TrimSpace(subject)), &identity)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, UserIdentity{}, ErrNotFound
	}
	if err == nil {
		var user User
		user, err = s.GetUser(ctx, identity.UserID)
		return user, identity, err
	}
	return User{}, UserIdentity{}, err
}

func (s *PostgresStore) ResolveOIDCIdentity(ctx context.Context, provision OIDCIdentityProvision) (User, UserIdentity, bool, error) {
	user, identity, created, err := s.resolveOIDCIdentityMutation(ctx, provision)
	if err == nil {
		s.invalidateLocal(CacheScopeUser, user.ID)
	}
	return user, identity, created, err
}

func (s *PostgresStore) resolveOIDCIdentityMutation(ctx context.Context, provision OIDCIdentityProvision) (User, UserIdentity, bool, error) {
	provision.Issuer = normalizeOIDCIssuer(provision.Issuer)
	provision.Subject = strings.TrimSpace(provision.Subject)
	if provision.ProviderID == "" || provision.Subject == "" {
		return User{}, UserIdentity{}, false, ErrNotFound
	}
	now := provision.OccurredAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, UserIdentity{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var identity UserIdentity
	err = scanUserIdentity(tx.QueryRowContext(ctx, `SELECT `+userIdentityColumns+` FROM user_identities WHERE provider_id=$1::uuid AND subject=$2 FOR UPDATE`, provision.ProviderID, provision.Subject), &identity)
	if err == nil {
		user, err := updateOIDCLogin(ctx, tx, identity, provision, now)
		if err != nil {
			return User{}, UserIdentity{}, false, err
		}
		identity = refreshUserIdentity(identity, provision, now)
		if err = tx.Commit(); err != nil {
			return User{}, UserIdentity{}, false, err
		}
		return user, identity, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return User{}, UserIdentity{}, false, err
	}

	var user User
	if provision.LinkAdminVerifiedEmail {
		linked, err := postgresAdminVerifiedEmailLink(ctx, tx, provision)
		if err != nil {
			return User{}, UserIdentity{}, false, err
		}
		user = linked
	}
	if user.ID == "" && !provision.Provision {
		return User{}, UserIdentity{}, false, ErrNotFound
	}
	if user.ID == "" && provision.MatchEmail && provision.EmailVerified && strings.TrimSpace(provision.Email) != "" {
		matches, err := postgresUsersByEmail(ctx, tx, provision.Email, 2, false)
		if err != nil {
			return User{}, UserIdentity{}, false, err
		}
		if len(matches) > 1 {
			return User{}, UserIdentity{}, false, ErrIdentityAmbiguous
		}
		if len(matches) == 1 {
			user = matches[0]
		}
	}

	created := false
	if user.ID == "" {
		role := provisionedUserRole(provision.Role, provision.DefaultRole)
		name, err := postgresProvisionedUsername(ctx, tx, provision)
		if err != nil {
			return User{}, UserIdentity{}, false, err
		}
		user = User{
			ID: ids.New(), Name: name, DisplayName: strings.TrimSpace(provision.DisplayName),
			Email: strings.TrimSpace(provision.Email), Role: role, State: UserActive,
			LastLoginAt: timePointer(now), SessionVersion: 1,
		}
		err = scanUser(tx.QueryRowContext(ctx, `INSERT INTO users (id,name,display_name,email,description,secret_hash,role,state,last_login_at,password_changed_at,session_version) VALUES ($1,$2,$3,$4,'','',$5,$6,$7,NULL,1) RETURNING `+userColumns,
			user.ID, user.Name, user.DisplayName, user.Email, user.Role, user.State, now,
		), &user)
		if isUnique(err) {
			return User{}, UserIdentity{}, false, ErrNameExists
		}
		if err != nil {
			return User{}, UserIdentity{}, false, err
		}
		created = true
	} else {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM user_identities WHERE user_id=$1 AND provider_id=$2::uuid)`, user.ID, provision.ProviderID).Scan(&exists); err != nil {
			return User{}, UserIdentity{}, false, err
		}
		if exists {
			return User{}, UserIdentity{}, false, ErrIdentityExists
		}
		if err = scanUser(tx.QueryRowContext(ctx, `UPDATE users SET last_login_at=$2,updated_at=$2,version=version+1 WHERE id=$1 RETURNING `+userColumns, user.ID, now), &user); err != nil {
			return User{}, UserIdentity{}, false, err
		}
	}

	identity = refreshUserIdentity(UserIdentity{
		ID: ids.New(), UserID: user.ID, Kind: provision.identityKind(), ProviderID: provision.ProviderID,
		Issuer: provision.Issuer, Subject: provision.Subject, CreatedAt: now,
	}, provision, now)
	err = scanUserIdentity(tx.QueryRowContext(ctx, `INSERT INTO user_identities (id,user_id,kind,provider_id,issuer,subject,email,display_name,email_verified,last_login_at,created_at,updated_at) VALUES ($1,$2,$3,$4::uuid,$5,$6,$7,$8,$9,$10,$11,$11) RETURNING `+userIdentityColumns,
		identity.ID, identity.UserID, identity.Kind, identity.ProviderID, identity.Issuer, identity.Subject,
		identity.Email, identity.DisplayName, identity.EmailVerified, identity.LastLoginAt, now,
	), &identity)
	if isUnique(err) {
		return User{}, UserIdentity{}, false, ErrIdentityExists
	}
	if err != nil {
		return User{}, UserIdentity{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, UserIdentity{}, false, err
	}
	return user, identity, created, nil
}

// postgresUsersByEmail locks and returns up to limit accounts whose email matches case-insensitively.
// With verifiedFirst the administrator-verified accounts come first, so an unverified duplicate cannot
// push the verified account out of the limit.
func postgresUsersByEmail(ctx context.Context, tx *sql.Tx, email string, limit int, verifiedFirst bool) ([]User, error) {
	order := "created_at, id"
	if verifiedFirst {
		order = "email_verified_by_admin DESC, " + order
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+userColumns+` FROM users WHERE lower(email)=lower($1) ORDER BY `+order+` LIMIT $2 FOR UPDATE`, strings.TrimSpace(email), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	matches := make([]User, 0, limit)
	for rows.Next() {
		var candidate User
		if err := scanUser(rows, &candidate); err != nil {
			return nil, err
		}
		matches = append(matches, candidate)
	}
	return matches, rows.Err()
}

// postgresAdminVerifiedEmailLink evaluates the SAML email-link path. It returns the account to link, or a
// zero user when the path refuses; ErrIdentityAmbiguous means two administrator-verified accounts match.
// The decision is written to provision.EmailLink for the caller's audit record.
func postgresAdminVerifiedEmailLink(ctx context.Context, tx *sql.Tx, provision OIDCIdentityProvision) (User, error) {
	var matches []User
	linkedAlready := map[string]bool{}
	if strings.TrimSpace(provision.Email) != "" {
		var err error
		if matches, err = postgresUsersByEmail(ctx, tx, provision.Email, 3, true); err != nil {
			return User{}, err
		}
		rows, err := tx.QueryContext(ctx, `SELECT user_id::text FROM user_identities WHERE provider_id=$1::uuid AND user_id IN (SELECT id FROM users WHERE lower(email)=lower($2))`, provision.ProviderID, strings.TrimSpace(provision.Email))
		if err != nil {
			return User{}, err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var userID string
			if err := rows.Scan(&userID); err != nil {
				return User{}, err
			}
			linkedAlready[userID] = true
		}
		if err := rows.Err(); err != nil {
			return User{}, err
		}
	}
	linked, decision := decideEmailLink(provision.Email, matches, func(candidate User) bool { return linkedAlready[candidate.ID] })
	if provision.EmailLink != nil {
		*provision.EmailLink = decision
	}
	if decision.Reason == EmailLinkAmbiguous {
		return User{}, ErrIdentityAmbiguous
	}
	return linked, nil
}

func updateOIDCLogin(ctx context.Context, tx *sql.Tx, identity UserIdentity, provision OIDCIdentityProvision, now time.Time) (User, error) {
	identity = refreshUserIdentity(identity, provision, now)
	if _, err := tx.ExecContext(ctx, `UPDATE user_identities SET email=$2,display_name=$3,email_verified=$4,last_login_at=$5,updated_at=$5 WHERE id=$1`, identity.ID, identity.Email, identity.DisplayName, identity.EmailVerified, now); err != nil {
		return User{}, err
	}
	var user User
	err := scanUser(tx.QueryRowContext(ctx, `UPDATE users SET last_login_at=$2,
		display_name=CASE WHEN secret_hash='' AND $3<>'' THEN $3 ELSE display_name END,
		email_verified_by_admin=email_verified_by_admin AND NOT (secret_hash='' AND $4 AND $5<>'' AND lower($5)<>lower(email)),
		email_verified_at=CASE WHEN secret_hash='' AND $4 AND $5<>'' AND lower($5)<>lower(email) THEN NULL ELSE email_verified_at END,
		email_verified_by=CASE WHEN secret_hash='' AND $4 AND $5<>'' AND lower($5)<>lower(email) THEN '' ELSE email_verified_by END,
		email=CASE WHEN secret_hash='' AND $4 AND $5<>'' THEN $5 ELSE email END,
		updated_at=$2,version=version+1 WHERE id=$1 RETURNING `+userColumns,
		identity.UserID, now, identity.DisplayName, identity.EmailVerified, identity.Email), &user)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return user, err
}

func postgresProvisionedUsername(ctx context.Context, tx *sql.Tx, provision OIDCIdentityProvision) (string, error) {
	base := provisionedUsernameBase(provision.PreferredUsername, provision.Email)
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE lower(name)=lower($1))`, base).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return base, nil
	}
	return provisionedUsernameSuffix(base, provision.Issuer, provision.Subject), nil
}
