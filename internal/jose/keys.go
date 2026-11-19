package jose

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	gojose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/cryptosigner"
)

// Supported signature algorithms.
const (
	RS256 = "RS256"
	ES256 = "ES256"
	EdDSA = "EdDSA"
)

// DefaultAlgorithms is the allow-list of an issuer that does not restrict itself.
func DefaultAlgorithms() []string { return []string{RS256, ES256, EdDSA} }

// MinRSABits is the smallest accepted RSA modulus.
const MinRSABits = 2048

// Key is a JSON Web Key. It aliases the go-jose type so callers need no direct dependency.
type Key = gojose.JSONWebKey

// KeyTypeFor returns the JWK kty an algorithm requires.
func keyTypeFor(alg string) (string, bool) {
	switch alg {
	case RS256:
		return "RSA", true
	case ES256:
		return "EC", true
	case EdDSA:
		return "OKP", true
	}
	return "", false
}

func keyType(key any) string {
	switch key.(type) {
	case *rsa.PublicKey, *rsa.PrivateKey:
		return "RSA"
	case *ecdsa.PublicKey, *ecdsa.PrivateKey:
		return "EC"
	case ed25519.PublicKey, ed25519.PrivateKey:
		return "OKP"
	}
	return ""
}

// checkKeyForAlg enforces that the key matches the algorithm and meets the hardening floor.
func checkKeyForAlg(key Key, alg string) error {
	want, ok := keyTypeFor(alg)
	if !ok {
		return invalid("algorithm not supported")
	}
	if keyType(key.Key) != want {
		return invalid("key type does not match alg")
	}
	if key.Algorithm != "" && key.Algorithm != alg {
		return invalid("key alg does not match token alg")
	}
	if key.Use != "" && key.Use != "sig" {
		return invalid("key is not a signature key")
	}
	switch k := key.Key.(type) {
	case *rsa.PublicKey:
		if k.N.BitLen() < MinRSABits {
			return invalid("rsa key is smaller than 2048 bits")
		}
	case *ecdsa.PublicKey:
		if k.Curve != elliptic.P256() {
			return invalid("ec key is not P-256")
		}
	}
	return nil
}

// PublicKey returns the JWK of a public key with the given key id and algorithm. An empty
// kid defaults to the RFC 7638 thumbprint.
func PublicKey(pub crypto.PublicKey, kid, alg string) (Key, error) {
	if _, ok := keyTypeFor(alg); !ok {
		return Key{}, badConfig("unsupported algorithm " + alg)
	}
	key := Key{Key: pub, Algorithm: alg, Use: "sig"}
	if keyType(pub) == "" {
		return Key{}, badConfig("unsupported public key type")
	}
	if kid == "" {
		thumb, err := Thumbprint(pub)
		if err != nil {
			return Key{}, err
		}
		kid = thumb
	}
	key.KeyID = kid
	return key, nil
}

// Thumbprint returns the base64url RFC 7638 SHA-256 thumbprint of a public key.
func Thumbprint(pub crypto.PublicKey) (string, error) {
	key := Key{Key: pub}
	sum, err := key.Thumbprint(crypto.SHA256)
	if err != nil {
		return "", fmt.Errorf("jose: thumbprint: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(sum), nil
}

// MarshalJWKS encodes public keys as a JWKS document. Private keys are refused so a
// signing key can never be published by accident.
func MarshalJWKS(keys ...Key) ([]byte, error) {
	set := gojose.JSONWebKeySet{Keys: make([]gojose.JSONWebKey, 0, len(keys))}
	for _, key := range keys {
		if !key.IsPublic() {
			return nil, badConfig("JWKS may contain public keys only")
		}
		if !key.Valid() {
			return nil, badConfig("invalid JWK")
		}
		set.Keys = append(set.Keys, key)
	}
	return json.Marshal(set)
}

// ParseJWKS decodes a JWKS document. Keys that cannot be parsed, private keys and keys
// not meant for signatures are skipped instead of failing the whole set, so one
// unsupported key in a provider's set does not lock every workload out.
func ParseJWKS(data []byte) ([]Key, error) {
	var document struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("jose: jwks is not JSON: %w", err)
	}
	keys := make([]Key, 0, len(document.Keys))
	for _, raw := range document.Keys {
		var key Key
		if json.Unmarshal(raw, &key) != nil || !key.Valid() || !key.IsPublic() || (key.Use != "" && key.Use != "sig") {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, errors.New("jose: jwks contains no usable key")
	}
	return keys, nil
}

// Signer signs compact JWTs with one key.
type Signer struct {
	signer gojose.Signer
	alg    string
	kid    string
}

// NewSigner returns a signer for alg using key (an *rsa.PrivateKey, *ecdsa.PrivateKey,
// ed25519.PrivateKey or any other crypto.Signer such as a KMS handle). An empty kid
// defaults to the key's RFC 7638 thumbprint.
func NewSigner(alg string, key crypto.Signer, kid string) (*Signer, error) {
	if _, ok := keyTypeFor(alg); !ok {
		return nil, badConfig("unsupported algorithm " + alg)
	}
	if keyType(key.Public()) != mustKeyType(alg) {
		return nil, badConfig("key type does not match alg")
	}
	public, err := PublicKey(key.Public(), kid, alg)
	if err != nil {
		return nil, err
	}
	if err := checkKeyForAlg(public, alg); err != nil {
		return nil, badConfig(err.Error())
	}
	opaque := cryptosigner.Opaque(key)
	signer, err := gojose.NewSigner(
		gojose.SigningKey{Algorithm: gojose.SignatureAlgorithm(alg), Key: gojose.JSONWebKey{Key: opaque, KeyID: public.KeyID, Algorithm: alg}},
		(&gojose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return nil, fmt.Errorf("jose: new signer: %w", err)
	}
	return &Signer{signer: signer, alg: alg, kid: public.KeyID}, nil
}

func mustKeyType(alg string) string {
	kty, _ := keyTypeFor(alg)
	return kty
}

// KeyID returns the kid placed in the header of every token.
func (s *Signer) KeyID() string { return s.kid }

// Algorithm returns the alg placed in the header of every token.
func (s *Signer) Algorithm() string { return s.alg }

// Sign serialises claims as JSON and returns the compact JWT.
func (s *Signer) Sign(claims any) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("jose: marshal claims: %w", err)
	}
	object, err := s.signer.Sign(payload)
	if err != nil {
		return "", fmt.Errorf("jose: sign: %w", err)
	}
	return object.CompactSerialize()
}
