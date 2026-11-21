package repository

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// SCIM provisioning (ADR 0018 section 4). The SCIM server authenticates with a per-provider bearer token and acts
// only on the accounts linked to that provider (a user_identities row of the provider) and on the groups of source
// scim that carry its provider_id.

// MaxSCIMTokenLifetime caps the expiry of a SCIM token (ADR 0018 section 4): Entra ID and Okta tokens tend to be
// long-lived, so the lifetime is bounded and the console warns before a token expires.
const MaxSCIMTokenLifetime = 365 * 24 * time.Hour

// MaxSCIMTokenNameLength bounds the label of a token.
const MaxSCIMTokenNameLength = 128

// SCIMGeneratedExternalIDPrefix marks the scim_external_id the store generates for an account the IdP created
// without an externalId (Okta does not send one): the identity row needs one key, and the account id is stable and
// unique. A generated value is never shown as the externalId of the resource and never matches a login attribute.
const SCIMGeneratedExternalIDPrefix = "notarx-scim-user:"

// SCIMDeletedExternalIDPrefix marks the scim_external_id of an identity whose SCIM link was removed by DELETE /Users:
// the row stays (it may carry the login subject and the account is only disabled), but SCIM no longer sees the
// account, and only the same provider can bring it back by creating the user again.
const SCIMDeletedExternalIDPrefix = "notarx-scim-deleted:"

// SCIMMaxPageSize bounds one page the store reads; the server clamps count to NOTARX_SCIM_MAX_PAGE first.
const SCIMMaxPageSize = 1000

