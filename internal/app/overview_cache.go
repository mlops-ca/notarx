package app

import (
	"context"
	"errors"
	"time"

	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

var (
	errOverviewCapacities = errors.New("list repository capacities failed")
	errOverviewRequests   = errors.New("list repository request statistics failed")
)

// overviewSnapshot is the caller-independent part of the overview statistics:
// capacity, request counts and proxy-cache sizes of every repository. Each
// request filters it to the repositories its principal administers, so the
// cache holds no authorization decision.
type overviewSnapshot struct {
	generatedAt     time.Time
	records         []repository.RepositoryCapacityRecord
	byName          map[string]repository.RepositoryRequestStatistics
	proxyCapacities map[string]repository.RepositoryCapacity
	proxyErr        error
}

// overviewCache holds the snapshot for NOTARX_OVERVIEW_CACHE_TTL (ADR 0023 section
// 6). A nil cache reads through.
type overviewCache struct {
	snapshots *readcache.Cache[struct{}, overviewSnapshot]
}

func newOverviewCache(ttl time.Duration) *overviewCache {
	registry := readcache.NewRegistry(ttl)
	return &overviewCache{snapshots: readcache.New[struct{}, overviewSnapshot](registry, readcache.Options[overviewSnapshot]{
		Name: "overview", Scope: "overview", MaxEntries: 1, IgnoreBypass: true,
	})}
}

func (c *overviewCache) get(ctx context.Context, load func(context.Context) (overviewSnapshot, error)) (overviewSnapshot, error) {
	if c == nil {
		return load(ctx)
	}
	return c.snapshots.Get(ctx, struct{}{}, load)
}
