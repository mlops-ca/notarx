package main

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"

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
	}
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
