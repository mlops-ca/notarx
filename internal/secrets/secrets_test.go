package secrets

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

const (
	keyA = "0123456789abcdef0123456789abcdef"
	keyB = "fedcba9876543210fedcba9876543210"
	keyC = "abcdefghijklmnopqrstuvwxyz012345"
)

func TestSealAndOpenBindsCiphertextToPurpose(t *testing.T) {
	t.Setenv(KeyEnv, keyA)
	sealed, err := Seal("oidc.client-secret", "client-secret")
	if err != nil {
		t.Fatal(err)
	}
	if sealed == "client-secret" {
		t.Fatal("plaintext was not encrypted")
	}
	opened, err := Open("oidc.client-secret", sealed)
	if err != nil || opened != "client-secret" {
		t.Fatalf("opened=%q err=%v", opened, err)
	}
	if _, err := Open("another-purpose", sealed); err == nil {
		t.Fatal("ciphertext must not be reusable for another setting")
	}
}

func TestSealUsesVersionedFormatWithKeyID(t *testing.T) {
	t.Setenv(KeyEnv, keyA)
	sealed, err := Seal("p", "value")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(sealed, ":")
	if len(parts) != 3 || parts[0] != "v1" || parts[1] != keyID([]byte(keyA)) {
		t.Fatalf("sealed=%q", sealed)
	}
	if _, err := base64.StdEncoding.DecodeString(parts[2]); err != nil {
		t.Fatal(err)
	}
	again, _ := Seal("p", "value")
	if again == sealed {
		t.Fatal("nonce must differ between seals")
	}
}

func TestEmptyValuesPassThrough(t *testing.T) {
	t.Setenv(KeyEnv, "")
	if sealed, err := Seal("p", ""); sealed != "" || err != nil {
		t.Fatalf("sealed=%q err=%v", sealed, err)
	}
	if opened, err := Open("p", ""); opened != "" || err != nil {
		t.Fatalf("opened=%q err=%v", opened, err)
	}
	if out, changed, err := Reseal("p", ""); out != "" || changed || err != nil {
		t.Fatalf("out=%q changed=%v err=%v", out, changed, err)
	}
	if need, err := NeedsReseal(""); need || err != nil {
		t.Fatalf("need=%v err=%v", need, err)
	}
}

// legacySeal reproduces the pre-ring format: plain base64 of nonce||ciphertext.
func legacySeal(t *testing.T, key, purpose, plaintext string) string {
	t.Helper()
	gcm, err := newGCM([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	for i := range nonce {
		nonce[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plaintext), []byte(purpose)))
}

func TestOpenReadsLegacyFormat(t *testing.T) {
	legacy := legacySeal(t, keyA, "p", "old-secret")
	t.Setenv(KeyEnv, keyA)
	opened, err := Open("p", legacy)
	if err != nil || opened != "old-secret" {
		t.Fatalf("opened=%q err=%v", opened, err)
	}
	if need, err := NeedsReseal(legacy); !need || err != nil {
		t.Fatalf("legacy must need reseal: %v %v", need, err)
	}
	// Legacy sealed with a retired key is found by trying the ring.
	t.Setenv(KeyEnv, keyB)
	t.Setenv(KeyPreviousEnv, keyA)
	if opened, err := Open("p", legacy); err != nil || opened != "old-secret" {
		t.Fatalf("opened=%q err=%v", opened, err)
	}
	if _, err := Open("other", legacy); err == nil {
		t.Fatal("legacy values stay bound to purpose")
	}
}

func TestRotationSealResealDropOldKey(t *testing.T) {
	t.Setenv(KeyEnv, keyA)
	sealedA, err := Seal("p", "rotate-me")
	if err != nil {
		t.Fatal(err)
	}
	legacy := legacySeal(t, keyA, "p", "legacy-me")

	// Add key B as current, keep A as previous: old values still open.
	t.Setenv(KeyEnv, keyB)
	t.Setenv(KeyPreviousEnv, keyA)
	if v, err := Open("p", sealedA); err != nil || v != "rotate-me" {
		t.Fatalf("v=%q err=%v", v, err)
	}
	fresh, _ := Seal("p", "new")
	if kid, _, _ := splitSealed(fresh); kid != keyID([]byte(keyB)) {
		t.Fatalf("seal must use the current key, kid=%s", kid)
	}
	resealed, changed, err := Reseal("p", sealedA)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	resealedLegacy, changed, err := Reseal("p", legacy)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	again, changed, err := Reseal("p", resealed)
	if err != nil || changed || again != resealed {
		t.Fatalf("current value must be left alone: changed=%v err=%v", changed, err)
	}

	// Drop A: resealed values open, un-resealed ones fail clearly.
	t.Setenv(KeyPreviousEnv, "")
	if v, err := Open("p", resealed); err != nil || v != "rotate-me" {
		t.Fatalf("v=%q err=%v", v, err)
	}
	if v, err := Open("p", resealedLegacy); err != nil || v != "legacy-me" {
		t.Fatalf("v=%q err=%v", v, err)
	}
	if _, err := Open("p", sealedA); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err=%v", err)
	}
	if _, err := Open("p", legacy); err == nil {
		t.Fatal("legacy value sealed with a dropped key must not open")
	}
}

