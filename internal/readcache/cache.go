// Package readcache holds the in-process read caches of ADR 0023 section 6.
//
// A Cache is a bounded LRU with a per-entry TTL, a singleflight on misses and a
// version stamp: a hit whose stored version differs from the version the caller
// supplies is a miss, so a bumped grants or policy version invalidates dependents
// even when an explicit eviction is missed. Entries are stored only when the
// scope epoch observed at load start is unchanged, so a slow load that raced an
// invalidation never re-installs stale data.
//
// The registry owns the settings every cache shares: the authorization cache TTL
// (NOTARX_AUTHZ_CACHE_TTL, the revocation window; 0 disables every cache), the
// negative-result cap and the epochs bumped by invalidation. It is the hard upper
// bound on staleness even when an invalidation is lost.
package readcache

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// DefaultMaxEntries bounds a cache that sets no limit of its own.
const DefaultMaxEntries = 10000

// DefaultMaxNegativeEntries bounds the negative results of a cache.
const DefaultMaxNegativeEntries = 1000

// MaxNegativeTTL caps how long a not-found or denied result is cached; it only
// absorbs floods and never outlives the authorization window.
const MaxNegativeTTL = time.Second

// Options configures one cache.
type Options[V any] struct {
	// Name is the cache label of the metrics (notarx_cache_hits_total{cache}).
	Name string
	// Scope names the invalidation scope; a notification "scope:id" evicts the
	// entries of every cache registered with the scope.
	Scope string
	// MaxEntries bounds the cache (DefaultMaxEntries when zero).
	MaxEntries int
	// MaxNegativeEntries bounds the negative results (DefaultMaxNegativeEntries when
	// zero). They live in their own LRU, so a flood of unknown credentials evicts
	// only other negative results and never the entries of valid principals.
	MaxNegativeEntries int
	// TTL overrides the registry TTL for this cache (the overview snapshot has its
	// own window). Zero uses the registry TTL.
	TTL time.Duration
	// Match reports whether a cached value belongs to the entity id of an
	// invalidation. When nil, an invalidation flushes the whole cache.
	Match func(value V, id string) bool
	// Valid reports whether a cached value may still be served at now (an API key
	// that expired since it was cached, for example). Nil means always valid.
	Valid func(value V, now time.Time) bool
	// IgnoreBypass makes the cache serve even a Bypass context: for aggregates that
	// carry no authorization decision (the overview statistics).
	IgnoreBypass bool
	// InvalidateNegatives makes an invalidation of any entity also drop every negative result of this cache. A
	// negative result carries no entity to match, and the entity it denied may be the one just created (a
	// repository whose name was looked up before it existed). Caches of credentials leave it off so an
	// unrelated revocation does not empty the flood absorber.
	InvalidateNegatives bool
	// IsNegative reports a load error that is a cacheable negative result (not
	// found, denied). Any other error is never cached.
	IsNegative func(error) bool
	// LeaderError reports a load error that belongs to the request that ran the load and that a follower of the same load must not
	// inherit (a load that runs on the leader's transaction fails with the state of that transaction). A follower that sees one
	// loads again on its own until it leads a load. Cancellation and deadline errors always count, once.
	LeaderError func(error) bool
}

// Stats is a point-in-time copy of one cache's counters.
type Stats struct {
	Name      string
	Hits      uint64
	Misses    uint64
	Evictions uint64
	Entries   int
}

type control interface {
	cacheName() string
	cacheScope() string
	flush()
	invalidate(id string)
	stats() Stats
}

// Registry groups the caches of one process.
type Registry struct {
	ttl func() time.Duration
	now func() time.Time

	mu     sync.Mutex
	caches []control
	scopes map[string]*atomic.Uint64
	global atomic.Uint64
}

// NewRegistry returns a registry whose caches hold entries for ttl. A ttl of
// zero disables every cache: each Get reads through to its loader.
func NewRegistry(ttl time.Duration) *Registry {
	return &Registry{ttl: func() time.Duration { return ttl }, now: time.Now, scopes: map[string]*atomic.Uint64{}}
}

// SetClock replaces the registry clock; tests only.
func (r *Registry) SetClock(now func() time.Time) { r.now = now }

// TTL is the authorization cache TTL; zero means disabled.
func (r *Registry) TTL() time.Duration { return r.ttl() }

// Enabled reports whether caching is on.
func (r *Registry) Enabled() bool { return r.ttl() > 0 }

func (r *Registry) scope(name string) *atomic.Uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	counter := r.scopes[name]
	if counter == nil {
		counter = new(atomic.Uint64)
		r.scopes[name] = counter
	}
	return counter
}

func (r *Registry) epoch(counter *atomic.Uint64) uint64 { return counter.Load() + r.global.Load() }

