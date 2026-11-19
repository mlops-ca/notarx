package secrets

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
)

const macPrefix = "m1:"

// ErrMACInvalid reports a MAC that does not match its fields, or that was made with a key
// that is not in the ring.
var ErrMACInvalid = errors.New("integrity mac is invalid")

// MAC returns an HMAC-SHA256 over fields, keyed by a key derived from the current ring key
// and the purpose, in the form m1:<ring key id>:<base64url>. The ring key id lets VerifyMAC
// pick the right key after a rotation. Fields are length prefixed, so ("ab","c") and
// ("a","bc") never collide. The result is not secret and may be stored next to the data.
func MAC(purpose string, fields ...string) (string, error) {
	ring, err := loadRing()
	if err != nil {
		return "", err
	}
	return macWith(ring.current, purpose, fields), nil
}

// VerifyMAC checks a MAC produced by MAC against fields, accepting any key of the ring.
func VerifyMAC(purpose, mac string, fields ...string) error {
	ring, err := loadRing()
	if err != nil {
		return err
	}
	kid, ok := macKeyID(mac)
	if !ok {
		return ErrMACInvalid
	}
	key, ok := ring.byID(kid)
	if !ok {
		return ErrMACInvalid
	}
	if !hmac.Equal([]byte(macWith(key, purpose, fields)), []byte(mac)) {
		return ErrMACInvalid
	}
	return nil
}

// MACNeedsRefresh reports whether a MAC was made with a key other than the current one, so a
// reseal pass can recompute it before the retired key is dropped.
func MACNeedsRefresh(mac string) (bool, error) {
	ring, err := loadRing()
	if err != nil {
		return false, err
	}
	kid, ok := macKeyID(mac)
	return !ok || kid != ring.current.id, nil
}

func macKeyID(mac string) (string, bool) {
	rest, ok := strings.CutPrefix(mac, macPrefix)
	if !ok {
		return "", false
	}
	kid, _, ok := strings.Cut(rest, ":")
	return kid, ok && kid != ""
}

func macWith(key ringKey, purpose string, fields []string) string {
	derive := hmac.New(sha256.New, key.material)
	derive.Write([]byte("notarx-secrets-mac-v1\x00" + purpose))
	mac := hmac.New(sha256.New, derive.Sum(nil))
	var length [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		mac.Write(length[:])
		mac.Write([]byte(field))
	}
	return macPrefix + key.id + ":" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
