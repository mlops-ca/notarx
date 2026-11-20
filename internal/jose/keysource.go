package jose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// JWKSMode selects where an issuer's verification keys come from.
type JWKSMode string

const (
	// ModeDiscovery fetches /.well-known/openid-configuration (or JWKSURI when set).
	ModeDiscovery JWKSMode = "discovery"
	// ModeStatic uses Issuer.StaticJWKS and never rotates on its own.
	ModeStatic JWKSMode = "static"
)

const maxNegativeKids = 256

// Metrics receives the operator-visible events ADR 0019 requires. Implementations must be
// safe for concurrent use and cheap.
type Metrics interface {
	// StaleKeysServed counts verifications that used expired keys because a refresh failed.
	StaleKeysServed(issuer string)
	// StaticKeyMiss counts tokens whose kid is absent from a static JWKS.
	StaticKeyMiss(issuer string)
	// KeyFetchFailed counts failed discovery or JWKS fetches.
	KeyFetchFailed(issuer string)
}

type noMetrics struct{}

func (noMetrics) StaleKeysServed(string) {}
func (noMetrics) StaticKeyMiss(string)   {}
func (noMetrics) KeyFetchFailed(string)  {}

// sourceConfig is the part of an Issuer that decides where keys come from; a change replaces the cached set.
type sourceConfig struct {
	issuer, mode, jwksURI, caBundle string
	staticJWKS                      []byte
	allowPrivate                    bool
}

func sourceOf(issuer Issuer) sourceConfig {
	return sourceConfig{issuer: issuer.Issuer, mode: string(issuer.Mode), jwksURI: issuer.JWKSURI, caBundle: issuer.CABundlePEM, staticJWKS: issuer.StaticJWKS, allowPrivate: issuer.AllowPrivateNetwork}
}

// same compares without hashing or copying: it runs on every verification.
func (s sourceConfig) same(o sourceConfig) bool {
	return s.issuer == o.issuer && s.mode == o.mode && s.jwksURI == o.jwksURI && s.caBundle == o.caBundle &&
		s.allowPrivate == o.allowPrivate && bytes.Equal(s.staticJWKS, o.staticJWKS)
}

type keySet struct {
	source sourceConfig
	static bool

	flight singleflight.Group

	mu           sync.Mutex
	keys         []Key
	fetchedAt    time.Time
	lastAttempt  time.Time
	lastKidFetch time.Time
	negative     map[string]time.Time
	jwksURI      string
	client       *http.Client
	// fetching is true while a refresh runs; callers that arrive meanwhile join it instead of being
	// rate limited. generation counts finished refreshes so a late joiner does not start another.
	fetching   bool
	generation uint64
	lastErr    error
}

