package repository

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

const scimTokenColumns = `id::text,provider_id::text,name,secret_hash,created_at,expires_at,revoked_at`

func scanSCIMToken(row interface{ Scan(...any) error }) (SCIMToken, error) {
	var token SCIMToken
	err := row.Scan(&token.ID, &token.ProviderID, &token.Name, &token.SecretHash, &token.CreatedAt, &token.ExpiresAt, &token.RevokedAt)
	return token, err
}

func (s *PostgresStore) CreateSCIMToken(ctx context.Context, token SCIMToken, audit ...AuditRecord) (SCIMToken, error) {
	token, err := NormalizeSCIMToken(token, time.Now().UTC())
	if err != nil {
		return SCIMToken{}, err
	}
	if !isUUID(token.ProviderID) {
		return SCIMToken{}, ErrNotFound
	}
	if token.ID == "" {
		token.ID = ids.New()
	}
	saved, err := runAudited(ctx, s, audit, func(q sqlRunner) (SCIMToken, error) {
		created, err := scanSCIMToken(q.QueryRowContext(ctx, `INSERT INTO identity_provider_scim_tokens (id,provider_id,name,secret_hash,expires_at) VALUES ($1::uuid,$2::uuid,$3,$4,$5) RETURNING `+scimTokenColumns,
			token.ID, token.ProviderID, token.Name, token.SecretHash, token.ExpiresAt))
		switch {
		case isForeignKeyViolation(err):
			return SCIMToken{}, ErrNotFound
		case isUnique(err):
			return SCIMToken{}, ErrSCIMTokenInvalid
		}
		return created, err
	})
	if err == nil {
		s.invalidateLocalScope(CacheScopeSCIMToken)
	}
	return saved, err
}

