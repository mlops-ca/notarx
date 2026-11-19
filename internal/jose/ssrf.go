package jose

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

const (
	// MaxJWKSBytes bounds every discovery document and JWKS body.
	MaxJWKSBytes = 1 << 20
	maxRedirects = 3
	fetchTimeout = 10 * time.Second
)

var errBlockedAddress = errors.New("jose: address is private, loopback or link-local")

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64 can embed any IPv4 address
	netip.MustParsePrefix("fec0::/10"),     // deprecated site-local
}

// blockedAddr reports whether an address must not be dialled unless the issuer
// opted into private networks.
func blockedAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return true
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// dialControl runs after name resolution with the concrete IP about to be
// connected, which closes DNS rebinding: a name that resolves to a public address
// when validated and to a private one when dialled is rejected here.
func dialControl(allowPrivate bool) func(network, address string, _ syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		addr, err := netip.ParseAddr(host)
		if err != nil {
			return fmt.Errorf("jose: dial address %q is not an IP", host)
		}
		if blockedAddr(addr) {
			return errBlockedAddress
		}
		return nil
	}
}

type httpsOnly struct{ base http.RoundTripper }

func (t httpsOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("jose: scheme %q is not allowed, https only", req.URL.Scheme)
	}
	return t.base.RoundTrip(req)
}

// checkRedirect allows a bounded number of https redirects that stay on the host
// of the original request. The dial-time check still runs for every hop.
func checkRedirect(allowPrivate bool) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return errors.New("jose: too many redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("jose: redirect to a non-https URL")
		}
		if !strings.EqualFold(req.URL.Hostname(), via[0].URL.Hostname()) {
			return errors.New("jose: redirect to another host")
		}
		if addr, err := netip.ParseAddr(req.URL.Hostname()); err == nil && !allowPrivate && blockedAddr(addr) {
			return errBlockedAddress
		}
		return nil
	}
}

// newGuardedClient builds the SSRF-guarded client used for discovery and JWKS
// fetches: https only, no proxy (a proxy would dial on the guard's behalf), a
// dial-time address check, bounded redirects and timeouts. A non-empty caPEM
// replaces the system roots for this issuer.
func newGuardedClient(allowPrivate bool, caPEM string) (*http.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if strings.TrimSpace(caPEM) != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, badConfig("ca_bundle_pem contains no certificate")
		}
		tlsConfig.RootCAs = pool
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: dialControl(allowPrivate)}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{Transport: httpsOnly{base: transport}, Timeout: fetchTimeout, CheckRedirect: checkRedirect(allowPrivate)}, nil
}

// readLimited reads at most MaxJWKSBytes and fails when the body is larger.
func readLimited(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, MaxJWKSBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxJWKSBytes {
		return nil, errors.New("jose: response exceeds 1 MiB")
	}
	return data, nil
}
