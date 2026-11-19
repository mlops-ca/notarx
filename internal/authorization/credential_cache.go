package authorization

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

// DefaultAPIKeyLastUsedInterval is NOTARX_API_KEY_LAST_USED_INTERVAL.
const DefaultAPIKeyLastUsedInterval = 60 * time.Second

// CachedAPIKeys serves FindActiveAPIKeyByHash from the read cache of ADR 0023
// section 6 and throttles last_used_at: a use is remembered in memory and flushed
// at most once per interval and key, so authenticating a request writes nothing.
//
// A key that is revoked, expires or belongs to a disabled service account is
// effective on every node within the registry TTL, sooner when the
// invalidation reaches the node. A hit past the key's expires_at is a miss.
type CachedAPIKeys struct {
	repository.APIKeyStore
	keys     *readcache.Cache[string, repository.APIKey]
	usage    repository.APIKeyUsageStore
	interval time.Duration
	logger   *slog.Logger
	now      func() time.Time

	mu      sync.Mutex
	pending map[string]time.Time
}

// NewCachedAPIKeys wraps inner. usage may be nil, which disables last_used_at.
func NewCachedAPIKeys(inner repository.APIKeyStore, registry *readcache.Registry, usage repository.APIKeyUsageStore, lastUsedInterval time.Duration, logger *slog.Logger) *CachedAPIKeys {
	if lastUsedInterval <= 0 {
		lastUsedInterval = DefaultAPIKeyLastUsedInterval
	}
	return &CachedAPIKeys{
		APIKeyStore: inner,
		keys: readcache.New[string, repository.APIKey](registry, readcache.Options[repository.APIKey]{
			Name:  "api_key_principals",
			Scope: repository.CacheScopeAPIKey,
			Match: func(key repository.APIKey, id string) bool { return key.ID == id },
			Valid: func(key repository.APIKey, now time.Time) bool {
				return key.ExpiresAt == nil || now.Before(*key.ExpiresAt)
			},
			IsNegative: func(err error) bool { return errors.Is(err, repository.ErrNotFound) },
		}),
		usage: usage, interval: lastUsedInterval, logger: logger, now: time.Now, pending: map[string]time.Time{},
	}
}

// FindActiveAPIKeyByHash returns the active key and remembers its use.
func (c *CachedAPIKeys) FindActiveAPIKeyByHash(ctx context.Context, hash string) (repository.APIKey, error) {
	key, err := c.keys.Get(ctx, hash, func(ctx context.Context) (repository.APIKey, error) {
		return c.APIKeyStore.FindActiveAPIKeyByHash(ctx, hash)
	})
	if err != nil {
		return repository.APIKey{}, err
	}
	c.touch(key.ID)
	key.Roles = append([]string(nil), key.Roles...)
	return key, nil
}

// CreateAPIKey evicts a cached not-found result for the new key's hash.
func (c *CachedAPIKeys) CreateAPIKey(ctx context.Context, key repository.APIKey) (repository.APIKey, error) {
	created, err := c.APIKeyStore.CreateAPIKey(ctx, key)
	if err == nil {
		c.keys.Evict(key.SecretHash)
	}
	return created, err
}

func (c *CachedAPIKeys) touch(id string) {
	if c.usage == nil || id == "" {
		return
	}
	c.mu.Lock()
	c.pending[id] = c.now().UTC()
	c.mu.Unlock()
}

// Flush writes the remembered uses. A row already written within the interval,
// by this or another node, is left alone. A failed write is retried at the next
// flush.
func (c *CachedAPIKeys) Flush(ctx context.Context) error {
	if c.usage == nil {
		return nil
	}
	c.mu.Lock()
	batch := c.pending
	c.pending = map[string]time.Time{}
	c.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	uses := make([]repository.APIKeyUse, 0, len(batch))
	for id, at := range batch {
		uses = append(uses, repository.APIKeyUse{ID: id, At: at})
	}
	if err := c.usage.TouchAPIKeys(ctx, uses, c.now().UTC().Add(-c.interval)); err != nil {
		c.mu.Lock()
		for id, at := range batch {
			if current, ok := c.pending[id]; !ok || current.Before(at) {
				c.pending[id] = at
			}
		}
		c.mu.Unlock()
		return err
	}
	return nil
}

// Run flushes every interval until ctx is cancelled, then once more.
func (c *CachedAPIKeys) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if err := c.Flush(flushCtx); err != nil && c.logger != nil {
				c.logger.Warn("flush API key last used", "error", err)
			}
			cancel()
			return
		case <-ticker.C:
			if err := c.Flush(ctx); err != nil && c.logger != nil && ctx.Err() == nil {
				c.logger.Warn("flush API key last used", "error", err)
			}
		}
	}
}

// CachedServiceAccounts serves GetServiceAccount from the read cache. Updates
// evict through the store's invalidation, so the account state of a
// service-account credential is no staler than the authorization window.
type CachedServiceAccounts struct {
	repository.ServiceAccountStore
	accounts *readcache.Cache[string, repository.ServiceAccount]
}

// NewCachedServiceAccounts wraps inner.
func NewCachedServiceAccounts(inner repository.ServiceAccountStore, registry *readcache.Registry) *CachedServiceAccounts {
	return &CachedServiceAccounts{
		ServiceAccountStore: inner,
		accounts: readcache.New[string, repository.ServiceAccount](registry, readcache.Options[repository.ServiceAccount]{
			Name:       "service_accounts",
			Scope:      repository.CacheScopeServiceAccount,
			Match:      func(account repository.ServiceAccount, id string) bool { return account.ID == id },
			IsNegative: func(err error) bool { return errors.Is(err, repository.ErrNotFound) },
		}),
	}
}

// GetServiceAccount returns the cached account.
func (c *CachedServiceAccounts) GetServiceAccount(ctx context.Context, id string) (repository.ServiceAccount, error) {
	return c.accounts.Get(ctx, id, func(ctx context.Context) (repository.ServiceAccount, error) {
		return c.ServiceAccountStore.GetServiceAccount(ctx, id)
	})
}
