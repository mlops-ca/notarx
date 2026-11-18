package authorization

import (
	"strings"
	"testing"
)

func TestAnonymousTokenCarriesNoAuthority(t *testing.T) {
	a := Authenticator{ResolverToken: "resolver-secret", AdminToken: "admin-secret"}
	token := a.IssueAnonymousToken()
	if token == "" || !a.IsAnonymousToken("Bearer "+token) {
		t.Fatalf("a freshly issued anonymous token must verify: %q", token)
	}
	if _, ok := a.Authenticate("Bearer " + token); ok {
		t.Fatal("an anonymous token must never authenticate as a principal")
	}
	for name, header := range map[string]string{
		"no scheme":  token,
		"empty":      "",
		"tampered":   "Bearer " + token[:len(token)-2] + "xx",
		"expiry":     "Bearer " + strings.Replace(token, "an1.", "an1.9", 1),
		"principal":  "Bearer " + a.IssueToken("admin"),
		"wrong kind": "Bearer v1.x.1.y",
	} {
		if a.IsAnonymousToken(header) {
			t.Errorf("%s: accepted as an anonymous token", name)
		}
	}
	if other := (Authenticator{ResolverToken: "another-secret"}); other.IsAnonymousToken("Bearer " + token) {
		t.Error("a token signed with another key verified")
	}
	if (Authenticator{}).IssueAnonymousToken() != "" {
		t.Error("an authenticator without a signing key must not issue tokens")
	}
}
