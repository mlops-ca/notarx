package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// hopFixture serves the CDN side of a redirect on a TLS test server and
// returns a follower whose dial hook records addresses and connects to it.
type hopFixture struct {
	follower *RedirectFollower
	server   *httptest.Server
	mu       sync.Mutex
	dialed   []string
	seen     http.Header
}

func newHopFixture(t *testing.T, allowlist []string, ips map[string][]net.IP, handler http.HandlerFunc) *hopFixture {
	t.Helper()
	fixture := &hopFixture{}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		fixture.seen = r.Header.Clone()
		fixture.mu.Unlock()
		if handler != nil {
			handler(w, r)
			return
		}
		_, _ = w.Write([]byte("payload"))
	}))
	t.Cleanup(fixture.server.Close)
	pool := x509.NewCertPool()
	pool.AddCert(fixture.server.Certificate())
	base := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "example.com", MinVersion: tls.VersionTLS12}}}
	hooks := testHooks(t, ips)
	hooks.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		fixture.mu.Lock()
		fixture.dialed = append(fixture.dialed, address)
		fixture.mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, fixture.server.Listener.Addr().String())
	}
	fixture.follower = FollowOnce(allowlist, nil, hooks, base)
	return fixture
}

func redirectTo(t *testing.T, location string) (*http.Request, *http.Response) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "https://origin.example/file", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "session=1")
	req.Header.Set("Proxy-Authorization", "Basic abc")
	req.Header.Set("Range", "bytes=0-3")
	header := http.Header{"Location": {location}}
	return req, &http.Response{StatusCode: http.StatusFound, Header: header, Body: http.NoBody, Request: req}
}

func TestFollowOnceFollowsOneAllowlistedHopAndStripsCredentials(t *testing.T) {
	public := map[string][]net.IP{"example.com": {net.ParseIP("93.184.216.34")}}
	fixture := newHopFixture(t, []string{"*.com"}, public, nil)
	req, resp := redirectTo(t, "https://example.com/blob")
	got, err := fixture.follower.Follow(context.Background(), req, resp)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	defer got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", got.StatusCode)
	}
	for _, name := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if fixture.seen.Get(name) != "" {
			t.Errorf("%s reached the redirect target", name)
		}
	}
	if fixture.seen.Get("Range") != "bytes=0-3" {
		t.Errorf("Range = %q, want it kept", fixture.seen.Get("Range"))
	}
	if len(fixture.dialed) != 1 || fixture.dialed[0] != "93.184.216.34:443" {
		t.Errorf("dialed = %v, want one pinned dial to the resolved address", fixture.dialed)
	}
	if req.Header.Get("Authorization") == "" {
		t.Error("original request headers must not be mutated")
	}
}

func TestFollowOnceRejections(t *testing.T) {
	public := map[string][]net.IP{
		"example.com":   {net.ParseIP("93.184.216.34")},
		"loopback.test": {net.ParseIP("127.0.0.1")},
		"private.test":  {net.ParseIP("10.0.0.5")},
		"link.test":     {net.ParseIP("169.254.169.254")},
		"mixed.test":    {net.ParseIP("93.184.216.34"), net.ParseIP("192.168.1.1")},
	}
	cases := []struct {
		name      string
		allowlist []string
		location  string
	}{
		{"http downgrade", []string{"example.com"}, "http://example.com/blob"},
		{"allowlist miss", []string{"cdn.other"}, "https://example.com/blob"},
		{"empty allowlist", nil, "https://example.com/blob"},
		{"wildcard does not match apex", []string{"*.example.com"}, "https://example.com/blob"},
		{"credentials in url", []string{"example.com"}, "https://user:pw@example.com/blob"},
		{"missing location", []string{"example.com"}, ""},
		{"loopback", []string{"loopback.test"}, "https://loopback.test/blob"},
		{"private", []string{"private.test"}, "https://private.test/blob"},
		{"link local", []string{"link.test"}, "https://link.test/blob"},
		{"mixed answers", []string{"mixed.test"}, "https://mixed.test/blob"},
		{"ip literal private", []string{"10.0.0.5"}, "https://10.0.0.5/blob"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHopFixture(t, tc.allowlist, public, nil)
			req, resp := redirectTo(t, tc.location)
			got, err := fixture.follower.Follow(context.Background(), req, resp)
			if err == nil {
				got.Body.Close()
				t.Fatal("Follow succeeded, want rejection")
			}
			if !errors.Is(err, ErrRedirectRejected) {
				t.Fatalf("error = %v, want ErrRedirectRejected", err)
			}
			if len(fixture.dialed) != 0 {
				t.Fatalf("rejected hop dialed %v", fixture.dialed)
			}
		})
	}
}

