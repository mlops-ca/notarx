package tokenissuer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"sync"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/readcache"
)

// DefaultJWKSCacheTTL is the lifetime of the published key set on a service that has no shared registry.
const DefaultJWKSCacheTTL = 30 * time.Second

// CacheScope is the invalidation scope of the published signing key set. The database announces a change of any
// token_signing_keys row on it (migration 000007), and the key manager evicts the local node synchronously.
const CacheScope = "signing_keys"

// JWKSService serves the public key set from a read cache of internal/readcache (ADR 0023 section 6), so
// anonymous traffic never reaches the database per request. The entry lives at most the authorization window of
// the registry (NOTARX_AUTHZ_CACHE_TTL) and is evicted sooner by a key change on any node. It reads public
// columns only and never holds a lock across a database call.
type JWKSService struct {
	Store PublicKeyReader
	// Registry owns the cache, its TTL and its invalidation scope. A nil registry gives the service a private
	// one with DefaultJWKSCacheTTL, for callers that run without the shared read caches.
	Registry *readcache.Registry
	Now      func() time.Time
	Alert    IntegrityAlert

	once  sync.Once
	cache *readcache.Cache[struct{}, jwksDocument]
}

type jwksDocument struct {
	body []byte
	etag string
}

func (s *JWKSService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *JWKSService) init() {
	s.once.Do(func() {
		if s.Registry == nil {
			s.Registry = readcache.NewRegistry(DefaultJWKSCacheTTL)
		}
		s.cache = readcache.New[struct{}, jwksDocument](s.Registry, readcache.Options[jwksDocument]{
			Name: "signing_key_set", Scope: CacheScope, IgnoreBypass: true})
	})
}

// Invalidate drops the cached document on this node; call it when keys change.
func (s *JWKSService) Invalidate() {
	s.init()
	s.Registry.Invalidate(CacheScope, "")
}

// Document returns the JWKS body and its strong ETag.
func (s *JWKSService) Document(ctx context.Context) (body []byte, etag string, err error) {
	s.init()
	doc, err := s.cache.Get(ctx, struct{}{}, func(ctx context.Context) (jwksDocument, error) {
		// One caller giving up must not fail the callers that share this load.
		records, err := s.Store.ListPublicKeys(context.WithoutCancel(ctx))
		if err != nil {
			return jwksDocument{}, err
		}
		alert := s.Alert
		if alert == nil {
			alert = LogIntegrityAlert
		}
		doc, err := jose.MarshalJWKS(PublishedKeys(records, s.now(), alert)...)
		if err != nil {
			return jwksDocument{}, err
		}
		sum := sha256.Sum256(doc)
		return jwksDocument{body: doc, etag: `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`}, nil
	})
	if err != nil {
		return nil, "", err
	}
	return doc.body, doc.etag, nil
}
