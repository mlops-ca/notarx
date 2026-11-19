package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/readcache"
)

func TestOverviewCacheReusesSnapshotEvenForManagementRequests(t *testing.T) {
	cache := newOverviewCache(nil, 30*time.Second)
	var loads atomic.Int32
	load := func(context.Context) (overviewSnapshot, error) {
		loads.Add(1)
		return overviewSnapshot{generatedAt: time.Unix(1, 0)}, nil
	}
	// The overview route is a management route: Bypass must not defeat this cache,
	// which holds no authorization decision.
	ctx := readcache.Bypass(context.Background())
	for range 3 {
		if _, err := cache.get(ctx, load); err != nil {
			t.Fatal(err)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("loads = %d, want 1", loads.Load())
	}
}

func TestOverviewCacheDisabledReadsThrough(t *testing.T) {
	for _, cache := range []*overviewCache{newOverviewCache(nil, 0), nil} {
		var loads atomic.Int32
		for range 3 {
			_, _ = cache.get(context.Background(), func(context.Context) (overviewSnapshot, error) {
				loads.Add(1)
				return overviewSnapshot{}, nil
			})
		}
		if loads.Load() != 3 {
			t.Fatalf("loads = %d, want 3", loads.Load())
		}
	}
}

func TestOverviewCacheReportsThroughSharedRegistry(t *testing.T) {
	registry := readcache.NewRegistry(0) // authorization caches disabled; the overview keeps its own TTL
	cache := newOverviewCache(registry, 30*time.Second)
	load := func(context.Context) (overviewSnapshot, error) { return overviewSnapshot{}, nil }
	for range 2 {
		_, _ = cache.get(context.Background(), load)
	}
	stats := registry.Stats()
	if len(stats) != 1 || stats[0].Name != "overview" || stats[0].Hits != 1 || stats[0].Misses != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}