func TestFollowOnceRejectsSecondRedirect(t *testing.T) {
	public := map[string][]net.IP{"example.com": {net.ParseIP("93.184.216.34")}}
	fixture := newHopFixture(t, []string{"example.com"}, public, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/again", http.StatusFound)
	})
	req, resp := redirectTo(t, "https://example.com/blob")
	got, err := fixture.follower.Follow(context.Background(), req, resp)
	if err == nil {
		got.Body.Close()
		t.Fatal("second redirect was followed")
	}
	if !errors.Is(err, ErrRedirectRejected) || !strings.Contains(err.Error(), "second redirect") {
		t.Fatalf("error = %v", err)
	}
	if len(fixture.dialed) != 1 {
		t.Fatalf("dialed %v, want exactly one hop", fixture.dialed)
	}
}

func TestFollowOnceDNSRebindingCannotChangeDialTarget(t *testing.T) {
	var mu sync.Mutex
	lookups := 0
	public := map[string][]net.IP{"example.com": {net.ParseIP("93.184.216.34")}}
	fixture := newHopFixture(t, []string{"example.com"}, public, nil)
	fixture.follower.Hooks.LookupIP = func(context.Context, string, string) ([]net.IP, error) {
		mu.Lock()
		defer mu.Unlock()
		lookups++
		if lookups == 1 {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	req, resp := redirectTo(t, "https://example.com/blob")
	got, err := fixture.follower.Follow(context.Background(), req, resp)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	got.Body.Close()
	for _, address := range fixture.dialed {
		if strings.HasPrefix(address, "127.") {
			t.Fatalf("dialed rebound address %s", address)
		}
	}
	if lookups != 1 {
		t.Fatalf("lookups = %d, want the dial to reuse the checked answer", lookups)
	}
}

func TestGuardDialRefusesPrivateLiteralAtDialTime(t *testing.T) {
	called := false
	guarded := guardDial(func(context.Context, string, string) (net.Conn, error) {
		called = true
		return nil, errors.New("reached")
	})
	for _, address := range []string{"127.0.0.1:443", "10.1.1.1:443", "169.254.169.254:443", "[::1]:443"} {
		if _, err := guarded(context.Background(), "tcp", address); err == nil || err.Error() == "reached" {
			t.Fatalf("%s: err = %v, want refusal", address, err)
		}
	}
	if _, err := guarded(context.Background(), "tcp", "no-port"); err == nil {
		t.Fatal("malformed address accepted")
	}
	if called {
		t.Fatal("private address reached the dialer")
	}
	if _, err := guarded(context.Background(), "tcp", "93.184.216.34:443"); err == nil || err.Error() != "reached" {
		t.Fatalf("public address was not forwarded: %v", err)
	}
}

func TestFollowRejectsNonRedirectAndUnsafeMethods(t *testing.T) {
	follower := FollowOnce([]string{"example.com"}, nil, testHooks(t, nil), nil)
	req, _ := redirectTo(t, "https://example.com/x")
	ok := &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}
	if _, err := follower.Follow(context.Background(), req, ok); !errors.Is(err, ErrRedirectRejected) {
		t.Fatalf("non-redirect: %v", err)
	}
	post := req.Clone(context.Background())
	post.Method = http.MethodPost
	_, resp := redirectTo(t, "https://example.com/x")
	if _, err := follower.Follow(context.Background(), post, resp); !errors.Is(err, ErrRedirectRejected) {
		t.Fatalf("POST: %v", err)
	}
}

func TestDoFollowsOriginRedirectOnce(t *testing.T) {
	cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("credential reached the CDN")
		}
		_, _ = w.Write([]byte("bytes"))
	}))
	defer cdn.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("origin lost its credential")
		}
		http.Redirect(w, r, "https://example.com/blob", http.StatusFound)
	}))
	defer origin.Close()
	pool := x509.NewCertPool()
	pool.AddCert(cdn.Certificate())
	pool.AddCert(origin.Certificate())
	base := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	hooks := testHooks(t, map[string][]net.IP{"example.com": {net.ParseIP("93.184.216.34")}})
	hooks.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, cdn.Listener.Addr().String())
	}
	follower := FollowOnce([]string{"example.com"}, nil, hooks, base)
	// The CDN certificate is valid for example.com; the origin client dials
	// the origin test server directly.
	req, _ := http.NewRequest(http.MethodGet, origin.URL+"/file", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := follower.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	passthrough, _ := http.NewRequest(http.MethodGet, cdn.URL+"/plain", nil)
	direct, err := follower.Do(context.Background(), passthrough)
	if err != nil || direct.StatusCode != http.StatusOK {
		t.Fatalf("non-redirect passthrough: %v", err)
	}
	direct.Body.Close()
}
