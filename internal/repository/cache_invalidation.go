package repository

import (
	"context"
	"time"
)

// CacheNotifyChannel is the NOTIFY channel of read-cache invalidations (ADR 0023
// section 6); the payload is scope||':'||id.
const CacheNotifyChannel = "notarx_cache"

// Cache invalidation scopes.
const (
	CacheScopeAPIKey         = "api_key"
	CacheScopeServiceAccount = "service_account"
	// CacheScopeRepository, CacheScopeGroup and CacheScopePolicy are the access-decision inputs of ADR 0029:
	// hosted repositories (visibility included), hosted groups with their members and the public access gate.
	// Their NOTIFY is sent by row triggers (migration 000006), so no mutation path can forget it.
	CacheScopeRepository = "repository"
	CacheScopeGroup      = "group"
	CacheScopePolicy     = "policy"
)

// CacheInvalidator evicts the cached entries of one entity in this process.
type CacheInvalidator func(scope, id string)

// SetCacheInvalidator installs the synchronous local eviction run after a
// mutation of authorization-relevant state commits. Other nodes learn of the
// change through the pg_notify the mutation sent in its transaction.
//
// Every write a serving process makes to the cached tables (hosted_repositories,
// hosted_groups, hosted_group_members, public_access_policy) goes through the
// store methods that run this eviction; TestCachedTablesHaveNoOtherWriters keeps
// it so. The two other writers rely on the trigger notification alone: the
// first-install repository presets run before the caches start, and
// notarxctl secrets reseal runs in its own process.
func (s *PostgresStore) SetCacheInvalidator(invalidate CacheInvalidator) {
	if invalidate == nil {
		s.cacheInvalidator.Store(nil)
		return
	}
	s.cacheInvalidator.Store(&invalidate)
}

func (s *PostgresStore) invalidateLocal(scope, id string) {
	if invalidate := s.cacheInvalidator.Load(); invalidate != nil && id != "" {
		(*invalidate)(scope, id)
	}
}

// invalidateLocalScope flushes every cached entry of a scope in this process, for a change whose affected
// entities are not known one by one (a repository visibility change affects the groups that contain it).
func (s *PostgresStore) invalidateLocalScope(scope string) {
	if invalidate := s.cacheInvalidator.Load(); invalidate != nil {
		(*invalidate)(scope, "")
	}
}

// notifyCache sends the invalidation in the caller's transaction; PostgreSQL
// delivers it at commit.
func notifyCache(ctx context.Context, q sqlRunner, scope, id string) error {
	_, err := q.ExecContext(ctx, `SELECT pg_notify($1, $2)`, CacheNotifyChannel, scope+":"+id)
	return err
}

// APIKeyUse is one key's most recent use, flushed in batches.
type APIKeyUse struct {
	ID string
	At time.Time
}

// APIKeyUsageStore records throttled last_used_at timestamps. A row is written
// only when its stored value is older than notBefore, so one write per key and
// interval holds across nodes.
type APIKeyUsageStore interface {
	TouchAPIKeys(ctx context.Context, uses []APIKeyUse, notBefore time.Time) error
}