// keySetFor returns the cached key set of an issuer, replacing it when the issuer's key
// source configuration changed.
func (v *Verifier) keySetFor(issuer Issuer) (*keySet, error) {
	source := sourceOf(issuer)
	v.mu.RLock()
	set, ok := v.sets[issuer.Issuer]
	v.mu.RUnlock()
	if ok && set.source.same(source) {
		return set, nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if set, ok := v.sets[issuer.Issuer]; ok && set.source.same(source) {
		return set, nil
	}
	source.staticJWKS = bytes.Clone(source.staticJWKS)
	set = &keySet{source: source, negative: map[string]time.Time{}}
	if issuer.Mode == ModeStatic {
		keys, err := ParseJWKS(issuer.StaticJWKS)
		if err != nil {
			return nil, badConfig("static jwks: " + err.Error())
		}
		set.static, set.keys = true, keys
	} else {
		if v.opts.HTTPClient != nil {
			set.client = v.opts.HTTPClient
		} else {
			client, err := newGuardedClient(issuer.AllowPrivateNetwork, issuer.CABundlePEM)
			if err != nil {
				return nil, err
			}
			set.client = client
		}
		set.jwksURI = issuer.JWKSURI
	}
	v.sets[issuer.Issuer] = set
	return set, nil
}

// find returns the key to try for kid. An empty kid is only accepted when the set holds
// exactly one key.
func (s *keySet) find(kid, alg string) (Key, bool) {
	if kid == "" {
		if len(s.keys) == 1 {
			return s.keys[0], true
		}
		return Key{}, false
	}
	want, _ := keyTypeFor(alg)
	var first *Key
	for i := range s.keys {
		if s.keys[i].KeyID != kid {
			continue
		}
		if keyType(s.keys[i].Key) == want {
			return s.keys[i], true
		}
		if first == nil {
			first = &s.keys[i]
		}
	}
	if first != nil {
		return *first, true
	}
	return Key{}, false
}

// lookup returns the verification key for kid, refreshing the set when required.
func (v *Verifier) lookup(ctx context.Context, issuer Issuer, kid, alg string) (Key, error) {
	set, err := v.keySetFor(issuer)
	if err != nil {
		return Key{}, err
	}
	now := v.opts.Now()
	set.mu.Lock()
	if set.static {
		key, ok := set.find(kid, alg)
		set.mu.Unlock()
		if !ok {
			v.opts.Metrics.StaticKeyMiss(issuer.Issuer)
			return Key{}, invalid("kid not in static jwks")
		}
		return key, nil
	}
	ttl := v.opts.JWKSTTL
	if issuer.JWKSTTL > 0 {
		ttl = issuer.JWKSTTL
	}
	fresh := !set.fetchedAt.IsZero() && now.Sub(set.fetchedAt) < ttl
	kidMiss := false
	if fresh {
		if key, ok := set.find(kid, alg); ok {
			set.mu.Unlock()
			return key, nil
		}
		if until, ok := set.negative[kid]; ok && now.Before(until) {
			set.mu.Unlock()
			return Key{}, invalid("unknown kid")
		}
		if !set.fetching {
			if !set.lastKidFetch.IsZero() && now.Sub(set.lastKidFetch) < v.opts.KidRefetchInterval {
				set.rememberMiss(kid, now.Add(v.opts.KidRefetchInterval))
				set.mu.Unlock()
				return Key{}, invalid("unknown kid")
			}
			set.lastKidFetch = now
		}
		kidMiss = true
	} else if !set.fetching && !set.lastAttempt.IsZero() && now.Sub(set.lastAttempt) < v.opts.FailedFetchRetry {
		// A refresh failed moments ago: do not hammer the issuer, use what we have. The failure is remembered for
		// FailedFetchRetry only (one second by default), so an issuer that recovers is served again at once.
		key, err := v.staleKey(set, issuer, kid, alg, now, ttl)
		set.mu.Unlock()
		return key, err
	}
	if !set.fetching {
		set.fetching, set.lastAttempt = true, now
	}
	generation := set.generation
	set.mu.Unlock()

	// Callers that need a refresh while one runs wait for it (singleflight), so a restart or a key
	// rotation does not turn the first concurrent requests into failures.
	_, fetchErr, _ := set.flight.Do("fetch", func() (any, error) {
		set.mu.Lock()
		if set.generation != generation {
			err := set.lastErr
			set.mu.Unlock()
			return nil, err
		}
		set.mu.Unlock()
		err := v.fetch(context.WithoutCancel(ctx), set, issuer)
		set.mu.Lock()
		set.generation++
		set.fetching, set.lastErr = false, err
		set.mu.Unlock()
		return nil, err
	})

	set.mu.Lock()
	defer set.mu.Unlock()
	if fetchErr != nil {
		v.opts.Metrics.KeyFetchFailed(issuer.Issuer)
		if kidMiss {
			return Key{}, invalid("unknown kid")
		}
		key, err := v.staleKey(set, issuer, kid, alg, now, ttl)
		if err != nil && errors.Is(err, ErrKeySourceUnavailable) {
			return Key{}, unavailable("fetch failed", fetchErr)
		}
		return key, err
	}
	if key, ok := set.find(kid, alg); ok {
		return key, nil
	}
	set.rememberMiss(kid, now.Add(v.opts.KidRefetchInterval))
	return Key{}, invalid("unknown kid")
}

// staleKey serves an expired key set for up to JWKSStaleMax past its TTL. The caller holds set.mu.
func (v *Verifier) staleKey(set *keySet, issuer Issuer, kid, alg string, now time.Time, ttl time.Duration) (Key, error) {
	if set.fetchedAt.IsZero() || now.Sub(set.fetchedAt) >= ttl+v.opts.JWKSStaleMax {
		return Key{}, unavailable("no usable keys", nil)
	}
	key, ok := set.find(kid, alg)
	if !ok {
		return Key{}, invalid("unknown kid")
	}
	v.opts.Metrics.StaleKeysServed(issuer.Issuer)
	return key, nil
}

func (s *keySet) rememberMiss(kid string, until time.Time) {
	if len(s.negative) >= maxNegativeKids {
		s.negative = map[string]time.Time{}
	}
	s.negative[kid] = until
}

func (v *Verifier) fetch(ctx context.Context, set *keySet, issuer Issuer) error {
	set.mu.Lock()
	uri, client := set.jwksURI, set.client
	set.mu.Unlock()
	if uri == "" {
		discovered, err := discover(ctx, client, issuer.Issuer)
		if err != nil {
			return err
		}
		uri = discovered
	}
	body, err := get(ctx, client, uri)
	if err != nil {
		set.mu.Lock()
		set.jwksURI = issuer.JWKSURI // rediscover next time
		set.mu.Unlock()
		return err
	}
	keys, err := ParseJWKS(body)
	if err != nil {
		return err
	}
	set.mu.Lock()
	set.keys, set.jwksURI, set.fetchedAt = keys, uri, v.opts.Now()
	set.negative = map[string]time.Time{}
	set.mu.Unlock()
	return nil
}

func discover(ctx context.Context, client *http.Client, issuer string) (string, error) {
	body, err := get(ctx, client, strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration")
	if err != nil {
		return "", err
	}
	var document struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return "", fmt.Errorf("jose: discovery document is not JSON: %w", err)
	}
	if document.Issuer != issuer {
		return "", errors.New("jose: discovery issuer does not match the configured issuer")
	}
	if document.JWKSURI == "" {
		return "", errors.New("jose: discovery document has no jwks_uri")
	}
	return document.JWKSURI, nil
}

func get(ctx context.Context, client *http.Client, uri string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jose: %s returned %d", uri, response.StatusCode)
	}
	return readLimited(response.Body)
}
