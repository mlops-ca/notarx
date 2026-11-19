package secrets

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// KeySource supplies the key material of the ring: the current key that Seal
// uses and the retired keys that Open still accepts. The ring is consumed only
// through this port, so a KMS or vault source can replace the env ring later
// without touching any caller of Seal or Open.
type KeySource interface {
	// Keys returns the current key, then any previous keys, as 32-byte AES-256
	// keys encoded as hex, base64 or raw text.
	Keys() (current string, previous []string, err error)
}

// EnvKeySource reads the ring from NOTARX_SETTINGS_ENCRYPTION_KEY (current)
// and NOTARX_SETTINGS_ENCRYPTION_KEY_PREVIOUS (comma-separated retired keys).
type EnvKeySource struct{}

func (EnvKeySource) Keys() (string, []string, error) {
	current := strings.TrimSpace(os.Getenv(KeyEnv))
	if current == "" {
		return "", nil, ErrKeyNotConfigured
	}
	var previous []string
	for _, raw := range strings.Split(os.Getenv(KeyPreviousEnv), ",") {
		if raw = strings.TrimSpace(raw); raw != "" {
			previous = append(previous, raw)
		}
	}
	return current, previous, nil
}

var (
	sourceMu sync.RWMutex
	source   KeySource = EnvKeySource{}
)

// SetKeySource replaces the process-wide key source and returns a function
// that restores the previous one. It exists for tests and for a future
// non-env source wired at startup.
func SetKeySource(next KeySource) (restore func()) {
	sourceMu.Lock()
	previous := source
	source = next
	sourceMu.Unlock()
	return func() {
		sourceMu.Lock()
		source = previous
		sourceMu.Unlock()
	}
}

type ringKey struct {
	id       string
	material []byte
}

// Ring is a validated set of keys with one current key.
type Ring struct {
	current ringKey
	keys    []ringKey // current first
}

func (r Ring) byID(id string) (ringKey, bool) {
	for _, key := range r.keys {
		if key.id == id {
			return key, true
		}
	}
	return ringKey{}, false
}

// ValidateRing checks that the configured ring is complete and well formed,
// without sealing or opening anything.
func ValidateRing() error {
	_, err := loadRing()
	return err
}

// invalidKeyError names the variable that holds the bad key.
type invalidKeyError struct{ env, detail string }

func (e *invalidKeyError) Error() string        { return e.env + " is invalid: " + e.detail }
func (e *invalidKeyError) Is(target error) bool { return target == ErrInvalidKey }

var (
	ringMu     sync.Mutex
	ringInput  string
	ringCached Ring
)

// loadRing returns the ring for the source's current key material. Parsing and
// key-id hashing are cached per distinct key material, because Open
// runs on request paths.
func loadRing() (Ring, error) {
	sourceMu.RLock()
	src := source
	sourceMu.RUnlock()
	current, previous, err := src.Keys()
	if err != nil {
		return Ring{}, err
	}
	input := current + "\x00" + strings.Join(previous, "\x00")
	ringMu.Lock()
	defer ringMu.Unlock()
	if ringInput == input && len(ringCached.keys) > 0 {
		return ringCached, nil
	}
	ring, err := buildRing(current, previous)
	if err != nil {
		return Ring{}, err
	}
	ringInput, ringCached = input, ring
	return ring, nil
}

func buildRing(current string, previous []string) (Ring, error) {
	material, err := parseKey(current)
	if err != nil {
		return Ring{}, &invalidKeyError{env: KeyEnv, detail: err.Error()}
	}
	ring := Ring{current: ringKey{id: keyID(material), material: material}}
	ring.keys = []ringKey{ring.current}
	for index, raw := range previous {
		material, err := parseKey(raw)
		if err != nil {
			return Ring{}, &invalidKeyError{env: KeyPreviousEnv, detail: fmt.Sprintf("entry %d: %s", index+1, err)}
		}
		key := ringKey{id: keyID(material), material: material}
		if _, dup := ring.byID(key.id); dup {
			continue
		}
		ring.keys = append(ring.keys, key)
	}
	return ring, nil
}
