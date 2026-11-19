package tokenissuer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/mlops-ca/notarx/internal/jose"
)

// DefaultJWKSCacheTTL bounds how stale the in-process JWKS can be on a node that did not make
// the change itself.
const DefaultJWKSCacheTTL = 30 * time.Second

// JWKSService serves the public key set from an in-process cache, so anonymous traffic never
// reaches the database per request. It reads public columns only and never holds its lock
// across a database call.
type JWKSService struct {
	Store PublicKeyReader
	TTL   time.Duration
	Now   func() time.Time
	Alert IntegrityAlert

	flight singleflight.Group
	mu     sync.Mutex
	body   []byte
	etag   string
	loaded time.Time
	epoch  uint64 // bumped by Invalidate so a load that raced with it is not cached
}

func (s *JWKSService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Invalidate drops the cached document; call it when keys change.
func (s *JWKSService) Invalidate() {
	s.mu.Lock()
	s.body, s.etag = nil, ""
	s.epoch++
	s.mu.Unlock()
}

func (s *JWKSService) cached() (body []byte, etag string, ok bool) {
	ttl := s.TTL
	if ttl <= 0 {
		ttl = DefaultJWKSCacheTTL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.body != nil && s.now().Sub(s.loaded) < ttl {
		return s.body, s.etag, true
	}
	return nil, "", false
}

// Document returns the JWKS body and its strong ETag.
func (s *JWKSService) Document(ctx context.Context) (body []byte, etag string, err error) {
	if body, etag, ok := s.cached(); ok {
		return body, etag, nil
	}
	type document struct {
		body []byte
		etag string
	}
	value, err, _ := s.flight.Do("jwks", func() (any, error) {
		if body, etag, ok := s.cached(); ok {
			return document{body, etag}, nil
		}
		s.mu.Lock()
		epoch := s.epoch
		s.mu.Unlock()
		// One caller giving up must not fail the callers that share this load.
		records, err := s.Store.ListPublicKeys(context.WithoutCancel(ctx))
		if err != nil {
			return nil, err
		}
		now := s.now()
		alert := s.Alert
		if alert == nil {
			alert = LogIntegrityAlert
		}
		doc, err := jose.MarshalJWKS(PublishedKeys(records, now, alert)...)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(doc)
		tag := `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`
		s.mu.Lock()
		if s.epoch == epoch {
			s.body, s.etag, s.loaded = doc, tag, now
		}
		s.mu.Unlock()
		return document{doc, tag}, nil
	})
	if err != nil {
		return nil, "", err
	}
	loaded := value.(document)
	return loaded.body, loaded.etag, nil
}
