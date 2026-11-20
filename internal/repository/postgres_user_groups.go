package repository

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mlops-ca/notarx/internal/ids"
)

const userGroupColumns = `user_groups.id::text,COALESCE(user_groups.provider_id::text,''),user_groups.external_id,user_groups.display_name,user_groups.source,user_groups.global_role,user_groups.version::text,user_groups.created_at,user_groups.updated_at`

const nilUUID = "00000000-0000-0000-0000-000000000000"

func scanUserGroup(scanner interface{ Scan(...any) error }) (UserGroup, error) {
	var group UserGroup
	var source string
	err := scanner.Scan(&group.ID, &group.ProviderID, &group.ExternalID, &group.DisplayName, &source, &group.GlobalRole, &group.Version, &group.CreatedAt, &group.UpdatedAt)
	group.Source = UserGroupSource(source)
	return group, err
}

// userGroupUniqueError names which unique index a violation hit.
func userGroupUniqueError(err error) error {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "23505" {
		return err
	}
	if postgresError.ConstraintName == "user_groups_provider_external_id_idx" {
		return ErrUserGroupExternalID
	}
	return ErrUserGroupNameExists
}

func (s *PostgresStore) CreateUserGroup(ctx context.Context, group UserGroup, audit ...AuditRecord) (UserGroup, error) {
	group, err := NormalizeUserGroup(group)
	if err != nil {
		return UserGroup{}, err
	}
	if group.ID == "" {
		group.ID = ids.New()
	}
	created, err := runAudited(ctx, s, audit, func(q sqlRunner) (UserGroup, error) {
		created, err := scanUserGroup(q.QueryRowContext(ctx, `INSERT INTO user_groups (id,provider_id,external_id,display_name,source,global_role) VALUES ($1::uuid,NULLIF($2,'')::uuid,$3,$4,$5,$6) RETURNING `+userGroupColumns,
			group.ID, group.ProviderID, group.ExternalID, group.DisplayName, string(group.Source), group.GlobalRole))
		if isForeignKeyViolation(err) {
			// The only reference is the provider.
			return UserGroup{}, ErrUserGroupProviderNotFound
		}
		return created, userGroupUniqueError(err)
	})
	if err == nil {
		s.invalidateLocalScope(CacheScopeUserGroup)
	}
	return created, err
}

