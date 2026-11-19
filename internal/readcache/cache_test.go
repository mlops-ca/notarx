package readcache

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errMissing = errors.New("missing")

type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestCache(ttl time.Duration, opts Options[string]) (*Registry, *Cache[string, string], *clock) {
	clk := &clock{now: time.Unix(1_700_000_000, 0)}
	reg := NewRegistry(ttl)
	reg.SetClock(clk.Now)
	if opts.Name == "" {
		opts.Name = "test"
	}
	if opts.Scope == "" {
		opts.Scope = "thing"
	}
	return reg, New[string, string](reg, opts), clk
}

func counting(value string, calls *atomic.Int32) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { calls.Add(1); return value, nil }
}

func TestHitMissAndTTL(t *testing.T) {
	_, cache, clk := newTestCache(5*time.Second, Options[string]{})
	var calls atomic.Int32
	for range 3 {
		if v, err := cache.Get(context.Background(), "k", counting("v", &calls)); err != nil || v != "v" {
			t.Fatalf("get = %q, %v", v, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("loads = %d, want 1", calls.Load())
	}
	clk.Advance(5 * time.Second)
	_, _ = cache.Get(context.Background(), "k", counting("v", &calls))
	if calls.Load() != 2 {
		t.Fatalf("loads after ttl = %d, want 2", calls.Load())
	}
	stats := cache.stats()
	if stats.Hits != 2 || stats.Misses != 2 || stats.Entries != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestZeroTTLDisablesEveryCache(t *testing.T) {
	reg, cache, _ := newTestCache(0, Options[string]{})
	if reg.Enabled() {
		t.Fatal("zero ttl reported enabled")
	}
	var calls atomic.Int32
	for range 3 {
		_, _ = cache.Get(context.Background(), "k", counting("v", &calls))
	}
	if calls.Load() != 3 || cache.Len() != 0 {
		t.Fatalf("loads = %d entries = %d, want 3 and 0", calls.Load(), cache.Len())
	}
}

func TestBypassReadsThrough(t *testing.T) {
	_, cache, _ := newTestCache(5*time.Second, Options[string]{})
	var calls atomic.Int32
	_, _ = cache.Get(context.Background(), "k", counting("v", &calls))
	for range 2 {
		_, _ = cache.Get(Bypass(context.Background()), "k", counting("v", &calls))
	}
	if calls.Load() != 3 {
		t.Fatalf("loads = %d, want 3", calls.Load())
	}
}

func TestVersionBumpTurnsHitIntoMiss(t *testing.T) {
	_, cache, _ := newTestCache(5*time.Second, Options[string]{})
	var calls atomic.Int32
	_, _ = cache.GetVersioned(context.Background(), "repo", 1, counting("v1", &calls))
	_, _ = cache.GetVersioned(context.Background(), "repo", 1, counting("v1", &calls))
	if calls.Load() != 1 {
		t.Fatalf("same version loads = %d, want 1", calls.Load())
	}
	v, _ := cache.GetVersioned(context.Background(), "repo", 2, counting("v2", &calls))
	if v != "v2" || calls.Load() != 2 {
		t.Fatalf("bumped version served %q after %d loads", v, calls.Load())
	}
}

func TestNegativeResultsCachedAtMostOneSecond(t *testing.T) {
	_, cache, clk := newTestCache(30*time.Second, Options[string]{IsNegative: func(err error) bool { return errors.Is(err, errMissing) }})
	var calls atomic.Int32
	load := func(context.Context) (string, error) { calls.Add(1); return "", errMissing }
	for range 3 {
		if _, err := cache.Get(context.Background(), "k", load); !errors.Is(err, errMissing) {
			t.Fatalf("err = %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("negative loads = %d, want 1", calls.Load())
	}
	clk.Advance(MaxNegativeTTL)
	_, _ = cache.Get(context.Background(), "k", load)
	if calls.Load() != 2 {
		t.Fatalf("negative entry outlived %s: loads = %d", MaxNegativeTTL, calls.Load())
	}
}

func TestOtherErrorsAreNeverCached(t *testing.T) {
	_, cache, _ := newTestCache(5*time.Second, Options[string]{IsNegative: func(err error) bool { return errors.Is(err, errMissing) }})
	var calls atomic.Int32
	boom := errors.New("database down")
	load := func(context.Context) (string, error) { calls.Add(1); return "", boom }
	_, _ = cache.Get(context.Background(), "k", load)
	_, _ = cache.Get(context.Background(), "k", load)
	if calls.Load() != 2 || cache.Len() != 0 {
		t.Fatalf("loads = %d entries = %d", calls.Load(), cache.Len())
	}
}

func TestMaxEntriesEnforcedLRU(t *testing.T) {
	_, cache, _ := newTestCache(time.Minute, Options[string]{MaxEntries: 3})
	var calls atomic.Int32
	for _, k := range []string{"a", "b", "c"} {
		_, _ = cache.Get(context.Background(), k, counting(k, &calls))
	}
	_, _ = cache.Get(context.Background(), "a", counting("a", &calls)) // a is now most recent
	_, _ = cache.Get(context.Background(), "d", counting("d", &calls)) // evicts b
	if cache.Len() != 3 {
		t.Fatalf("entries = %d, want 3", cache.Len())
	}
	before := calls.Load()
	_, _ = cache.Get(context.Background(), "a", counting("a", &calls))
	if calls.Load() != before {
		t.Fatal("recently used entry was evicted")
	}
	_, _ = cache.Get(context.Background(), "b", counting("b", &calls))
	if calls.Load() != before+1 {
		t.Fatal("least recently used entry was not evicted")
	}
	if cache.stats().Evictions == 0 {
		t.Fatal("evictions not counted")
	}
}

func TestSingleflightStampede(t *testing.T) {
	_, cache, _ := newTestCache(5*time.Second, Options[string]{})
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{})
	load := func(context.Context) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return "v", nil
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, err := cache.Get(context.Background(), "k", load); err != nil || v != "v" {
				t.Errorf("get = %q, %v", v, err)
			}
		}()
	}
	<-started
	// Every caller that reaches the group joins the single in-flight load.
	deadline := time.Now().Add(2 * time.Second)
	for cache.misses.Load() < 50 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("loads = %d, want 1", calls.Load())
	}
}

func TestLoadRacingEvictionDoesNotInstallStaleData(t *testing.T) {
	reg, cache, _ := newTestCache(5*time.Second, Options[string]{})
	started, release := make(chan struct{}), make(chan struct{})
	stale := func(context.Context) (string, error) {
		close(started)
		<-release
		return "stale", nil
	}
	done := make(chan string)
	go func() {
		v, _ := cache.Get(context.Background(), "k", stale)
		done <- v
	}()
	<-started
	reg.Invalidate("thing", "k")
	close(release)
	if v := <-done; v != "stale" {
		t.Fatalf("racing load returned %q", v)
	}
	if cache.Len() != 0 {
		t.Fatal("a load that raced an eviction re-installed its result")
	}
	var calls atomic.Int32
	if v, _ := cache.Get(context.Background(), "k", counting("fresh", &calls)); v != "fresh" {
		t.Fatalf("after eviction served %q", v)
	}
}

func TestInvalidateMatchesEntityAndScope(t *testing.T) {
	reg, cache, _ := newTestCache(time.Minute, Options[string]{Match: func(v, id string) bool { return v == id }})
	other := New[string, string](reg, Options[string]{Name: "other", Scope: "other"})
	var calls atomic.Int32
	for _, k := range []string{"a", "b"} {
		_, _ = cache.Get(context.Background(), k, counting(k, &calls))
	}
	_, _ = other.Get(context.Background(), "a", counting("a", &calls))
	reg.HandleNotification(Payload("thing", "a"))
	if cache.Len() != 1 || other.Len() != 1 {
		t.Fatalf("entries after invalidate = %d, %d", cache.Len(), other.Len())
	}
	reg.HandleNotification("thing")
	if cache.Len() != 0 || other.Len() != 1 {
		t.Fatalf("scope flush = %d, %d", cache.Len(), other.Len())
	}
	reg.HandleNotification("")
	if other.Len() != 1 {
		t.Fatal("an empty payload invalidated something")
	}
}

func TestValidDropsExpiredValue(t *testing.T) {
	deadline := time.Unix(1_700_000_002, 0)
	type key struct{ until time.Time }
	clk := &clock{now: time.Unix(1_700_000_000, 0)}
	reg := NewRegistry(time.Minute)
	reg.SetClock(clk.Now)
	cache := New[string, key](reg, Options[key]{Name: "k", Scope: "k", Valid: func(v key, now time.Time) bool { return now.Before(v.until) }})
	var calls int
	load := func(context.Context) (key, error) { calls++; return key{until: deadline}, nil }
	_, _ = cache.Get(context.Background(), "k", load)
	clk.Advance(time.Second)
	_, _ = cache.Get(context.Background(), "k", load)
	clk.Advance(time.Second)
	_, _ = cache.Get(context.Background(), "k", load)
	if calls != 2 {
		t.Fatalf("loads = %d, want 2", calls)
	}
}

func TestFollowerRetriesWhenLeaderWasCancelled(t *testing.T) {
	_, cache, _ := newTestCache(5*time.Second, Options[string]{})
	leaderCtx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _ = cache.Get(leaderCtx, "k", func(ctx context.Context) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		})
	}()
	<-started
	result := make(chan string, 1)
	go func() {
		v, _ := cache.Get(context.Background(), "k", func(context.Context) (string, error) { return "ok", nil })
		result <- v
	}()
	deadline := time.Now().Add(2 * time.Second)
	for cache.misses.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-leaderDone
	if v := <-result; v != "ok" {
		t.Fatalf("follower got %q", v)
	}
}

