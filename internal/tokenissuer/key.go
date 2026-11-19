package tokenissuer

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/secrets"
)

// State is the lifecycle state of a signing key.
type State string

const (
	// StateNext keys are published in the JWKS and never sign.
	StateNext State = "next"
	// StateActive keys sign and are published.
	StateActive State = "active"
	// StateRetired keys are published and never sign until they expire.
	StateRetired State = "retired"
)

const (
	// SealingLocal marks a private key sealed with the local secrets key-ring.
	SealingLocal = "local"
	// DefaultRSABits is the RSA modulus size of RS256 keys (ADR 0020, question 2).
	DefaultRSABits = 3072

	sealPurposePrefix = "token-signing-key:"
	integrityPurpose  = "token-signing-key-integrity"
)

// ErrIntegrity reports a key row whose integrity_mac does not match its content: it never
// signs and is never published.
var ErrIntegrity = errors.New("tokenissuer: signing key integrity check failed")

// KeyRecord is one row of token_signing_keys. Zero times mean NULL.
type KeyRecord struct {
	KID           string
	Alg           string
	State         State
	PublicJWK     []byte
	PrivateSealed string
	SealingRef    string
	IntegrityMAC  string
	CreatedAt     time.Time
	PublishedAt   time.Time
	ActivatedAt   time.Time
	RetiredAt     time.Time
	ExpiresAt     time.Time
}

// Expired reports whether the key has left the JWKS.
func (r KeyRecord) Expired(now time.Time) bool {
	return !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt)
}

// Published reports whether the key is visible in the JWKS at now.
func (r KeyRecord) Published(now time.Time) bool { return !r.Expired(now) }

// SealPurpose is the secrets purpose that binds a sealed private key to its kid.
func SealPurpose(kid string) string { return sealPurposePrefix + kid }

// CanonicalJWK re-encodes a JWK with sorted keys and no insignificant whitespace, so the
// integrity MAC survives a jsonb round trip.
func CanonicalJWK(raw []byte) (string, error) {
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return "", fmt.Errorf("tokenissuer: public jwk is not JSON: %w", err)
	}
	out, err := json.Marshal(generic) // map keys are emitted sorted
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// MACFields are the inputs of the integrity MAC: kid, alg, state and the canonical public
// JWK, exactly as ADR 0020 specifies.
func (r KeyRecord) MACFields() ([]string, error) {
	jwk, err := CanonicalJWK(r.PublicJWK)
	if err != nil {
		return nil, err
	}
	return []string{r.KID, r.Alg, string(r.State), jwk}, nil
}

// Seal computes the integrity MAC with the current ring key and stores it in the record.
func (r *KeyRecord) Seal() error {
	fields, err := r.MACFields()
	if err != nil {
		return err
	}
	mac, err := secrets.MAC(integrityPurpose, fields...)
	if err != nil {
		return err
	}
	r.IntegrityMAC = mac
	return nil
}

// VerifyIntegrity checks the MAC and that the kid is the thumbprint of the public JWK, so
// neither a swapped public key nor a swapped state passes.
func (r KeyRecord) VerifyIntegrity() error {
	fields, err := r.MACFields()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrIntegrity, err)
	}
	if err := secrets.VerifyMAC(integrityPurpose, r.IntegrityMAC, fields...); err != nil {
		if errors.Is(err, secrets.ErrMACInvalid) {
			return fmt.Errorf("%w: kid %s", ErrIntegrity, r.KID)
		}
		return err
	}
	public, err := r.PublicKey()
	if err != nil {
		return fmt.Errorf("%w: kid %s: %v", ErrIntegrity, r.KID, err)
	}
	thumb, err := jose.Thumbprint(public.Key)
	if err != nil || thumb != r.KID || public.KeyID != r.KID || public.Algorithm != r.Alg {
		return fmt.Errorf("%w: kid %s does not match its public key", ErrIntegrity, r.KID)
	}
	return nil
}

// PublicKey parses the public JWK.
func (r KeyRecord) PublicKey() (jose.Key, error) {
	var key jose.Key
	if err := json.Unmarshal(r.PublicJWK, &key); err != nil || !key.Valid() || !key.IsPublic() {
		return jose.Key{}, errors.New("invalid public jwk")
	}
	return key, nil
}

// Generator creates signing keys.
type Generator struct {
	// RSABits is the RS256 modulus size; zero means DefaultRSABits. Values below 2048 are refused.
	RSABits int
}

// Generate creates a key of alg, seals its private half and returns the record in state
// with a valid integrity MAC. The record carries no timestamps beyond CreatedAt.
func (g Generator) Generate(alg string, state State, now time.Time) (KeyRecord, error) {
	var private crypto.Signer
	switch alg {
	case jose.RS256:
		bits := g.RSABits
		if bits == 0 {
			bits = DefaultRSABits
		}
		if bits < jose.MinRSABits {
			return KeyRecord{}, errors.New("tokenissuer: rsa keys must be at least 2048 bits")
		}
		key, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			return KeyRecord{}, fmt.Errorf("tokenissuer: generate rsa key: %w", err)
		}
		private = key
	case jose.ES256:
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return KeyRecord{}, fmt.Errorf("tokenissuer: generate ecdsa key: %w", err)
		}
		private = key
	default:
		return KeyRecord{}, fmt.Errorf("tokenissuer: unsupported algorithm %q", alg)
	}
	public, err := jose.PublicKey(private.Public(), "", alg)
	if err != nil {
		return KeyRecord{}, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return KeyRecord{}, fmt.Errorf("tokenissuer: marshal private key: %w", err)
	}
	sealed, err := secrets.Seal(SealPurpose(public.KeyID), base64.StdEncoding.EncodeToString(der))
	if err != nil {
		return KeyRecord{}, err
	}
	rawJWK, err := json.Marshal(public)
	if err != nil {
		return KeyRecord{}, err
	}
	record := KeyRecord{
		KID: public.KeyID, Alg: alg, State: state, PublicJWK: rawJWK,
		PrivateSealed: sealed, SealingRef: SealingLocal, CreatedAt: now.UTC(),
	}
	if err := record.Seal(); err != nil {
		return KeyRecord{}, err
	}
	return record, nil
}

// Signer unseals the private key of record and returns a JOSE signer for it. The record's
// integrity is verified first, so a row that fails never signs.
func (r KeyRecord) Signer() (*jose.Signer, error) {
	if err := r.VerifyIntegrity(); err != nil {
		return nil, err
	}
	if r.SealingRef != SealingLocal || r.PrivateSealed == "" {
		return nil, fmt.Errorf("tokenissuer: key %s is not locally sealed", r.KID)
	}
	encoded, err := secrets.Open(SealPurpose(r.KID), r.PrivateSealed)
	if err != nil {
		return nil, fmt.Errorf("tokenissuer: unseal key %s: %w", r.KID, err)
	}
	der, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("tokenissuer: key %s is corrupt", r.KID)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("tokenissuer: key %s is corrupt", r.KID)
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("tokenissuer: key %s cannot sign", r.KID)
	}
	return jose.NewSigner(r.Alg, signer, r.KID)
}

// sortRecords orders records by creation time then kid, so listings are deterministic.
func sortRecords(records []KeyRecord) {
	sort.Slice(records, func(i, j int) bool {
		if !records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].CreatedAt.Before(records[j].CreatedAt)
		}
		return records[i].KID < records[j].KID
	})
}
