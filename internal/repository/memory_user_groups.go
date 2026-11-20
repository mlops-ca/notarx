package repository

import (
	"context"
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

// memoryUserGroupState is the in-memory twin of user_groups and user_group_members.
type memoryUserGroupState struct {
	groups  map[string]UserGroup
	members map[string]map[string]UserGroupMember
}

// userGroupsLocked returns the state, creating it on first use. The caller holds s.mu for writing.
func (s *MemoryStore) userGroupsLocked() *memoryUserGroupState {
	if s.userGroupState == nil {
		s.userGroupState = &memoryUserGroupState{groups: map[string]UserGroup{}, members: map[string]map[string]UserGroupMember{}}
	}
	return s.userGroupState
}

// encodeUserGroupCursor and decodeUserGroupCursor build the opaque keyset cursor of (created_at, id).
func encodeUserGroupCursor(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at.UTC().UnixNano(), 10) + "|" + id))
}

func decodeUserGroupCursor(cursor string) (time.Time, string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", false
	}
	nanos, id, found := strings.Cut(string(raw), "|")
	if !found || !isUUID(id) {
		return time.Time{}, "", false
	}
	value, err := strconv.ParseInt(nanos, 10, 64)
	if err != nil {
		return time.Time{}, "", false
	}
	return time.Unix(0, value).UTC(), id, true
}

func (s *MemoryStore) CreateUserGroup(ctx context.Context, group UserGroup, audit ...AuditRecord) (UserGroup, error) {
	group, err := NormalizeUserGroup(group)
	if err != nil {
		return UserGroup{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.userGroupsLocked()
	for _, existing := range state.groups {
		if group.Source == UserGroupSourceLocal && existing.Source == UserGroupSourceLocal && strings.EqualFold(existing.DisplayName, group.DisplayName) {
			return UserGroup{}, ErrUserGroupNameExists
		}
		if group.ExternalID != "" && existing.ProviderID == group.ProviderID && existing.ExternalID == group.ExternalID {
			return UserGroup{}, ErrUserGroupExternalID
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return UserGroup{}, err
	}
	now := time.Now().UTC()
	if group.ID == "" {
		group.ID = ids.New()
	}
	group.Version = "1"
	group.CreatedAt, group.UpdatedAt = now, now
	state.groups[group.ID] = group
	return group, nil
}

func (s *MemoryStore) ListUserGroups(_ context.Context, limit int, cursor string) ([]UserGroup, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]UserGroup, 0)
	if s.userGroupState != nil {
		for _, group := range s.userGroupState.groups {
			items = append(items, group)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].ID < items[j].ID
	})
	if cursor != "" {
		at, id, ok := decodeUserGroupCursor(cursor)
		if !ok {
			return nil, "", ErrUserGroupInvalid
		}
		kept := items[:0]
		for _, item := range items {
			if item.CreatedAt.After(at) || item.CreatedAt.Equal(at) && item.ID > id {
				kept = append(kept, item)
			}
		}
		items = kept
	}
	limit = ClampUserGroupPageSize(limit)
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = encodeUserGroupCursor(items[limit-1].CreatedAt, items[limit-1].ID)
	}
	return items, next, nil
}

func (s *MemoryStore) GetUserGroup(_ context.Context, id string) (UserGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.userGroupState != nil {
		if group, ok := s.userGroupState.groups[id]; ok {
			return group, nil
		}
	}
	return UserGroup{}, ErrNotFound
}