func TestMetricsOutput(t *testing.T) {
	reg, cache, _ := newTestCache(time.Minute, Options[string]{Name: "api_keys"})
	var calls atomic.Int32
	_, _ = cache.Get(context.Background(), "k", counting("v", &calls))
	_, _ = cache.Get(context.Background(), "k", counting("v", &calls))
	var out strings.Builder
	WriteMetrics(&out, reg.Stats())
	for _, want := range []string{
		"notarx_cache_hits_total{cache=\"api_keys\"} 1",
		"notarx_cache_misses_total{cache=\"api_keys\"} 1",
		"notarx_cache_evictions_total{cache=\"api_keys\"} 0",
		"notarx_cache_entries{cache=\"api_keys\"} 1",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("metrics missing %q:\n%s", want, out.String())
		}
	}
}

func TestPayloadRoundTrip(t *testing.T) {
	scope, id := SplitPayload(Payload("repository", "a:b"))
	if scope != "repository" || id != "a:b" {
		t.Fatalf("split = %q %q", scope, id)
	}
}

func TestNegativeFloodNeverEvictsValidEntries(t *testing.T) {
	_, cache, _ := newTestCache(time.Minute, Options[string]{MaxEntries: 3, MaxNegativeEntries: 2, IsNegative: func(err error) bool { return errors.Is(err, errMissing) }})
	var calls atomic.Int32
	for _, k := range []string{"a", "b", "c"} {
		_, _ = cache.Get(context.Background(), k, counting(k, &calls))
	}
	missing := func(context.Context) (string, error) { return "", errMissing }
	for i := range 50 {
		_, _ = cache.Get(context.Background(), "unknown-"+string(rune('A'+i)), missing)
	}
	before := calls.Load()
	for _, k := range []string{"a", "b", "c"} {
		_, _ = cache.Get(context.Background(), k, counting(k, &calls))
	}
	if calls.Load() != before {
		t.Fatal("a flood of negative results evicted valid entries")
	}
	if cache.Len() != 5 {
		t.Fatalf("entries = %d, want 3 valid and 2 negative", cache.Len())
	}
}

