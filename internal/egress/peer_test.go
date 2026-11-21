package egress

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func peerGet(client *http.Client, target string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(request)
}

func TestPeerClientRefusesNonPublicAddresses(t *testing.T) {
	var dials atomic.Int32
	hooks := Hooks{DialContext: func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("must not dial")
	}}
	client := NewPeerClient(PeerOptions{Hooks: hooks})
	for _, host := range []string{
		"127.0.0.1", "[::1]", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.169.254", "0.0.0.0", "100.64.0.1",
		"198.18.0.1", "192.0.0.1", "240.0.0.1", "[fd00::1]", "[fe80::1]", "[::ffff:10.0.0.1]", "[::ffff:127.0.0.1]", "[64:ff9b::a00:1]", "[2002:a00:1::1]", "224.0.0.1",
	} {
		_, err := peerGet(client, "https://"+host+":8443/.well-known/notarx")
		if !errors.Is(err, ErrPeerAddressBlocked) {
			t.Errorf("%s: err = %v, want ErrPeerAddressBlocked", host, err)
		}
	}
	if dials.Load() != 0 {
		t.Fatalf("dialled %d times", dials.Load())
	}
}

func TestPeerClientChecksEveryResolvedAddressAndPinsTheDial(t *testing.T) {
	var lookups atomic.Int32
	var dialled []string
	resolved := []net.IP{net.ParseIP("93.184.216.34")}
	hooks := Hooks{
		LookupIP: func(context.Context, string, string) ([]net.IP, error) {
			lookups.Add(1)
			return resolved, nil
		},
		DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
			dialled = append(dialled, address)
			return nil, errors.New("stop here")
		},
	}
	client := NewPeerClient(PeerOptions{Hooks: hooks})

	// A name with one private answer among public ones is refused: a rebinding mix cannot pick the private one later.
	resolved = []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.1.2.3")}
	if _, err := peerGet(client, "https://peer.example.test/.well-known/notarx"); !errors.Is(err, ErrPeerAddressBlocked) {
		t.Fatalf("mixed answers err = %v", err)
	}
	if len(dialled) != 0 {
		t.Fatalf("dialled %v despite a blocked answer", dialled)
	}

	resolved = []net.IP{net.ParseIP("93.184.216.34")}
	if _, err := peerGet(client, "https://peer.example.test:9443/.well-known/notarx"); err == nil {
		t.Fatal("expected the stubbed dial to fail")
	}
	if len(dialled) != 1 || dialled[0] != "93.184.216.34:9443" {
		t.Fatalf("dialled %v, want exactly the checked address", dialled)
	}
	if lookups.Load() != 2 {
		t.Fatalf("lookups = %d, want one per request", lookups.Load())
	}
}

func TestPeerClientAllowsPrivateNetworksOnlyWhenConfigured(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())

	if _, err := peerGet(NewPeerClient(PeerOptions{RootCAs: roots}), server.URL); !errors.Is(err, ErrPeerAddressBlocked) {
		t.Fatalf("private network without the allow flag: err = %v", err)
	}
	response, err := peerGet(NewPeerClient(PeerOptions{RootCAs: roots, AllowPrivateNetworks: true}), server.URL)
	if err != nil {
		t.Fatalf("private network with the allow flag: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestPeerClientDoesNotFollowRedirects(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		_, _ = w.Write([]byte("internal"))
	}))
	defer target.Close()
	var origin *httptest.Server
	origin = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cross-host":
			http.Redirect(w, r, target.URL+"/secret", http.StatusFound)
		case "/same-host":
			http.Redirect(w, r, origin.URL+"/final", http.StatusTemporaryRedirect)
		default:
			_, _ = w.Write([]byte("final"))
		}
	}))
	defer origin.Close()
	roots := x509.NewCertPool()
	roots.AddCert(origin.Certificate())
	roots.AddCert(target.Certificate())
	client := NewPeerClient(PeerOptions{RootCAs: roots, AllowPrivateNetworks: true})
	for _, path := range []string{"/cross-host", "/same-host"} {
		response, err := peerGet(client, origin.URL+path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_ = response.Body.Close()
		if !IsRedirect(response.StatusCode) {
			t.Fatalf("%s: status %d, the redirect must be returned, not followed", path, response.StatusCode)
		}
	}
	if targetHits.Load() != 0 {
		t.Fatalf("the redirect target was contacted %d times", targetHits.Load())
	}
}

func TestPeerClientVerifiesTLSAndRefusesPlainHTTP(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	// No root for the test certificate: verification fails even with private networks allowed, and no option disables it.
	if _, err := peerGet(NewPeerClient(PeerOptions{AllowPrivateNetworks: true}), server.URL); err == nil {
		t.Fatal("an unverifiable certificate was accepted")
	} else {
		var unknown x509.UnknownAuthorityError
		if !errors.As(err, &unknown) {
			t.Fatalf("err = %v, want an unknown authority error", err)
		}
	}
	plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer plain.Close()
	if _, err := peerGet(NewPeerClient(PeerOptions{AllowPrivateNetworks: true}), plain.URL); err == nil {
		t.Fatal("a plain HTTP request was sent")
	}
}

func TestNonPublicAddress(t *testing.T) {
	for _, public := range []string{"93.184.216.34", "8.8.8.8", "2606:4700:4700::1111"} {
		if NonPublicAddress(net.ParseIP(public)) {
			t.Errorf("%s must be allowed", public)
		}
	}
	if !NonPublicAddress(nil) || !NonPublicAddress(net.IP{1, 2, 3}) {
		t.Error("an invalid address must be blocked")
	}
}

func TestNonPublicAddressSpecialPurposeRanges(t *testing.T) {
	for _, blocked := range []string{
		"0.0.0.0", "0.1.2.3", "192.0.2.1", "198.51.100.7", "203.0.113.9", "192.88.99.1", "100.64.0.1", "198.19.255.1", "192.0.0.9", "255.255.255.255",
		"100::1", "2001:db8::1", "2001::1", "3fff::1", "::ffff:192.0.2.1", "64:ff9b::1", "2002::1", "fec0::1",
	} {
		if !NonPublicAddress(net.ParseIP(blocked)) {
			t.Errorf("%s must be non-public", blocked)
		}
	}
	for _, public := range []string{"93.184.216.34", "192.0.3.1", "198.51.101.1", "203.0.114.1", "2606:4700:4700::1111"} {
		if NonPublicAddress(net.ParseIP(public)) {
			t.Errorf("%s must be public", public)
		}
	}
}
