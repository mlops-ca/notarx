// Read caches of the access-decision inputs (ADR 0023 section 6). Repositories by id and by name (visibility
// included), hosted groups with their members, the instance-wide public access gate, users by id and by name,
// the quarantine read policy of a repository, repository grant sets and the workload issuers and trust
// policies of ADR 0019 are served from the read caches of internal/readcache.
//
// A grant set is keyed by (repository id, grants_version): the version comes from the cached repository row, so a
// warm grant lookup costs no statement and a bumped version is a miss even when an eviction was lost.
//
// Every entry lives at most the authorization window (NOTARX_AUTHZ_CACHE_TTL) and is evicted sooner by the
// invalidation of its scope, which the database announces from a row trigger (migration 000006) and the
// mutating node applies synchronously. A not-found result is cached for at most one second. Management API
// requests, writes, token minting and login carry readcache.Bypass and read through to the database.

package access

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

// Source is the store side of the caches.
type Source interface {
	GetHostedRepository(context.Context, string) (repository.HostedRepository, error)
	GetHostedRepositoryByName(context.Context, string) (repository.HostedRepository, error)
	GetHostedGroup(context.Context, string) (repository.HostedGroup, error)
	GetHostedGroupByName(context.Context, string) (repository.HostedGroup, error)
	GetPublicAccessPolicy(context.Context) (repository.PublicAccessPolicy, error)
	GetUser(context.Context, string) (repository.User, error)
	GetUserByName(context.Context, string) (repository.User, error)
	GetRepositoryQuarantineReadPolicy(context.Context, string) (repository.RepositoryQuarantineReadPolicy, error)
	GetRepositoryGrants(context.Context, string) (repository.RepositoryGrantSet, error)
	GetWorkloadIssuer(context.Context, string) (repository.WorkloadIssuer, error)
	GetWorkloadIssuerByIssuer(context.Context, string) (repository.WorkloadIssuer, error)
	GetWorkloadTrustPolicy(context.Context, string) (repository.WorkloadTrustPolicy, error)
	ListActiveWorkloadTrustPolicies(ctx context.Context, issuerID string, now time.Time) ([]repository.WorkloadTrustPolicy, error)
}

// grantEntry and quarantineEntry carry the repository id the invalidation of their scope names.
type grantEntry struct {
	RepositoryID string
	Set          repository.RepositoryGrantSet
}

type quarantineEntry struct {
	RepositoryID string
	Policy       repository.RepositoryQuarantineReadPolicy
}

// ReadCaches holds the access-decision caches of one process.
type ReadCaches struct {
	source Source

	reposByID    *readcache.Cache[string, repository.HostedRepository]
	reposByName  *readcache.Cache[string, repository.HostedRepository]
	groupsByID   *readcache.Cache[string, repository.HostedGroup]
	groupsByName *readcache.Cache[string, repository.HostedGroup]
	policy       *readcache.Cache[struct{}, repository.PublicAccessPolicy]

	usersByID      *readcache.Cache[string, repository.User]
	usersByName    *readcache.Cache[string, repository.User]
	quarantine     *readcache.Cache[string, quarantineEntry]
	grants         *readcache.Cache[string, grantEntry]
	issuersByID    *readcache.Cache[string, repository.WorkloadIssuer]
	issuersByIss   *readcache.Cache[string, repository.WorkloadIssuer]
	issuerPolicies *readcache.Cache[string, []repository.WorkloadTrustPolicy]
	policiesByID   *readcache.Cache[string, repository.WorkloadTrustPolicy]
}

func notFound(err error) bool { return errors.Is(err, repository.ErrNotFound) }

