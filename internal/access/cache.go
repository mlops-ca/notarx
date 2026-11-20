// Read caches of the access-decision inputs (ADR 0023 section 6). Repositories by id and by name (visibility
// included), hosted groups with their members and the instance-wide public access gate are served from the
// read caches of internal/readcache.
//
// Every entry lives at most the authorization window (NOTARX_AUTHZ_CACHE_TTL) and is evicted sooner by the
// invalidation of its scope, which the database announces from a row trigger (migration 000006) and the
// mutating node applies synchronously. A not-found result is cached for at most one second. Management API
// requests, writes, token minting and login carry readcache.Bypass and read through to the database.

package access

import (
	"context"
	"errors"
	"slices"

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
}

// ReadCaches holds the access-decision caches of one process.
type ReadCaches struct {
	source Source

	reposByID    *readcache.Cache[string, repository.HostedRepository]
	reposByName  *readcache.Cache[string, repository.HostedRepository]
	groupsByID   *readcache.Cache[string, repository.HostedGroup]
	groupsByName *readcache.Cache[string, repository.HostedGroup]
	policy       *readcache.Cache[struct{}, repository.PublicAccessPolicy]
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
	}
}

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