func (s *MemoryStore) RenameUserGroup(ctx context.Context, id, displayName, expectedVersion string, audit ...AuditRecord) (UserGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.userGroupsLocked()
	current, ok := state.groups[id]
	if !ok {
		return UserGroup{}, ErrNotFound
	}
	if current.Version != expectedVersion {
		return UserGroup{}, ErrVersionConflict
	}
	name, err := NormalizeUserGroupName(displayName)
	if err != nil {
		return UserGroup{}, err
	}
	for other, existing := range state.groups {
		if other != id && current.Source == UserGroupSourceLocal && existing.Source == UserGroupSourceLocal && strings.EqualFold(existing.DisplayName, name) {
			return UserGroup{}, ErrUserGroupNameExists
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return UserGroup{}, err
	}
	current.DisplayName = name
	current.Version = nextHostedGroupVersion(current.Version)
	current.UpdatedAt = time.Now().UTC()
	state.groups[id] = current
	return current, nil
}

func (s *MemoryStore) SetUserGroupGlobalRole(ctx context.Context, id, role, expectedVersion string, audit ...AuditRecord) (UserGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.userGroupsLocked()
	current, ok := state.groups[id]
	if !ok {
		return UserGroup{}, ErrNotFound
	}
	if current.Version != expectedVersion {
		return UserGroup{}, ErrVersionConflict
	}
	if !UserGroupRoleChangeAllowed(current.Source, role) {
		return UserGroup{}, ErrUserGroupInvalid
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return UserGroup{}, err
	}
	current.GlobalRole = role
	current.Version = nextHostedGroupVersion(current.Version)
	current.UpdatedAt = time.Now().UTC()
	state.groups[id] = current
	return current, nil
}

func (s *MemoryStore) DeleteUserGroup(ctx context.Context, id string, audit ...AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.userGroupsLocked()
	if _, ok := state.groups[id]; !ok {
		return ErrNotFound
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	delete(state.groups, id)
	delete(state.members, id)
	s.removeGroupGrantsLocked(groupPrincipal(id))
	s.removeGroupTemplateGrantsLocked(groupPrincipal(id))
	return nil
}

// removeGroupGrantsLocked drops the grants that name a deleted group and versions the affected grant sets.
func (s *MemoryStore) removeGroupGrantsLocked(principal string) {
	for repositoryID, set := range s.repositoryGrants {
		kept := make([]RepositoryGrant, 0, len(set.Grants))
		for _, grant := range set.Grants {
			if grant.Principal != principal {
				kept = append(kept, grant)
			}
		}
		if len(kept) == len(set.Grants) {
			continue
		}
		set.Grants = kept
		set.Version = nextHostedGroupVersion(set.Version)
		s.repositoryGrants[repositoryID] = set
		if repo, ok := s.hostedRepositories[repositoryID]; ok {
			repo.GrantsVersion++
			s.hostedRepositories[repositoryID] = repo
		}
	}
}

// removeGroupTemplateGrantsLocked drops the template grants that name a deleted group.
func (s *MemoryStore) removeGroupTemplateGrantsLocked(principal string) {
	for id, template := range s.authorizationTemplates {
		kept := make([]RepositoryGrant, 0, len(template.Grants))
		for _, grant := range template.Grants {
			if grant.Principal != principal {
				kept = append(kept, grant)
			}
		}
		if len(kept) != len(template.Grants) {
			template.Grants = kept
			template.Version = nextHostedGroupVersion(template.Version)
			s.authorizationTemplates[id] = template
		}
	}
}

func (s *MemoryStore) AddUserGroupMember(ctx context.Context, groupID, userID string, source UserGroupSource, audit ...AuditRecord) error {
	if source != UserGroupSourceLocal && source != UserGroupSourceSCIM && source != UserGroupSourceClaims {
		return ErrUserGroupInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.userGroupsLocked()
	if _, ok := state.groups[groupID]; !ok {
		return ErrNotFound
	}
	if _, ok := s.users[userID]; !ok {
		return ErrNotFound
	}
	if _, exists := state.members[groupID][userID]; exists {
		return ErrUserGroupMemberExists
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	if state.members[groupID] == nil {
		state.members[groupID] = map[string]UserGroupMember{}
	}
	state.members[groupID][userID] = UserGroupMember{GroupID: groupID, UserID: userID, UserName: s.users[userID].Name, Source: source, CreatedAt: time.Now().UTC()}
	return nil
}

func (s *MemoryStore) RemoveUserGroupMember(ctx context.Context, groupID, userID string, audit ...AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.userGroupsLocked()
	if _, ok := state.members[groupID][userID]; !ok {
		return ErrNotFound
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	delete(state.members[groupID], userID)
	return nil
}

func (s *MemoryStore) ListUserGroupMembers(_ context.Context, groupID string, limit int, cursor string) ([]UserGroupMember, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.userGroupState == nil {
		return nil, "", ErrNotFound
	}
	if _, ok := s.userGroupState.groups[groupID]; !ok {
		return nil, "", ErrNotFound
	}
	items := make([]UserGroupMember, 0, len(s.userGroupState.members[groupID]))
	for _, member := range s.userGroupState.members[groupID] {
		if user, ok := s.users[member.UserID]; ok {
			member.UserName = user.Name
		}
		if cursor == "" || member.UserID > cursor {
			items = append(items, member)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].UserID < items[j].UserID })
	limit = ClampUserGroupPageSize(limit)
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = items[limit-1].UserID
	}
	return items, next, nil
}

func (s *MemoryStore) ListUserGroupMemberships(_ context.Context, userID string) ([]UserGroupMembership, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]UserGroupMembership, 0)
	if s.userGroupState == nil {
		return items, nil
	}
	for groupID, members := range s.userGroupState.members {
		member, ok := members[userID]
		if !ok {
			continue
		}
		group := s.userGroupState.groups[groupID]
		if group.Source != UserGroupSourceLocal && group.ProviderID == "" {
			continue // the provider is gone: the group is inert
		}
		items = append(items, UserGroupMembership{GroupID: groupID, GroupSource: group.Source, GlobalRole: group.GlobalRole, MemberSource: member.Source})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].GroupID < items[j].GroupID })
	if len(items) > MaxUserGroupMemberships {
		items = items[:MaxUserGroupMemberships]
	}
	return items, nil
}

func (s *MemoryStore) SyncClaimsUserGroups(ctx context.Context, sync ClaimsGroupSync, audit ...AuditRecord) (int, error) {
	if sync.ProviderID == "" || sync.UserID == "" || len(sync.ExternalIDs) > MaxClaimsGroups {
		return 0, ErrUserGroupInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.userGroupsLocked()
	if _, ok := s.users[sync.UserID]; !ok {
		return 0, ErrNotFound
	}
	wanted := map[string]bool{}
	for _, external := range sync.ExternalIDs {
		wanted[strings.TrimSpace(external)] = true
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return 0, err
	}
	matched := 0
	for groupID, group := range state.groups {
		if group.Source != UserGroupSourceClaims || group.ProviderID != sync.ProviderID {
			continue
		}
		current, isMember := state.members[groupID][sync.UserID]
		switch {
		case wanted[group.ExternalID] && group.GlobalRole == UserGroupRoleNone:
			matched++
			if !isMember {
				if state.members[groupID] == nil {
					state.members[groupID] = map[string]UserGroupMember{}
				}
				state.members[groupID][sync.UserID] = UserGroupMember{GroupID: groupID, UserID: sync.UserID, Source: UserGroupSourceClaims, CreatedAt: time.Now().UTC()}
			}
		case isMember && current.Source == UserGroupSourceClaims:
			delete(state.members[groupID], sync.UserID)
		}
	}
	return matched, nil
}