func TestWrongKeyAndTamperFail(t *testing.T) {
	t.Setenv(KeyEnv, keyA)
	sealed, _ := Seal("p", "value")
	t.Setenv(KeyEnv, keyC)
	if _, err := Open("p", sealed); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err=%v", err)
	}
	// Forge a value that claims the current key id but was sealed by another.
	forged := sealedPrefix + keyID([]byte(keyC)) + ":" + strings.SplitN(sealed, ":", 3)[2]
	if _, err := Open("p", forged); !errors.Is(err, errCannotDecrypt) {
		t.Fatalf("err=%v", err)
	}

	t.Setenv(KeyEnv, keyA)
	parts := strings.SplitN(sealed, ":", 3)
	raw, _ := base64.StdEncoding.DecodeString(parts[2])
	raw[len(raw)-1] ^= 0x01
	tampered := sealedPrefix + parts[1] + ":" + base64.StdEncoding.EncodeToString(raw)
	if _, err := Open("p", tampered); !errors.Is(err, errCannotDecrypt) {
		t.Fatalf("err=%v", err)
	}
	if _, err := Open("p", sealedPrefix+parts[1]+":"+base64.StdEncoding.EncodeToString(raw[:5])); !errors.Is(err, errTruncated) {
		t.Fatalf("err=%v", err)
	}
	if _, err := Open("p", "!!!not-base64"); err == nil {
		t.Fatal("garbage must fail")
	}
	if _, err := Open("p", sealedPrefix+"nokid"); err == nil {
		t.Fatal("malformed v1 value must fail")
	}
	if _, _, err := Reseal("p", tampered); err == nil {
		t.Fatal("reseal must refuse a value that does not open")
	}
	// A legacy value too short for a nonce is reported as truncated.
	if _, err := Open("p", base64.StdEncoding.EncodeToString([]byte("short"))); !errors.Is(err, errTruncated) {
		t.Fatalf("err=%v", err)
	}
}

func TestSealRequiresEncryptionKey(t *testing.T) {
	t.Setenv(KeyEnv, "")
	if _, err := Seal("oidc.client-secret", "client-secret"); !errors.Is(err, ErrKeyNotConfigured) {
		t.Fatalf("err=%v", err)
	}
	if _, err := Open("p", "v1:abc:AAAA"); !errors.Is(err, ErrKeyNotConfigured) {
		t.Fatalf("err=%v", err)
	}
	if _, err := NeedsReseal("x"); !errors.Is(err, ErrKeyNotConfigured) {
		t.Fatalf("err=%v", err)
	}
	if _, _, err := Reseal("p", "x"); !errors.Is(err, ErrKeyNotConfigured) {
		t.Fatalf("err=%v", err)
	}
}

func TestSealRejectsInvalidEncryptionKey(t *testing.T) {
	t.Setenv(KeyEnv, "too-short")
	if _, err := Seal("oidc.client-secret", "client-secret"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("err=%v", err)
	}
	t.Setenv(KeyEnv, keyA)
	t.Setenv(KeyPreviousEnv, "bad")
	if _, err := Seal("p", "v"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("a malformed previous key must be rejected: %v", err)
	}
}

func TestInvalidPreviousKeyNamesItsVariable(t *testing.T) {
	t.Setenv(KeyEnv, keyA)
	t.Setenv(KeyPreviousEnv, keyB+",bad")
	err := ValidateRing()
	if !errors.Is(err, ErrInvalidKey) || !strings.Contains(err.Error(), KeyPreviousEnv+" is invalid: entry 2") {
		t.Fatalf("err=%v", err)
	}
	t.Setenv(KeyPreviousEnv, "")
	if err := ValidateRing(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(KeyEnv, "short")
	if err := ValidateRing(); err == nil || !strings.HasPrefix(err.Error(), KeyEnv+" is invalid") {
		t.Fatalf("err=%v", err)
	}
}

func TestRingCacheFollowsKeyMaterial(t *testing.T) {
	t.Setenv(KeyEnv, keyA)
	first, _ := Seal("p", "v")
	t.Setenv(KeyEnv, keyB)
	second, _ := Seal("p", "v")
	if kid1, _, _ := splitSealed(first); kid1 == "" {
		t.Fatal("missing kid")
	} else if kid2, _, _ := splitSealed(second); kid1 == kid2 {
		t.Fatal("a changed key must not reuse the cached ring")
	}
}

func TestKeyEncodingsAndPreviousList(t *testing.T) {
	hexKey := strings.Repeat("ab", 32)
	b64Key := base64.StdEncoding.EncodeToString([]byte(keyB))
	t.Setenv(KeyEnv, hexKey)
	t.Setenv(KeyPreviousEnv, " "+b64Key+" , ,"+keyA+","+hexKey)
	ring, err := loadRing()
	if err != nil {
		t.Fatal(err)
	}
	if len(ring.keys) != 3 {
		t.Fatalf("duplicates of the current key must be dropped, got %d keys", len(ring.keys))
	}
}

// fakeSource proves Seal and Open consume the ring only through KeySource.
type fakeSource struct{ current string }

func (f fakeSource) Keys() (string, []string, error) { return f.current, nil, nil }

func TestRingIsConsumedOnlyThroughKeySource(t *testing.T) {
	t.Setenv(KeyEnv, keyA)
	restore := SetKeySource(fakeSource{current: keyB})
	sealed, err := Seal("p", "v")
	if err != nil {
		t.Fatal(err)
	}
	if kid, _, _ := splitSealed(sealed); kid != keyID([]byte(keyB)) {
		t.Fatalf("seal ignored the injected source, kid=%s", kid)
	}
	if v, err := Open("p", sealed); err != nil || v != "v" {
		t.Fatalf("v=%q err=%v", v, err)
	}
	restore()
	if _, err := Open("p", sealed); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("restored env source must not know key B: %v", err)
	}
}
