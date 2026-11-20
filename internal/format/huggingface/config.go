package huggingface

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Environment variables of the format (ADR 0027).
const (
	EnvRefTTL               = "NOTARX_HF_REF_TTL"
	EnvServeStale           = "NOTARX_HF_SERVE_STALE"
	EnvMaxFilesPerCommit    = "NOTARX_HF_MAX_FILES_PER_COMMIT"
	EnvCDNAllowlist         = "NOTARX_HF_CDN_ALLOWLIST"
	EnvMaxObjectBytes       = "NOTARX_HF_MAX_OBJECT_BYTES"
	EnvMaxConcurrentFetches = "NOTARX_HF_MAX_CONCURRENT_FETCHES"
)

// Defaults and bounds of the configuration.
const (
	DefaultUpstream             = "https://huggingface.co"
	DefaultRefTTL               = 5 * time.Minute
	DefaultMaxFilesPerCommit    = 200000
	DefaultMaxObjectBytes       = int64(200) << 30
	HardMaxObjectBytes          = int64(1) << 40
	DefaultMaxConcurrentFetches = 4
)

// DefaultCDNAllowlist is the redirect target allowlist that applies to the default
// upstream only.
var DefaultCDNAllowlist = []string{"cdn-lfs.huggingface.co", "cdn-lfs-us-1.hf.co", "*.hf.co", "*.xethub.hf.co"}

// Config holds the deployment settings of the format.
type Config struct {
	// RefTTL is how long a branch or tag resolution stays fresh before it is revalidated
	// against upstream.
	RefTTL time.Duration
	// ServeStale serves an expired ref resolution (with X-Notarx-Stale) while upstream is
	// unreachable.
	ServeStale bool
	// MaxFilesPerCommit bounds the tree of a commit; a larger tree is refused with 502
	// upstream_tree_too_large.
	MaxFilesPerCommit int
	// CDNAllowlist is the explicit allowlist of redirect hosts; nil selects the default for
	// the default upstream and no redirect for any other upstream.
	CDNAllowlist []string
	// MaxObjectBytes replaces the 1 GiB Raw cache cap for Hugging Face objects.
	MaxObjectBytes int64
	// MaxConcurrentFetches bounds the cold object fetches of one process.
	MaxConcurrentFetches int
}

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{
		RefTTL:               DefaultRefTTL,
		ServeStale:           true,
		MaxFilesPerCommit:    DefaultMaxFilesPerCommit,
		MaxObjectBytes:       DefaultMaxObjectBytes,
		MaxConcurrentFetches: DefaultMaxConcurrentFetches,
	}
}

// CDNAllowlistFor returns the redirect allowlist for an upstream endpoint. The default
// list applies only to the default upstream: an overridden upstream follows no redirect
// until the operator sets NOTARX_HF_CDN_ALLOWLIST explicitly.
func (c Config) CDNAllowlistFor(endpoint string) []string {
	if c.CDNAllowlist != nil {
		return c.CDNAllowlist
	}
	if sameOrigin(endpoint, DefaultUpstream) {
		return DefaultCDNAllowlist
	}
	return nil
}

func sameOrigin(a, b string) bool {
	left, err1 := url.Parse(a)
	right, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && left.Scheme == right.Scheme && strings.EqualFold(left.Host, right.Host)
}

// LoadConfig reads the NOTARX_HF_* variables through lookup; an empty value keeps the
// default, an invalid one is an error naming the variable.
func LoadConfig(lookup func(string) (string, bool)) (Config, error) {
	cfg := DefaultConfig()
	get := func(name string) (string, bool) {
		value, ok := lookup(name)
		value = strings.TrimSpace(value)
		return value, ok && value != ""
	}
	if value, ok := get(EnvRefTTL); ok {
		ttl, err := time.ParseDuration(value)
		if err != nil || ttl < 0 {
			return Config{}, fmt.Errorf("%s must be a non-negative duration such as 5m", EnvRefTTL)
		}
		cfg.RefTTL = ttl
	}
	if value, ok := get(EnvServeStale); ok {
		stale, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s must be true or false", EnvServeStale)
		}
		cfg.ServeStale = stale
	}
	if value, ok := get(EnvMaxFilesPerCommit); ok {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("%s must be a positive integer", EnvMaxFilesPerCommit)
		}
		cfg.MaxFilesPerCommit = n
	}
	if value, ok := get(EnvCDNAllowlist); ok {
		cfg.CDNAllowlist = []string{}
		for _, host := range strings.Split(value, ",") {
			if host = strings.ToLower(strings.TrimSpace(host)); host != "" {
				if strings.ContainsAny(host, "/:@ ") {
					return Config{}, fmt.Errorf("%s entries must be host names or *.suffix patterns", EnvCDNAllowlist)
				}
				cfg.CDNAllowlist = append(cfg.CDNAllowlist, host)
			}
		}
	}
	if value, ok := get(EnvMaxObjectBytes); ok {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 1 || n > HardMaxObjectBytes {
			return Config{}, fmt.Errorf("%s must be an integer from 1 to %d (1 TiB)", EnvMaxObjectBytes, HardMaxObjectBytes)
		}
		cfg.MaxObjectBytes = n
	}
	if value, ok := get(EnvMaxConcurrentFetches); ok {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("%s must be a positive integer", EnvMaxConcurrentFetches)
		}
		cfg.MaxConcurrentFetches = n
	}
	return cfg, nil
}
