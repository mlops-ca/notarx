package repository

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

type UserListQuery struct {
	Search string
	Role   string
	State  string
	Limit  int
	// PageToken continues a listing from the NextPageToken of the previous page (keyset on created_at, id).
	PageToken string
}

type UserPage struct {
	Items []User
	Total ListTotal
	Limit int
	// NextPageToken is set while more users follow the page.
	NextPageToken string
}

// filterKey binds a page token to the filters that produced it.
func (q UserListQuery) filterKey() string {
	return "users\x00" + strings.TrimSpace(q.Search) + "\x00" + q.Role + "\x00" + q.State
}

// afterCursor decodes the page token into the (created_at, id) of the last user already served.
func (q UserListQuery) afterCursor() (createdAt time.Time, id string, present bool, err error) {
	if q.PageToken == "" {
		return time.Time{}, "", false, nil
	}
	keys, err := DecodeKeysetToken(q.PageToken, q.filterKey(), 2)
	if err != nil {
		return time.Time{}, "", false, err
	}
	createdAt, err = time.Parse(time.RFC3339Nano, keys[0])
	if err != nil || keys[1] == "" {
		return time.Time{}, "", false, ErrInvalidPageToken
	}
	return createdAt, keys[1], true, nil
}

func userPageToken(q UserListQuery, last User) string {
	return EncodeKeysetToken(q.filterKey(), last.CreatedAt.UTC().Format(time.RFC3339Nano), last.ID)
}

type UserUpdate struct {
	ID          string
	DisplayName *string
	Email       *string
	Description *string
	Role        *string
	State       *string
	// EmailVerifiedByAdmin sets or clears the administrator verification of the account's email.
	// EmailVerifiedBy is the acting administrator, recorded when it is set. Changing the email
	// clears an existing verification unless the same update sets it again.
	EmailVerifiedByAdmin *bool
	EmailVerifiedBy      string
}

// userRoleAdmin is the stored role of a platform administrator.
const userRoleAdmin = "admin"

// ErrVerifierRequired rejects verifying an email without naming the verifying administrator.
var ErrVerifierRequired = errors.New("the email verification needs the acting administrator")

// ErrEmailRequired rejects verifying an account that has no email to verify.
var ErrEmailRequired = errors.New("the account has no email to verify")

// applyEmailVerification applies the profile email and the verification flag of an update to a user in
// memory with the rules the PostgreSQL store enforces in SQL: a changed email drops the verification, an
// explicit request sets or clears it, and a verified account always names the verifier and the time.
func applyEmailVerification(current User, update UserUpdate, now time.Time) (User, error) {
	if update.Email != nil {
		// A different address drops the verification; a case-only change is the same address, which
		// every email match in the identity code already treats case-insensitively.
		if !strings.EqualFold(strings.TrimSpace(*update.Email), strings.TrimSpace(current.Email)) {
			current = clearEmailVerification(current)
		}
		current.Email = *update.Email
	}
	if update.EmailVerifiedByAdmin == nil {
		return current, nil
	}
	if !*update.EmailVerifiedByAdmin {
		return clearEmailVerification(current), nil
	}
	if strings.TrimSpace(current.Email) == "" {
		return User{}, ErrEmailRequired
	}
	if current.EmailVerifiedByAdmin {
		// Already verified: keep the original verifier and time instead of overwriting them.
		return current, nil
	}
	if update.EmailVerifiedBy == "" {
		return User{}, ErrVerifierRequired
	}
	current.EmailVerifiedByAdmin, current.EmailVerifiedBy = true, update.EmailVerifiedBy
	current.EmailVerifiedAt = timePointer(now)
	return current, nil
}

func clearEmailVerification(user User) User {
	user.EmailVerifiedByAdmin, user.EmailVerifiedAt, user.EmailVerifiedBy = false, nil, ""
	return user
}

