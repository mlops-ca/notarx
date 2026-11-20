package main

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/mlops-ca/notarx/internal/access"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

// readCaches holds the in-process read caches of ADR 0023 section 6 and the
// goroutines that keep them correct: the NOTIFY listener (invalidation, and a
// flush of every cache on reconnect) and the batched last_used_at writer.
type readCaches struct {
	registry *readcache.Registry
	keys     *authorization.CachedAPIKeys
	accounts *authorization.CachedServiceAccounts
	access   *access.ReadCaches

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newReadCaches(cfg config.Config, store *repository.PostgresStore) *readCaches {
	registry := readcache.NewRegistry(cfg.AuthzCacheTTL)
	// The mutating node evicts synchronously; the other nodes hear the NOTIFY the
	// mutation sent in its transaction, and the TTL bounds a lost one.
	store.SetCacheInvalidator(registry.Invalidate)
	return &readCaches{
		registry: registry,
		keys:     authorization.NewCachedAPIKeys(store, registry, store, cfg.APIKeyLastUsedInterval, slog.Default()),
		accounts: authorization.NewCachedServiceAccounts(store, registry),
		access:   access.NewReadCaches(store, registry),
	}
}

// cachedStore is the store the gateway reads the access-decision inputs through (ADR 0023 section 6): every
// method is the PostgreSQL store's except the repository, group and public access policy getters, which the
// caches answer. Writes and management reads carry readcache.Bypass and reach the database.
type cachedStore struct {
	*repository.PostgresStore
	access *access.ReadCaches
}

func (c *readCaches) cachedStore(store *repository.PostgresStore) *cachedStore {
	return &cachedStore{PostgresStore: store, access: c.access}
}

func (s *cachedStore) GetHostedRepository(ctx context.Context, id string) (repository.HostedRepository, error) {
	return s.access.GetHostedRepository(ctx, id)
}

func (s *cachedStore) GetHostedRepositoryByName(ctx context.Context, name string) (repository.HostedRepository, error) {
	return s.access.GetHostedRepositoryByName(ctx, name)
}

func (s *cachedStore) GetHostedGroup(ctx context.Context, id string) (repository.HostedGroup, error) {
	return s.access.GetHostedGroup(ctx, id)
}

func (s *cachedStore) GetHostedGroupByName(ctx context.Context, name string) (repository.HostedGroup, error) {
	return s.access.GetHostedGroupByName(ctx, name)
}

func (s *cachedStore) GetUser(ctx context.Context, id string) (repository.User, error) {
	return s.access.GetUser(ctx, id)
}

func (s *cachedStore) GetUserByName(ctx context.Context, name string) (repository.User, error) {
	return s.access.GetUserByName(ctx, name)
}

func (s *cachedStore) GetRepositoryQuarantineReadPolicy(ctx context.Context, repositoryID string) (repository.RepositoryQuarantineReadPolicy, error) {
	return s.access.GetRepositoryQuarantineReadPolicy(ctx, repositoryID)
}

func (s *cachedStore) GetRepositoryGrants(ctx context.Context, repositoryID string) (repository.RepositoryGrantSet, error) {
	return s.access.GetRepositoryGrants(ctx, repositoryID)
}

func (s *cachedStore) ListUserGroupMemberships(ctx context.Context, userID string) ([]repository.UserGroupMembership, error) {
	return s.access.ListUserGroupMemberships(ctx, userID)
}

func (s *cachedStore) GetWorkloadIssuer(ctx context.Context, id string) (repository.WorkloadIssuer, error) {
	return s.access.GetWorkloadIssuer(ctx, id)
}

func (s *cachedStore) GetWorkloadIssuerByIssuer(ctx context.Context, issuer string) (repository.WorkloadIssuer, error) {
	return s.access.GetWorkloadIssuerByIssuer(ctx, issuer)
}

func (s *cachedStore) GetWorkloadTrustPolicy(ctx context.Context, id string) (repository.WorkloadTrustPolicy, error) {
	return s.access.GetWorkloadTrustPolicy(ctx, id)
}

func (s *cachedStore) ListActiveWorkloadTrustPolicies(ctx context.Context, issuerID string, now time.Time) ([]repository.WorkloadTrustPolicy, error) {
	return s.access.ListActiveWorkloadTrustPolicies(ctx, issuerID, now)
}

func (s *cachedStore) GetPublicAccessPolicy(ctx context.Context) (repository.PublicAccessPolicy, error) {
	return s.access.GetPublicAccessPolicy(ctx)
}

// start runs the listener on a dedicated connection of the notification pool.
// With the caches disabled (TTL 0) there is nothing to invalidate.
func (c *readCaches) start(notifications *sql.DB) {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.keys.Run(ctx)
	}()
	if !c.registry.Enabled() {
		return
	}
	listener := &readcache.Listener{Registry: c.registry, Connect: readcache.PostgresConnect(notifications), Logger: slog.Default()}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		listener.Run(ctx)
	}()
}

// stop ends the goroutines and writes the last_used_at values still in memory.
func (c *readCaches) stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
}
