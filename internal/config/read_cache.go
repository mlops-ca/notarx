package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Bounds of NOTARX_AUTHZ_CACHE_TTL (ADR 0023 section 6): 0 disables every read
// cache, otherwise 1s to 30s.
const (
	MinAuthzCacheTTL = time.Second
	MaxAuthzCacheTTL = 30 * time.Second
)

// loadReadCacheConfig reads the read-cache settings of ADR 0023 section 6:
// NOTARX_AUTHZ_CACHE_TTL, NOTARX_API_KEY_LAST_USED_INTERVAL and
// NOTARX_OVERVIEW_CACHE_TTL. NOTARX_CACHE_POLL_INTERVAL arrives with the epoch
// poller wiring for engines without push notifications.
func loadReadCacheConfig(cfg *Config) error {
	var err error
	if cfg.AuthzCacheTTL, err = authzCacheTTLEnv("NOTARX_AUTHZ_CACHE_TTL", cfg.AuthzCacheTTL); err != nil {
		return err
	}
	if cfg.APIKeyLastUsedInterval, err = durationEnv("NOTARX_API_KEY_LAST_USED_INTERVAL", cfg.APIKeyLastUsedInterval); err != nil {
		return err
	}
	if cfg.OverviewCacheTTL, err = durationEnv("NOTARX_OVERVIEW_CACHE_TTL", cfg.OverviewCacheTTL); err != nil {
		return err
	}
	return nil
}

func authzCacheTTLEnv(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	var ttl time.Duration
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		ttl = time.Duration(seconds) * time.Second
	} else if ttl, err = time.ParseDuration(raw); err != nil {
		return 0, fmt.Errorf("%s must be 0 or a duration from 1s to 30s", name)
	}
	if ttl != 0 && (ttl < MinAuthzCacheTTL || ttl > MaxAuthzCacheTTL) {
		return 0, fmt.Errorf("%s must be 0 (caches disabled) or between 1s and 30s", name)
	}
	return ttl, nil
}