func (s *PostgresStore) ListSCIMTokens(ctx context.Context, providerID string) ([]SCIMToken, error) {
	if !isUUID(providerID) {
		return []SCIMToken{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+scimTokenColumns+` FROM identity_provider_scim_tokens WHERE provider_id=$1::uuid ORDER BY identity_provider_scim_tokens.id LIMIT $2`, providerID, SCIMMaxPageSize)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []SCIMToken{}
	for rows.Next() {
		token, err := scanSCIMToken(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, token)
	}
	return items, rows.Err()
}

func (s *PostgresStore) RevokeSCIMToken(ctx context.Context, providerID, tokenID string, audit ...AuditRecord) (SCIMToken, error) {
	if !isUUID(providerID) || !isUUID(tokenID) {
		return SCIMToken{}, ErrNotFound
	}
	saved, err := runAudited(ctx, s, audit, func(q sqlRunner) (SCIMToken, error) {
		token, err := scanSCIMToken(q.QueryRowContext(ctx, `UPDATE identity_provider_scim_tokens SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1::uuid AND provider_id=$2::uuid RETURNING `+scimTokenColumns, tokenID, providerID))
		if errors.Is(err, sql.ErrNoRows) {
			return SCIMToken{}, ErrNotFound
		}
		return token, err
	})
	if err == nil {
		s.invalidateLocalScope(CacheScopeSCIMToken)
	}
	return saved, err
}

func (s *PostgresStore) FindActiveSCIMTokenByHash(ctx context.Context, hash string) (SCIMToken, error) {
	token, err := scanSCIMToken(s.db.QueryRowContext(ctx, `SELECT `+scimTokenColumns+` FROM identity_provider_scim_tokens WHERE secret_hash=$1 AND revoked_at IS NULL AND expires_at>now()`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return SCIMToken{}, ErrNotFound
	}
	return token, err
}

// scimVisible is the condition of the identity rows SCIM can see: the accounts it provisioned. A just-in-time or
// administrator-linked account has no scim_external_id, and a link that DELETE /Users removed carries a tombstone in
// it; both are hidden. scimVisibleBare is the same for statements without the i alias.
const (
	scimVisible     = `(i.scim_external_id IS NOT NULL AND i.scim_external_id NOT LIKE '` + SCIMDeletedExternalIDPrefix + `%')`
	scimVisibleBare = `(scim_external_id IS NOT NULL AND scim_external_id NOT LIKE '` + SCIMDeletedExternalIDPrefix + `%')`
)

const scimUserColumns = `u.id::text, u.name, u.display_name, u.email, u.description, u.secret_hash, u.role, u.state, u.last_login_at, u.password_changed_at, u.failed_login_attempts, u.locked_until, u.must_change_password, u.session_version, u.email_verified_by_admin, u.email_verified_at, u.email_verified_by, u.created_at, u.updated_at, u.version::text, COALESCE(u.disabled_reason, ''),
	i.id::text,i.user_id::text,i.kind,i.provider_id::text,i.issuer,COALESCE(i.subject,''),COALESCE(i.scim_external_id,''),i.email,i.display_name,i.email_verified,i.last_login_at,i.created_at,i.updated_at`

func scanSCIMUser(row interface{ Scan(...any) error }) (SCIMUser, error) {
	var user User
	var identity UserIdentity
	err := row.Scan(
		&user.ID, &user.Name, &user.DisplayName, &user.Email, &user.Description,
		&user.SecretHash, &user.Role, &user.State, &user.LastLoginAt,
		&user.PasswordChangedAt, &user.FailedLoginAttempts, &user.LockedUntil,
		&user.MustChangePassword, &user.SessionVersion, &user.EmailVerifiedByAdmin,
		&user.EmailVerifiedAt, &user.EmailVerifiedBy, &user.CreatedAt,
		&user.UpdatedAt, &user.Version, &user.DisabledReason,
		&identity.ID, &identity.UserID, &identity.Kind, &identity.ProviderID, &identity.Issuer,
		&identity.Subject, &identity.SCIMExternalID, &identity.Email, &identity.DisplayName,
		&identity.EmailVerified, &identity.LastLoginAt, &identity.CreatedAt, &identity.UpdatedAt,
	)
	return SCIMUser{User: user, Identity: identity}, err
}

// getSCIMUserOn reads the account of a provider on the pool or inside a transaction; lock takes the row locks of a
// change. An account the provider has no identity row for is not found: SCIM sees nothing else.
func getSCIMUserOn(ctx context.Context, q sqlRunner, providerID, userID string, lock bool) (SCIMUser, error) {
	if !isUUID(providerID) || !isUUID(userID) {
		return SCIMUser{}, ErrNotFound
	}
	query := `SELECT ` + scimUserColumns + ` FROM user_identities i JOIN users u ON u.id=i.user_id WHERE i.provider_id=$1::uuid AND i.user_id=$2::uuid AND ` + scimVisible
	if lock {
		query += ` FOR UPDATE OF u, i`
	}
	user, err := scanSCIMUser(q.QueryRowContext(ctx, query, providerID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return SCIMUser{}, ErrNotFound
	}
	return user, err
}

func (s *PostgresStore) CreateSCIMUser(ctx context.Context, in SCIMUserCreate, audit ...AuditRecord) (SCIMUser, error) {
	if !isUUID(in.ProviderID) {
		return SCIMUser{}, ErrNotFound
	}
	created, err := runTransactional(ctx, s, nil, func(q sqlRunner) (SCIMUser, error) {
		result, taken, err := createSCIMUserOn(ctx, q, in)
		if err != nil {
			return SCIMUser{}, err
		}
		// The audit names the account the call created or took over, which only this transaction knows.
		kindOfResult := "created"
		if taken {
			kindOfResult = "taken_over"
		}
		if err := insertAudits(ctx, q, scimAuditTarget(audit, "scim/Users/"+result.User.ID, map[string]string{"result": kindOfResult})); err != nil {
			return SCIMUser{}, err
		}
		return result, nil
	})
	if err == nil {
		s.invalidateLocal(CacheScopeUser, created.User.ID)
	}
	return created, err
}

// createSCIMUserOn creates the account and its identity row, or takes over an account the provider is linked to; taken
// reports the latter.
func createSCIMUserOn(ctx context.Context, q sqlRunner, in SCIMUserCreate) (SCIMUser, bool, error) {
	var kind, slug string
	err := q.QueryRowContext(ctx, `SELECT kind, slug FROM identity_providers WHERE id=$1::uuid FOR SHARE`, in.ProviderID).Scan(&kind, &slug)
	if errors.Is(err, sql.ErrNoRows) {
		return SCIMUser{}, false, ErrNotFound
	}
	if err != nil {
		return SCIMUser{}, false, err
	}
	state, reason := UserActive, ""
	if !in.Active {
		state, reason = UserDisabled, DisabledBySCIM
	}
	if claimed, found, err := claimSCIMUser(ctx, q, in, state, reason); err != nil || found {
		return claimed, found, err
	}
	userID := ids.New()
	var user User
	err = scanUser(q.QueryRowContext(ctx, `INSERT INTO users (id,name,display_name,email,description,secret_hash,role,state,disabled_reason,session_version) VALUES ($1::uuid,$2,$3,$4,'','','none',$5,NULLIF($6,''),1) RETURNING `+userColumns,
		userID, in.UserName, in.DisplayName, in.Email, state, reason), &user)
	if isUnique(err) {
		return SCIMUser{}, false, ErrNameExists
	}
	if err != nil {
		return SCIMUser{}, false, err
	}
	externalID := in.ExternalID
	if externalID == "" {
		externalID = SCIMGeneratedExternalIDPrefix + userID
	}
	var identity UserIdentity
	err = scanUserIdentity(q.QueryRowContext(ctx, `INSERT INTO user_identities (id,user_id,kind,provider_id,issuer,subject,scim_external_id,email,display_name) VALUES ($1::uuid,$2::uuid,$3,$4::uuid,$5,NULL,$6,$7,$8) RETURNING `+userIdentityColumns,
		ids.New(), userID, kind, in.ProviderID, "scim:"+slug, externalID, in.Email, in.DisplayName), &identity)
	if isUnique(err) {
		return SCIMUser{}, false, ErrIdentityExists
	}
	if err != nil {
		return SCIMUser{}, false, err
	}
	return SCIMUser{User: user, Identity: identity}, false, nil
}

// claimSCIMUser takes over an account the provider is linked to but SCIM does not own: one it deleted earlier (its link
// carries the tombstone) or one a login or an administrator linked (no scim_external_id). The account must be at level
// none, without a password and active or disabled by SCIM; it keeps its name, takes the profile of the request and is
// active again only when the request says so, and an active account the request deactivates loses its sessions at once.
// found is false when no such account exists, and the create goes on.
func claimSCIMUser(ctx context.Context, q sqlRunner, in SCIMUserCreate, state, reason string) (SCIMUser, bool, error) {
	var userID, identityID string
	err := q.QueryRowContext(ctx, `SELECT u.id::text, i.id::text FROM users u JOIN user_identities i ON i.user_id=u.id AND i.provider_id=$1::uuid
		WHERE lower(u.name)=lower($2) AND (i.scim_external_id IS NULL OR i.scim_external_id LIKE '`+SCIMDeletedExternalIDPrefix+`%') AND u.role='none' AND u.secret_hash=''
		AND (u.state='active' OR (u.state='disabled' AND u.disabled_reason='scim')) FOR UPDATE OF u, i`, in.ProviderID, in.UserName).Scan(&userID, &identityID)
	if errors.Is(err, sql.ErrNoRows) {
		return SCIMUser{}, false, nil
	}
	if err != nil {
		return SCIMUser{}, false, err
	}
	current, err := getSCIMUserRowsOn(ctx, q, identityID)
	if err != nil {
		return SCIMUser{}, false, err
	}
	externalID := in.ExternalID
	if externalID == "" {
		externalID = SCIMGeneratedExternalIDPrefix + userID
	}
	verified := current.User
	if !strings.EqualFold(in.Email, current.User.Email) {
		verified = clearEmailVerification(current.User)
	}
	revoke := current.User.State == UserActive && state == UserDisabled
	var user User
	if err = scanUser(q.QueryRowContext(ctx, `UPDATE users SET display_name=$2, email=$3, email_verified_by_admin=$4, email_verified_at=$5, email_verified_by=$6, state=$7, disabled_reason=NULLIF($8,''), session_version=session_version+CASE WHEN $9 THEN 1 ELSE 0 END, version=version+1, updated_at=now() WHERE id=$1::uuid RETURNING `+userColumns,
		userID, in.DisplayName, in.Email, verified.EmailVerifiedByAdmin, verified.EmailVerifiedAt, verified.EmailVerifiedBy, state, reason, revoke), &user); err != nil {
		return SCIMUser{}, false, err
	}
	var identity UserIdentity
	err = scanUserIdentity(q.QueryRowContext(ctx, `UPDATE user_identities SET scim_external_id=$2, email=$3, display_name=$4, updated_at=now() WHERE id=$1::uuid RETURNING `+userIdentityColumns,
		identityID, externalID, in.Email, in.DisplayName), &identity)
	if isUnique(err) {
		return SCIMUser{}, false, ErrIdentityExists
	}
	if err != nil {
		return SCIMUser{}, false, err
	}
	if revoke {
		if _, err = q.ExecContext(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1::uuid AND revoked_at IS NULL`, userID); err != nil {
			return SCIMUser{}, false, err
		}
	}
	return SCIMUser{User: user, Identity: identity}, true, nil
}

// getSCIMUserRowsOn reads an account and its identity row by the identity id, whatever the state of the link.
func getSCIMUserRowsOn(ctx context.Context, q sqlRunner, identityID string) (SCIMUser, error) {
	user, err := scanSCIMUser(q.QueryRowContext(ctx, `SELECT `+scimUserColumns+` FROM user_identities i JOIN users u ON u.id=i.user_id WHERE i.id=$1::uuid`, identityID))
	if errors.Is(err, sql.ErrNoRows) {
		return SCIMUser{}, ErrNotFound
	}
	return user, err
}

func (s *PostgresStore) GetSCIMUser(ctx context.Context, providerID, userID string) (SCIMUser, error) {
	return getSCIMUserOn(ctx, s.db, providerID, userID, false)
}

// scimUserFilterSQL is the FROM and WHERE of a SCIM user list without the cursor, shared by the count and the page. A
// filter is added to the text only when it is given, so the planner sees a plain equality it can serve from
// users_name_case_insensitive_idx or the (provider_id, scim_external_id) unique index instead of an OR over a
// parameter; no client value reaches the text, only placeholders.
func scimUserFilterSQL(query SCIMUserQuery) (string, []any) {
	filter := `user_identities i JOIN users u ON u.id=i.user_id WHERE i.provider_id=$1::uuid AND ` + scimVisible
	args := []any{query.ProviderID}
	if query.UserName != "" {
		args = append(args, query.UserName)
		filter += " AND lower(u.name)=lower($" + strconv.Itoa(len(args)) + ")"
	}
	if query.ExternalID != "" {
		args = append(args, query.ExternalID)
		filter += " AND i.scim_external_id=$" + strconv.Itoa(len(args))
	}
	return filter, args
}

// scimUserPageSQL is one keyset page of a provider's users, served by user_identities_provider_created_idx; after is
// whether the page continues a cursor, whose two key placeholders follow the filter's.
func scimUserPageSQL(query SCIMUserQuery, after bool, limit int) (string, []any) {
	filter, args := scimUserFilterSQL(query)
	columns := scimUserColumns
	if query.KeysOnly {
		columns = "i.created_at, i.id::text"
	}
	statement := "SELECT " + columns + " FROM " + filter
	if after {
		statement += " AND (i.created_at,i.id)>($" + strconv.Itoa(len(args)+1) + "::timestamptz,$" + strconv.Itoa(len(args)+2) + "::uuid)"
	}
	return statement + " ORDER BY i.created_at, i.id LIMIT " + strconv.Itoa(limit), args
}

func (s *PostgresStore) ListSCIMUsers(ctx context.Context, query SCIMUserQuery) (SCIMUserPage, error) {
	afterAt, afterID, after, err := decodeSCIMCursor(query.Cursor, query.filterKey())
	if err != nil {
		return SCIMUserPage{}, err
	}
	if !isUUID(query.ProviderID) {
		return SCIMUserPage{Items: []SCIMUser{}, Total: BoundedListTotal(0)}, nil
	}
	limit := clampSCIMPage(query.Limit)
	var total ListTotal
	if !query.SkipTotal {
		filter, args := scimUserFilterSQL(query)
		if total, err = boundedTotal(ctx, s.db, "", filter, args...); err != nil {
			return SCIMUserPage{}, err
		}
	}
	statement, args := scimUserPageSQL(query, after, limit+1)
	if after {
		args = append(args, afterAt, afterID)
	}
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return SCIMUserPage{}, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]SCIMUser, 0, limit)
	for rows.Next() {
		var user SCIMUser
		var err error
		if query.KeysOnly {
			err = rows.Scan(&user.Identity.CreatedAt, &user.Identity.ID)
		} else {
			user, err = scanSCIMUser(rows)
		}
		if err != nil {
			return SCIMUserPage{}, err
		}
		items = append(items, user)
	}
	if err := rows.Err(); err != nil {
		return SCIMUserPage{}, err
	}
	page := SCIMUserPage{Items: items, Total: total}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1].Identity
		page.NextCursor = encodeSCIMCursor(query.filterKey(), last.CreatedAt, last.ID)
	}
	return page, nil
}

func (s *PostgresStore) UpdateSCIMUser(ctx context.Context, providerID, userID string, change SCIMUserChange, audit ...AuditRecord) (SCIMUser, error) {
	updated, err := runTransactional(ctx, s, audit, func(q sqlRunner) (SCIMUser, error) {
		current, err := getSCIMUserOn(ctx, q, providerID, userID, true)
		if err != nil {
			return SCIMUser{}, err
		}
		user, identity := current.User, current.Identity
		if user.Role == userRoleAdmin {
			return SCIMUser{}, ErrSCIMAdminProtected
		}
		if !scimVersionAllowed(user.Version, change.IfVersions) {
			return SCIMUser{}, ErrVersionConflict
		}
		if change.UserName != nil && !strings.EqualFold(*change.UserName, user.Name) {
			return SCIMUser{}, ErrSCIMUserNameImmutable
		}
		if change.ExternalID != nil && *change.ExternalID == "" && identity.Subject == "" {
			return SCIMUser{}, ErrSCIMInvalid
		}
		state, reason, revoke := user.State, user.DisabledReason, false
		if change.Active != nil {
			switch {
			case *change.Active && state == UserDisabled && reason != DisabledBySCIM:
				return SCIMUser{}, ErrSCIMAdminDisabled
			case *change.Active:
				state, reason = UserActive, ""
			case state != UserDisabled:
				state, reason, revoke = UserDisabled, DisabledBySCIM, true
			}
		}
		verified := user
		if change.Email != nil && !strings.EqualFold(strings.TrimSpace(*change.Email), strings.TrimSpace(user.Email)) {
			verified = clearEmailVerification(user)
		}
		err = scanUser(q.QueryRowContext(ctx, `UPDATE users SET display_name=CASE WHEN $2 THEN $3 ELSE display_name END, email=CASE WHEN $4 THEN $5 ELSE email END, email_verified_by_admin=$6, email_verified_at=$7, email_verified_by=$8, state=$9, disabled_reason=NULLIF($10,''), session_version=session_version+CASE WHEN $11 THEN 1 ELSE 0 END, version=version+1, updated_at=now() WHERE id=$1::uuid RETURNING `+userColumns,
			userID,
			change.DisplayName != nil, stringValue(change.DisplayName),
			change.Email != nil, stringValue(change.Email),
			verified.EmailVerifiedByAdmin, verified.EmailVerifiedAt, verified.EmailVerifiedBy,
			state, reason, revoke), &user)
		if err != nil {
			return SCIMUser{}, err
		}
		err = scanUserIdentity(q.QueryRowContext(ctx, `UPDATE user_identities SET scim_external_id=CASE WHEN $2 THEN NULLIF($3,'') ELSE scim_external_id END, email=CASE WHEN $4 THEN $5 ELSE email END, display_name=CASE WHEN $6 THEN $7 ELSE display_name END, updated_at=now() WHERE id=$1::uuid RETURNING `+userIdentityColumns,
			identity.ID, change.ExternalID != nil, stringValue(change.ExternalID), change.Email != nil, stringValue(change.Email), change.DisplayName != nil, stringValue(change.DisplayName)), &identity)
		if isUnique(err) {
			return SCIMUser{}, ErrIdentityExists
		}
		if err != nil {
			return SCIMUser{}, err
		}
		if revoke {
			if _, err = q.ExecContext(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1::uuid AND revoked_at IS NULL`, userID); err != nil {
				return SCIMUser{}, err
			}
		}
		return SCIMUser{User: user, Identity: identity}, nil
	})
	if err == nil {
		s.invalidateLocal(CacheScopeUser, userID)
	}
	return updated, err
}

func (s *PostgresStore) DeleteSCIMUser(ctx context.Context, providerID, userID string, ifVersions []string, audit ...AuditRecord) error {
	_, err := runTransactional(ctx, s, audit, func(q sqlRunner) (struct{}, error) {
		current, err := getSCIMUserOn(ctx, q, providerID, userID, true)
		if err != nil {
			return struct{}{}, err
		}
		if current.User.Role == userRoleAdmin {
			return struct{}{}, ErrSCIMAdminProtected
		}
		if !scimVersionAllowed(current.User.Version, ifVersions) {
			return struct{}{}, ErrVersionConflict
		}
		// The row stays: it may carry the login subject, and the tombstone hides the account from SCIM until the same
		// provider creates the user again.
		if _, err = q.ExecContext(ctx, `UPDATE user_identities SET scim_external_id=$2, updated_at=now() WHERE id=$1::uuid`, current.Identity.ID, SCIMDeletedExternalIDPrefix+userID); err != nil {
			return struct{}{}, err
		}
		// The memberships SCIM wrote go with the link: a revived account must not come back with access the provider
		// has since withdrawn.
		// The groups that lose a member change version, so their ETags move.
		if _, err = q.ExecContext(ctx, `WITH removed AS (DELETE FROM user_group_members m USING user_groups g WHERE m.user_id=$1::uuid AND m.source='scim' AND g.id=m.group_id AND g.provider_id=$2::uuid AND g.source='scim' RETURNING m.group_id)
			UPDATE user_groups SET version=version+1, updated_at=now() WHERE id IN (SELECT group_id FROM removed)`, userID, providerID); err != nil {
			return struct{}{}, err
		}
		if _, err = q.ExecContext(ctx, `UPDATE users SET state='disabled', disabled_reason=CASE WHEN state='disabled' THEN disabled_reason ELSE 'scim' END, session_version=session_version+1, version=version+1, updated_at=now() WHERE id=$1::uuid`, userID); err != nil {
			return struct{}{}, err
		}
		_, err = q.ExecContext(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1::uuid AND revoked_at IS NULL`, userID)
		return struct{}{}, err
	})
	if err == nil {
		s.invalidateLocal(CacheScopeUser, userID)
		// The memberships SCIM wrote went with the link.
		s.invalidateLocalScope(CacheScopeUserGroup)
	}
	return err
}

// scimMembersLinked reports whether every user id is an account of the provider.
func scimMembersLinked(ctx context.Context, q sqlRunner, providerID string, memberIDs []string) (bool, error) {
	memberIDs = uniqueStrings(memberIDs)
	if len(memberIDs) == 0 {
		return true, nil
	}
	for _, id := range memberIDs {
		if !isUUID(id) {
			return false, nil
		}
	}
	// The rows are share-locked: a concurrent DELETE /Users locks the same identity row to tombstone it, so an account
	// deleted between this check and the insert of its membership is seen as deleted, never left in the group.
	rows, err := q.QueryContext(ctx, `SELECT user_id::text FROM user_identities WHERE provider_id=$1::uuid AND `+scimVisibleBare+` AND user_id IN (SELECT m::uuid FROM unnest($2::text[]) AS m) FOR SHARE`, providerID, memberIDs)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	linked := 0
	for rows.Next() {
		linked++
	}
	return linked == len(memberIDs), rows.Err()
}

// adoptLocalMemberships makes the memberships an administrator added for accounts the identity provider now names
// SCIM-managed, so the provider sees them and can remove them; an INSERT ... ON CONFLICT DO NOTHING alone would leave
// them local and invisible to it.
func adoptLocalMemberships(ctx context.Context, q sqlRunner, groupID string, userIDs []string) error {
	_, err := q.ExecContext(ctx, `UPDATE user_group_members SET source='scim' WHERE group_id=$1::uuid AND source='local' AND user_id IN (SELECT m::uuid FROM unnest($2::text[]) AS m)`, groupID, userIDs)
	return err
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			unique = append(unique, value)
		}
	}
	return unique
}

func (s *PostgresStore) CreateSCIMGroup(ctx context.Context, in SCIMGroupCreate, audit ...AuditRecord) (UserGroup, error) {
	group, err := NormalizeUserGroup(UserGroup{ProviderID: in.ProviderID, ExternalID: in.ExternalID, DisplayName: in.DisplayName, Source: UserGroupSourceSCIM, GlobalRole: UserGroupRoleNone})
	if err != nil {
		return UserGroup{}, ErrSCIMInvalid
	}
	if !isUUID(in.ProviderID) {
		return UserGroup{}, ErrUserGroupProviderNotFound
	}
	group.ID = ids.New()
	created, err := runTransactional(ctx, s, scimAuditTarget(audit, "scim/Groups/"+group.ID, nil), func(q sqlRunner) (UserGroup, error) {
		if linked, err := scimMembersLinked(ctx, q, in.ProviderID, in.MemberIDs); err != nil {
			return UserGroup{}, err
		} else if !linked {
			return UserGroup{}, ErrSCIMInvalid
		}
		// The role is never an input here: a SCIM group is created at none and only an administrator flags it.
		created, err := scanUserGroup(q.QueryRowContext(ctx, `INSERT INTO user_groups (id,provider_id,external_id,display_name,source) VALUES ($1::uuid,$2::uuid,$3,$4,'scim') RETURNING `+userGroupColumns,
			group.ID, group.ProviderID, group.ExternalID, group.DisplayName))
		if isForeignKeyViolation(err) {
			return UserGroup{}, ErrUserGroupProviderNotFound
		}
		if err = userGroupUniqueError(err); err != nil {
			return UserGroup{}, err
		}
		if members := uniqueStrings(in.MemberIDs); len(members) > 0 {
			if err = adoptLocalMemberships(ctx, q, created.ID, members); err != nil {
				return UserGroup{}, err
			}
			if _, err = q.ExecContext(ctx, `INSERT INTO user_group_members (group_id,user_id,source) SELECT $1::uuid, m::uuid, 'scim' FROM unnest($2::text[]) AS m ON CONFLICT DO NOTHING`, created.ID, members); err != nil {
				return UserGroup{}, err
			}
		}
		return created, nil
	})
	if err == nil {
		s.invalidateLocalScope(CacheScopeUserGroup)
	}
	return created, err
}

func getSCIMGroupOn(ctx context.Context, q sqlRunner, providerID, groupID string, lock bool) (UserGroup, error) {
	if !isUUID(providerID) || !isUUID(groupID) {
		return UserGroup{}, ErrNotFound
	}
	query := `SELECT ` + userGroupColumns + ` FROM user_groups WHERE id=$1::uuid AND provider_id=$2::uuid AND source='scim'`
	if lock {
		query += ` FOR UPDATE`
	}
	group, err := scanUserGroup(q.QueryRowContext(ctx, query, groupID, providerID))
	if errors.Is(err, sql.ErrNoRows) {
		return UserGroup{}, ErrNotFound
	}
	return group, err
}

func (s *PostgresStore) GetSCIMGroup(ctx context.Context, providerID, groupID string) (UserGroup, error) {
	return getSCIMGroupOn(ctx, s.db, providerID, groupID, false)
}

// scimGroupFilterSQL is the FROM and WHERE of a SCIM group list without the cursor; as for users, a filter is added to
// the text only when it is given.
func scimGroupFilterSQL(query SCIMGroupQuery) (string, []any) {
	filter := "user_groups WHERE provider_id=$1::uuid AND source='scim'"
	args := []any{query.ProviderID}
	if query.DisplayName != "" {
		args = append(args, query.DisplayName)
		filter += " AND display_name=$" + strconv.Itoa(len(args))
	}
	if query.ExternalID != "" {
		args = append(args, query.ExternalID)
		filter += " AND external_id=$" + strconv.Itoa(len(args))
	}
	return filter, args
}

func (s *PostgresStore) ListSCIMGroups(ctx context.Context, query SCIMGroupQuery) (SCIMGroupPage, error) {
	afterAt, afterID, after, err := decodeSCIMCursor(query.Cursor, query.filterKey())
	if err != nil {
		return SCIMGroupPage{}, err
	}
	if !isUUID(query.ProviderID) {
		return SCIMGroupPage{Items: []UserGroup{}, Total: BoundedListTotal(0)}, nil
	}
	limit := clampSCIMPage(query.Limit)
	filter, args := scimGroupFilterSQL(query)
	var total ListTotal
	if !query.SkipTotal {
		if total, err = boundedTotal(ctx, s.db, "", filter, args...); err != nil {
			return SCIMGroupPage{}, err
		}
	}
	columns := userGroupColumns
	if query.KeysOnly {
		columns = "user_groups.id::text, user_groups.created_at"
	}
	statement := "SELECT " + columns + " FROM " + filter
	if after {
		statement += " AND (user_groups.created_at,user_groups.id)>($" + strconv.Itoa(len(args)+1) + "::timestamptz,$" + strconv.Itoa(len(args)+2) + "::uuid)"
		args = append(args, afterAt, afterID)
	}
	rows, err := s.db.QueryContext(ctx, statement+" ORDER BY user_groups.created_at, user_groups.id LIMIT "+strconv.Itoa(limit+1), args...)
	if err != nil {
		return SCIMGroupPage{}, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]UserGroup, 0, limit)
	for rows.Next() {
		var group UserGroup
		var err error
		if query.KeysOnly {
			err = rows.Scan(&group.ID, &group.CreatedAt)
		} else {
			group, err = scanUserGroup(rows)
		}
		if err != nil {
			return SCIMGroupPage{}, err
		}
		items = append(items, group)
	}
	if err := rows.Err(); err != nil {
		return SCIMGroupPage{}, err
	}
	page := SCIMGroupPage{Items: items, Total: total}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = encodeSCIMCursor(query.filterKey(), page.Items[limit-1].CreatedAt, page.Items[limit-1].ID)
	}
	return page, nil
}

func (s *PostgresStore) UpdateSCIMGroup(ctx context.Context, providerID, groupID string, change SCIMGroupChange, audit ...AuditRecord) (UserGroup, error) {
	if change.DisplayName != nil {
		name, err := NormalizeUserGroupName(*change.DisplayName)
		if err != nil {
			return UserGroup{}, ErrSCIMInvalid
		}
		change.DisplayName = &name
	}
	updated, err := runTransactional(ctx, s, audit, func(q sqlRunner) (UserGroup, error) {
		group, err := getSCIMGroupOn(ctx, q, providerID, groupID, true)
		if err != nil {
			return UserGroup{}, err
		}
		if !scimVersionAllowed(group.Version, change.IfVersions) {
			return UserGroup{}, ErrVersionConflict
		}
		added := append([]string(nil), change.AddMembers...)
		if change.ReplaceMembers != nil {
			added = append(added, *change.ReplaceMembers...)
		}
		if linked, err := scimMembersLinked(ctx, q, providerID, added); err != nil {
			return UserGroup{}, err
		} else if !linked {
			return UserGroup{}, ErrSCIMInvalid
		}
		// The statement names no role column: SCIM can rename a group but never reach its global role.
		updated, err := scanUserGroup(q.QueryRowContext(ctx, `UPDATE user_groups SET display_name=CASE WHEN $2 THEN $3 ELSE display_name END, external_id=CASE WHEN $4 THEN $5 ELSE external_id END, version=version+1, updated_at=now() WHERE id=$1::uuid RETURNING `+userGroupColumns,
			group.ID, change.DisplayName != nil, stringValue(change.DisplayName), change.ExternalID != nil, strings.TrimSpace(stringValue(change.ExternalID))))
		if err = userGroupUniqueError(err); err != nil {
			return UserGroup{}, err
		}
		if change.ReplaceMembers != nil {
			// Only the SCIM-sourced memberships are authoritative here; a membership an administrator added stays.
			if _, err = q.ExecContext(ctx, `DELETE FROM user_group_members WHERE group_id=$1::uuid AND source='scim' AND user_id NOT IN (SELECT m::uuid FROM unnest($2::text[]) AS m)`, group.ID, uniqueStrings(*change.ReplaceMembers)); err != nil {
				return UserGroup{}, err
			}
		}
		if members := uniqueStrings(added); len(members) > 0 {
			if err = adoptLocalMemberships(ctx, q, group.ID, members); err != nil {
				return UserGroup{}, err
			}
			if _, err = q.ExecContext(ctx, `INSERT INTO user_group_members (group_id,user_id,source) SELECT $1::uuid, m::uuid, 'scim' FROM unnest($2::text[]) AS m ON CONFLICT DO NOTHING`, group.ID, members); err != nil {
				return UserGroup{}, err
			}
		}
		if removed := uniqueStrings(change.RemoveMembers); len(removed) > 0 {
			for _, id := range removed {
				if !isUUID(id) {
					return UserGroup{}, ErrSCIMInvalid
				}
			}
			if _, err = q.ExecContext(ctx, `DELETE FROM user_group_members WHERE group_id=$1::uuid AND source='scim' AND user_id IN (SELECT m::uuid FROM unnest($2::text[]) AS m)`, group.ID, removed); err != nil {
				return UserGroup{}, err
			}
		}
		return updated, nil
	})
	if err == nil {
		s.invalidateLocalScope(CacheScopeUserGroup)
	}
	return updated, err
}

func (s *PostgresStore) DeleteSCIMGroup(ctx context.Context, providerID, groupID string, ifVersions []string, audit ...AuditRecord) error {
	repositoryIDs, err := runTransactional(ctx, s, audit, func(q sqlRunner) ([]string, error) {
		group, err := getSCIMGroupOn(ctx, q, providerID, groupID, true)
		if err != nil {
			return nil, err
		}
		if !scimVersionAllowed(group.Version, ifVersions) {
			return nil, ErrVersionConflict
		}
		return deleteUserGroupOn(ctx, q, group.ID)
	})
	if err == nil {
		s.invalidateLocalScope(CacheScopeUserGroup)
		for _, repositoryID := range repositoryIDs {
			s.invalidateGrants(repositoryID)
		}
	}
	return err
}

func (s *PostgresStore) ListSCIMGroupMembers(ctx context.Context, providerID, groupID string, limit int) ([]UserGroupMember, error) {
	if _, err := getSCIMGroupOn(ctx, s.db, providerID, groupID, false); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.group_id::text,m.user_id::text,u.name,m.source,m.created_at FROM user_group_members m JOIN users u ON u.id=m.user_id JOIN user_identities i ON i.user_id=m.user_id AND i.provider_id=$3::uuid WHERE m.group_id=$1::uuid AND m.source='scim' AND `+scimVisible+` ORDER BY m.user_id LIMIT $2`, groupID, max(limit, 1), providerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanSCIMMembers(rows)
}

func (s *PostgresStore) ListSCIMGroupsMembers(ctx context.Context, providerID string, groupIDs []string, perGroup int) (map[string][]UserGroupMember, error) {
	members := make(map[string][]UserGroupMember, len(groupIDs))
	if !isUUID(providerID) || len(groupIDs) == 0 {
		return members, nil
	}
	for _, id := range groupIDs {
		if !isUUID(id) {
			return nil, ErrNotFound
		}
	}
	// One pass over the page: the window numbers each group's members by user id and keeps the first perGroup; the
	// join to user_groups confines it to the groups of the provider.
	rows, err := s.db.QueryContext(ctx, `SELECT group_id, user_id, name, source, created_at FROM (
		SELECT m.group_id::text AS group_id, m.user_id::text AS user_id, u.name AS name, m.source AS source, m.created_at AS created_at,
			row_number() OVER (PARTITION BY m.group_id ORDER BY m.user_id) AS position
		FROM user_group_members m JOIN users u ON u.id=m.user_id JOIN user_groups g ON g.id=m.group_id JOIN user_identities i ON i.user_id=m.user_id AND i.provider_id=g.provider_id
		WHERE g.provider_id=$1::uuid AND g.source='scim' AND m.source='scim' AND `+scimVisible+` AND m.group_id IN (SELECT x::uuid FROM unnest($2::text[]) AS x)) ranked
		WHERE position <= $3 ORDER BY group_id, user_id`, providerID, groupIDs, max(perGroup, 1))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	list, err := scanSCIMMembers(rows)
	if err != nil {
		return nil, err
	}
	for _, member := range list {
		members[member.GroupID] = append(members[member.GroupID], member)
	}
	return members, nil
}

func scanSCIMMembers(rows *sql.Rows) ([]UserGroupMember, error) {
	items := []UserGroupMember{}
	for rows.Next() {
		var member UserGroupMember
		var source string
		if err := rows.Scan(&member.GroupID, &member.UserID, &member.UserName, &source, &member.CreatedAt); err != nil {
			return nil, err
		}
		member.Source = UserGroupSource(source)
		items = append(items, member)
	}
	return items, rows.Err()
}

func (s *PostgresStore) LinkProviderIdentity(ctx context.Context, link ProviderIdentityLink, audit ...AuditRecord) (UserIdentity, error) {
	link.Subject, link.Issuer = strings.TrimSpace(link.Subject), normalizeOIDCIssuer(link.Issuer)
	if link.Subject == "" || link.Issuer == "" || link.Kind != UserIdentityOIDC && link.Kind != UserIdentitySAML {
		return UserIdentity{}, ErrSCIMInvalid
	}
	if !isUUID(link.ProviderID) || !isUUID(link.UserID) {
		return UserIdentity{}, ErrNotFound
	}
	return runTransactional(ctx, s, audit, func(q sqlRunner) (UserIdentity, error) {
		var exists bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM identity_providers WHERE id=$1::uuid) AND EXISTS (SELECT 1 FROM users WHERE id=$2::uuid)`, link.ProviderID, link.UserID).Scan(&exists); err != nil {
			return UserIdentity{}, err
		}
		if !exists {
			return UserIdentity{}, ErrNotFound
		}
		var identity UserIdentity
		err := scanUserIdentity(q.QueryRowContext(ctx, `SELECT `+userIdentityColumns+` FROM user_identities WHERE provider_id=$1::uuid AND user_id=$2::uuid FOR UPDATE`, link.ProviderID, link.UserID), &identity)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			err = scanUserIdentity(q.QueryRowContext(ctx, `INSERT INTO user_identities (id,user_id,kind,provider_id,issuer,subject) VALUES ($1::uuid,$2::uuid,$3,$4::uuid,$5,$6) RETURNING `+userIdentityColumns,
				ids.New(), link.UserID, link.Kind, link.ProviderID, link.Issuer, link.Subject), &identity)
		case err != nil:
			return UserIdentity{}, err
		case identity.Subject != "" && identity.Subject != link.Subject:
			return UserIdentity{}, ErrIdentityExists
		default:
			err = scanUserIdentity(q.QueryRowContext(ctx, `UPDATE user_identities SET subject=$2, issuer=$3, updated_at=now() WHERE id=$1::uuid RETURNING `+userIdentityColumns, identity.ID, link.Subject, link.Issuer), &identity)
		}
		if isUnique(err) {
			return UserIdentity{}, ErrIdentityExists
		}
		return identity, err
	})
}
