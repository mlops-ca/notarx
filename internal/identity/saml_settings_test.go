package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

func testCertificatePEM(t *testing.T, name string, notAfter time.Time) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, NotBefore: notAfter.Add(-24 * time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func samlProvider(t *testing.T, enabled bool, settings SAMLSettings) Provider {
	t.Helper()
	provider, err := Provider{Slug: "corp", Kind: KindSAML, DisplayName: "Corp", Enabled: enabled}.WithSAML(settings)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestSAMLSettingsNormalizeAndDeriveCertificateExpiry(t *testing.T) {
	notAfter := time.Date(2031, 5, 1, 12, 0, 0, 0, time.UTC)
	pemValue := testCertificatePEM(t, "idp", notAfter)
	provider, err := samlProvider(t, true, SAMLSettings{
		EntityID: " https://idp.example.test/m ", SSOURL: "https://idp.example.test/sso",
		// The notAfter a client sends is ignored, a duplicate certificate collapses, a missing state is active.
		Certificates: []SAMLCertificate{{PEM: "  " + pemValue + "\n", NotAfter: time.Unix(1, 0)}, {PEM: pemValue, State: SAMLCertificateNext}},
	}).NormalizeAndValidate()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := provider.SAML()
	if err != nil {
		t.Fatal(err)
	}
	if settings.EntityID != "https://idp.example.test/m" || settings.NameIDFormat != SAMLNameIDPersistent || len(settings.Certificates) != 1 ||
		!settings.Certificates[0].NotAfter.Equal(notAfter) || settings.Certificates[0].State != SAMLCertificateActive || settings.RelayStateAllowlist == nil {
		t.Fatalf("settings = %+v", settings)
	}
	certs, err := settings.ParsedCertificates()
	if err != nil || len(certs) != 1 || certs[0].Subject.CommonName != "idp" {
		t.Fatalf("parsed = %v, %v", certs, err)
	}
}

func TestSAMLSettingsValidationRules(t *testing.T) {
	good := testCertificatePEM(t, "idp", time.Now().Add(365*24*time.Hour))
	base := func() SAMLSettings {
		return SAMLSettings{EntityID: "e", SSOURL: "https://idp.example.test/sso", Certificates: []SAMLCertificate{{PEM: good}}}
	}
	cases := map[string]func(*SAMLSettings){
		"bad format":      func(s *SAMLSettings) { s.NameIDFormat = "transient" },
		"bad certificate": func(s *SAMLSettings) { s.Certificates = []SAMLCertificate{{PEM: "nope"}} },
		"two blocks":      func(s *SAMLSettings) { s.Certificates = []SAMLCertificate{{PEM: good + good}} },
		"private key pem": func(s *SAMLSettings) {
			s.Certificates = []SAMLCertificate{{PEM: strings.ReplaceAll(good, "CERTIFICATE", "PRIVATE KEY")}}
		},
		"garbage der": func(s *SAMLSettings) {
			s.Certificates = []SAMLCertificate{{PEM: "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"}}
		},
		"bad state":         func(s *SAMLSettings) { s.Certificates[0].State = "retired" },
		"too many":          func(s *SAMLSettings) { s.Certificates = make([]SAMLCertificate, MaxSAMLCertificates+1) },
		"http sso":          func(s *SAMLSettings) { s.SSOURL = "http://idp.example.test/sso" },
		"http metadata":     func(s *SAMLSettings) { s.MetadataURL = "http://idp.example.test/m" },
		"idp initiated":     func(s *SAMLSettings) { s.AllowIdPInitiated = true },
		"enabled no sso":    func(s *SAMLSettings) { s.SSOURL = "" },
		"enabled no entity": func(s *SAMLSettings) { s.EntityID = "" },
		"enabled no certs":  func(s *SAMLSettings) { s.Certificates = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			settings := base()
			mutate(&settings)
			if _, err := samlProvider(t, true, settings).NormalizeAndValidate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted: %v", err)
			}
		})
	}
	// A disabled provider may be saved incomplete; allowlist entries are dropped unless IdP-initiated is on.
	draft, err := samlProvider(t, false, SAMLSettings{RelayStateAllowlist: []string{"/x"}}).NormalizeAndValidate()
	if err != nil {
		t.Fatal(err)
	}
	if settings, _ := draft.SAML(); len(settings.RelayStateAllowlist) != 0 {
		t.Fatalf("allowlist = %v", settings.RelayStateAllowlist)
	}
	allowed := base()
	allowed.AllowIdPInitiated, allowed.RelayStateAllowlist = true, []string{" /repositories ", "/repositories"}
	provider, err := samlProvider(t, true, allowed).NormalizeAndValidate()
	if settings, _ := provider.SAML(); err != nil || len(settings.RelayStateAllowlist) != 1 {
		t.Fatalf("allowed = %+v, %v", settings, err)
	}
	for _, format := range []string{SAMLNameIDPersistent, SAMLNameIDEmailAddress, SAMLNameIDUnspecified} {
		settings := base()
		settings.NameIDFormat = format
		if _, err := samlProvider(t, true, settings).NormalizeAndValidate(); err != nil {
			t.Fatalf("%s: %v", format, err)
		}
	}
	if _, err := (Provider{Slug: "corp", Kind: KindSAML, DisplayName: "C", Settings: []byte("[1]")}).NormalizeAndValidate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("settings that are not an object: %v", err)
	}
	if empty, err := (Provider{Slug: "corp", Kind: KindSAML, DisplayName: "C"}).NormalizeAndValidate(); err != nil || len(empty.Settings) == 0 {
		t.Fatalf("empty settings: %v", err)
	}
}

func TestExpiringCertificatesWindow(t *testing.T) {
	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	settings := SAMLSettings{Certificates: []SAMLCertificate{
		{PEM: "a", NotAfter: now.Add(29 * 24 * time.Hour)}, {PEM: "b", NotAfter: now.Add(31 * 24 * time.Hour)}, {PEM: "c", NotAfter: now.Add(-time.Hour)},
	}}
	got := settings.ExpiringCertificates(now, 30*24*time.Hour)
	if len(got) != 2 || got[0].PEM != "a" || got[1].PEM != "c" {
		t.Fatalf("expiring = %+v", got)
	}
}