// New registers the caches with registry.
func NewReadCaches(source Source, registry *readcache.Registry) *ReadCaches {
	repoMatch := func(repo repository.HostedRepository, id string) bool { return repo.ID == id }
	groupMatch := func(group repository.HostedGroup, id string) bool { return group.ID == id }
	return &ReadCaches{
		source: source,
		reposByID: readcache.New[string, repository.HostedRepository](registry, readcache.Options[repository.HostedRepository]{
			Name: "repositories_by_id", Scope: repository.CacheScopeRepository, Match: repoMatch, IsNegative: notFound, InvalidateNegatives: true}),
		reposByName: readcache.New[string, repository.HostedRepository](registry, readcache.Options[repository.HostedRepository]{
			Name: "repositories_by_name", Scope: repository.CacheScopeRepository, Match: repoMatch, IsNegative: notFound, InvalidateNegatives: true}),
		groupsByID: readcache.New[string, repository.HostedGroup](registry, readcache.Options[repository.HostedGroup]{
			Name: "groups_by_id", Scope: repository.CacheScopeGroup, Match: groupMatch, IsNegative: notFound, InvalidateNegatives: true}),
		groupsByName: readcache.New[string, repository.HostedGroup](registry, readcache.Options[repository.HostedGroup]{
			Name: "groups_by_name", Scope: repository.CacheScopeGroup, Match: groupMatch, IsNegative: notFound, InvalidateNegatives: true}),
		policy: readcache.New[struct{}, repository.PublicAccessPolicy](registry, readcache.Options[repository.PublicAccessPolicy]{
			Name: "public_access_policy", Scope: repository.CacheScopePolicy}),
		usersByID: readcache.New[string, repository.User](registry, readcache.Options[repository.User]{
			Name: "users_by_id", Scope: repository.CacheScopeUser, Match: userMatch, IsNegative: notFound, InvalidateNegatives: true}),
		usersByName: readcache.New[string, repository.User](registry, readcache.Options[repository.User]{
			Name: "users_by_name", Scope: repository.CacheScopeUser, Match: userMatch, IsNegative: notFound, InvalidateNegatives: true}),
		quarantine: readcache.New[string, quarantineEntry](registry, readcache.Options[quarantineEntry]{
			Name: "quarantine_read_policies", Scope: repository.CacheScopeQuarantine,
			Match: func(e quarantineEntry, id string) bool { return e.RepositoryID == id }, IsNegative: notFound, InvalidateNegatives: true}),
		grants: readcache.New[string, grantEntry](registry, readcache.Options[grantEntry]{
			Name: "repository_grants", Scope: repository.CacheScopeGrants,
			Match: func(e grantEntry, id string) bool { return e.RepositoryID == id }, IsNegative: notFound, InvalidateNegatives: true}),
		issuersByID: readcache.New[string, repository.WorkloadIssuer](registry, readcache.Options[repository.WorkloadIssuer]{
			Name: "workload_issuers_by_id", Scope: repository.CacheScopeWorkloadIssuer, Match: issuerMatch, IsNegative: notFound, InvalidateNegatives: true}),
		issuersByIss: readcache.New[string, repository.WorkloadIssuer](registry, readcache.Options[repository.WorkloadIssuer]{
			Name: "workload_issuers_by_issuer", Scope: repository.CacheScopeWorkloadIssuer, Match: issuerMatch, IsNegative: notFound, InvalidateNegatives: true}),
		// A new or moved policy belongs to a list that does not hold it yet, so any policy change flushes the cache.
		issuerPolicies: readcache.New[string, []repository.WorkloadTrustPolicy](registry, readcache.Options[[]repository.WorkloadTrustPolicy]{
			Name: "workload_trust_policies", Scope: repository.CacheScopeWorkloadPolicy}),
		policiesByID: readcache.New[string, repository.WorkloadTrustPolicy](registry, readcache.Options[repository.WorkloadTrustPolicy]{
			Name: "workload_trust_policies_by_id", Scope: repository.CacheScopeWorkloadPolicy,
			Match: func(policy repository.WorkloadTrustPolicy, id string) bool { return policy.ID == id }, IsNegative: notFound, InvalidateNegatives: true}),
	}
}

func userMatch(user repository.User, id string) bool { return user.ID == id }

func issuerMatch(issuer repository.WorkloadIssuer, id string) bool { return issuer.ID == id }

// A failed or not-found load is never shared as a different error: the cache returns what the store returned.

// GetHostedRepository returns the repository, from the cache when it is fresh.
func (c *ReadCaches) GetHostedRepository(ctx context.Context, id string) (repository.HostedRepository, error) {
	repo, err := c.reposByID.Get(ctx, id, func(ctx context.Context) (repository.HostedRepository, error) {
		return c.source.GetHostedRepository(ctx, id)
	})
	return cloneRepository(repo), err
}

// GetHostedRepositoryByName returns the repository of a name, from the cache when it is fresh.
func (c *ReadCaches) GetHostedRepositoryByName(ctx context.Context, name string) (repository.HostedRepository, error) {
	repo, err := c.reposByName.Get(ctx, name, func(ctx context.Context) (repository.HostedRepository, error) {
		return c.source.GetHostedRepositoryByName(ctx, name)
	})
	return cloneRepository(repo), err
}

