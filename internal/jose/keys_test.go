package jose

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"
)

// RFC 7638 section 3.1 example key and thumbprint.
func TestThumbprintMatchesRFC7638(t *testing.T) {
	const n = "0vx7agoebGcQSuuPiLJXZptN9nndrQmbXEps2aiAFbWhM78LhWx4cbbfAAtVT86zwu1RK7aPFFxuhDR1L6tSoc_BJECPebWKRXjBZCiFV4n3oknjhMstn64tZ_2W-5JsGY4Hc5n9yBXArwl93lqt7_RN5w6Cf0h4QyQ5v-65YGjQR0_FDW2QvzqY368QQMicAtaSqzs8KJZgnYb9c7d0zgdAZHzu6qMQvRL5hajrn1n91CbOpbISD08qNLyrdkt-bFTWhAI4vMQFh6WeZu0fM4lFd2NcRwr3XPksINHaQ-G_xBniIqbw0Ls1jF44-csFCur-kEgU8awapJzKnqDKgw"
	const want = "NzbLsXh8uDCcd-6MNwXF4W_7noWXFZAfHkxZsRGC9Xs"
	modulus, err := base64.RawURLEncoding.DecodeString(n)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Thumbprint(&rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: 65537})
	if err != nil || got != want {
		t.Fatalf("Thumbprint = %q, %v; want %q", got, err, want)
	}
	if _, err := Thumbprint("not a key"); err == nil {
		t.Fatal("thumbprint of an unsupported key succeeded")
	}
}

func TestSignerRoundTripAndDefaultKid(t *testing.T) {
	testKeys(t)
	for _, alg := range DefaultAlgorithms() {
		signer, err := NewSigner(alg, keyFor(alg), "")
		if err != nil {
			t.Fatalf("%s: %v", alg, err)
		}
		thumb, _ := Thumbprint(keyFor(alg).Public())
		if signer.KeyID() != thumb || signer.Algorithm() != alg {
			t.Fatalf("%s: kid %q alg %q", alg, signer.KeyID(), signer.Algorithm())
		}
		clk := newClock()
		jwk, _ := PublicKey(keyFor(alg).Public(), signer.KeyID(), alg)
		doc, err := MarshalJWKS(jwk)
		if err != nil {
			t.Fatal(err)
		}
		token, err := signer.Sign(claimsAt(clk, "https://issuer.example.test", nil))
		if err != nil {
			t.Fatal(err)
		}
		v := newTestVerifier(t, clk, nil)
		verified, err := v.Verify(t.Context(), token, Issuer{Issuer: "https://issuer.example.test", Audiences: []string{"notarx"}, Mode: ModeStatic, StaticJWKS: doc})
		if err != nil || verified.KeyID != thumb {
			t.Fatalf("%s: %v %+v", alg, err, verified)
		}
	}
}

func TestSignerRejectsBadInputs(t *testing.T) {
	testKeys(t)
	if _, err := NewSigner("HS256", rsaKey, ""); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("HS256 err = %v", err)
	}
	if _, err := NewSigner(ES256, rsaKey, ""); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("mismatched key err = %v", err)
	}
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err == nil {
		if _, err := NewSigner(RS256, weak, ""); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("weak key err = %v", err)
		}
	}
	signer, _ := NewSigner(RS256, rsaKey, "k")
	if _, err := signer.Sign(make(chan int)); err == nil {
		t.Fatal("unmarshalable claims accepted")
	}
	if _, err := PublicKey(rsaKey.Public(), "", "HS256"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("PublicKey alg err = %v", err)
	}
	if _, err := PublicKey("nope", "", RS256); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("PublicKey type err = %v", err)
	}
}

func TestMarshalJWKSRefusesPrivateKeys(t *testing.T) {
	testKeys(t)
	if _, err := MarshalJWKS(Key{Key: rsaKey, KeyID: "k"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("private key err = %v", err)
	}
	if _, err := MarshalJWKS(Key{Key: nil}); err == nil {
		t.Fatal("invalid key accepted")
	}
	doc, err := MarshalJWKS()
	if err != nil {
		t.Fatal(err)
	}
	var set struct{ Keys []json.RawMessage }
	if json.Unmarshal(doc, &set) != nil || set.Keys == nil {
		t.Fatalf("empty set must encode keys as an array: %s", doc)
	}
}

func TestNumericDate(t *testing.T) {
	if _, ok := numericDate("1"); ok {
		t.Fatal("string accepted")
	}
	if _, ok := numericDate(json.Number("1e99")); ok {
		t.Fatal("huge value accepted")
	}
	if got, ok := numericDate(json.Number("10.5")); !ok || got.UnixNano() != 10_500_000_000 {
		t.Fatalf("got %v %v", got, ok)
	}
	_ = time.Second
}
