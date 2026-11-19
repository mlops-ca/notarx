// Package upstreamauth owns protected repository credentials at outbound HTTP
// and cache identity seams. It does not construct stores or transports.
package upstreamauth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

const SecretPurpose = "repository-upstream-auth"

var ErrCredential = errors.New("upstream credential unavailable")

// Validate also runs on cache hits so a missing encryption key or corrupted
// credential can never be hidden by a previously authorized cache entry.
//
// An entra-workload credential is checked for configuration only: a cache hit must not
// trigger a token exchange.
func Validate(auth *repository.UpstreamAuth) error {
	if auth != nil && auth.Scheme == repository.UpstreamAuthSchemeEntraWorkload {
		if auth.Validate() != nil || entraProvider.Load().validate(auth) != nil {
			return ErrCredential
		}
		return nil
	}
	return Apply(&http.Request{Header: make(http.Header)}, auth)
}

func Apply(request *http.Request, auth *repository.UpstreamAuth) error {
	if err := auth.Validate(); err != nil {
		return ErrCredential
	}
	if auth == nil || auth.Scheme == repository.UpstreamAuthSchemeNone {
		return nil
	}
	if auth.Scheme == repository.UpstreamAuthSchemeEntraWorkload {
		return applyEntra(request, auth)
	}
	secret, err := secrets.Open(SecretPurpose, auth.Secret)
	if err != nil || secret == "" {
		return ErrCredential
	}
	switch auth.Scheme {
	case repository.UpstreamAuthSchemeBasic:
		request.SetBasicAuth(auth.Username, secret)
	case repository.UpstreamAuthSchemeBearer:
		request.Header.Set("Authorization", "Bearer "+secret)
	default:
		return ErrCredential
	}
	return nil
}

// applyEntra sets the Entra access token, or for an ACR upstream the registry access token. The
// reason for a failure is logged without any token; callers see only ErrCredential.
func applyEntra(request *http.Request, auth *repository.UpstreamAuth) error {
	provider := entraProvider.Load()
	var token string
	var err error
	if auth.RegistryKind == repository.RegistryKindACR {
		token, err = provider.ACRToken(request.Context(), auth, request.URL)
	} else {
		token, err = provider.AccessToken(request.Context(), auth)
	}
	if err != nil {
		slog.Warn("entra-workload credential unavailable", "repository", auth.Repository, "tenant", auth.TenantID, "client", auth.ClientID, "reason", err.Error())
		return ErrCredential
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return nil
}

// RetryUnauthorized is the once-only recovery for an entra-workload token the upstream refused
// (ADR 0020): when response is a 401 for such a credential it closes the response, drops the
// cached token and sends request again through do with a fresh exchange. Any other response is
// returned untouched. Requests with a body are never replayed.
func RetryUnauthorized(response *http.Response, request *http.Request, endpoint string, auth *repository.UpstreamAuth, do func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	if response == nil || response.StatusCode != http.StatusUnauthorized || request == nil || auth == nil ||
		auth.Scheme != repository.UpstreamAuthSchemeEntraWorkload || (request.Body != nil && request.Body != http.NoBody) {
		return response, nil
	}
	_ = response.Body.Close()
	Invalidate(auth)
	retry := request.Clone(request.Context())
	retry.Header.Del("Authorization")
	if err := ApplyOrigin(retry, endpoint, auth); err != nil {
		return nil, err
	}
	return do(retry)
}

func SameOrigin(left, right *url.URL) bool {
	return left != nil && right != nil && strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) && originPort(left) == originPort(right)
}

// RFC 9110 section 4.3.1 compares origins using the numeric port, including
// the scheme's default when omitted. URL validity is enforced by callers.
func originPort(target *url.URL) string {
	port := target.Port()
	if port == "" {
		switch strings.ToLower(target.Scheme) {
		case "https":
			return "443"
		case "http":
			return "80"
		default:
			return ""
		}
	}
	if port = strings.TrimLeft(port, "0"); port == "" {
		return "0"
	}
	return port
}

// ApplyOrigin never sends a repository credential to an allowed asset CDN.
// Egress permission does not grant that destination access to source secrets.
func ApplyOrigin(request *http.Request, endpoint string, auth *repository.UpstreamAuth) error {
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Host == "" {
		return ErrCredential
	}
	if !SameOrigin(origin, request.URL) {
		return nil
	}
	return Apply(request, auth)
}

// StripRedirect follows the entire redirect chain. A return to the original
// origin cannot recover a credential stripped at an earlier hop.
func StripRedirect(next *http.Request, via []*http.Request, endpoint string) {
	origin, err := url.Parse(endpoint)
	strip := err != nil || !SameOrigin(origin, next.URL)
	for _, previous := range via {
		if !SameOrigin(origin, previous.URL) {
			strip = true
		}
	}
	if strip {
		next.Header.Del("Authorization")
	}
	// Forward-proxy credentials are owned by the transport, never a redirect.
	next.Header.Del("Proxy-Authorization")
}

// Namespace includes configuration version even after credentials are cleared.
// Neither plaintext nor ciphertext is placed in the cache index or its URL.
func Namespace(name string, member repository.Member) string {
	encoded, _ := json.Marshal([]any{member.RepositoryID, member.RepositoryVersion, member.UpstreamAuth})
	sum := sha256.Sum256(encoded)
	return name + "\x00credentials:" + hex.EncodeToString(sum[:])
}