func TestCallerAfterInvalidationDoesNotJoinEarlierLoad(t *testing.T) {
	reg, cache, _ := newTestCache(5*time.Second, Options[string]{})
	started, release := make(chan struct{}), make(chan struct{})
	old := make(chan string, 1)
	go func() {
		v, _ := cache.Get(context.Background(), "k", func(context.Context) (string, error) {
			close(started)
			<-release
			return "stale", nil
		})
		old <- v
	}()
	<-started
	reg.Invalidate("thing", "k") // the revocation commits while the old load is in flight
	fresh, _ := cache.Get(context.Background(), "k", func(context.Context) (string, error) { return "fresh", nil })
	close(release)
	if fresh != "fresh" {
		t.Fatalf("a caller after the invalidation was served %q from the earlier load", fresh)
	}
	if v := <-old; v != "stale" {
		t.Fatalf("earlier caller = %q", v)
	}
}

func TestOptionTTLOverridesDisabledRegistry(t *testing.T) {
	clk := &clock{now: time.Unix(1_700_000_000, 0)}
	reg := NewRegistry(0)
	reg.SetClock(clk.Now)
	cache := New[string, string](reg, Options[string]{Name: "own", Scope: "own", TTL: 30 * time.Second, IgnoreBypass: true})
	var calls atomic.Int32
	for range 3 {
		_, _ = cache.Get(Bypass(context.Background()), "k", counting("v", &calls))
	}
	if calls.Load() != 1 {
		t.Fatalf("loads = %d, want 1", calls.Load())
	}
	clk.Advance(30 * time.Second)
	_, _ = cache.Get(context.Background(), "k", counting("v", &calls))
	if calls.Load() != 2 {
		t.Fatal("own TTL not honoured")
	}
}