// Epoch returns the current epoch of a scope.
func (r *Registry) Epoch(scope string) uint64 { return r.epoch(r.scope(scope)) }

// Invalidate evicts the entries of the entity id in every cache of the scope and
// bumps the scope epoch, so a load that started earlier does not store its
// result. An empty id flushes the whole scope.
func (r *Registry) Invalidate(scope, id string) {
	r.scope(scope).Add(1)
	r.mu.Lock()
	caches := append([]control(nil), r.caches...)
	r.mu.Unlock()
	for _, c := range caches {
		if c.cacheScope() != scope {
			continue
		}
		if id == "" {
			c.flush()
		} else {
			c.invalidate(id)
		}
	}
}

// FlushAll empties every cache and bumps every epoch. It runs when a listener
// reconnects or detects a gap, because notifications sent meanwhile are lost.
func (r *Registry) FlushAll() {
	r.global.Add(1)
	r.mu.Lock()
	caches := append([]control(nil), r.caches...)
	r.mu.Unlock()
	for _, c := range caches {
		c.flush()
	}
}

// HandleNotification applies a "scope:id" payload of the notarx_cache channel.
// A payload without an id flushes the scope; an empty one is ignored.
func (r *Registry) HandleNotification(payload string) {
	scope, id := SplitPayload(payload)
	if scope == "" {
		return
	}
	r.Invalidate(scope, id)
}

// Payload builds the pg_notify payload of a scope and entity id.
func Payload(scope, id string) string { return scope + ":" + id }

// SplitPayload is the inverse of Payload.
func SplitPayload(payload string) (scope, id string) {
	for i := 0; i < len(payload); i++ {
		if payload[i] == ':' {
			return payload[:i], payload[i+1:]
		}
	}
	return payload, ""
}

// Stats returns the counters of every cache in registration order.
func (r *Registry) Stats() []Stats {
	r.mu.Lock()
	caches := append([]control(nil), r.caches...)
	r.mu.Unlock()
	out := make([]Stats, 0, len(caches))
	for _, c := range caches {
		out = append(out, c.stats())
	}
	return out
}

type entry[K comparable, V any] struct {
	list      *list.List
	key       K
	value     V
	err       error
	version   uint64
	expiresAt time.Time
}

// Cache is a bounded LRU with TTL, singleflight and version stamps.
type Cache[K comparable, V any] struct {
	reg    *Registry
	opts   Options[V]
	scope  *atomic.Uint64
	max    int
	maxNeg int

	mu       sync.Mutex
	entries  map[K]*list.Element
	order    *list.List // valid entries, most recent first
	negOrder *list.List // negative results, most recent first
	group    singleflight.Group

	hits, misses, evictions atomic.Uint64
}

// New registers a cache with the registry.
func New[K comparable, V any](reg *Registry, opts Options[V]) *Cache[K, V] {
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = DefaultMaxEntries
	}
	if opts.MaxNegativeEntries <= 0 {
		opts.MaxNegativeEntries = DefaultMaxNegativeEntries
	}
	c := &Cache[K, V]{reg: reg, opts: opts, scope: reg.scope(opts.Scope), max: opts.MaxEntries, maxNeg: opts.MaxNegativeEntries,
		entries: map[K]*list.Element{}, order: list.New(), negOrder: list.New()}
	reg.mu.Lock()
	reg.caches = append(reg.caches, c)
	reg.mu.Unlock()
	return c
}

func (c *Cache[K, V]) cacheName() string  { return c.opts.Name }
func (c *Cache[K, V]) cacheScope() string { return c.opts.Scope }

func (c *Cache[K, V]) stats() Stats {
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	return Stats{Name: c.opts.Name, Hits: c.hits.Load(), Misses: c.misses.Load(), Evictions: c.evictions.Load(), Entries: n}
}

func (c *Cache[K, V]) flush() {
	c.mu.Lock()
	c.evictions.Add(uint64(len(c.entries)))
	c.entries = map[K]*list.Element{}
	c.order.Init()
	c.negOrder.Init()
	c.mu.Unlock()
}

func (c *Cache[K, V]) invalidate(id string) {
	if c.opts.Match == nil {
		c.flush()
		return
	}
	c.mu.Lock()
	for el := c.order.Front(); el != nil; {
		next := el.Next()
		if c.opts.Match(el.Value.(*entry[K, V]).value, id) {
			c.remove(el)
		}
		el = next
	}
	if c.opts.InvalidateNegatives {
		for el := c.negOrder.Front(); el != nil; {
			next := el.Next()
			c.remove(el)
			el = next
		}
	}
	c.mu.Unlock()
}

