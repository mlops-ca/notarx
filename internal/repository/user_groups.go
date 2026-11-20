package repository

import (
	"context"
	"errors"
	"strings"
	"time"
)

// User groups (ADR 0018 Decision 5). A user group is a persisted set of accounts that a repository grant can
// name as the principal group:<id>. The name is user_groups, not groups, because a Group in this codebase is a
// repository group (ADR 0012).

// UserGroupSource says where a group, or one membership of it, comes from.
type UserGroupSource string

const (
	// UserGroupSourceLocal is a group an administrator manages in notarx.
	UserGroupSourceLocal UserGroupSource = "local"
	// UserGroupSourceSCIM is a group an identity provider provisions through SCIM.
	UserGroupSourceSCIM UserGroupSource = "scim"
	// UserGroupSourceClaims is a group whose membership follows the groups claim of a login. It is matched by
	// the provider's external id, never created from a claim.
	UserGroupSourceClaims UserGroupSource = "claims"
)

// Global roles a group may carry. They never grant repository access by themselves; the effective level of an
// account is the maximum of its own role and the global role of its groups.
const (
	UserGroupRoleNone   = "none"
	UserGroupRoleMember = "member"
	UserGroupRoleAdmin  = "admin"
)

// MaxUserGroupMemberships bounds the groups one request resolves for an account (ADR 0018 Decision 5). The
// list is cut at the bound in a stable order, so an account in more groups loses authority, never gains it.
const MaxUserGroupMemberships = 512

// MaxClaimsGroups bounds the group values one login may assert; a longer list is treated as no groups.
const MaxClaimsGroups = MaxUserGroupMemberships

// UserGroup is one group. ProviderID is empty for a local group. Version is the optimistic-concurrency token.
type UserGroup struct {
	ID          string
	ProviderID  string
	ExternalID  string
	DisplayName string
	Source      UserGroupSource
	GlobalRole  string
	Version     string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// UserGroupMember is one membership row.
type UserGroupMember struct {
	GroupID   string
	UserID    string
	UserName  string
	Source    UserGroupSource
	CreatedAt time.Time
}

// UserGroupMembership is the group of one account as the authentication path reads it: the group id, the
// group's source and global role, and the source of the membership.
type UserGroupMembership struct {
	GroupID      string
	GroupSource  UserGroupSource
	GlobalRole   string
	MemberSource UserGroupSource
}

// ClaimsGroupSync is one login's claims-sourced group population.
type ClaimsGroupSync struct {
	ProviderID string
	UserID     string
	// ExternalIDs are the group values the provider asserted, already limited to MaxClaimsGroups.
	ExternalIDs []string
}

// Errors of the user group store.
var (
	ErrUserGroupNameExists   = errors.New("user group name already exists")
	ErrUserGroupExternalID   = errors.New("user group external id already exists for this provider")
	ErrUserGroupInvalid      = errors.New("user group is invalid")
	ErrUserGroupMemberExists = errors.New("user is already a member of the group")
	// ErrUserGroupProviderNotFound reports a provisioned or claims-sourced group of an identity provider that does not exist.
	ErrUserGroupProviderNotFound = errors.New("identity provider of the user group not found")
)

// UserGroupStore persists user groups and their memberships. Every mutation takes the audit records to write in
// the same transaction (ADR 0023 section 1).
type UserGroupStore interface {
	CreateUserGroup(ctx context.Context, group UserGroup, audit ...AuditRecord) (UserGroup, error)
	// ListUserGroups pages by keyset on (created_at, id); the cursor is opaque and empty at the end.
	ListUserGroups(ctx context.Context, limit int, cursor string) ([]UserGroup, string, error)
	GetUserGroup(ctx context.Context, id string) (UserGroup, error)
	// RenameUserGroup changes only the display name. SCIM uses it, so it can never reach the global role.
	RenameUserGroup(ctx context.Context, id, displayName, expectedVersion string, audit ...AuditRecord) (UserGroup, error)
	// SetUserGroupGlobalRole is the administrator's flag. A claims-sourced group only ever has role none.
	SetUserGroupGlobalRole(ctx context.Context, id, role, expectedVersion string, audit ...AuditRecord) (UserGroup, error)
	DeleteUserGroup(ctx context.Context, id string, audit ...AuditRecord) error
	AddUserGroupMember(ctx context.Context, groupID, userID string, source UserGroupSource, audit ...AuditRecord) error
	RemoveUserGroupMember(ctx context.Context, groupID, userID string, audit ...AuditRecord) error
	// ListUserGroupMembers pages by keyset on user id; the cursor is opaque and empty at the end.
	ListUserGroupMembers(ctx context.Context, groupID string, limit int, cursor string) ([]UserGroupMember, string, error)
	// ListUserGroupMemberships returns at most MaxUserGroupMemberships groups of an account, one indexed query.
	ListUserGroupMemberships(ctx context.Context, userID string) ([]UserGroupMembership, error)
	// SyncClaimsUserGroups replaces the claims-sourced memberships of an account at one provider with the
	// groups whose external id the login asserted. Only source claims, provider-bound groups of role none match.
	SyncClaimsUserGroups(ctx context.Context, sync ClaimsGroupSync, audit ...AuditRecord) (int, error)
}

// ValidUserGroupRole reports whether role is a global role a group may carry.
func ValidUserGroupRole(role string) bool {
	switch role {
	case UserGroupRoleNone, UserGroupRoleMember, UserGroupRoleAdmin:
		return true
	}
	return false
}

// ClampUserGroupPageSize applies the page size of the contract: 50 by default, at most 200.
func ClampUserGroupPageSize(limit int) int {
	switch {
	case limit <= 0:
		return 50
	case limit > 200:
		return 200
	}
	return limit
}

// NormalizeUserGroupName trims and bounds a display name.
func NormalizeUserGroupName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 255 {
		return "", ErrUserGroupInvalid
	}
	return name, nil
}