func (s *PostgresStore) ListUserGroups(ctx context.Context, limit int, cursor string) ([]UserGroup, string, error) {
	limit = ClampUserGroupPageSize(limit)
	after, afterID := time.Unix(0, 0).UTC(), nilUUID
	if cursor != "" {
		at, id, ok := decodeUserGroupCursor(cursor)
		if !ok {
			return nil, "", ErrUserGroupInvalid
		}
		after, afterID = at, id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+userGroupColumns+` FROM user_groups WHERE (user_groups.created_at, user_groups.id) > ($1::timestamptz, $2::uuid) ORDER BY user_groups.created_at, user_groups.id LIMIT $3`, after, afterID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	items := make([]UserGroup, 0, limit)
	for rows.Next() {
		group, err := scanUserGroup(rows)
		if err != nil {
			return nil, "", err
		}
		items = append(items, group)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = encodeUserGroupCursor(items[limit-1].CreatedAt, items[limit-1].ID)
	}
	return items, next, nil
}

func (s *PostgresStore) GetUserGroup(ctx context.Context, id string) (UserGroup, error) {
	return getUserGroupOn(ctx, s.db, id)
}

func getUserGroupOn(ctx context.Context, q sqlRunner, id string) (UserGroup, error) {
	if !isUUID(id) {
		return UserGroup{}, ErrNotFound
	}
	group, err := scanUserGroup(q.QueryRowContext(ctx, `SELECT `+userGroupColumns+` FROM user_groups WHERE user_groups.id=$1::uuid`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return UserGroup{}, ErrNotFound
	}
	return group, err
}

// groupVersionUpdate runs an UPDATE that is guarded by the expected version and tells a missing row from a stale one.
func groupVersionUpdate(ctx context.Context, q sqlRunner, id, expectedVersion, query string, args ...any) (UserGroup, error) {
	version, parseErr := strconv.ParseInt(expectedVersion, 10, 64)
	if parseErr != nil {
		if _, err := getUserGroupOn(ctx, q, id); err != nil {
			return UserGroup{}, err
		}
		return UserGroup{}, ErrVersionConflict
	}
	args = append(args, id, version)
	updated, err := scanUserGroup(q.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		if _, getErr := getUserGroupOn(ctx, q, id); getErr != nil {
			return UserGroup{}, getErr
		}
		return UserGroup{}, ErrVersionConflict
	}
	return updated, userGroupUniqueError(err)
}

func (s *PostgresStore) RenameUserGroup(ctx context.Context, id, displayName, expectedVersion string, audit ...AuditRecord) (UserGroup, error) {
	updated, err := runAudited(ctx, s, audit, func(q sqlRunner) (UserGroup, error) {
		name, err := NormalizeUserGroupName(displayName)
		if err != nil {
			return UserGroup{}, err
		}
		return groupVersionUpdate(ctx, q, id, expectedVersion,
			`UPDATE user_groups SET display_name=$1,version=version+1,updated_at=now() WHERE id=$2::uuid AND version=$3::bigint RETURNING `+userGroupColumns, name)
	})
	if err == nil {
		s.invalidateLocalScope(CacheScopeUserGroup)
	}
	return updated, err
}

func (s *PostgresStore) SetUserGroupGlobalRole(ctx context.Context, id, role, expectedVersion string, audit ...AuditRecord) (UserGroup, error) {
	updated, err := runAudited(ctx, s, audit, func(q sqlRunner) (UserGroup, error) {
		current, err := getUserGroupOn(ctx, q, id)
		if err != nil {
			return UserGroup{}, err
		}
		if !UserGroupRoleChangeAllowed(current.Source, role) {
			return UserGroup{}, ErrUserGroupInvalid
		}
		return groupVersionUpdate(ctx, q, id, expectedVersion,
			`UPDATE user_groups SET global_role=$1,version=version+1,updated_at=now() WHERE id=$2::uuid AND version=$3::bigint RETURNING `+userGroupColumns, role)
	})
	if err == nil {
		s.invalidateLocalScope(CacheScopeUserGroup)
	}
	return updated, err
}

// DeleteUserGroup deletes the group with its memberships and the repository grants that name group:<id>, so no
// grant is left naming a principal that can never match again; the grant sets of the affected repositories are
// versioned like any grant change.
func (s *PostgresStore) DeleteUserGroup(ctx context.Context, id string, audit ...AuditRecord) error {
	repositoryIDs, err := runAudited(ctx, s, audit, func(q sqlRunner) ([]string, error) {
		if !isUUID(id) {
			return nil, ErrNotFound
		}
		result, err := q.ExecContext(ctx, `DELETE FROM user_groups WHERE id=$1::uuid`, id)
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return nil, ErrNotFound
		}
		rows, err := q.QueryContext(ctx, `WITH removed AS (DELETE FROM repository_grants WHERE principal=$1 RETURNING repository_id),
touched AS (SELECT DISTINCT repository_id FROM removed),
sets AS (UPDATE repository_grant_sets s SET version=s.version+1 FROM touched t WHERE s.repository_id=t.repository_id RETURNING s.repository_id)
UPDATE hosted_repositories r SET grants_version=r.grants_version+1 FROM touched t WHERE r.id=t.repository_id RETURNING r.id::text`, groupPrincipal(id))
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		var touched []string
		for rows.Next() {
			var repositoryID string
			if err := rows.Scan(&repositoryID); err != nil {
				return nil, err
			}
			touched = append(touched, repositoryID)
		}
		return touched, rows.Err()
	})
	if err == nil {
		s.invalidateLocalScope(CacheScopeUserGroup)
		for _, repositoryID := range repositoryIDs {
			s.invalidateGrants(repositoryID)
		}
	}
	return err
}

func (s *PostgresStore) AddUserGroupMember(ctx context.Context, groupID, userID string, source UserGroupSource, audit ...AuditRecord) error {
	if source != UserGroupSourceLocal && source != UserGroupSourceSCIM && source != UserGroupSourceClaims {
		return ErrUserGroupInvalid
	}
	_, err := runAudited(ctx, s, audit, func(q sqlRunner) (struct{}, error) {
		if !isUUID(groupID) || !isUUID(userID) {
			return struct{}{}, ErrNotFound
		}
		_, err := q.ExecContext(ctx, `INSERT INTO user_group_members (group_id,user_id,source) VALUES ($1::uuid,$2::uuid,$3)`, groupID, userID, string(source))
		switch {
		case isUnique(err):
			return struct{}{}, ErrUserGroupMemberExists
		case isForeignKeyViolation(err):
			return struct{}{}, ErrNotFound
		}
		return struct{}{}, err
	})
	if err == nil {
		s.invalidateLocal(CacheScopeUserGroup, userID)
	}
	return err
}

func (s *PostgresStore) RemoveUserGroupMember(ctx context.Context, groupID, userID string, audit ...AuditRecord) error {
	_, err := runAudited(ctx, s, audit, func(q sqlRunner) (struct{}, error) {
		if !isUUID(groupID) || !isUUID(userID) {
			return struct{}{}, ErrNotFound
		}
		result, err := q.ExecContext(ctx, `DELETE FROM user_group_members WHERE group_id=$1::uuid AND user_id=$2::uuid`, groupID, userID)
		if err != nil {
			return struct{}{}, err
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return struct{}{}, ErrNotFound
		}
		return struct{}{}, nil
	})
	if err == nil {
		s.invalidateLocal(CacheScopeUserGroup, userID)
	}
	return err
}

func (s *PostgresStore) ListUserGroupMembers(ctx context.Context, groupID string, limit int, cursor string) ([]UserGroupMember, string, error) {
	limit = ClampUserGroupPageSize(limit)
	if _, err := s.GetUserGroup(ctx, groupID); err != nil {
		return nil, "", err
	}
	after := nilUUID
	if cursor != "" {
		if !isUUID(cursor) {
			return nil, "", ErrUserGroupInvalid
		}
		after = cursor
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.group_id::text,m.user_id::text,u.name,m.source,m.created_at FROM user_group_members m JOIN users u ON u.id=m.user_id WHERE m.group_id=$1::uuid AND m.user_id>$2::uuid ORDER BY m.user_id LIMIT $3`, groupID, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	items := make([]UserGroupMember, 0, limit)
	for rows.Next() {
		var member UserGroupMember
		var source string
		if err := rows.Scan(&member.GroupID, &member.UserID, &member.UserName, &source, &member.CreatedAt); err != nil {
			return nil, "", err
		}
		member.Source = UserGroupSource(source)
		items = append(items, member)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = items[limit-1].UserID
	}
	return items, next, nil
}

// userGroupMembershipsQuery is the per-request lookup of ADR 0018 Decision 5. It is served by
// user_group_members_user_idx (user_id, group_id) and bounded by MaxUserGroupMemberships, so an account in
// more groups loses authority at the bound and never gains it.
const userGroupMembershipsQuery = `SELECT m.group_id::text,g.source,g.global_role,m.source
FROM user_group_members m JOIN user_groups g ON g.id=m.group_id
WHERE m.user_id=$1::uuid
ORDER BY m.group_id
LIMIT $2`

func (s *PostgresStore) ListUserGroupMemberships(ctx context.Context, userID string) ([]UserGroupMembership, error) {
	if !isUUID(userID) {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, userGroupMembershipsQuery, userID, MaxUserGroupMemberships)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]UserGroupMembership, 0, 8)
	for rows.Next() {
		var item UserGroupMembership
		var groupSource, memberSource string
		if err := rows.Scan(&item.GroupID, &groupSource, &item.GlobalRole, &memberSource); err != nil {
			return nil, err
		}
		item.GroupSource, item.MemberSource = UserGroupSource(groupSource), UserGroupSource(memberSource)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) SyncClaimsUserGroups(ctx context.Context, sync ClaimsGroupSync, audit ...AuditRecord) (int, error) {
	if !isUUID(sync.ProviderID) || !isUUID(sync.UserID) || len(sync.ExternalIDs) > MaxClaimsGroups {
		return 0, ErrUserGroupInvalid
	}
	external := make([]string, 0, len(sync.ExternalIDs))
	for _, value := range sync.ExternalIDs {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			external = append(external, trimmed)
		}
	}
	matched, err := runTransactional(ctx, s, audit, func(q sqlRunner) (int, error) {
		// Drop the claims-sourced memberships of this provider that the login no longer asserts, then add the
		// ones it does. Only source claims and role none ever match, so a claim never reaches a privileged group.
		if _, err := q.ExecContext(ctx, `DELETE FROM user_group_members m USING user_groups g
WHERE m.group_id=g.id AND m.user_id=$1::uuid AND m.source='claims' AND g.source='claims' AND g.provider_id=$2::uuid
  AND NOT (g.global_role='none' AND g.external_id = ANY($3))`, sync.UserID, sync.ProviderID, external); err != nil {
			return 0, err
		}
		rows, err := q.QueryContext(ctx, `SELECT g.id::text FROM user_groups g
WHERE g.source='claims' AND g.provider_id=$1::uuid AND g.global_role='none' AND g.external_id = ANY($2)`, sync.ProviderID, external)
		if err != nil {
			return 0, err
		}
		var groupIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return 0, err
			}
			groupIDs = append(groupIDs, id)
		}
		if err := rows.Close(); err != nil {
			return 0, err
		}
		if len(groupIDs) > 0 {
			if _, err := q.ExecContext(ctx, `INSERT INTO user_group_members (group_id,user_id,source) SELECT g.id,$1::uuid,'claims' FROM user_groups g WHERE g.id = ANY($2::text[]::uuid[]) ON CONFLICT (group_id,user_id) DO NOTHING`, sync.UserID, groupIDs); err != nil {
				return 0, err
			}
		}
		return len(groupIDs), nil
	})
	if isForeignKeyViolation(err) {
		return 0, ErrNotFound
	}
	if err == nil {
		s.invalidateLocal(CacheScopeUserGroup, sync.UserID)
	}
	return matched, err
}

// groupPrincipal is the grant principal of a user group (authorization.GroupPrincipal; the repository package cannot import it).
func groupPrincipal(groupID string) string { return "group:" + groupID }

func isUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}
