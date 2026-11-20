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

	mu       sync.Mutex
	last     map[string]claimsRefresh
	inflight map[string][sha256.Size]byte
}

type claimsRefresh struct {
	fingerprint [sha256.Size]byte
	at          time.Time
	// failed marks an attempt that did not reach the store.
	failed bool
}

// NewClaimsGroupRefresher returns a refresher; interval <= 0 means DefaultClaimsGroupRefreshInterval and a nil
// audit records nothing.
func NewClaimsGroupRefresher(syncer ClaimsGroupSyncer, audit ClaimsGroupAuditor, interval time.Duration) *ClaimsGroupRefresher {
	if interval <= 0 {
		interval = DefaultClaimsGroupRefreshInterval
	}
	return &ClaimsGroupRefresher{syncer: syncer, audit: audit, interval: interval, now: time.Now, last: map[string]claimsRefresh{}, inflight: map[string][sha256.Size]byte{}}
}

// Refresh applies the groups the identity asserts to the account. A failed write is retried on the next request
// and never fails the authentication: it can only leave the account with the memberships it already had.
func (r *ClaimsGroupRefresher) Refresh(ctx context.Context, identity OIDCIdentity, userID string) {
	if r == nil || r.syncer == nil || identity.ProviderID == "" || userID == "" {
		return
	}
	if !identity.GroupsPresent && !identity.GroupsOverage {
		// A token without the claim says nothing about groups: an access token from a provider whose groups
		// mapper is not configured must not wipe the memberships a sign-in established.
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
	if inflight, busy := r.inflight[key]; busy && inflight == fingerprint {
		r.mu.Unlock()
		return
	}
	if seen && previous.fingerprint == fingerprint && now.Sub(previous.at) < r.interval {
		r.mu.Unlock()
		return
	}
	r.inflight[key] = fingerprint
	r.mu.Unlock()
	_, err := r.syncer.SyncClaimsUserGroups(ctx, repository.ClaimsGroupSync{ProviderID: identity.ProviderID, UserID: userID, ExternalIDs: groups})
	r.mu.Lock()
	delete(r.inflight, key)
	r.mu.Unlock()
	if err != nil {
		// A failing store is retried at the pace of the interval, not on every request; a change of the asserted
		// groups is still tried at once.
		r.remember(key, fingerprint, now, true)
		return
	}
	if identity.GroupsOverage && (!seen || previous.fingerprint != fingerprint || previous.failed) && r.audit != nil {
		slug := identity.ProviderSlug
		if slug == "" {
			slug = identity.ProviderID
		}
		_ = r.audit.RecordAudit(ctx, repository.GroupsOverageAudit("oidc", slug, now, 200, ""))
	}
	r.remember(key, fingerprint, now, false)
}

// remember records an attempt for the account, keeping the table bounded.
func (r *ClaimsGroupRefresher) remember(key string, fingerprint [sha256.Size]byte, now time.Time, failed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
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
	r.last[key] = claimsRefresh{fingerprint: fingerprint, at: now, failed: failed}
}

func boolText(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
