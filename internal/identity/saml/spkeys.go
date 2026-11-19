package saml

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/secrets"
)

const (
	// SPKeyPurpose is the sealing purpose of the service provider key set. It is the provider secret
	// purpose of the registry, so notarxctl secrets reseal rewrites the sealed set with the rest.
	SPKeyPurpose = identity.SecretPurpose
	// MaxSPKeys is the number of service provider keys kept: the current key and the previous one,
	// both published in the SP metadata so an IdP that cached the old certificate keeps working.
	MaxSPKeys = 2
	spKeyBits = 2048
	spKeyLife = 5 * 365 * 24 * time.Hour
)

// SPKey is one notarx-generated service provider signing key. The private key exists only inside
// the sealed secret; API responses carry no field of it.
type SPKey struct {
	ID             string    `json:"id"`
	PrivateKeyPEM  string    `json:"privateKeyPem"`
	CertificatePEM string    `json:"certificatePem"`
	NotAfter       time.Time `json:"notAfter"`
}

// SPKeySet is the ordered key list; the last key signs, every key is published.
type SPKeySet struct {
	Keys []SPKey `json:"spKeys"`
}

// Signing returns the key that signs new requests.
func (s SPKeySet) Signing() (SPKey, bool) {
	if len(s.Keys) == 0 {
		return SPKey{}, false
	}
	return s.Keys[len(s.Keys)-1], true
}

// Rotated returns the set with a freshly generated key appended and the oldest dropped beyond MaxSPKeys.
func (s SPKeySet) Rotated(entityID string, now time.Time) (SPKeySet, error) {
	key, err := GenerateSPKey(entityID, now)
	if err != nil {
		return s, err
	}
	keys := append(append([]SPKey(nil), s.Keys...), key)
	if len(keys) > MaxSPKeys {
		keys = keys[len(keys)-MaxSPKeys:]
	}
	return SPKeySet{Keys: keys}, nil
}

// GenerateSPKey creates an RSA key and a self-signed certificate for it.
func GenerateSPKey(commonName string, now time.Time) (SPKey, error) {
	private, err := rsa.GenerateKey(rand.Reader, spKeyBits)
	if err != nil {
		return SPKey{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return SPKey{}, err
	}
	if len(commonName) > 64 {
		commonName = commonName[:64]
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: commonName, Organization: []string{"notarx"}},
		NotBefore: now.Add(-time.Hour).UTC(), NotAfter: now.Add(spKeyLife).UTC(),
		KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if err != nil {
		return SPKey{}, err
	}
	return SPKey{
		ID:             ids.New(),
		PrivateKeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(private)})),
		CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		NotAfter:       template.NotAfter,
	}, nil
}

func mustPKCS8(key *rsa.PrivateKey) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		// An RSA key generated above always marshals; a failure is a programming error.
		panic(err)
	}
	return der
}

// Seal encrypts the key set for storage in the provider's secrets_sealed column.
func (s SPKeySet) Seal() (string, error) {
	encoded, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return secrets.Seal(SPKeyPurpose, string(encoded))
}

// OpenSPKeySet decrypts a sealed key set. An empty value is an empty set.
func OpenSPKeySet(sealed string) (SPKeySet, error) {
	if sealed == "" {
		return SPKeySet{}, nil
	}
	plain, err := secrets.Open(SPKeyPurpose, sealed)
	if err != nil {
		return SPKeySet{}, err
	}
	var set SPKeySet
	if err := json.Unmarshal([]byte(plain), &set); err != nil {
		return SPKeySet{}, errors.New("saml: the sealed service provider key set is not valid")
	}
	for _, key := range set.Keys {
		if _, err := key.signer(); err != nil {
			return SPKeySet{}, err
		}
	}
	return set, nil
}

func (k SPKey) signer() (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(k.PrivateKeyPEM))
	if block == nil {
		return nil, errors.New("saml: a service provider key is not valid PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("saml: a service provider key cannot be parsed")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("saml: a service provider key is not an RSA key")
	}
	return key, nil
}

// Certificate returns the DER bytes of the key's certificate.
func (k SPKey) Certificate() ([]byte, error) {
	block, _ := pem.Decode([]byte(k.CertificatePEM))
	if block == nil {
		return nil, errors.New("saml: a service provider certificate is not valid PEM")
	}
	return block.Bytes, nil
}
