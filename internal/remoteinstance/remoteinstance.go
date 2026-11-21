// Package remoteinstance is the client side of the notarx instance protocol (ADR 0021, "Remote targets"): the
// well-known document a deployment serves at GET /.well-known/notarx and the check a sender runs against a remote
// instance when it is registered and when it is tested. Every request goes through internal/egress.NewPeerClient.
package remoteinstance

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/egress"
)

const (
	// WellKnownPath is the root-mounted path of the instance document.
	WellKnownPath = "/.well-known/notarx"
	// APIVersion is the version of the instance protocol this build speaks; a sender requires an equal version.
	APIVersion = "1"
	// MaxDocumentBytes bounds the body read from a remote instance.
	MaxDocumentBytes = 64 << 10
	// MaxBaseURLLength bounds a registered base URL.
	MaxBaseURLLength = 2048
)

// Default import limits advertised until the importer slice makes them configurable (NOTARX_IMPORT_MAX_*).
const (
	DefaultMaxBlobBytes                int64 = 10 << 30
	DefaultMaxSessionBytes             int64 = 50 << 30
	DefaultMaxBlobsPerSession          int32 = 2000
	DefaultMaxOpenSessionsPerPrincipal int32 = 4
)

// Limits are the import limits a sender must respect.
type Limits struct {
	MaxBlobBytes                int64 `json:"maxBlobBytes"`
	MaxSessionBytes             int64 `json:"maxSessionBytes"`
	MaxBlobsPerSession          int32 `json:"maxBlobsPerSession"`
	MaxOpenSessionsPerPrincipal int32 `json:"maxOpenSessionsPerPrincipal"`
}

// DefaultLimits returns the limits of an instance with default configuration.
func DefaultLimits() Limits {
	return Limits{MaxBlobBytes: DefaultMaxBlobBytes, MaxSessionBytes: DefaultMaxSessionBytes,
		MaxBlobsPerSession: DefaultMaxBlobsPerSession, MaxOpenSessionsPerPrincipal: DefaultMaxOpenSessionsPerPrincipal}
}

// Document is the body of GET /.well-known/notarx.
type Document struct {
	InstanceID string   `json:"instanceId"`
	APIVersion string   `json:"apiVersion"`
	Formats    []string `json:"formats"`
	Limits     Limits   `json:"limits"`
}

// ErrInvalidDocument reports a remote answer that is not a usable instance document.
var ErrInvalidDocument = errors.New("remote instance document is invalid")

// Validate checks the document and returns it with the instance id in canonical form.
func (d Document) Validate() (Document, error) {
	id, err := uuid.Parse(d.InstanceID)
	if err != nil {
		return Document{}, fmt.Errorf("%w: instanceId is not a UUID", ErrInvalidDocument)
	}
	d.InstanceID = id.String()
	if d.APIVersion != APIVersion {
		return Document{}, fmt.Errorf("%w: apiVersion %q is not supported, expected %q", ErrInvalidDocument, truncate(d.APIVersion, 16), APIVersion)
	}
	seen := make(map[string]bool, len(d.Formats))
	for _, format := range d.Formats {
		if format == "" || len(format) > 32 || seen[format] {
			return Document{}, fmt.Errorf("%w: formats must be unique names of at most 32 bytes", ErrInvalidDocument)
		}
		seen[format] = true
	}
	if d.Formats == nil {
		d.Formats = []string{}
	}
	limits := d.Limits
	if limits.MaxBlobBytes < 1 || limits.MaxSessionBytes < 1 || limits.MaxBlobsPerSession < 1 || limits.MaxOpenSessionsPerPrincipal < 1 {
		return Document{}, fmt.Errorf("%w: limits must be positive", ErrInvalidDocument)
	}
	return d, nil
}

// NormalizeBaseURL validates a remote base URL and returns it without a trailing slash. The scheme must be https;
// credentials, a query and a fragment are refused.
func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxBaseURLLength {
		return "", errors.New("remote base URL must be 1 to 2048 characters")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("remote base URL must be an HTTPS URL without credentials, query or fragment")
	}
	if port := parsed.Port(); port != "" && (len(port) > 5 || strings.Trim(port, "0123456789") != "") {
		return "", errors.New("remote base URL port is invalid")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String(), nil
}

// Discoverer fetches the instance document of a remote instance.
type Discoverer interface {
	Discover(ctx context.Context, baseURL string) (Document, error)
}

// Options configure the production Discoverer.
type Options struct {
	// AllowPrivateNetworks is NOTARX_REMOTE_ALLOW_PRIVATE_NETWORKS.
	AllowPrivateNetworks bool
	// RootCAs and Hooks are injection points for tests and private deployments; see egress.PeerOptions.
	RootCAs *x509.CertPool
	Hooks   egress.Hooks
}

type client struct{ http *http.Client }

// NewDiscoverer returns a Discoverer that reaches remote instances only through the egress policy. The request is
// anonymous: no credential of any kind is sent to the well-known path.
func NewDiscoverer(options Options) Discoverer {
	return client{http: egress.NewPeerClient(egress.PeerOptions{AllowPrivateNetworks: options.AllowPrivateNetworks, RootCAs: options.RootCAs, Hooks: options.Hooks})}
}

// Discover fetches and validates GET {baseURL}/.well-known/notarx.
func (c client) Discover(ctx context.Context, baseURL string) (Document, error) {
	normalized, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return Document{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, normalized+WellKnownPath, nil)
	if err != nil {
		return Document{}, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return Document{}, fmt.Errorf("remote instance request failed: %w", unwrapURLError(err))
	}
	defer func() { _ = response.Body.Close() }()
	if egress.IsRedirect(response.StatusCode) {
		return Document{}, egress.ErrPeerRedirect
	}
	if response.StatusCode != http.StatusOK {
		return Document{}, fmt.Errorf("remote instance answered status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxDocumentBytes+1))
	if err != nil {
		return Document{}, fmt.Errorf("read remote instance document: %w", unwrapURLError(err))
	}
	if len(body) > MaxDocumentBytes {
		return Document{}, fmt.Errorf("%w: larger than %d bytes", ErrInvalidDocument, MaxDocumentBytes)
	}
	var document Document
	if err = json.Unmarshal(body, &document); err != nil {
		return Document{}, fmt.Errorf("%w: not a JSON object", ErrInvalidDocument)
	}
	return document.Validate()
}

// unwrapURLError drops the URL wrapper of a transport error: the URL is already known to the caller.
func unwrapURLError(err error) error {
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return urlError.Err
	}
	return err
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

// TokenPurpose is the purpose bound to a sealed static token (internal/secrets); the id is the remote instance id.
func TokenPurpose(id string) string { return "remote-instance:" + id }
