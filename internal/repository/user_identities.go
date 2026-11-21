package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/mlops-ca/notarx/internal/ids"
)

func (s *MemoryStore) ListUserIdentities(_ context.Context, userID string) ([]UserIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.users[userID]; !ok {
		return nil, ErrNotFound
	}
	items := make([]UserIdentity, 0)
	for _, identity := range s.userIdentities {
		if identity.UserID == userID {
			items = append(items, identity)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items, nil
}

func (s *MemoryStore) CreateUserIdentity(ctx context.Context, identity UserIdentity, audit ...AuditRecord) (UserIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[identity.UserID]; !ok {
		return UserIdentity{}, ErrNotFound
	}
	identity = normalizeUserIdentity(identity)
	if (identity.Kind != UserIdentityOIDC && identity.Kind != UserIdentitySAML) || identity.ProviderID == "" || identity.Issuer == "" || identity.Subject == "" {
		return UserIdentity{}, ErrIdentityExists
	}
	for _, existing := range s.userIdentities {
		if existing.ProviderID == identity.ProviderID && (existing.Subject == identity.Subject || existing.UserID == identity.UserID) {
			return UserIdentity{}, ErrIdentityExists
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return UserIdentity{}, err
	}
	if identity.ID == "" {
		identity.ID = ids.New()
	}
	now := time.Now().UTC()
	identity.CreatedAt = now
	identity.UpdatedAt = now
	s.userIdentities[identity.ID] = identity
	return identity, nil
}

func (s *MemoryStore) DeleteUserIdentity(ctx context.Context, userID, identityID string, audit ...AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, ok := s.userIdentities[identityID]
	if !ok || identity.UserID != userID {
		return ErrNotFound
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	delete(s.userIdentities, identityID)
	return nil
}

func (s *MemoryStore) GetUserByProviderIdentity(_ context.Context, providerID, subject string) (User, UserIdentity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, identity := range s.userIdentities {
		if identity.ProviderID != providerID || identity.Subject == "" || identity.Subject != strings.TrimSpace(subject) {
			continue
		}
		user, ok := s.users[identity.UserID]
		if !ok {
			return User{}, UserIdentity{}, ErrNotFound
		}
		return user, identity, nil
	}
	return User{}, UserIdentity{}, ErrNotFound
}

func (s *MemoryStore) ResolveOIDCIdentity(_ context.Context, provision OIDCIdentityProvision) (User, UserIdentity, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	provision.Issuer = normalizeOIDCIssuer(provision.Issuer)
	provision.Subject = strings.TrimSpace(provision.Subject)
	if provision.ProviderID == "" || provision.Subject == "" {
		return User{}, UserIdentity{}, false, ErrNotFound
	}
	now := provision.OccurredAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.bindSCIMIdentityLocked(provision, now)
	for id, identity := range s.userIdentities {
		if identity.ProviderID != provision.ProviderID || identity.Subject != provision.Subject {
			continue
		}
		user, ok := s.users[identity.UserID]
		if !ok {
			return User{}, UserIdentity{}, false, ErrNotFound
		}
		identity = refreshUserIdentity(identity, provision, now)
		if user.SecretHash == "" {
			if identity.DisplayName != "" {
				user.DisplayName = identity.DisplayName
			}
			if identity.EmailVerified && identity.Email != "" {
				if !strings.EqualFold(user.Email, identity.Email) {
					// The address changed under the administrator's verification: it no longer applies.
					user = clearEmailVerification(user)
				}
				user.Email = identity.Email
			}
		}
		user.LastLoginAt = timePointer(now)
		user.UpdatedAt = now
		user.Version = nextHostedGroupVersion(user.Version)
		s.userIdentities[id] = identity
		s.users[user.ID] = user
		return user, identity, false, nil
	}

	var user User
	if provision.LinkAdminVerifiedEmail {
		var matches []User
		for _, candidate := range s.users {
			if strings.TrimSpace(provision.Email) != "" && strings.EqualFold(candidate.Email, strings.TrimSpace(provision.Email)) {
				matches = append(matches, candidate)
			}
		}
		linked, decision := decideEmailLink(provision.Email, matches, func(candidate User) bool {
			for _, existing := range s.userIdentities {
				if existing.UserID == candidate.ID && existing.ProviderID == provision.ProviderID {
					return true
				}
			}
			return false
		})
		if provision.EmailLink != nil {
			*provision.EmailLink = decision
		}
		if decision.Reason == EmailLinkAmbiguous {
			return User{}, UserIdentity{}, false, ErrIdentityAmbiguous
		}
		user = linked
	}
	if user.ID == "" && !provision.Provision {
		return User{}, UserIdentity{}, false, ErrNotFound
	}
	if user.ID == "" && provision.MatchEmail && provision.EmailVerified && strings.TrimSpace(provision.Email) != "" {
		for _, candidate := range s.users {
			if !strings.EqualFold(candidate.Email, strings.TrimSpace(provision.Email)) {
				continue
			}
			if user.ID != "" {
				return User{}, UserIdentity{}, false, ErrIdentityAmbiguous
			}
			user = candidate
		}
	}
	created := false
	if user.ID == "" {
		user = newOIDCProvisionedUser(provision, s.users, now)
		s.users[user.ID] = user
		created = true
	} else {
		for _, existing := range s.userIdentities {
			if existing.UserID == user.ID && existing.ProviderID == provision.ProviderID {
				return User{}, UserIdentity{}, false, ErrIdentityExists
			}
		}
		user.LastLoginAt = timePointer(now)
		user.UpdatedAt = now
		user.Version = nextHostedGroupVersion(user.Version)
		s.users[user.ID] = user
	}
	identity := refreshUserIdentity(UserIdentity{
		ID: ids.New(), UserID: user.ID, Kind: provision.identityKind(), ProviderID: provision.ProviderID,
		Issuer: provision.Issuer, Subject: provision.Subject, CreatedAt: now,
	}, provision, now)
	s.userIdentities[identity.ID] = identity
	return user, identity, created, nil
}

// bindSCIMIdentityLocked is the first login of an account SCIM created (ADR 0018 section 4): when no identity of
// the provider has the subject yet, the asserted externalId finds the row that has no subject, and the subject is
// bound to it. A row already bound to another subject is left alone. The caller holds s.mu.
func (s *MemoryStore) bindSCIMIdentityLocked(provision OIDCIdentityProvision, now time.Time) {
	if !provision.bindsSCIM() {
		return
	}
	for _, identity := range s.userIdentities {
		if identity.ProviderID == provision.ProviderID && identity.Subject == provision.Subject {
			return
		}
	}
	for id, identity := range s.userIdentities {
		if identity.ProviderID == provision.ProviderID && identity.SCIMExternalID == provision.ExternalID && identity.Subject == "" {
			identity.Subject, identity.Issuer, identity.UpdatedAt = provision.Subject, provision.Issuer, now
			s.userIdentities[id] = identity
			return
		}
	}
}

func normalizeUserIdentity(identity UserIdentity) UserIdentity {
	identity.Kind = strings.TrimSpace(identity.Kind)
	identity.Issuer = normalizeOIDCIssuer(identity.Issuer)
	identity.Subject = strings.TrimSpace(identity.Subject)
	identity.Email = strings.TrimSpace(identity.Email)
	identity.DisplayName = strings.TrimSpace(identity.DisplayName)
	return identity
}

func normalizeOIDCIssuer(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

// identityKind is the stored kind of the identity a sign-in creates.
func (p OIDCIdentityProvision) identityKind() string {
	if p.Kind == UserIdentitySAML {
		return UserIdentitySAML
	}
	return UserIdentityOIDC
}

func refreshUserIdentity(identity UserIdentity, provision OIDCIdentityProvision, now time.Time) UserIdentity {
	identity.Email = strings.TrimSpace(provision.Email)
	identity.DisplayName = strings.TrimSpace(provision.DisplayName)
	identity.EmailVerified = provision.EmailVerified
	identity.LastLoginAt = timePointer(now)
	identity.UpdatedAt = now
	return identity
}

// provisionedUserRole resolves the level a newly provisioned account gets. A
// recognized mapped role wins over the configured default, and anything else -
// including a removed legacy value such as "reader" or "writer" - falls back to
// the member level, which carries no repository capability, so an unknown value
// can never hand out repository authority.
func provisionedUserRole(mapped, defaultRole string) string {
	role := mapped
	if role != "admin" && role != "member" && role != "none" {
		role = defaultRole
	}
	if role != "admin" && role != "member" && role != "none" {
		role = "member"
	}
	return role
}

func newOIDCProvisionedUser(provision OIDCIdentityProvision, existing map[string]User, now time.Time) User {
	role := provisionedUserRole(provision.Role, provision.DefaultRole)
	name := provisionedUsername(provision.PreferredUsername, provision.Email, provision.Issuer, provision.Subject, existing)
	return User{
		ID: ids.New(), Name: name, DisplayName: strings.TrimSpace(provision.DisplayName),
		Email: strings.TrimSpace(provision.Email), Role: role, State: UserActive,
		LastLoginAt: timePointer(now), SessionVersion: 1,
		CreatedAt: now, UpdatedAt: now, Version: "1",
	}
}

func provisionedUsername(preferred, email, issuer, subject string, existing map[string]User) string {
	base := provisionedUsernameBase(preferred, email)
	available := true
	for _, user := range existing {
		if strings.EqualFold(user.Name, base) {
			available = false
			break
		}
	}
	if available {
		return base
	}
	return provisionedUsernameSuffix(base, issuer, subject)
}

func provisionedUsernameBase(preferred, email string) string {
	base := sanitizeUsername(preferred)
	if base == "" {
		if local, _, ok := strings.Cut(strings.TrimSpace(email), "@"); ok {
			base = sanitizeUsername(local)
		}
	}
	if base == "" {
		base = "oidc-user"
	}
	runes := []rune(base)
	if len(runes) > 100 {
		base = string(runes[:100])
	}
	return base
}

func provisionedUsernameSuffix(base, issuer, subject string) string {
	digest := sha256.Sum256([]byte(issuer + "\x00" + subject))
	return base + "-" + hex.EncodeToString(digest[:5])
}

func sanitizeUsername(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}
