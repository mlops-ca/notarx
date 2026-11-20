package authorization

import (
	"context"
	"crypto/sha256"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

// DefaultClaimsGroupRefreshInterval bounds how long the claims-sourced memberships of an account that only uses
// OIDC bearer tokens can lag behind its identity provider.
const DefaultClaimsGroupRefreshInterval = 15 * time.Second

// maxClaimsRefreshEntries bounds the per-process memory of the refresher.
const maxClaimsRefreshEntries = 10000

// ClaimsGroupSyncer writes the claims-sourced memberships of an account (repository.UserGroupStore).
type ClaimsGroupSyncer interface {
	SyncClaimsUserGroups(ctx context.Context, sync repository.ClaimsGroupSync, audit ...repository.AuditRecord) (int, error)
}

// ClaimsGroupAuditor receives the audit events of a refresh.
type ClaimsGroupAuditor interface {
	RecordAudit(context.Context, repository.AuditRecord) error
}

// ClaimsGroupRefresher keeps the claims-sourced groups of an account current for the API path. A browser
// sign-in refreshes them from its ID token (internal/app); an account that only presents OIDC bearer tokens
// (the CLI, CI) never signs in, so the groups its token asserts are applied here, at most once per interval for
// unchanged groups and at once when the asserted groups change. A token that overflowed its groups claim asserts no
// groups, the same rule as the sign-in path, and the overage is audited when it first appears.
type ClaimsGroupRefresher struct {
	syncer   ClaimsGroupSyncer
	audit    ClaimsGroupAuditor
	interval time.Duration
	now      func() time.Time

	mu   sync.Mutex
	last map[string]claimsRefresh
}

type claimsRefresh struct {
	fingerprint [sha256.Size]byte
	at          time.Time
}

// NewClaimsGroupRefresher returns a refresher; interval <= 0 means DefaultClaimsGroupRefreshInterval and a nil
// audit records nothing.
func NewClaimsGroupRefresher(syncer ClaimsGroupSyncer, audit ClaimsGroupAuditor, interval time.Duration) *ClaimsGroupRefresher {
	if interval <= 0 {
		interval = DefaultClaimsGroupRefreshInterval
	}
	return &ClaimsGroupRefresher{syncer: syncer, audit: audit, interval: interval, now: time.Now, last: map[string]claimsRefresh{}}
}

// Refresh applies the groups the identity asserts to the account. A failed write is retried on the next request
// and never fails the authentication: it can only leave the account with the memberships it already had.
func (r *ClaimsGroupRefresher) Refresh(ctx context.Context, identity OIDCIdentity, userID string) {
	if r == nil || r.syncer == nil || identity.ProviderID == "" || userID == "" {
		return
	}
	groups := identity.Groups
	if identity.GroupsOverage {
		groups = nil
	}
	sorted := slices.Clone(groups)
	slices.Sort(sorted)
	fingerprint := sha256.Sum256([]byte(strings.Join(sorted, "\x00") + "\x01" + boolText(identity.GroupsOverage)))
	key := identity.ProviderID + "\x00" + userID
	now := r.now()

	r.mu.Lock()
	previous, seen := r.last[key]
	r.mu.Unlock()
	if seen && previous.fingerprint == fingerprint && now.Sub(previous.at) < r.interval {
		return
	}
	if _, err := r.syncer.SyncClaimsUserGroups(ctx, repository.ClaimsGroupSync{ProviderID: identity.ProviderID, UserID: userID, ExternalIDs: groups}); err != nil {
		return
	}
	if identity.GroupsOverage && (!seen || previous.fingerprint != fingerprint) && r.audit != nil {
		slug := identity.ProviderSlug
		if slug == "" {
			slug = identity.ProviderID
		}
		_ = r.audit.RecordAudit(ctx, repository.AuditRecord{
			Class: repository.AuditClassSync, Actor: "oidc:" + slug, Outcome: repository.AuditResolved, OccurredAt: now.UTC(), Format: "management",
			Resource: "auth/oidc/" + slug, Operation: "auth.oidc.groups_overage", Status: 200, CacheDisposition: "bypass",
			AuthorizationSource: "oidc", AuthorizationReason: "groups_overage",
		})
	}
	r.mu.Lock()
	if len(r.last) >= maxClaimsRefreshEntries {
		for k, v := range r.last {
			if now.Sub(v.at) >= r.interval {
				delete(r.last, k)
			}
		}
		if len(r.last) >= maxClaimsRefreshEntries {
			r.last = map[string]claimsRefresh{}
		}
	}
	r.last[key] = claimsRefresh{fingerprint: fingerprint, at: now}
	r.mu.Unlock()
}

func boolText(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
