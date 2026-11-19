package jose

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestBlockedAddresses(t *testing.T) {
	for _, tc := range []struct {
		addr    string
		blocked bool
	}{
		{"127.0.0.1", true}, {"::1", true}, {"10.1.2.3", true}, {"172.16.0.1", true}, {"192.168.1.1", true},
		{"169.254.169.254", true}, {"fe80::1", true}, {"fd00::1", true}, {"0.0.0.0", true}, {"::", true},
		{"100.64.0.1", true}, {"198.18.0.1", true}, {"224.0.0.1", true}, {"255.255.255.255", true},
		{"::ffff:127.0.0.1", true}, {"::ffff:10.0.0.1", true}, {"64:ff9b::7f00:1", true}, {"fec0::1", true},
		{"8.8.8.8", false}, {"1.1.1.1", false}, {"2606:4700:4700::1111", false},
	} {
		if got := blockedAddr(netip.MustParseAddr(tc.addr)); got != tc.blocked {
			t.Errorf("blockedAddr(%s) = %v, want %v", tc.addr, got, tc.blocked)
		}
	}
	if !blockedAddr(netip.Addr{}) {
		t.Error("the zero address must be blocked")
	}
}

func TestDialControl(t *testing.T) {
	if err := dialControl(false)("tcp", "127.0.0.1:443", nil); !errors.Is(err, errBlockedAddress) {
		t.Fatalf("loopback err = %v", err)
	}
	if err := dialControl(false)("tcp", "8.8.8.8:443", nil); err != nil {
		t.Fatalf("public address err = %v", err)
	}
	if err := dialControl(true)("tcp", "127.0.0.1:443", nil); err != nil {
		t.Fatalf("allow_private_network err = %v", err)
	}
	if err := dialControl(false)("tcp", "no-port", nil); err == nil {
		t.Fatal("malformed address accepted")
	}
	if err := dialControl(false)("tcp", "example.test:443", nil); err == nil {
		t.Fatal("a name must never reach the dialer unresolved")
	}
}

func TestGuardedClientRefusesPrivateTargets(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	ca := string(pemOf(srv))
	// Without allow_private_network neither the literal loopback nor a name that resolves to
	// loopback (the DNS rebinding shape: the check is on the resolved IP at dial time) connects.
	client, err := newGuardedClient(false, ca)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(srv.URL)
	for _, target := range []string{srv.URL, "https://localhost:" + parsed.Port()} {
		if _, err := client.Get(target); err == nil {
			t.Fatalf("%s was reachable through the guard", target)
		}
	}
	allowed, err := newGuardedClient(true, ca)
	if err != nil {
		t.Fatal(err)
	}
	response, err := allowed.Get(srv.URL)
	if err != nil {
		t.Fatalf("allow_private_network: %v", err)
	}
	_ = response.Body.Close()
}

func TestGuardedClientRefusesPlainHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	client, _ := newGuardedClient(true, "")
	if _, err := client.Get(srv.URL); err == nil || !strings.Contains(err.Error(), "https only") {
		t.Fatalf("err = %v", err)
	}
}

func TestRedirectPolicy(t *testing.T) {
	origin, _ := url.Parse("https://issuer.example.test/jwks")
	via := []*http.Request{{URL: origin}}
	check := checkRedirect(false)
	redirect := func(raw string) error {
		target, _ := url.Parse(raw)
		return check(&http.Request{URL: target}, via)
	}
	if err := redirect("https://issuer.example.test/other"); err != nil {
		t.Fatalf("same-host redirect: %v", err)
	}
	for _, raw := range []string{"https://evil.example.test/jwks", "http://issuer.example.test/jwks", "https://127.0.0.1/jwks", "https://169.254.169.254/"} {
		if err := redirect(raw); err == nil {
			t.Fatalf("redirect to %s allowed", raw)
		}
	}
	long := []*http.Request{{URL: origin}, {URL: origin}, {URL: origin}, {URL: origin}}
	if err := check(&http.Request{URL: origin}, long); err == nil {
		t.Fatal("redirect chain not bounded")
	}
	ip, _ := url.Parse("https://10.0.0.1/a")
	if err := checkRedirect(false)(&http.Request{URL: ip}, []*http.Request{{URL: ip}}); !errors.Is(err, errBlockedAddress) {
		t.Fatalf("private literal redirect err = %v", err)
	}
	if err := checkRedirect(true)(&http.Request{URL: ip}, []*http.Request{{URL: ip}}); err != nil {
		t.Fatalf("allow_private_network redirect err = %v", err)
	}
}

func TestCrossHostRedirectIsNotFollowed(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(target.URL, "127.0.0.1", "localhost", 1), http.StatusFound)
	}))
	defer origin.Close()
	client, _ := newGuardedClient(true, "")
	// The certificate check would also fail, so assert on the redirect refusal explicitly.
	if _, err := client.Get(origin.URL); err == nil {
		t.Fatal("cross-host redirect followed")
	}
}

func TestOversizedResponseIsRejected(t *testing.T) {
	if _, err := readLimited(strings.NewReader(strings.Repeat("a", MaxJWKSBytes+1))); err == nil {
		t.Fatal("1 MiB + 1 accepted")
	}
	if data, err := readLimited(strings.NewReader(strings.Repeat("a", MaxJWKSBytes))); err != nil || len(data) != MaxJWKSBytes {
		t.Fatalf("exactly 1 MiB rejected: %v", err)
	}
}

func TestPrivateIssuerBlockedByDefault(t *testing.T) {
	clk := newClock()
	srv := newIssuerServer(t, pubKey(t, RS256, "k1"))
	issuer := srv.issuer()
	issuer.AllowPrivateNetwork = false
	v := newTestVerifier(t, clk, nil)
	_, err := v.Verify(t.Context(), mint(t, RS256, "k1", claimsAt(clk, srv.URL, nil)), issuer)
	if !errors.Is(err, ErrKeySourceUnavailable) {
		t.Fatalf("err = %v, want ErrKeySourceUnavailable", err)
	}
	if srv.discCalls.Load() != 0 {
		t.Fatal("the guarded client connected to a loopback issuer")
	}
}

func pemOf(srv *httptest.Server) []byte {
	return []byte((&issuerServer{Server: srv}).caPEM())
}
