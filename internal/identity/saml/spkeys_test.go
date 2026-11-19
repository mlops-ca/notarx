package saml

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/secrets"
)

const testSealingKey = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

func TestSPKeySetSealRotateAndOpen(t *testing.T) {
	t.Setenv(secrets.KeyEnv, testSealingKey)
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	set, err := SPKeySet{}.Rotated("corp", now)
	if err != nil || len(set.Keys) != 1 {
		t.Fatalf("first key = %+v, %v", set, err)
	}
	if !set.Keys[0].NotAfter.After(now.Add(4 * 365 * 24 * time.Hour)) {
		t.Fatalf("certificate life is too short: %s", set.Keys[0].NotAfter)
	}
	second, err := set.Rotated("corp", now)
	if err != nil || len(second.Keys) != 2 || second.Keys[0].ID != set.Keys[0].ID {
		t.Fatalf("second rotation = %+v, %v", second, err)
	}
	third, err := second.Rotated("corp", now)
	if err != nil || len(third.Keys) != MaxSPKeys || third.Keys[0].ID != second.Keys[1].ID {
		t.Fatalf("the oldest key must drop at the third rotation: %+v, %v", third, err)
	}
	if signing, ok := third.Signing(); !ok || signing.ID != third.Keys[1].ID {
		t.Fatal("the newest key signs")
	}
	if _, ok := (SPKeySet{}).Signing(); ok {
		t.Fatal("an empty set has no signing key")
	}
	sealed, err := third.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "PRIVATE KEY") || strings.Contains(sealed, third.Keys[0].ID) {
		t.Fatal("the sealed value leaks plaintext")
	}
	opened, err := OpenSPKeySet(sealed)
	if err != nil || len(opened.Keys) != 2 || opened.Keys[1].CertificatePEM != third.Keys[1].CertificatePEM {
		t.Fatalf("opened = %+v, %v", opened, err)
	}
	if empty, err := OpenSPKeySet(""); err != nil || len(empty.Keys) != 0 {
		t.Fatalf("empty = %+v, %v", empty, err)
	}
	der, err := opened.Keys[0].Certificate()
	if err != nil {
		t.Fatal(err)
	}
	if cert, err := x509.ParseCertificate(der); err != nil || cert.Subject.CommonName != "corp" {
		t.Fatalf("certificate = %v, %v", cert, err)
	}
}

func TestSPKeySetRefusesTamperedAndForeignSecrets(t *testing.T) {
	t.Setenv(secrets.KeyEnv, testSealingKey)
	if _, err := OpenSPKeySet("not-sealed"); err == nil {
		t.Fatal("garbage opened")
	}
	// A secret sealed for another purpose (an OIDC client secret) is refused by the purpose binding or
	// its content, never returned as a key set.
	other, err := secrets.Seal("email-target:x", `{"spKeys":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSPKeySet(other); err == nil {
		t.Fatal("a secret of another purpose opened")
	}
	notJSON, err := secrets.Seal(SPKeyPurpose, "plain-client-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSPKeySet(notJSON); err == nil {
		t.Fatal("a non-key-set secret opened")
	}
	badKey, err := secrets.Seal(SPKeyPurpose, `{"spKeys":[{"id":"1","privateKeyPem":"nope"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSPKeySet(badKey); err == nil {
		t.Fatal("an invalid private key opened")
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("junk")})
	junk, err := secrets.Seal(SPKeyPurpose, `{"spKeys":[{"id":"1","privateKeyPem":`+quoteJSON(string(block))+`}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSPKeySet(junk); err == nil {
		t.Fatal("an unparsable private key opened")
	}
}

func quoteJSON(value string) string {
	return "\"" + strings.ReplaceAll(strings.ReplaceAll(value, "\n", "\\n"), "\"", "\\\"") + "\""
}

func TestSPKeyUnsealWithoutAConfiguredKey(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "")
	set := SPKeySet{Keys: []SPKey{{ID: "1"}}}
	if _, err := set.Seal(); err == nil {
		t.Fatal("sealing without a key succeeded")
	}
}
