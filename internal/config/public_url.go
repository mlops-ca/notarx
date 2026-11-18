package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// PublicURLEnv names the single shared public-URL setting (ADR 0013). No other
// setting may carry the externally visible base URL.
const PublicURLEnv = "NOTARX_PUBLIC_URL"

// ParsePublicURL validates the externally visible base URL of this instance. An
// empty (or whitespace-only) value is allowed and returns "": features that need
// the URL are disabled or refuse to start (the result is nil). A non-empty value must be an absolute
// https:// URL with a host and no trailing slash, user information, query or
// fragment; http:// is accepted only for a loopback host.
func ParsePublicURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s is not a valid URL", PublicURLEnv)
	}
	if !parsed.IsAbs() || parsed.Opaque != "" || parsed.Hostname() == "" {
		return nil, fmt.Errorf("%s must be an absolute URL with a host", PublicURLEnv)
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(parsed.Hostname()) {
			return nil, fmt.Errorf("%s must use https unless the host is loopback", PublicURLEnv)
		}
	default:
		return nil, fmt.Errorf("%s must use https unless the host is loopback", PublicURLEnv)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("%s must not contain user information", PublicURLEnv)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(raw, "#") {
		return nil, fmt.Errorf("%s must not contain a query or fragment", PublicURLEnv)
	}
	if strings.HasSuffix(raw, "/") {
		return nil, fmt.Errorf("%s must not end with a slash", PublicURLEnv)
	}
	return parsed, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// PublicURL is the typed accessor for NOTARX_PUBLIC_URL. It returns the validated
// URL and true, or nil and false when the setting is empty so callers can disable
// or refuse the features that need it. The returned value is a copy.
func (c Config) PublicURL() (*url.URL, bool) {
	if c.publicURL == nil {
		return nil, false
	}
	copied := *c.publicURL
	return &copied, true
}
