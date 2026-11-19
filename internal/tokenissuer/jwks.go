package tokenissuer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"sync"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
)

// DefaultJWKSCacheTTL bounds how stale the in-process JWKS can be on a node that did not make
// the change itself.
const DefaultJWKSCacheTTL = 30 * time.Second

// JWKSService serves the public key set from an in-process cache, so anonymous traffic never
// reaches the database per request.
type JWKSService struct {
	Store KeyReader
	TTL   time.Duration
	Now   func() time.Time
	Alert IntegrityAlert

	mu     sync.Mutex
	body   []byte
	etag   string
	loaded time.Time
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
	s.mu.Unlock()
}

// Document returns the JWKS body and its strong ETag.
func (s *JWKSService) Document(ctx context.Context) (body []byte, etag string, err error) {
	ttl := s.TTL
	if ttl <= 0 {
		ttl = DefaultJWKSCacheTTL
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.body != nil && now.Sub(s.loaded) < ttl {
		return s.body, s.etag, nil
	}
	records, err := s.Store.ListKeys(ctx)
	if err != nil {
		return nil, "", err
	}
	alert := s.Alert
	if alert == nil {
		alert = LogIntegrityAlert
	}
	doc, err := jose.MarshalJWKS(PublishedKeys(records, now, alert)...)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(doc)
	s.body, s.etag, s.loaded = doc, `"`+base64.RawURLEncoding.EncodeToString(sum[:16])+`"`, now
	return s.body, s.etag, nil
}
