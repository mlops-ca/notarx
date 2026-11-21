package repository

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

// The in-memory twins of identity_provider_scim_tokens and of the SCIM provisioning queries. They enforce the same
// rules as the PostgreSQL store, so the HTTP tests that run on the memory store prove the contract.

func (s *MemoryStore) CreateSCIMToken(ctx context.Context, token SCIMToken, audit ...AuditRecord) (SCIMToken, error) {
	now := time.Now().UTC()
	token, err := NormalizeSCIMToken(token, now)
	if err != nil {
		return SCIMToken{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.identityProviders[token.ProviderID]; !ok {
		return SCIMToken{}, ErrNotFound
	}
	if s.scimTokens == nil {
		s.scimTokens = map[string]SCIMToken{}
	}
	for _, existing := range s.scimTokens {
		if existing.SecretHash == token.SecretHash {
			return SCIMToken{}, ErrSCIMTokenInvalid
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return SCIMToken{}, err
	}
	if token.ID == "" {
		token.ID = ids.New()
	}
	token.CreatedAt, token.RevokedAt = now, nil
	s.scimTokens[token.ID] = token
	return token, nil
}

func (s *MemoryStore) ListSCIMTokens(_ context.Context, providerID string) ([]SCIMToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]SCIMToken, 0)
	for _, token := range s.scimTokens {
		if token.ProviderID == providerID {
			items = append(items, token)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if len(items) > SCIMMaxPageSize {
		items = items[:SCIMMaxPageSize]
	}
	return items, nil
}

func (s *MemoryStore) RevokeSCIMToken(ctx context.Context, providerID, tokenID string, audit ...AuditRecord) (SCIMToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.scimTokens[tokenID]
	if !ok || token.ProviderID != providerID {
		return SCIMToken{}, ErrNotFound
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return SCIMToken{}, err
	}
	if token.RevokedAt == nil {
		token.RevokedAt = timePointer(time.Now().UTC())
		s.scimTokens[tokenID] = token
	}
	return token, nil
}

func (s *MemoryStore) FindActiveSCIMTokenByHash(_ context.Context, hash string) (SCIMToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	for _, token := range s.scimTokens {
		if token.SecretHash == hash && token.Active(now) {
			return token, nil
		}
	}
	return SCIMToken{}, ErrNotFound
}

// scimIdentityLocked finds the identity row of an account at a provider, whether or not SCIM still sees it. The
// caller holds s.mu.
func (s *MemoryStore) scimIdentityLocked(providerID, userID string) (UserIdentity, bool) {
	for _, identity := range s.userIdentities {
		if identity.ProviderID == providerID && identity.UserID == userID {
			return identity, true
		}
	}
	return UserIdentity{}, false
}

// scimVisibleIdentityLocked is scimIdentityLocked for the accounts SCIM can see: a link a DELETE removed is hidden.
func (s *MemoryStore) scimVisibleIdentityLocked(providerID, userID string) (UserIdentity, bool) {
	identity, ok := s.scimIdentityLocked(providerID, userID)
	return identity, ok && scimOwned(identity)
}

// scimOwned reports whether SCIM provisioned the account of an identity row: it has a scim_external_id and the link
// was not removed. A just-in-time or administrator-linked account has none and is invisible to SCIM.
func scimOwned(identity UserIdentity) bool {
	return identity.SCIMExternalID != "" && !scimLinkRemoved(identity.SCIMExternalID)
}

func (s *MemoryStore) CreateSCIMUser(ctx context.Context, in SCIMUserCreate, audit ...AuditRecord) (SCIMUser, error) {
	provider, ok := s.providerForSCIM(in.ProviderID)
	if !ok {
		return SCIMUser{}, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.userIdentities {
		if in.ExternalID != "" && existing.ProviderID == in.ProviderID && existing.SCIMExternalID == in.ExternalID {
			return SCIMUser{}, ErrIdentityExists
		}
	}
	for _, existing := range s.users {
		if !strings.EqualFold(existing.Name, in.UserName) {
			continue
		}
		identity, linked := s.scimIdentityLocked(in.ProviderID, existing.ID)
		if !linked || scimOwned(identity) || !scimClaimable(existing) {
			return SCIMUser{}, ErrNameExists
		}
		return s.claimSCIMUserLocked(ctx, existing, identity, in, scimAuditTarget(audit, "scim/Users/"+existing.ID, map[string]string{"result": "taken_over"}))
	}
	userID := ids.New()
	if err := s.commitAuditLocked(ctx, scimAuditTarget(audit, "scim/Users/"+userID, map[string]string{"result": "created"})); err != nil {
		return SCIMUser{}, err
	}
	now := time.Now().UTC()
	user := User{
		ID: userID, Name: in.UserName, DisplayName: in.DisplayName, Email: in.Email, Role: "none", State: UserActive,
		SessionVersion: 1, CreatedAt: now, UpdatedAt: now, Version: "1",
	}
	if !in.Active {
		user.State, user.DisabledReason = UserDisabled, DisabledBySCIM
	}
	externalID := in.ExternalID
	if externalID == "" {
		externalID = SCIMGeneratedExternalIDPrefix + user.ID
	}
	identity := UserIdentity{
		ID: ids.New(), UserID: user.ID, Kind: string(provider.Kind), ProviderID: provider.ID, Issuer: "scim:" + provider.Slug,
		SCIMExternalID: externalID, Email: in.Email, DisplayName: in.DisplayName, CreatedAt: now, UpdatedAt: now,
	}
	s.users[user.ID] = user
	s.userIdentities[identity.ID] = identity
	return SCIMUser{User: user, Identity: identity}, nil
}

// claimSCIMUserLocked takes over an account the provider is linked to but SCIM does not own: one it deleted earlier or one
// a login or an administrator linked. The account keeps its name, takes the profile of the request and is active
// again only when the request says so; an active account the request deactivates loses its sessions at once.
func (s *MemoryStore) claimSCIMUserLocked(ctx context.Context, user User, identity UserIdentity, in SCIMUserCreate, audit []AuditRecord) (SCIMUser, error) {
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return SCIMUser{}, err
	}
	now := time.Now().UTC()
	externalID := in.ExternalID
	if externalID == "" {
		externalID = SCIMGeneratedExternalIDPrefix + user.ID
	}
	if !strings.EqualFold(in.Email, user.Email) {
		user = clearEmailVerification(user)
	}
	wasActive := user.State == UserActive
	user.DisplayName, user.Email = in.DisplayName, in.Email
	user.State, user.DisabledReason = UserActive, ""
	if !in.Active {
		user.State, user.DisabledReason = UserDisabled, DisabledBySCIM
		if wasActive {
			user.SessionVersion++
			s.revokeUserSessionRecordsLocked(user.ID, now)
		}
	}
	user.Version = nextHostedGroupVersion(user.Version)
	user.UpdatedAt = now
	identity.SCIMExternalID, identity.Email, identity.DisplayName, identity.UpdatedAt = externalID, in.Email, in.DisplayName, now
	s.users[user.ID] = user
	s.userIdentities[identity.ID] = identity
	return SCIMUser{User: user, Identity: identity}, nil
}

func (s *MemoryStore) GetSCIMUser(_ context.Context, providerID, userID string) (SCIMUser, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scimUserLocked(providerID, userID)
}

func (s *MemoryStore) scimUserLocked(providerID, userID string) (SCIMUser, error) {
	identity, ok := s.scimVisibleIdentityLocked(providerID, userID)
	user, userOK := s.users[userID]
	if !ok || !userOK {
		return SCIMUser{}, ErrNotFound
	}
	return SCIMUser{User: user, Identity: identity}, nil
}

func (s *MemoryStore) ListSCIMUsers(_ context.Context, query SCIMUserQuery) (SCIMUserPage, error) {
	afterAt, afterID, after, err := decodeSCIMCursor(query.Cursor, query.filterKey())
	if err != nil {
		return SCIMUserPage{}, err
	}
	limit := clampSCIMPage(query.Limit)
	s.mu.RLock()
	defer s.mu.RUnlock()
	matches := make([]SCIMUser, 0)
	for _, identity := range s.userIdentities {
		if identity.ProviderID != query.ProviderID {
			continue
		}
		user, ok := s.users[identity.UserID]
		if !ok || !scimOwned(identity) || query.UserName != "" && !strings.EqualFold(user.Name, query.UserName) || query.ExternalID != "" && identity.SCIMExternalID != query.ExternalID {
			continue
		}
		matches = append(matches, SCIMUser{User: user, Identity: identity})
	}
	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i].Identity, matches[j].Identity
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID < b.ID
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
	page := SCIMUserPage{}
	if !query.SkipTotal {
		page.Total = BoundedListTotal(int64(len(matches)))
	}
	if after {
		start := sort.Search(len(matches), func(i int) bool {
			identity := matches[i].Identity
			if identity.CreatedAt.Equal(afterAt) {
				return identity.ID > afterID
			}
			return identity.CreatedAt.After(afterAt)
		})
		matches = matches[start:]
	}
	if len(matches) > limit {
		matches = matches[:limit]
		last := matches[limit-1].Identity
		page.NextCursor = encodeSCIMCursor(query.filterKey(), last.CreatedAt, last.ID)
	}
	page.Items = matches
	return page, nil
}

func (s *MemoryStore) UpdateSCIMUser(ctx context.Context, providerID, userID string, change SCIMUserChange, audit ...AuditRecord) (SCIMUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.scimUserLocked(providerID, userID)
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
	if change.ExternalID != nil {
		if *change.ExternalID == "" && identity.Subject == "" {
			return SCIMUser{}, ErrSCIMInvalid
		}
		for _, existing := range s.userIdentities {
			if existing.ID != identity.ID && existing.ProviderID == providerID && *change.ExternalID != "" && existing.SCIMExternalID == *change.ExternalID {
				return SCIMUser{}, ErrIdentityExists
			}
		}
	}
	if change.Active != nil && *change.Active && user.State == UserDisabled && user.DisabledReason != DisabledBySCIM {
		return SCIMUser{}, ErrSCIMAdminDisabled
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return SCIMUser{}, err
	}
	now := time.Now().UTC()
	if change.DisplayName != nil {
		user.DisplayName, identity.DisplayName = *change.DisplayName, *change.DisplayName
	}
	if change.Email != nil {
		if !strings.EqualFold(*change.Email, user.Email) {
			user = clearEmailVerification(user)
		}
		user.Email, identity.Email = *change.Email, *change.Email
	}
	if change.ExternalID != nil {
		identity.SCIMExternalID = *change.ExternalID
	}
	if change.Active != nil {
		if *change.Active {
			if user.State == UserDisabled {
				user.State, user.DisabledReason = UserActive, ""
			}
		} else if user.State != UserDisabled {
			user.State, user.DisabledReason = UserDisabled, DisabledBySCIM
			user.SessionVersion++
			s.revokeUserSessionRecordsLocked(user.ID, now)
		}
	}
	user.Version = nextHostedGroupVersion(user.Version)
	user.UpdatedAt, identity.UpdatedAt = now, now
	s.users[user.ID] = user
	s.userIdentities[identity.ID] = identity
	return SCIMUser{User: user, Identity: identity}, nil
}

func (s *MemoryStore) DeleteSCIMUser(ctx context.Context, providerID, userID string, ifVersions []string, audit ...AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.scimUserLocked(providerID, userID)
	if err != nil {
		return err
	}
	if current.User.Role == userRoleAdmin {
		return ErrSCIMAdminProtected
	}
	if !scimVersionAllowed(current.User.Version, ifVersions) {
		return ErrVersionConflict
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	now := time.Now().UTC()
	user, identity := current.User, current.Identity
	identity.SCIMExternalID, identity.UpdatedAt = SCIMDeletedExternalIDPrefix+user.ID, now
	s.userIdentities[identity.ID] = identity
	// The memberships SCIM wrote for the account go with the link: a revived account must not come back with access
	// the provider has since withdrawn.
	for groupID, group := range s.userGroupsLocked().groups {
		if group.Source == UserGroupSourceSCIM && group.ProviderID == providerID {
			if member, ok := s.userGroupsLocked().members[groupID][user.ID]; ok && member.Source == UserGroupSourceSCIM {
				delete(s.userGroupsLocked().members[groupID], user.ID)
				group.Version, group.UpdatedAt = nextHostedGroupVersion(group.Version), now
				s.userGroupsLocked().groups[groupID] = group
			}
		}
	}
	if user.State != UserDisabled {
		user.State, user.DisabledReason = UserDisabled, DisabledBySCIM
	}
	user.SessionVersion++
	user.Version = nextHostedGroupVersion(user.Version)
	user.UpdatedAt = now
	s.users[user.ID] = user
	s.revokeUserSessionRecordsLocked(user.ID, now)
	return nil
}

func (s *MemoryStore) providerForSCIM(providerID string) (providerRef, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	provider, ok := s.identityProviders[providerID]
	return providerRef{ID: provider.ID, Slug: provider.Slug, Kind: string(provider.Kind)}, ok
}

// providerRef is the part of a provider the SCIM stores copy into an identity row.
type providerRef struct {
	ID, Slug, Kind string
}

func clampSCIMPage(limit int) int {
	switch {
	case limit <= 0:
		return 50
	case limit > SCIMMaxPageSize:
		return SCIMMaxPageSize
	}
	return limit
}

func encodeSCIMCursor(filter string, at time.Time, id string) string {
	return EncodeKeysetToken(filter, at.UTC().Format(time.RFC3339Nano), id)
}

// decodeSCIMCursor reads the cursor of a SCIM list; an empty cursor is the first page.
func decodeSCIMCursor(cursor, filter string) (at time.Time, id string, present bool, err error) {
	if cursor == "" {
		return time.Time{}, "", false, nil
	}
	keys, err := DecodeKeysetToken(cursor, filter, 2)
	if err != nil {
		return time.Time{}, "", false, err
	}
	at, err = time.Parse(time.RFC3339Nano, keys[0])
	if err != nil || !isUUID(keys[1]) {
		return time.Time{}, "", false, ErrInvalidPageToken
	}
	return at, keys[1], true, nil
}

func (s *MemoryStore) CreateSCIMGroup(ctx context.Context, in SCIMGroupCreate, audit ...AuditRecord) (UserGroup, error) {
	group, err := NormalizeUserGroup(UserGroup{ProviderID: in.ProviderID, ExternalID: in.ExternalID, DisplayName: in.DisplayName, Source: UserGroupSourceSCIM, GlobalRole: UserGroupRoleNone})
	if err != nil {
		return UserGroup{}, ErrSCIMInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.identityProviders[in.ProviderID]; !ok {
		return UserGroup{}, ErrUserGroupProviderNotFound
	}
	state := s.userGroupsLocked()
	for _, existing := range state.groups {
		if group.ExternalID != "" && existing.ProviderID == group.ProviderID && existing.ExternalID == group.ExternalID {
			return UserGroup{}, ErrUserGroupExternalID
		}
	}
	for _, memberID := range in.MemberIDs {
		if _, ok := s.scimVisibleIdentityLocked(in.ProviderID, memberID); !ok {
			return UserGroup{}, ErrSCIMInvalid
		}
	}
	groupID := ids.New()
	if err := s.commitAuditLocked(ctx, scimAuditTarget(audit, "scim/Groups/"+groupID, nil)); err != nil {
		return UserGroup{}, err
	}
	now := time.Now().UTC()
	group.ID, group.Version, group.CreatedAt, group.UpdatedAt = groupID, "1", now, now
	state.groups[group.ID] = group
	for _, memberID := range in.MemberIDs {
		s.addSCIMMemberLocked(state, group.ID, memberID, now)
	}
	return group, nil
}

func (s *MemoryStore) addSCIMMemberLocked(state *memoryUserGroupState, groupID, userID string, now time.Time) {
	if state.members[groupID] == nil {
		state.members[groupID] = map[string]UserGroupMember{}
	}
	if existing, exists := state.members[groupID][userID]; exists {
		// The identity provider names the account, so the membership is SCIM-managed from now on: a membership an
		// administrator added for the same account is taken over instead of staying invisible to the provider.
		if existing.Source == UserGroupSourceLocal {
			existing.Source = UserGroupSourceSCIM
			state.members[groupID][userID] = existing
		}
		return
	}
	state.members[groupID][userID] = UserGroupMember{GroupID: groupID, UserID: userID, UserName: s.users[userID].Name, Source: UserGroupSourceSCIM, CreatedAt: now}
}

// scimGroupLocked returns the group of the provider; any other group is not found.
func (s *MemoryStore) scimGroupLocked(providerID, groupID string) (UserGroup, error) {
	group, ok := s.userGroupsLocked().groups[groupID]
	if !ok || group.Source != UserGroupSourceSCIM || group.ProviderID != providerID {
		return UserGroup{}, ErrNotFound
	}
	return group, nil
}

func (s *MemoryStore) GetSCIMGroup(_ context.Context, providerID, groupID string) (UserGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scimGroupLocked(providerID, groupID)
}

func (s *MemoryStore) ListSCIMGroups(_ context.Context, query SCIMGroupQuery) (SCIMGroupPage, error) {
	afterAt, afterID, after, err := decodeSCIMCursor(query.Cursor, query.filterKey())
	if err != nil {
		return SCIMGroupPage{}, err
	}
	limit := clampSCIMPage(query.Limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	matches := make([]UserGroup, 0)
	for _, group := range s.userGroupsLocked().groups {
		if group.Source != UserGroupSourceSCIM || group.ProviderID != query.ProviderID {
			continue
		}
		if query.DisplayName != "" && group.DisplayName != query.DisplayName || query.ExternalID != "" && group.ExternalID != query.ExternalID {
			continue
		}
		matches = append(matches, group)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].CreatedAt.Equal(matches[j].CreatedAt) {
			return matches[i].ID < matches[j].ID
		}
		return matches[i].CreatedAt.Before(matches[j].CreatedAt)
	})
	page := SCIMGroupPage{}
	if !query.SkipTotal {
		page.Total = BoundedListTotal(int64(len(matches)))
	}
	if after {
		start := sort.Search(len(matches), func(i int) bool {
			if matches[i].CreatedAt.Equal(afterAt) {
				return matches[i].ID > afterID
			}
			return matches[i].CreatedAt.After(afterAt)
		})
		matches = matches[start:]
	}
	if len(matches) > limit {
		matches = matches[:limit]
		page.NextCursor = encodeSCIMCursor(query.filterKey(), matches[limit-1].CreatedAt, matches[limit-1].ID)
	}
	page.Items = matches
	return page, nil
}

func (s *MemoryStore) UpdateSCIMGroup(ctx context.Context, providerID, groupID string, change SCIMGroupChange, audit ...AuditRecord) (UserGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	group, err := s.scimGroupLocked(providerID, groupID)
	if err != nil {
		return UserGroup{}, err
	}
	if !scimVersionAllowed(group.Version, change.IfVersions) {
		return UserGroup{}, ErrVersionConflict
	}
	state := s.userGroupsLocked()
	if change.DisplayName != nil {
		name, err := NormalizeUserGroupName(*change.DisplayName)
		if err != nil {
			return UserGroup{}, ErrSCIMInvalid
		}
		change.DisplayName = &name
	}
	if change.ExternalID != nil {
		for _, existing := range state.groups {
			if existing.ID != group.ID && *change.ExternalID != "" && existing.ProviderID == providerID && existing.ExternalID == *change.ExternalID {
				return UserGroup{}, ErrUserGroupExternalID
			}
		}
	}
	added := append([]string(nil), change.AddMembers...)
	if change.ReplaceMembers != nil {
		added = append(added, *change.ReplaceMembers...)
	}
	for _, memberID := range added {
		if _, ok := s.scimVisibleIdentityLocked(providerID, memberID); !ok {
			return UserGroup{}, ErrSCIMInvalid
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return UserGroup{}, err
	}
	now := time.Now().UTC()
	if change.DisplayName != nil {
		group.DisplayName = *change.DisplayName
	}
	if change.ExternalID != nil {
		group.ExternalID = strings.TrimSpace(*change.ExternalID)
	}
	if change.ReplaceMembers != nil {
		keep := map[string]bool{}
		for _, memberID := range *change.ReplaceMembers {
			keep[memberID] = true
		}
		// Only the SCIM-sourced memberships are authoritative here; a membership an administrator added stays.
		for userID, member := range state.members[group.ID] {
			if member.Source == UserGroupSourceSCIM && !keep[userID] {
				delete(state.members[group.ID], userID)
			}
		}
	}
	for _, memberID := range added {
		s.addSCIMMemberLocked(state, group.ID, memberID, now)
	}
	for _, memberID := range change.RemoveMembers {
		if member, ok := state.members[group.ID][memberID]; ok && member.Source == UserGroupSourceSCIM {
			delete(state.members[group.ID], memberID)
		}
	}
	group.Version = nextHostedGroupVersion(group.Version)
	group.UpdatedAt = now
	state.groups[group.ID] = group
	return group, nil
}

func (s *MemoryStore) DeleteSCIMGroup(ctx context.Context, providerID, groupID string, ifVersions []string, audit ...AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	group, err := s.scimGroupLocked(providerID, groupID)
	if err != nil {
		return err
	}
	if !scimVersionAllowed(group.Version, ifVersions) {
		return ErrVersionConflict
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	state := s.userGroupsLocked()
	delete(state.groups, group.ID)
	delete(state.members, group.ID)
	s.removeGroupGrantsLocked(groupPrincipal(group.ID))
	s.removeGroupTemplateGrantsLocked(groupPrincipal(group.ID))
	return nil
}

func (s *MemoryStore) ListSCIMGroupMembers(_ context.Context, providerID, groupID string, limit int) ([]UserGroupMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.scimGroupLocked(providerID, groupID); err != nil {
		return nil, err
	}
	return s.scimMembersLocked(providerID, groupID, limit), nil
}

func (s *MemoryStore) ListSCIMGroupsMembers(_ context.Context, providerID string, groupIDs []string, perGroup int) (map[string][]UserGroupMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	members := make(map[string][]UserGroupMember, len(groupIDs))
	for _, groupID := range groupIDs {
		if _, err := s.scimGroupLocked(providerID, groupID); err != nil {
			continue
		}
		if list := s.scimMembersLocked(providerID, groupID, perGroup); len(list) > 0 {
			members[groupID] = list
		}
	}
	return members, nil
}

// scimMembersLocked returns at most limit SCIM-sourced members of a group ordered by user id, only the accounts the
// provider's SCIM can see. The caller holds s.mu.
func (s *MemoryStore) scimMembersLocked(providerID, groupID string, limit int) []UserGroupMember {
	list := make([]UserGroupMember, 0, len(s.userGroupsLocked().members[groupID]))
	for _, member := range s.userGroupsLocked().members[groupID] {
		user, ok := s.users[member.UserID]
		if _, visible := s.scimVisibleIdentityLocked(providerID, member.UserID); !ok || !visible || member.Source != UserGroupSourceSCIM {
			continue
		}
		member.UserName = user.Name
		list = append(list, member)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].UserID < list[j].UserID })
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list
}

func (s *MemoryStore) LinkProviderIdentity(ctx context.Context, link ProviderIdentityLink, audit ...AuditRecord) (UserIdentity, error) {
	link.Subject, link.Issuer = strings.TrimSpace(link.Subject), normalizeOIDCIssuer(link.Issuer)
	if link.Subject == "" || link.Issuer == "" || link.Kind != UserIdentityOIDC && link.Kind != UserIdentitySAML {
		return UserIdentity{}, ErrSCIMInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.identityProviders[link.ProviderID]; !ok {
		return UserIdentity{}, ErrNotFound
	}
	user, ok := s.users[link.UserID]
	if !ok {
		return UserIdentity{}, ErrNotFound
	}
	existing, linked := s.scimIdentityLocked(link.ProviderID, link.UserID)
	for _, other := range s.userIdentities {
		if other.ProviderID == link.ProviderID && other.Subject == link.Subject && other.ID != existing.ID {
			return UserIdentity{}, ErrIdentityExists
		}
	}
	if linked && existing.Subject != "" && existing.Subject != link.Subject {
		return UserIdentity{}, ErrIdentityExists
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return UserIdentity{}, err
	}
	now := time.Now().UTC()
	if !linked {
		existing = UserIdentity{ID: ids.New(), UserID: user.ID, Kind: link.Kind, ProviderID: link.ProviderID, CreatedAt: now}
	}
	existing.Subject, existing.Issuer, existing.UpdatedAt = link.Subject, link.Issuer, now
	s.userIdentities[existing.ID] = existing
	return existing, nil
}