// UserStore persists local user accounts. SecretHash is a bcrypt hash; it is
// never returned by the management surface (handlers project to a safe shape).
// Mutating methods take the audit of the change as a trailing optional argument and
// write it in the same transaction (ADR 0023 section 1, see runAudited).
type UserStore interface {
	CreateUser(context.Context, User, ...AuditRecord) (User, error)
	ListUsers(context.Context, UserListQuery) (UserPage, error)
	GetUser(context.Context, string) (User, error)
	GetUserByName(context.Context, string) (User, error)
	UpdateUser(context.Context, UserUpdate, string, ...AuditRecord) (User, error)
	DeleteUser(context.Context, string, ...AuditRecord) error
	RecordUserLoginSuccess(context.Context, string, time.Time, ...AuditRecord) (User, error)
	RecordUserLoginFailure(context.Context, string, time.Time, int, time.Duration, ...AuditRecord) (User, error)
	UpdateUserPassword(context.Context, string, string, string, bool, ...AuditRecord) (User, error)
	RevokeUserSessions(context.Context, string, string, ...AuditRecord) (User, error)
}

func (s *MemoryStore) CreateUser(ctx context.Context, user User, audit ...AuditRecord) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.users {
		if strings.EqualFold(existing.Name, user.Name) {
			return User{}, ErrNameExists
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return User{}, err
	}
	user.State = UserActive
	user.CreatedAt = time.Now().UTC()
	user.UpdatedAt = user.CreatedAt
	if user.SecretHash != "" {
		user.PasswordChangedAt = timePointer(user.CreatedAt)
	}
	user.SessionVersion = 1
	user.Version = "1"
	s.users[user.ID] = user
	return user, nil
}

func (s *MemoryStore) ListUsers(_ context.Context, query UserListQuery) (UserPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	search := strings.ToLower(strings.TrimSpace(query.Search))
	filtered := make([]User, 0, len(s.users))
	for _, u := range s.users {
		if search != "" && !strings.Contains(strings.ToLower(u.Name+" "+u.DisplayName+" "+u.Email), search) {
			continue
		}
		if query.Role != "" && u.Role != query.Role || query.State != "" && u.State != query.State {
			continue
		}
		filtered = append(filtered, u)
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].CreatedAt.Equal(filtered[j].CreatedAt) {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].CreatedAt.Before(filtered[j].CreatedAt)
	})
	afterAt, afterID, after, err := query.afterCursor()
	if err != nil {
		return UserPage{}, err
	}
	page := UserPage{Total: BoundedListTotal(int64(len(filtered))), Limit: query.Limit}
	if page.Limit <= 0 || page.Limit > 500 {
		page.Limit = 100
	}
	if after {
		start := sort.Search(len(filtered), func(i int) bool {
			if filtered[i].CreatedAt.Equal(afterAt) {
				return filtered[i].ID > afterID
			}
			return filtered[i].CreatedAt.After(afterAt)
		})
		filtered = filtered[start:]
	}
	if len(filtered) > page.Limit {
		filtered = filtered[:page.Limit]
		page.NextPageToken = userPageToken(query, filtered[len(filtered)-1])
	}
	page.Items = append([]User(nil), filtered...)
	return page, nil
}

func (s *MemoryStore) GetUser(_ context.Context, id string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return user, nil
}

func (s *MemoryStore) GetUserByName(_ context.Context, name string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, user := range s.users {
		if strings.EqualFold(user.Name, name) {
			return user, nil
		}
	}
	return User{}, ErrNotFound
}

func (s *MemoryStore) UpdateUser(ctx context.Context, update UserUpdate, expectedVersion string, audit ...AuditRecord) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.users[update.ID]
	if !ok {
		return User{}, ErrNotFound
	}
	if current.Version != expectedVersion {
		return User{}, ErrVersionConflict
	}
	if removesLastActiveAdmin(current, update.Role, update.State) && !hasOtherActiveAdmin(s.users, current.ID) {
		return User{}, ErrLastActiveAdmin
	}
	now := time.Now().UTC()
	current, err := applyEmailVerification(current, update, now)
	if err != nil {
		return User{}, err
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return User{}, err
	}
	if update.Role != nil {
		current.Role = *update.Role
	}
	if update.State != nil {
		current.State = *update.State
		current.DisabledReason = ""
		if current.State == UserDisabled {
			current.DisabledReason = DisabledByAdmin
		}
	}
	if update.DisplayName != nil {
		current.DisplayName = *update.DisplayName
	}
	if update.Description != nil {
		current.Description = *update.Description
	}
	current.Version = nextHostedGroupVersion(current.Version)
	current.UpdatedAt = now
	s.users[update.ID] = current
	return current, nil
}

