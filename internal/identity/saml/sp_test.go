package saml

import (
	"bytes"
	"compress/flate"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	dsig "github.com/russellhaering/goxmldsig"

	"github.com/mlops-ca/notarx/internal/secrets"
)

func testEndpoints(t *testing.T) SPEndpoints {
	t.Helper()
	base, err := url.Parse("https://notarx.example.test/")
	if err != nil {
		t.Fatal(err)
	}
	return EndpointsFor(base, "corp")
}

func TestEndpointsFor(t *testing.T) {
	endpoints := testEndpoints(t)
	if endpoints.EntityID != testSPEntityID || endpoints.ACSURL != testACS ||
		endpoints.MetadataURL != testSPEntityID || endpoints.LoginURL != "https://notarx.example.test/auth/saml/corp/login" {
		t.Fatalf("endpoints = %+v", endpoints)
	}
}

func inflateRequest(t *testing.T, loginURL string) (query url.Values, xml string) {
	t.Helper()
	parsed, err := url.Parse(loginURL)
	if err != nil {
		t.Fatal(err)
	}
	query = parsed.Query()
	raw, err := base64.StdEncoding.DecodeString(query.Get("SAMLRequest"))
	if err != nil {
		t.Fatal(err)
	}
	inflated, err := io.ReadAll(flate.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return query, string(inflated)
}

func TestBuildLoginURLCarriesTheRequestID(t *testing.T) {
	endpoints := testEndpoints(t)
	loginURL, err := BuildLoginURL(LoginRequest{
		IdPEntityID: testEntityID, SSOURL: "https://idp.example.test/sso?tenant=1", NameIDFormat: NameIDEmailAddress,
		SP: endpoints, RequestID: RequestIDFor("0190d9c0-0000-7000-8000-000000000001"), RelayState: "relay-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	query, document := inflateRequest(t, loginURL)
	if query.Get("RelayState") != "relay-1" || query.Get("tenant") != "1" || query.Get("Signature") != "" {
		t.Fatalf("query = %v", query)
	}
	for _, want := range []string{
		`ID="_0190d9c0-0000-7000-8000-000000000001"`, `AssertionConsumerServiceURL="` + testACS + `"`,
		`<saml:Issuer>` + testSPEntityID + `</saml:Issuer>`, `Format="urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress"`, `Destination="https://idp.example.test/sso?tenant=1"`,
	} {
		if !strings.Contains(document, want) {
			t.Fatalf("AuthnRequest lacks %s:\n%s", want, document)
		}
	}
	if _, err := BuildLoginURL(LoginRequest{SSOURL: "https://idp.example.test/sso", NameIDFormat: "transient", SP: endpoints}); err == nil {
		t.Fatal("transient accepted")
	}
}

func TestBuildLoginURLSignsTheQueryWithTheServiceProviderKey(t *testing.T) {
	t.Setenv(secrets.KeyEnv, testSealingKey)
	keys, err := SPKeySet{}.Rotated("corp", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	key, _ := keys.Signing()
	loginURL, err := BuildLoginURL(LoginRequest{
		IdPEntityID: testEntityID, SSOURL: "https://idp.example.test/sso", NameIDFormat: NameIDPersistent,
		SP: testEndpoints(t), RequestID: "_abc", RelayState: "relay", SignKey: &key,
	})
	if err != nil {
		t.Fatal(err)
	}
	query, _ := inflateRequest(t, loginURL)
	signature, err := base64.StdEncoding.DecodeString(query.Get("Signature"))
	if err != nil || query.Get("SigAlg") != dsig.RSASHA256SignatureMethod {
		t.Fatalf("signature = %v, SigAlg = %q", err, query.Get("SigAlg"))
	}
	signed := "SAMLRequest=" + url.QueryEscape(query.Get("SAMLRequest")) + "&RelayState=" + url.QueryEscape("relay") + "&SigAlg=" + url.QueryEscape(query.Get("SigAlg"))
	digest := sha256.Sum256([]byte(signed))
	der, _ := key.Certificate()
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(cert.PublicKey.(*rsa.PublicKey), crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("the redirect signature does not verify against the published certificate: %v", err)
	}
	bad := key
	bad.PrivateKeyPEM = "junk"
	if _, err := BuildLoginURL(LoginRequest{SSOURL: "https://idp.example.test/sso", NameIDFormat: NameIDPersistent, SP: testEndpoints(t), SignKey: &bad}); err == nil {
		t.Fatal("an unreadable signing key was accepted")
	}
	bad = key
	bad.CertificatePEM = "junk"
	if _, err := BuildLoginURL(LoginRequest{SSOURL: "https://idp.example.test/sso", NameIDFormat: NameIDPersistent, SP: testEndpoints(t), SignKey: &bad}); err == nil {
		t.Fatal("an unreadable signing certificate was accepted")
	}
}

func TestRequestIDRoundTrip(t *testing.T) {
	if id := RequestIDFor("abc"); id != "_abc" {
		t.Fatal(id)
	}
	if state, ok := StateIDFrom("_abc"); !ok || state != "abc" {
		t.Fatal("round trip failed")
	}
	for _, bad := range []string{"", "_", "abc"} {
		if _, ok := StateIDFrom(bad); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestSPMetadataPublishesEveryKeyAndNoPrivateMaterial(t *testing.T) {
	t.Setenv(secrets.KeyEnv, testSealingKey)
	keys, _ := SPKeySet{}.Rotated("corp", time.Now())
	keys, err := keys.Rotated("corp", time.Now())
	if err != nil || len(keys.Keys) != 2 {
		t.Fatal(err)
	}
	body, err := SPMetadata(testEndpoints(t), NameIDPersistent, true, keys)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		`entityID="` + testSPEntityID + `"`, `AuthnRequestsSigned="true"`, `WantAssertionsSigned="true"`,
		`Location="` + testACS + `"`, "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST", "urn:oasis:names:tc:SAML:2.0:nameid-format:persistent",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metadata lacks %s:\n%s", want, text)
		}
	}
	if got := strings.Count(text, "<md:KeyDescriptor"); got != 2 {
		t.Fatalf("published %d keys, want 2", got)
	}
	for _, key := range keys.Keys {
		if strings.Contains(text, "PRIVATE KEY") || strings.Contains(text, key.ID) {
			t.Fatal("private material or an internal id leaked into the metadata")
		}
	}
	unsigned, err := SPMetadata(testEndpoints(t), NameIDPersistent, false, SPKeySet{})
	if err != nil || !strings.Contains(string(unsigned), `AuthnRequestsSigned="false"`) || strings.Contains(string(unsigned), "KeyDescriptor") {
		t.Fatalf("unsigned metadata = %s, %v", unsigned, err)
	}
	if _, err := SPMetadata(testEndpoints(t), "transient", false, SPKeySet{}); err == nil {
		t.Fatal("transient accepted")
	}
	if _, err := SPMetadata(testEndpoints(t), NameIDPersistent, true, SPKeySet{Keys: []SPKey{{CertificatePEM: "junk"}}}); err == nil {
		t.Fatal("an unreadable certificate was accepted")
	}
	if _, err := (SPKey{CertificatePEM: "junk"}).Certificate(); err == nil {
		t.Fatal("junk certificate parsed")
	}
}
