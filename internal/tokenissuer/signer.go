package tokenissuer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
)

// ErrNoActiveKey reports that no usable signing key exists for the configured algorithm.
var ErrNoActiveKey = errors.New("tokenissuer: no active signing key")

// DefaultKeyCacheTTL is how long a node keeps its active key (ADR 0020: at most 30 s).
const DefaultKeyCacheTTL = 30 * time.Second

// Signer is the port that signs assertions. A KMS implementation can replace the
// PostgreSQL-sealed LocalSigner without touching callers.
type Signer interface {
	// Sign returns the compact JWT and the kid of the key that signed it.
	Sign(ctx context.Context, claims Claims) (compact string, kid string, err error)
	// PublicKeys returns the keys that verifiers may trust right now.
	PublicKeys(ctx context.Context) []jose.Key
	// Algorithm is the algorithm of the tokens Sign produces.
	Algorithm() string
}

// KeyReader is the read side of the Store.
type KeyReader interface {
	ListKeys(ctx context.Context) ([]KeyRecord, error)
}

// KeyStore is what a LocalSigner reads: full rows to sign, public rows to publish.
type KeyStore interface {
	KeyReader
	PublicKeyReader
}

// IntegrityAlert is called for a key row that fails its integrity check.
type IntegrityAlert func(kid string, err error)

// LogIntegrityAlert logs the failure at error level, the alert operators watch for.
func LogIntegrityAlert(kid string, err error) {
	slog.Error("signing key failed its integrity check and is ignored", "kid", kid, "error", err)
}

// SignerConfig configures a LocalSigner.
type SignerConfig struct {
	Alg      string
	CacheTTL time.Duration
	Now      func() time.Time
	Alert    IntegrityAlert
}

// LocalSigner signs with keys sealed in the database. It caches the unsealed active key for
// CacheTTL; since every key is published for the whole lead time before it activates, a
// node with a stale cache still signs with a key every verifier already trusts.
type LocalSigner struct {
	store  KeyStore
	config SignerConfig

	mu     sync.Mutex
	cached *cachedKey
}

type cachedKey struct {
	signer *jose.Signer
	record KeyRecord
	until  time.Time
}

// NewLocalSigner returns a signer for config.Alg.
func NewLocalSigner(store KeyStore, config SignerConfig) (*LocalSigner, error) {
	if config.Alg != jose.RS256 && config.Alg != jose.ES256 {
		return nil, errors.New("tokenissuer: signing algorithm must be RS256 or ES256")
	}
	if config.CacheTTL <= 0 || config.CacheTTL > DefaultKeyCacheTTL {
		config.CacheTTL = DefaultKeyCacheTTL
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Alert == nil {
		config.Alert = LogIntegrityAlert
	}
	return &LocalSigner{store: store, config: config}, nil
}

// Algorithm implements Signer.
func (s *LocalSigner) Algorithm() string { return s.config.Alg }

// Sign implements Signer.
func (s *LocalSigner) Sign(ctx context.Context, claims Claims) (string, string, error) {
	key, err := s.activeKey(ctx)
	if err != nil {
		return "", "", err
	}
	compact, err := key.Sign(claims)
	if err != nil {
		return "", "", err
	}
	return compact, key.KeyID(), nil
}

// PublicKeys implements Signer. A store failure yields no keys, never stale ones.
func (s *LocalSigner) PublicKeys(ctx context.Context) []jose.Key {
	records, err := s.store.ListPublicKeys(ctx)
	if err != nil {
		return nil
	}
	return PublishedKeys(records, s.config.Now(), s.config.Alert)
}

// Invalidate drops the cached signing key. The key manager calls it after a rotation or revoke
// on this node, so a revoked key stops signing here at once instead of after the cache TTL.
func (s *LocalSigner) Invalidate() {
	s.mu.Lock()
	s.cached = nil
	s.mu.Unlock()
}

func (s *LocalSigner) activeKey(ctx context.Context) (*jose.Signer, error) {
	now := s.config.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && now.Before(s.cached.until) && !s.cached.record.Expired(now) {
		return s.cached.signer, nil
	}
	s.cached = nil
	records, err := s.store.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	var failure error
	for _, record := range records {
		if record.Alg != s.config.Alg || record.State != StateActive || record.Expired(now) {
			continue
		}
		signer, err := record.Signer()
		if err != nil {
			if errors.Is(err, ErrIntegrity) {
				s.config.Alert(record.KID, err)
			} else {
				// An undecryptable key (changed encryption key without reseal, corrupt blob) must
				// not look like a missing key: name it so operators can act.
				slog.Error("active signing key cannot be used; signing fails closed", "kid", record.KID, "error", err)
			}
			failure = fmt.Errorf("key %s: %w", record.KID, err)
			continue
		}
		s.cached = &cachedKey{signer: signer, record: record, until: now.Add(s.config.CacheTTL)}
		return signer, nil
	}
	if failure != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoActiveKey, failure)
	}
	return nil, ErrNoActiveKey
}

// PublishedKeys returns the public JWKs of next, active and unexpired retired keys that pass
// the integrity check; failing rows are reported to alert and omitted.
func PublishedKeys(records []KeyRecord, now time.Time, alert IntegrityAlert) []jose.Key {
	keys := make([]jose.Key, 0, len(records))
	for _, record := range records {
		if !record.Published(now) {
			continue
		}
		if err := record.VerifyIntegrity(); err != nil {
			if alert != nil && errors.Is(err, ErrIntegrity) {
				alert(record.KID, err)
			}
			continue
		}
		key, err := record.PublicKey()
		if err != nil {
			continue
		}
		keys = append(keys, key)
	}
	return keys
}