func (s *MemoryStore) DeleteUser(ctx context.Context, id string, audit ...AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[id]
	if !ok {
		return ErrNotFound
	}
	if user.Role == "admin" && user.State == UserActive && !hasOtherActiveAdmin(s.users, user.ID) {
		return ErrLastActiveAdmin
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	delete(s.users, id)
	for identityID, identity := range s.userIdentities {
		if identity.UserID == id {
			delete(s.userIdentities, identityID)
		}
	}
	for sessionID, session := range s.userSessions {
		if session.UserID == id {
			delete(s.userSessions, sessionID)
		}
	}
	return nil
}

func removesLastActiveAdmin(current User, role, state *string) bool {
	if current.Role != "admin" || current.State != UserActive {
		return false
	}
	nextRole, nextState := current.Role, current.State
	if role != nil {
		nextRole = *role
	}
	if state != nil {
		nextState = *state
	}
	return nextRole != "admin" || nextState != UserActive
}

func hasOtherActiveAdmin(users map[string]User, excludedID string) bool {
	for _, user := range users {
		if user.ID != excludedID && user.Role == "admin" && user.State == UserActive {
			return true
		}
	}
	return false
}

func (s *MemoryStore) RecordUserLoginSuccess(ctx context.Context, id string, occurredAt time.Time, audit ...AuditRecord) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return User{}, err
	}
	user.LastLoginAt = timePointer(occurredAt.UTC())
	user.FailedLoginAttempts = 0
	user.LockedUntil = nil
	user.Version = nextHostedGroupVersion(user.Version)
	user.UpdatedAt = occurredAt.UTC()
	s.users[id] = user
	return user, nil
}

func (s *MemoryStore) RecordUserLoginFailure(ctx context.Context, id string, occurredAt time.Time, maxAttempts int, lockout time.Duration, audit ...AuditRecord) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return User{}, err
	}
	user.FailedLoginAttempts++
	if maxAttempts > 0 && user.FailedLoginAttempts >= maxAttempts && lockout > 0 {
		until := occurredAt.UTC().Add(lockout)
		user.LockedUntil = &until
	}
	user.Version = nextHostedGroupVersion(user.Version)
	user.UpdatedAt = occurredAt.UTC()
	s.users[id] = user
	return user, nil
}

func (s *MemoryStore) UpdateUserPassword(ctx context.Context, id, secretHash, expectedVersion string, mustChange bool, audit ...AuditRecord) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	if user.Version != expectedVersion {
		return User{}, ErrVersionConflict
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return User{}, err
	}
	user.SecretHash = secretHash
	user.PasswordChangedAt = timePointer(time.Now().UTC())
	user.MustChangePassword = mustChange
	user.FailedLoginAttempts = 0
	user.LockedUntil = nil
	user.SessionVersion++
	user.Version = nextHostedGroupVersion(user.Version)
	now := time.Now().UTC()
	user.UpdatedAt = now
	s.users[id] = user
	s.revokeUserSessionRecordsLocked(id, now)
	return user, nil
}

func (s *MemoryStore) RevokeUserSessions(ctx context.Context, id, expectedVersion string, audit ...AuditRecord) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	if user.Version != expectedVersion {
		return User{}, ErrVersionConflict
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return User{}, err
	}
	user.SessionVersion++
	user.Version = nextHostedGroupVersion(user.Version)
	now := time.Now().UTC()
	user.UpdatedAt = now
	s.users[id] = user
	s.revokeUserSessionRecordsLocked(id, now)
	return user, nil
}

func timePointer(value time.Time) *time.Time {
	return &value
}
