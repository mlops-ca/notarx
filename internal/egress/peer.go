package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// ErrPeerAddressBlocked reports a remote instance endpoint that resolves to a private, loopback, link-local or
// otherwise non-public address while NOTARX_REMOTE_ALLOW_PRIVATE_NETWORKS is false.
var ErrPeerAddressBlocked = errors.New("remote instance address is private or not publicly routable")

// ErrPeerRedirect reports a redirect answer from a remote instance. Remote instance requests never follow one: a
// redirect could carry a credential or a request to another host.
var ErrPeerRedirect = errors.New("remote instance answered with a redirect")

// peerBlockedPrefixes are the ranges besides the private, loopback and link-local ones that are not public
// destinations, or that embed an arbitrary IPv4 address.
var peerBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved, includes the broadcast address
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64
	netip.MustParsePrefix("2002::/16"),     // 6to4
	netip.MustParsePrefix("2001::/32"),     // Teredo
	netip.MustParsePrefix("fec0::/10"),     // deprecated site-local
}

// PeerOptions configures the client used to talk to a remote notarx instance (ADR 0021, "Remote targets").
type PeerOptions struct {
	// AllowPrivateNetworks is NOTARX_REMOTE_ALLOW_PRIVATE_NETWORKS: when false, an endpoint that resolves to a
	// non-public address is refused at dial time.
	AllowPrivateNetworks bool
	// Hooks supplies name resolution and dialling; the zero value selects DefaultHooks. Tests inject here.
	Hooks Hooks
	// RootCAs replaces the system roots (tests and private deployments); nil keeps the system roots. TLS
	// verification itself cannot be disabled.
	RootCAs *x509.CertPool
	// Timeout bounds one request end to end; zero selects 10 seconds.
	Timeout time.Duration
}

// PeerAddressBlocked reports whether a remote instance must not be dialled at ip unless private networks are allowed.
func PeerAddressBlocked(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	address = address.Unmap()
	if PrivateAddress(ip) || !address.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range peerBlockedPrefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// NewPeerClient returns an HTTP client for remote instance requests: HTTPS only with mandatory certificate
// verification, no proxy (a proxy would dial on the policy's behalf), no redirects, and a dial that resolves the
// host once, refuses every non-public address unless private networks are allowed, and connects to exactly the
// addresses it checked, so a second resolution cannot rebind the connection.
func NewPeerClient(options PeerOptions) *http.Client {
	hooks := options.Hooks
	defaults := DefaultHooks()
	if hooks.LookupIP == nil {
		hooks.LookupIP = defaults.LookupIP
	}
	if hooks.DialContext == nil {
		hooks.DialContext = defaults.DialContext
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialPeer(ctx, hooks, options.AllowPrivateNetworks, network, address)
		},
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: options.RootCAs},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		DisableKeepAlives:     true,
	}
	return &http.Client{
		Transport:     peerHTTPSOnly{base: transport},
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type peerHTTPSOnly struct{ base http.RoundTripper }

func (t peerHTTPSOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("remote instance scheme %q is not allowed, https only", req.URL.Scheme)
	}
	return t.base.RoundTrip(req)
}

func dialPeer(ctx context.Context, hooks Hooks, allowPrivate bool, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	var addresses []net.IP
	if ip := net.ParseIP(host); ip != nil {
		addresses = []net.IP{ip}
	} else {
		if addresses, err = hooks.LookupIP(ctx, "ip", host); err != nil {
			return nil, fmt.Errorf("resolve remote instance host: %w", err)
		}
		if len(addresses) == 0 {
			return nil, errors.New("resolve remote instance host: no address")
		}
	}
	if !allowPrivate {
		for _, ip := range addresses {
			if PeerAddressBlocked(ip) {
				return nil, ErrPeerAddressBlocked
			}
		}
	}
	var last error
	for _, ip := range addresses {
		connection, dialErr := hooks.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		last = dialErr
	}
	return nil, last
}