// SCIMToken is one bearer credential of the SCIM server. SecretHash is never returned by a management response.
type SCIMToken struct {
	ID         string
	ProviderID string
	Name       string
	SecretHash string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

// Active reports whether the token still authenticates at now.
func (t SCIMToken) Active(now time.Time) bool {
	return t.RevokedAt == nil && now.Before(t.ExpiresAt)
}

// Errors of the SCIM stores.
var (
	// ErrSCIMTokenInvalid reports a token without a usable name or with an expiry that is missing, past or beyond
	// MaxSCIMTokenLifetime.
	ErrSCIMTokenInvalid = errors.New("SCIM token is invalid")
	// ErrSCIMAdminProtected refuses a SCIM change to a platform administrator: an IdP compromise must not be able to
	// edit or lock out administrators.
	ErrSCIMAdminProtected = errors.New("SCIM cannot modify a platform administrator")
	// ErrSCIMAdminDisabled refuses to re-activate an account that an administrator disabled.
	ErrSCIMAdminDisabled = errors.New("the account was disabled by an administrator")
	// ErrSCIMInvalid reports a SCIM change the stores cannot apply (for example a member that is not a user of the
	// provider).
	ErrSCIMInvalid = errors.New("SCIM change is invalid")
)

// NormalizeSCIMToken trims and validates a token before it is stored: a name of 1 to 128 characters and a mandatory
// expiry in the future and at most MaxSCIMTokenLifetime ahead.
func NormalizeSCIMToken(token SCIMToken, now time.Time) (SCIMToken, error) {
	token.Name = strings.TrimSpace(token.Name)
	if length := utf8.RuneCountInString(token.Name); length < 1 || length > MaxSCIMTokenNameLength || token.SecretHash == "" || token.ProviderID == "" {
		return SCIMToken{}, ErrSCIMTokenInvalid
	}
	if token.ExpiresAt.IsZero() || !token.ExpiresAt.After(now) || token.ExpiresAt.After(now.Add(MaxSCIMTokenLifetime)) {
		return SCIMToken{}, ErrSCIMTokenInvalid
	}
	token.ExpiresAt = token.ExpiresAt.UTC()
	return token, nil
}

// SCIMTokenStore persists the SCIM bearer tokens. Mutations take the audit records to write in the same
// transaction (ADR 0023 section 1).
type SCIMTokenStore interface {
	// CreateSCIMToken stores a token for an existing provider (ErrNotFound otherwise) after NormalizeSCIMToken.
	CreateSCIMToken(ctx context.Context, token SCIMToken, audit ...AuditRecord) (SCIMToken, error)
	// ListSCIMTokens returns the tokens of one provider, oldest first, at most SCIMMaxPageSize.
	ListSCIMTokens(ctx context.Context, providerID string) ([]SCIMToken, error)
	// RevokeSCIMToken revokes a token of the provider; revoking a revoked token keeps the first revocation time.
	RevokeSCIMToken(ctx context.Context, providerID, tokenID string, audit ...AuditRecord) (SCIMToken, error)
	// FindActiveSCIMTokenByHash finds the token whose secret hash matches, unrevoked and unexpired (ErrNotFound
	// otherwise). The hash is a unique key, so a miss and a revoked or expired token cost the same lookup.
	FindActiveSCIMTokenByHash(ctx context.Context, hash string) (SCIMToken, error)
}

// SCIMUser is one account as the SCIM server of a provider sees it: the account and the provider's identity row.
type SCIMUser struct {
	User     User
	Identity UserIdentity
}

// ExternalID is the externalId the IdP set, empty when the store generated the identity key.
func (u SCIMUser) ExternalID() string {
	if strings.HasPrefix(u.Identity.SCIMExternalID, SCIMGeneratedExternalIDPrefix) || scimLinkRemoved(u.Identity.SCIMExternalID) {
		return ""
	}
	return u.Identity.SCIMExternalID
}

// scimLinkRemoved reports the tombstone a DELETE leaves in scim_external_id.
func scimLinkRemoved(externalID string) bool {
	return strings.HasPrefix(externalID, SCIMDeletedExternalIDPrefix)
}

// scimVersionAllowed reports whether the current version satisfies an If-Match list; an empty list is unconditional.
func scimVersionAllowed(current string, ifVersions []string) bool {
	return len(ifVersions) == 0 || slices.Contains(ifVersions, current)
}

// SCIMUserCreate is a SCIM create. The account gets level none; Active false creates it disabled by SCIM.
//
// A user the provider deleted earlier (its link carries the tombstone) is brought back by a create with the same
// userName: the account must still be unprivileged and without a password, and it is re-activated or left disabled
// as the request says.
type SCIMUserCreate struct {
	ProviderID  string
	UserName    string
	DisplayName string
	Email       string
	ExternalID  string
	Active      bool
}

// SCIMUserChange is a SCIM replace or patch: a nil field is left alone.
type SCIMUserChange struct {
	UserName    *string
	DisplayName *string
	Email       *string
	ExternalID  *string
	Active      *bool
	// IfVersions are the versions the If-Match header allows; the change is ErrVersionConflict unless the
	// account's version is one of them, checked under the row lock. Empty means unconditional.
	IfVersions []string
}

// SCIMUserQuery lists the users of one provider by keyset on (created_at, id) of their link. UserName and
// ExternalID are the two eq filters the server supports.
type SCIMUserQuery struct {
	ProviderID string
	UserName   string
	ExternalID string
	Limit      int
	Cursor     string
	// SkipTotal leaves the count out of the page (Total is zero), for the pages that are only read to find a cursor.
	SkipTotal bool
}

// filterKey binds a cursor to the query that produced it.
func (q SCIMUserQuery) filterKey() string {
	return "scim-users\x00" + q.ProviderID + "\x00" + strings.ToLower(q.UserName) + "\x00" + q.ExternalID
}

// SCIMUserPage is one page of a SCIM user list. NextCursor is empty at the end.
type SCIMUserPage struct {
	Items      []SCIMUser
	NextCursor string
	Total      ListTotal
}

// SCIMGroupCreate is a SCIM group create: always source scim and global role none.
type SCIMGroupCreate struct {
	ProviderID  string
	ExternalID  string
	DisplayName string
	MemberIDs   []string
}

// SCIMGroupChange is a SCIM replace or patch of a group. Members are user ids of the provider: ReplaceMembers
// makes the set of SCIM-sourced memberships equal to the list, Add and Remove change it by the difference.
type SCIMGroupChange struct {
	DisplayName    *string
	ExternalID     *string
	AddMembers     []string
	RemoveMembers  []string
	ReplaceMembers *[]string
	// IfVersions are the versions the If-Match header allows (see SCIMUserChange.IfVersions).
	IfVersions []string
}

// SCIMGroupQuery lists the SCIM groups of one provider by keyset on (created_at, id).
type SCIMGroupQuery struct {
	ProviderID  string
	DisplayName string
	ExternalID  string
	Limit       int
	Cursor      string
	SkipTotal   bool
}

func (q SCIMGroupQuery) filterKey() string {
	return "scim-groups\x00" + q.ProviderID + "\x00" + q.DisplayName + "\x00" + q.ExternalID
}

// SCIMGroupPage is one page of a SCIM group list.
type SCIMGroupPage struct {
	Items      []UserGroup
	NextCursor string
	Total      ListTotal
}

// SCIMStore is the provisioning port of the SCIM server. Every method takes the provider id of the token and only
// sees what the provider owns. A group is only ever created with source scim and global role none and is never
// given another role by this port (SetUserGroupGlobalRole is the administrator's flag).
type SCIMStore interface {
	CreateSCIMUser(ctx context.Context, in SCIMUserCreate, audit ...AuditRecord) (SCIMUser, error)
	GetSCIMUser(ctx context.Context, providerID, userID string) (SCIMUser, error)
	ListSCIMUsers(ctx context.Context, query SCIMUserQuery) (SCIMUserPage, error)
	// UpdateSCIMUser applies the change atomically: an account that is an administrator is refused with
	// ErrSCIMAdminProtected; active=false disables the account (reason scim), bumps session_version and revokes
	// its sessions in the same transaction; active=true on an account an administrator disabled is
	// ErrSCIMAdminDisabled.
	UpdateSCIMUser(ctx context.Context, providerID, userID string, change SCIMUserChange, audit ...AuditRecord) (SCIMUser, error)
	// DeleteSCIMUser removes the SCIM link (the identity row keeps a tombstone, so SCIM no longer sees the account)
	// and disables the account; it never deletes the account. ifVersions is the If-Match list (empty: unconditional).
	DeleteSCIMUser(ctx context.Context, providerID, userID string, ifVersions []string, audit ...AuditRecord) error

	CreateSCIMGroup(ctx context.Context, in SCIMGroupCreate, audit ...AuditRecord) (UserGroup, error)
	GetSCIMGroup(ctx context.Context, providerID, groupID string) (UserGroup, error)
	ListSCIMGroups(ctx context.Context, query SCIMGroupQuery) (SCIMGroupPage, error)
	UpdateSCIMGroup(ctx context.Context, providerID, groupID string, change SCIMGroupChange, audit ...AuditRecord) (UserGroup, error)
	// DeleteSCIMGroup deletes a group of the provider with its memberships and grants; a group recreated later
	// starts again at global role none.
	DeleteSCIMGroup(ctx context.Context, providerID, groupID string, ifVersions []string, audit ...AuditRecord) error
	// ListSCIMGroupMembers returns at most limit members of a group of the provider, ordered by user id, in one query.
	ListSCIMGroupMembers(ctx context.Context, providerID, groupID string, limit int) ([]UserGroupMember, error)
	// ListSCIMGroupsMembers returns at most perGroup members of each of the named groups of the provider, in one
	// query; a group with no members has no entry.
	ListSCIMGroupsMembers(ctx context.Context, providerID string, groupIDs []string, perGroup int) (map[string][]UserGroupMember, error)
}

// UserIdentityLinker binds a login subject to the account an administrator names (ADR 0018 section 4, the admin
// link flow for a provider that does not assert externalId).
type UserIdentityLinker interface {
	// LinkProviderIdentity sets the subject of the account's identity at the provider: it binds a SCIM-created row
	// whose subject is empty and creates the row when the account has none. A subject already taken by another
	// account, or a row bound to another subject, is ErrIdentityExists; an unknown account or provider is ErrNotFound.
	LinkProviderIdentity(ctx context.Context, link ProviderIdentityLink, audit ...AuditRecord) (UserIdentity, error)
}

// ProviderIdentityLink is one admin link request.
type ProviderIdentityLink struct {
	ProviderID string
	UserID     string
	Subject    string
	// Issuer is the informational issuer text of the identity.
	Issuer string
	// Kind is the provider's kind (oidc or saml).
	Kind string
}

// NormalizeSCIMName trims a SCIM userName or displayName and bounds it.
func NormalizeSCIMName(value string, limit int) (string, bool) {
	value = strings.TrimSpace(value)
	return value, len(value) >= 1 && len(value) <= limit
}
