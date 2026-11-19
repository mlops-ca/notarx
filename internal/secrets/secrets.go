// Package secrets encrypts small application settings before persistence.
//
// Sealed values come in two formats. The current format is
// "v1:<kid>:<base64>", where kid names the ring key that sealed the value.
// The legacy format is plain standard base64 sealed by the single key of the
// pre-ring releases; it stays readable and is rewritten by Reseal. Seal always
// uses the current key. Open selects the key by kid (legacy values try each
// ring key). The purpose is bound as AES-256-GCM additional data in both
// formats, so a ciphertext cannot be replayed into another setting.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Environment variables read by the env key source.
const (
	KeyEnv = "NOTARX_SETTINGS_ENCRYPTION_KEY"
	// KeyPreviousEnv holds retired keys, comma separated, that stay readable
	// while values sealed with them are resealed.
	KeyPreviousEnv = "NOTARX_SETTINGS_ENCRYPTION_KEY_PREVIOUS"
)

const sealedPrefix = "v1:"

var ErrKeyNotConfigured = errors.New(KeyEnv + " is not configured")
var ErrInvalidKey = errors.New(KeyEnv + " is invalid")

// ErrUnknownKey reports a sealed value whose key id is not in the ring.
var ErrUnknownKey = errors.New("encrypted setting was sealed with a key that is not in the ring")

// Seal encrypts plaintext with the current ring key. An empty plaintext seals
// to the empty string.
func Seal(purpose, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	ring, err := loadRing()
	if err != nil {
		return "", err
	}
	return ring.seal(purpose, plaintext)
}

// Open decrypts a value produced by Seal, in either format. An empty value
// opens to the empty string.
func Open(purpose, encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	ring, err := loadRing()
	if err != nil {
		return "", err
	}
	return ring.open(purpose, encoded)
}

// NeedsReseal reports whether Reseal would change encoded: it is legacy or was
// sealed with a key other than the current one.
func NeedsReseal(encoded string) (bool, error) {
	if encoded == "" {
		return false, nil
	}
	ring, err := loadRing()
	if err != nil {
		return false, err
	}
	return ring.needsReseal(encoded), nil
}

// Reseal re-encrypts encoded with the current key and reports whether it
// changed. A value that is already current is returned unchanged.
func Reseal(purpose, encoded string) (string, bool, error) {
	if encoded == "" {
		return "", false, nil
	}
	ring, err := loadRing()
	if err != nil {
		return "", false, err
	}
	if !ring.needsReseal(encoded) {
		// Still prove the value opens, so a corrupt row is never reported as done.
		if _, err := ring.open(purpose, encoded); err != nil {
			return "", false, err
		}
		return encoded, false, nil
	}
	plaintext, err := ring.open(purpose, encoded)
	if err != nil {
		return "", false, err
	}
	sealed, err := ring.seal(purpose, plaintext)
	if err != nil {
		return "", false, err
	}
	return sealed, true, nil
}

func (r Ring) seal(purpose, plaintext string) (string, error) {
	gcm, err := newGCM(r.current.material)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), []byte(purpose))
	return sealedPrefix + r.current.id + ":" + base64.StdEncoding.EncodeToString(sealed), nil
}

func (r Ring) needsReseal(encoded string) bool {
	kid, _, ok := splitSealed(encoded)
	return !ok || kid != r.current.id
}

func (r Ring) open(purpose, encoded string) (string, error) {
	kid, body, versioned := splitSealed(encoded)
	payload, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("decode encrypted setting: %w", err)
	}
	if versioned {
		key, ok := r.byID(kid)
		if !ok {
			return "", ErrUnknownKey
		}
		return openWith(key, purpose, payload)
	}
	// Legacy values carry no key id: try the current key first, then the rest.
	for _, key := range r.keys {
		if plaintext, err := openWith(key, purpose, payload); err == nil {
			return plaintext, nil
		} else if errors.Is(err, errTruncated) {
			return "", err
		}
	}
	return "", errCannotDecrypt
}

var (
	errTruncated     = errors.New("encrypted setting is truncated")
	errCannotDecrypt = errors.New("encrypted setting cannot be decrypted with the configured key")
)

func openWith(key ringKey, purpose string, payload []byte) (string, error) {
	gcm, err := newGCM(key.material)
	if err != nil {
		return "", err
	}
	if len(payload) < gcm.NonceSize()+gcm.Overhead() {
		return "", errTruncated
	}
	plaintext, err := gcm.Open(nil, payload[:gcm.NonceSize()], payload[gcm.NonceSize():], []byte(purpose))
	if err != nil {
		return "", errCannotDecrypt
	}
	return string(plaintext), nil
}

// splitSealed separates a v1 value into key id and base64 body. A value
// without the prefix is legacy: base64 never contains a colon, so the two
// formats cannot be confused.
func splitSealed(encoded string) (kid, body string, versioned bool) {
	rest, ok := strings.CutPrefix(encoded, sealedPrefix)
	if !ok {
		return "", encoded, false
	}
	kid, body, ok = strings.Cut(rest, ":")
	if !ok {
		return "", encoded, false
	}
	return kid, body, true
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func parseKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if len(raw) == 32 {
		return []byte(raw), nil
	}
	return nil, fmt.Errorf("%w: must decode to 32 bytes (hex, base64, or raw)", ErrInvalidKey)
}

// keyID derives a stable, non-secret identifier from the key material. It is a
// truncated, domain-separated SHA-256, which reveals nothing usable about a
// 256-bit key.
func keyID(material []byte) string {
	sum := sha256.Sum256(append([]byte("notarx-secrets-kid-v1\x00"), material...))
	return hex.EncodeToString(sum[:6])
}