// Len is the number of cached entries.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Evict removes one key.
func (c *Cache[K, V]) Evict(key K) {
	c.mu.Lock()
	if el, ok := c.entries[key]; ok {
		c.remove(el)
	}
	c.mu.Unlock()
}

func (c *Cache[K, V]) remove(el *list.Element) {
	e := el.Value.(*entry[K, V])
	delete(c.entries, e.key)
	e.list.Remove(el)
	c.evictions.Add(1)
}

type bypassKey struct{}

// Bypass marks a context so every cache reads through. Management-API
// authorization, writes, token minting and login use it (ADR 0023 section 6).
func Bypass(ctx context.Context) context.Context { return context.WithValue(ctx, bypassKey{}, true) }

// Bypassed reports whether the context asks caches to read through.
func Bypassed(ctx context.Context) bool { v, _ := ctx.Value(bypassKey{}).(bool); return v }

// Get returns the cached value of key, loading it on a miss.
func (c *Cache[K, V]) Get(ctx context.Context, key K, load func(context.Context) (V, error)) (V, error) {
	return c.GetVersioned(ctx, key, 0, load)
}

// GetVersioned is Get with the entity version the caller currently knows. A hit
// stored under a different version is a miss.
func (c *Cache[K, V]) GetVersioned(ctx context.Context, key K, version uint64, load func(context.Context) (V, error)) (V, error) {
	ttl := c.opts.TTL
	if ttl <= 0 {
		ttl = c.reg.ttl()
	}
	if ttl <= 0 || (Bypassed(ctx) && !c.opts.IgnoreBypass) {
		return load(ctx)
	}
	now := c.reg.now()
	if value, err, ok := c.lookup(key, version, now); ok {
		c.hits.Add(1)
		return value, err
	}
	c.misses.Add(1)
	for attempt := 0; ; attempt++ {
		epoch := c.reg.epoch(c.scope)
		// The epoch is part of the flight: a caller that arrives after an
		// invalidation never joins a load that started before it.
		sfKey := fmt.Sprintf("%v@%d@%d", key, version, epoch)
		led := false
		res, err, _ := c.group.Do(sfKey, func() (any, error) {
			led = true
			// A flight that ended between this caller's miss and its arrival has
			// already stored the entry: read it instead of loading again.
			if value, cachedErr, ok := c.lookup(key, version, c.reg.now()); ok {
				return &loaded[V]{value: value, err: cachedErr}, nil
			}
			value, loadErr := load(ctx)
			c.store(key, version, epoch, value, loadErr, ttl)
			return &loaded[V]{value: value, err: loadErr}, nil
		})
		if err != nil {
			var zero V
			return zero, err
		}
		l := res.(*loaded[V])
		// A follower must not inherit the cancellation of the leader's request.
		if l.err != nil && ctx.Err() == nil {
			if attempt == 0 && (errors.Is(l.err, context.Canceled) || errors.Is(l.err, context.DeadlineExceeded)) {
				continue
			}
			// A follower of a load that failed with its leader's own error loads again until it leads a load itself, so
			// it never fails with the state of another caller's transaction.
			if !led && c.opts.LeaderError != nil && c.opts.LeaderError(l.err) {
				continue
			}
		}
		return l.value, l.err
	}
}

type loaded[V any] struct {
	value V
	err   error
}

func (c *Cache[K, V]) lookup(key K, version uint64, now time.Time) (V, error, bool) {
	var zero V
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return zero, nil, false
	}
	e := el.Value.(*entry[K, V])
	if !now.Before(e.expiresAt) || e.version != version ||
		(e.err == nil && c.opts.Valid != nil && !c.opts.Valid(e.value, now)) {
		c.remove(el)
		return zero, nil, false
	}
	e.list.MoveToFront(el)
	return e.value, e.err, true
}

func (c *Cache[K, V]) store(key K, version, epoch uint64, value V, err error, ttl time.Duration) {
	if err != nil {
		if c.opts.IsNegative == nil || !c.opts.IsNegative(err) {
			return
		}
		ttl = min(ttl, MaxNegativeTTL)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// The epoch check sits under the lock an invalidation also takes, so an
	// eviction cannot slip between the check and the store.
	if c.reg.epoch(c.scope) != epoch {
		return
	}
	if el, ok := c.entries[key]; ok {
		c.remove(el)
	}
	e := &entry[K, V]{key: key, value: value, err: err, version: version, expiresAt: c.reg.now().Add(ttl), list: c.order}
	limit := c.max
	if err != nil {
		e.list, limit = c.negOrder, c.maxNeg
	}
	c.entries[key] = e.list.PushFront(e)
	for e.list.Len() > limit {
		c.remove(e.list.Back())
	}
}