// GetHostedGroup returns the group with its members, from the cache when it is fresh.
func (c *ReadCaches) GetHostedGroup(ctx context.Context, id string) (repository.HostedGroup, error) {
	group, err := c.groupsByID.Get(ctx, id, func(ctx context.Context) (repository.HostedGroup, error) {
		return c.source.GetHostedGroup(ctx, id)
	})
	return cloneGroup(group), err
}

// GetHostedGroupByName returns the group of a name with its members, from the cache when it is fresh.
func (c *ReadCaches) GetHostedGroupByName(ctx context.Context, name string) (repository.HostedGroup, error) {
	group, err := c.groupsByName.Get(ctx, name, func(ctx context.Context) (repository.HostedGroup, error) {
		return c.source.GetHostedGroupByName(ctx, name)
	})
	return cloneGroup(group), err
}

// GetPublicAccessPolicy returns the instance gate. A read error is never cached, and the access package
// treats it as a closed gate.
func (c *ReadCaches) GetPublicAccessPolicy(ctx context.Context) (repository.PublicAccessPolicy, error) {
	return c.policy.Get(ctx, struct{}{}, func(ctx context.Context) (repository.PublicAccessPolicy, error) {
		return c.source.GetPublicAccessPolicy(ctx)
	})
}

// cloneRepository copies the slices and pointers of a cached value, so a caller that edits its copy cannot
// change what other requests read.
func cloneRepository(repo repository.HostedRepository) repository.HostedRepository {
	repo.AllowedHosts = slices.Clone(repo.AllowedHosts)
	if repo.EgressProxy != nil {
		proxy := *repo.EgressProxy
		proxy.NoProxy = slices.Clone(proxy.NoProxy)
		repo.EgressProxy = &proxy
	}
	if repo.UpstreamAuth != nil {
		auth := *repo.UpstreamAuth
		repo.UpstreamAuth = &auth
	}
	if repo.OCIBearer != nil {
		bearer := *repo.OCIBearer
		repo.OCIBearer = &bearer
	}
	return repo
}

func cloneGroup(group repository.HostedGroup) repository.HostedGroup {
	group.Members = slices.Clone(group.Members)
	group.LessVisibleMembers = slices.Clone(group.LessVisibleMembers)
	return group
}

// GetUser returns the account by id, from the cache when it is fresh. Session and login paths carry
// readcache.Bypass and read through.
func (c *ReadCaches) GetUser(ctx context.Context, id string) (repository.User, error) {
	user, err := c.usersByID.Get(ctx, id, func(ctx context.Context) (repository.User, error) {
		return c.source.GetUser(ctx, id)
	})
	return cloneUser(user), err
}

// GetUserByName returns the account of a user name, from the cache when it is fresh. The key is the name as
// the caller spelled it; the store decides what matches, so two spellings of one account are two entries.
func (c *ReadCaches) GetUserByName(ctx context.Context, name string) (repository.User, error) {
	user, err := c.usersByName.Get(ctx, name, func(ctx context.Context) (repository.User, error) {
		return c.source.GetUserByName(ctx, name)
	})
	return cloneUser(user), err
}

// GetRepositoryQuarantineReadPolicy returns the quarantine read policy of a repository.
func (c *ReadCaches) GetRepositoryQuarantineReadPolicy(ctx context.Context, repositoryID string) (repository.RepositoryQuarantineReadPolicy, error) {
	entry, err := c.quarantine.Get(ctx, repositoryID, func(ctx context.Context) (quarantineEntry, error) {
		policy, err := c.source.GetRepositoryQuarantineReadPolicy(ctx, repositoryID)
		return quarantineEntry{RepositoryID: repositoryID, Policy: policy}, err
	})
	return entry.Policy, err
}

// GetRepositoryGrants returns the grant set of a repository keyed by the grants_version of the cached
// repository row, so a warm lookup costs no statement and a bumped version is a miss. A read that bypasses the
// caches goes straight to the store, without the extra repository read.
func (c *ReadCaches) GetRepositoryGrants(ctx context.Context, repositoryID string) (repository.RepositoryGrantSet, error) {
	if readcache.Bypassed(ctx) {
		return c.source.GetRepositoryGrants(ctx, repositoryID)
	}
	repo, err := c.GetHostedRepository(ctx, repositoryID)
	if err != nil {
		return repository.RepositoryGrantSet{}, err
	}
	entry, err := c.grants.GetVersioned(ctx, repositoryID, uint64(max(repo.GrantsVersion, 0)), func(ctx context.Context) (grantEntry, error) {
		set, err := c.source.GetRepositoryGrants(ctx, repositoryID)
		return grantEntry{RepositoryID: repositoryID, Set: set}, err
	})
	return cloneGrantSet(entry.Set), err
}

