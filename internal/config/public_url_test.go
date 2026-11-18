package config

import (
	"strings"
	"testing"
)

func TestParsePublicURLAccepts(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"", ""},
		{"   ", ""},
		{"https://notarx.example.com", "https://notarx.example.com"},
		{" https://notarx.example.com:8443 ", "https://notarx.example.com:8443"},
		{"https://example.com/notarx", "https://example.com/notarx"},
		{"http://localhost:8080", "http://localhost:8080"},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"http://127.1.2.3", "http://127.1.2.3"},
		{"http://[::1]:8080", "http://[::1]:8080"},
	} {
		got, err := ParsePublicURL(tc.raw)
		gotString := ""
		if got != nil {
			gotString = got.String()
		}
		if err != nil || gotString != tc.want || (tc.want == "") != (got == nil) {
			t.Errorf("ParsePublicURL(%q) = %q, %v; want %q", tc.raw, gotString, err, tc.want)
		}
	}
}

func TestParsePublicURLRejects(t *testing.T) {
	for _, raw := range []string{
		"notarx.example.com",
		"//notarx.example.com",
		"/relative",
		"https://",
		"https:///path",
		"https://notarx.example.com/",
		"https://notarx.example.com/notarx/",
		"http://notarx.example.com",
		"http://10.0.0.1",
		"http://localhost.example.com",
		"ftp://notarx.example.com",
		"mailto:ops@example.com",
		"https://user:pass@notarx.example.com",
		"https://user@notarx.example.com",
		"https://notarx.example.com?x=1",
		"https://notarx.example.com?",
		"https://notarx.example.com#frag",
		"https://notarx.example.com#",
		"https://not arx.example.com",
		"https://notarx.example.com:port",
	} {
		if got, err := ParsePublicURL(raw); err == nil {
			t.Errorf("ParsePublicURL(%q) = %v, nil; want error", raw, got)
		} else if !strings.Contains(err.Error(), PublicURLEnv) {
			t.Errorf("ParsePublicURL(%q) error %q does not name %s", raw, err, PublicURLEnv)
		}
	}
}

func TestLoadPublicURL(t *testing.T) {
	setValidNativeEnv(t)
	if cfg, err := Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	} else if u, ok := cfg.PublicURL(); ok || u != nil {
		t.Fatalf("PublicURL() = %v, %v; want unset", u, ok)
	}
	t.Setenv(PublicURLEnv, "https://notarx.example.com")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	u, ok := cfg.PublicURL()
	if !ok || u.String() != "https://notarx.example.com" || u.Host != "notarx.example.com" {
		t.Fatalf("PublicURL() = %v, %v", u, ok)
	}
	u.Host = "mutated.example.com"
	if again, _ := cfg.PublicURL(); again.Host != "notarx.example.com" {
		t.Fatal("PublicURL() must return a copy")
	}
	t.Setenv(PublicURLEnv, "http://notarx.example.com")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a non-loopback http public URL")
	}
	t.Setenv(PublicURLEnv, "https://notarx.example.com/")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a trailing slash")
	}
}
