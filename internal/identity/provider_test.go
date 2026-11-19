package identity

import (
	"errors"
	"strings"
	"testing"
)

func oidcProvider(t *testing.T, enabled bool, settings OIDCSettings) Provider {
	t.Helper()
	provider, err := Provider{Slug: "corp", Kind: KindOIDC, DisplayName: "Corp", Enabled: enabled}.WithOIDC(settings)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestOIDCProviderRequiresCompleteBrowserConfiguration(t *testing.T) {
	_, err := oidcProvider(t, true, OIDCSettings{Issuer: "https://identity.example.test", Audience: "notarx-api", ClientID: "notarx-console"}).NormalizeAndValidate()
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "clientId and redirectUrl") {
		t.Fatalf("partial browser configuration err=%v", err)
	}
	bearerOnly, err := oidcProvider(t, true, OIDCSettings{Issuer: "https://identity.example.test", Audience: "notarx-api"}).NormalizeAndValidate()
	if err != nil || !bearerOnly.Enabled {
		t.Fatalf("bearer-only provider=%#v err=%v", bearerOnly, err)
	}
	settings, _ := bearerOnly.OIDC()
	if len(settings.Algorithms) != 1 || settings.Algorithms[0] != "RS256" || settings.Scopes[0] != "openid" {
		t.Fatalf("defaults=%#v", settings)
	}
	if bearerOnly.ProvisioningMode != ProvisioningDisabled || bearerOnly.JITDefaultRole != JITRoleNone {
		t.Fatalf("defaults=%#v", bearerOnly)
	}
}

func TestProviderValidationRules(t *testing.T) {
	valid := OIDCSettings{Issuer: "https://identity.example.test", Audience: "a"}
	cases := map[string]func(*Provider){
		"slug too short":    func(p *Provider) { p.Slug = "a" },
		"slug upper case":   func(p *Provider) { p.Slug = "Corp" },
		"slug leading dash": func(p *Provider) { p.Slug = "-corp" },
		"empty name":        func(p *Provider) { p.DisplayName = "  " },
		"bad kind":          func(p *Provider) { p.Kind = "ldap" },
		"bad mode":          func(p *Provider) { p.ProvisioningMode = "auto" },
		"admin jit role":    func(p *Provider) { p.JITDefaultRole = "admin" },
		"plain http": func(p *Provider) {
			*p = oidcProvider(t, true, OIDCSettings{Issuer: "http://identity.example.test", Audience: "a"})
		},
		"bad algorithm": func(p *Provider) { *p = oidcProvider(t, false, OIDCSettings{Algorithms: []string{"HS256"}}) },
	}
	for name, mutate := range cases {
		provider := oidcProvider(t, true, valid)
		mutate(&provider)
		if _, err := provider.NormalizeAndValidate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	loopback, err := oidcProvider(t, true, OIDCSettings{Issuer: "http://localhost:8080/realms/dev", Audience: "a"}).NormalizeAndValidate()
	if err != nil || loopback.Slug != "corp" {
		t.Fatalf("loopback http issuer must stay usable for development: %v", err)
	}
	for _, algorithm := range []string{"RS256", "ES256", "EdDSA"} {
		if _, err := oidcProvider(t, true, OIDCSettings{Issuer: "https://i.example.test", Audience: "a", Algorithms: []string{algorithm}}).NormalizeAndValidate(); err != nil {
			t.Errorf("%s rejected: %v", algorithm, err)
		}
	}
}

func TestAttributeMappingSubjectClaims(t *testing.T) {
	if got := (AttributeMapping{}).SubjectClaims(); len(got) != 1 || got[0] != "sub" {
		t.Fatalf("default=%v", got)
	}
	if got := (AttributeMapping{Subject: " tid : oid "}).SubjectClaims(); len(got) != 2 || got[0] != "tid" || got[1] != "oid" {
		t.Fatalf("entra=%v", got)
	}
	if _, err := (Provider{Slug: "corp", Kind: KindOIDC, DisplayName: "C", AttributeMapping: AttributeMapping{Subject: "a:b:c"}}).NormalizeAndValidate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a subject of three claims must be refused: %v", err)
	}
}

func TestNormalizeStrings(t *testing.T) {
	got := NormalizeStrings([]string{" a ", "", "a", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got=%v", got)
	}
}