// GetWorkloadIssuer returns the workload issuer by id, active or not.
func (c *ReadCaches) GetWorkloadIssuer(ctx context.Context, id string) (repository.WorkloadIssuer, error) {
	issuer, err := c.issuersByID.Get(ctx, id, func(ctx context.Context) (repository.WorkloadIssuer, error) {
		return c.source.GetWorkloadIssuer(ctx, id)
	})
	return cloneIssuer(issuer), err
}

// GetWorkloadIssuerByIssuer returns the workload issuer of an exact iss value, active or not. The key is the
// iss claim of a token that is not yet verified, so the cache holds only issuers the operator registered: an
// unknown value is a negative result of at most one second in its own bounded list.
func (c *ReadCaches) GetWorkloadIssuerByIssuer(ctx context.Context, issuerURL string) (repository.WorkloadIssuer, error) {
	issuer, err := c.issuersByIss.Get(ctx, issuerURL, func(ctx context.Context) (repository.WorkloadIssuer, error) {
		return c.source.GetWorkloadIssuerByIssuer(ctx, issuerURL)
	})
	return cloneIssuer(issuer), err
}

// GetWorkloadTrustPolicy returns the trust policy by id, active or not. The revalidation of an issued
// workload token reads it, so a disabled policy stops the token within the authorization window.
func (c *ReadCaches) GetWorkloadTrustPolicy(ctx context.Context, id string) (repository.WorkloadTrustPolicy, error) {
	policy, err := c.policiesByID.Get(ctx, id, func(ctx context.Context) (repository.WorkloadTrustPolicy, error) {
		return c.source.GetWorkloadTrustPolicy(ctx, id)
	})
	return cloneTrustPolicy(policy), err
}

// ListActiveWorkloadTrustPolicies returns the active policies of an issuer that have not expired at now. The
// list is cached unfiltered by time and the expiry is applied on every read, so a policy that expires inside
// the window stops matching at its expiry.
func (c *ReadCaches) ListActiveWorkloadTrustPolicies(ctx context.Context, issuerID string, now time.Time) ([]repository.WorkloadTrustPolicy, error) {
	policies, err := c.issuerPolicies.Get(ctx, issuerID, func(ctx context.Context) ([]repository.WorkloadTrustPolicy, error) {
		return c.source.ListActiveWorkloadTrustPolicies(ctx, issuerID, now)
	})
	if err != nil {
		return nil, err
	}
	live := make([]repository.WorkloadTrustPolicy, 0, len(policies))
	for _, policy := range policies {
		if policy.ExpiresAt != nil && !now.Before(*policy.ExpiresAt) {
			continue
		}
		live = append(live, cloneTrustPolicy(policy))
	}
	return live, nil
}

func cloneUser(user repository.User) repository.User {
	user.LastLoginAt = cloneTime(user.LastLoginAt)
	user.PasswordChangedAt = cloneTime(user.PasswordChangedAt)
	user.LockedUntil = cloneTime(user.LockedUntil)
	user.EmailVerifiedAt = cloneTime(user.EmailVerifiedAt)
	return user
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func cloneGrantSet(set repository.RepositoryGrantSet) repository.RepositoryGrantSet {
	set.Grants = slices.Clone(set.Grants)
	for i := range set.Grants {
		set.Grants[i].Scopes = slices.Clone(set.Grants[i].Scopes)
	}
	return set
}

func cloneIssuer(issuer repository.WorkloadIssuer) repository.WorkloadIssuer {
	issuer.Audiences = slices.Clone(issuer.Audiences)
	issuer.AllowedAlgs = slices.Clone(issuer.AllowedAlgs)
	issuer.StaticJWKS = json.RawMessage(slices.Clone([]byte(issuer.StaticJWKS)))
	return issuer
}

func cloneTrustPolicy(policy repository.WorkloadTrustPolicy) repository.WorkloadTrustPolicy {
	policy.Conditions = json.RawMessage(slices.Clone([]byte(policy.Conditions)))
	policy.ExpiresAt = cloneTime(policy.ExpiresAt)
	return policy
}