// NormalizeUserGroup trims and validates a group before it is stored. A SCIM group can never be created with
// the admin role and a claims group is only ever role none (ADR 0018 Decision 5): the flag is an administrator
// action on an existing group, and a delete followed by a recreate starts again at none.
func NormalizeUserGroup(group UserGroup) (UserGroup, error) {
	name, err := NormalizeUserGroupName(group.DisplayName)
	if err != nil {
		return UserGroup{}, err
	}
	group.DisplayName = name
	group.ExternalID = strings.TrimSpace(group.ExternalID)
	if group.GlobalRole == "" {
		group.GlobalRole = UserGroupRoleNone
	}
	if len(group.ExternalID) > 255 || !ValidUserGroupRole(group.GlobalRole) {
		return UserGroup{}, ErrUserGroupInvalid
	}
	switch group.Source {
	case UserGroupSourceLocal:
		if group.ProviderID != "" || group.ExternalID != "" {
			return UserGroup{}, ErrUserGroupInvalid
		}
	case UserGroupSourceSCIM:
		if group.ProviderID == "" || group.GlobalRole != UserGroupRoleNone {
			return UserGroup{}, ErrUserGroupInvalid
		}
	case UserGroupSourceClaims:
		if group.ProviderID == "" || group.ExternalID == "" || group.GlobalRole != UserGroupRoleNone {
			return UserGroup{}, ErrUserGroupInvalid
		}
	default:
		return UserGroup{}, ErrUserGroupInvalid
	}
	return group, nil
}

// UserGroupRoleChangeAllowed reports whether a group of a source may take role.
func UserGroupRoleChangeAllowed(source UserGroupSource, role string) bool {
	if !ValidUserGroupRole(role) {
		return false
	}
	return source != UserGroupSourceClaims || role == UserGroupRoleNone
}

// EffectiveGroupRole is the global role a membership contributes. A claims-sourced group never contributes one,
// whatever its row says, so an IdP-asserted string cannot raise the level of an account.
func (m UserGroupMembership) EffectiveGroupRole() string {
	if m.GroupSource == UserGroupSourceClaims {
		return UserGroupRoleNone
	}
	return m.GlobalRole
}
