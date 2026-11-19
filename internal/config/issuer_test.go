package config

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func mustPublic(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := ParsePublicURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestParseIssuerURL(t *testing.T) {
	public := mustPublic(t, "https://notarx.example.com")
	cases := []struct {
		name, raw string
		public    *url.URL
		want      string
		warn      bool
		wantErr   string
	}{
		{name: "defaults to the public url", public: public, want: "https://notarx.example.com"},
		{name: "override", raw: "https://issuer.example.com", public: public, want: "https://issuer.example.com"},
		{name: "trailing slash is normalised", raw: "https://issuer.example.com/", public: public, want: "https://issuer.example.com"},
		{name: "path is kept", raw: "https://example.com/notarx/", public: nil, want: "https://example.com/notarx"},
		{name: "loopback http warns", raw: "http://localhost:8080", want: "http://localhost:8080", warn: true},
		{name: "ipv4 loopback", raw: "http://127.0.0.1:8080", want: "http://127.0.0.1:8080", warn: true},
		{name: "ipv6 loopback", raw: "http://[::1]:8080", want: "http://[::1]:8080", warn: true},
		{name: "http on a real host", raw: "http://issuer.example.com", wantErr: "https"},
		{name: "empty without public url", wantErr: "required"},
		{name: "query", raw: "https://issuer.example.com?x=1", wantErr: "query"},
		{name: "fragment", raw: "https://issuer.example.com#x", wantErr: "query or fragment"},
		{name: "user info", raw: "https://u:p@issuer.example.com", wantErr: "user information"},
		{name: "relative", raw: "issuer.example.com", wantErr: "absolute"},
		{name: "only slashes", raw: "///", wantErr: "must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, warnings, err := ParseIssuerURL(tc.raw, tc.public)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err=%v want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want || (len(warnings) > 0) != tc.warn {
				t.Fatalf("got=%q warnings=%v err=%v", got, warnings, err)
			}
		})
	}
}

func clearIssuerEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{IssuerEnabledEnv, IssuerURLEnv, IssuerAlgEnv, IssuerKeyRotationEnv, IssuerKeyPublishLeadEnv,
		IssuerMaxTokenLifetimeEnv, IssuerAllowSelfTrustEnv, EntraAuthorityHostEnv, EntraAudienceEnv} {
		t.Setenv(name, "")
	}
}

func TestIssuerConfigDefaultsAndDisabledIsInert(t *testing.T) {
	clearIssuerEnv(t)
	t.Setenv(IssuerURLEnv, "http://not-checked-while-disabled.example.com")
	cfg, err := loadIssuerConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.URL != "" || cfg.Alg != "RS256" || cfg.KeyRotationInterval != 720*time.Hour ||
		cfg.KeyPublishLead != 24*time.Hour || cfg.MaxTokenLifetime != 10*time.Minute || cfg.EntraAuthorityHost != DefaultEntraAuthorityHost {
		t.Fatalf("%+v", cfg)
	}
}

func TestIssuerConfigEnabled(t *testing.T) {
	clearIssuerEnv(t)
	t.Setenv(IssuerEnabledEnv, "true")
	cfg, err := loadIssuerConfig(mustPublic(t, "https://notarx.example.com"))
	if err != nil || cfg.URL != "https://notarx.example.com" || !cfg.Enabled {
		t.Fatalf("%+v %v", cfg, err)
	}
	if lifecycle := cfg.Lifecycle(); lifecycle.Validate() != nil || lifecycle.Alg != "RS256" {
		t.Fatalf("%+v", lifecycle)
	}
	t.Setenv(IssuerAlgEnv, "ES256")
	t.Setenv(IssuerKeyRotationEnv, "48h")
	t.Setenv(IssuerKeyPublishLeadEnv, "1h")
	t.Setenv(IssuerMaxTokenLifetimeEnv, "5m")
	t.Setenv(IssuerAllowSelfTrustEnv, "true")
	t.Setenv(EntraAuthorityHostEnv, "https://login.microsoftonline.us/")
	t.Setenv(EntraAudienceEnv, "api://custom")
	cfg, err = loadIssuerConfig(nil)
	if err == nil {
		t.Fatal("an enabled issuer needs a url")
	}
	t.Setenv(IssuerURLEnv, "https://issuer.example.com/")
	cfg, err = loadIssuerConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Alg != "ES256" || cfg.KeyRotationInterval != 48*time.Hour || cfg.KeyPublishLead != time.Hour || cfg.MaxTokenLifetime != 5*time.Minute ||
		!cfg.AllowSelfTrust || cfg.EntraAuthorityHost != "https://login.microsoftonline.us" || cfg.EntraAudience != "api://custom" || cfg.URL != "https://issuer.example.com" {
		t.Fatalf("%+v", cfg)
	}
}

func TestIssuerConfigRejectsInvalidValues(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"enabled flag":        {IssuerEnabledEnv: "yes"},
		"self trust flag":     {IssuerAllowSelfTrustEnv: "1"},
		"alg":                 {IssuerAlgEnv: "HS256"},
		"lifetime":            {IssuerMaxTokenLifetimeEnv: "11m"},
		"zero lead":           {IssuerKeyPublishLeadEnv: "0"},
		"bad duration":        {IssuerKeyRotationEnv: "soon"},
		"lead above rotation": {IssuerKeyRotationEnv: "1h", IssuerKeyPublishLeadEnv: "2h"},
		"authority http":      {EntraAuthorityHostEnv: "http://login.example.com"},
		"authority path":      {EntraAuthorityHostEnv: "https://login.example.com/tenant"},
		"authority creds":     {EntraAuthorityHostEnv: "https://u@login.example.com"},
		"bad url":             {IssuerURLEnv: "http://issuer.example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			clearIssuerEnv(t)
			t.Setenv(IssuerEnabledEnv, "true")
			t.Setenv(IssuerURLEnv, "https://issuer.example.com")
			for key, value := range env {
				t.Setenv(key, value)
			}
			if _, err := loadIssuerConfig(nil); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
