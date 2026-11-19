package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/tokenissuer"
)

const (
	issuerDiscoveryPath = "/.well-known/openid-configuration"
	issuerJWKSPath      = "/.well-known/jwks.json"
	// issuerRotationInterval is how often every node checks whether keys must move.
	issuerRotationInterval = time.Minute
	issuerCacheControl     = "public, max-age=300"
	// issuerRateLimit is the per-client budget of the anonymous issuer routes: a burst of 30
	// requests, refilled at one request per two seconds.
	issuerRateBurst    = 30
	issuerRateInterval = 2 * time.Second
	issuerRateMaxHosts = 10000
)

// IssuerRuntime is notarx as an OIDC issuer (ADR 0020): the signer, the public key set, the
// rotation worker and the two root-mounted anonymous routes. A nil runtime, or one built from a
// disabled configuration, serves 404 on both routes and runs nothing.
type IssuerRuntime struct {
	Config       config.IssuerConfig
	Signer       tokenissuer.Signer
	JWKS         *tokenissuer.JWKSService
	Manager      *tokenissuer.Manager
	DeploymentID string

	discovery     []byte
	discoveryETag string
	limiter       *clientLimiter
}

// NewIssuerRuntime wires the issuer for cfg on store. It returns nil when the issuer is disabled.
func NewIssuerRuntime(cfg config.IssuerConfig, store tokenissuer.Store, deploymentID string) (*IssuerRuntime, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	jwks := &tokenissuer.JWKSService{Store: store}
	signer, err := tokenissuer.NewLocalSigner(store, tokenissuer.SignerConfig{Alg: cfg.Alg})
	if err != nil {
		return nil, err
	}
	manager := &tokenissuer.Manager{Store: store, Config: cfg.Lifecycle(), Changed: jwks.Invalidate}
	if err := manager.Config.Validate(); err != nil {
		return nil, err
	}
	document, err := json.Marshal(map[string]any{
		"issuer":                                cfg.URL,
		"jwks_uri":                              cfg.URL + issuerJWKSPath,
		"id_token_signing_alg_values_supported": []string{jose.RS256, jose.ES256},
		"response_types_supported":              []string{"id_token"},
		"subject_types_supported":               []string{"public"},
		"claims_supported":                      []string{"iss", "sub", "aud", "iat", "nbf", "exp", "jti", "notarx_sa", "notarx_cred"},
	})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(document)
	return &IssuerRuntime{
		Config: cfg, Signer: signer, JWKS: jwks, Manager: manager, DeploymentID: deploymentID,
		discovery: document, discoveryETag: `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`,
		limiter: newClientLimiter(issuerRateBurst, issuerRateInterval, issuerRateMaxHosts),
	}, nil
}

// Enabled reports whether the issuer is serving.
func (r *IssuerRuntime) Enabled() bool { return r != nil && r.Config.Enabled }

// StartRotation runs the key rotation worker on this node until ctx ends. Every node runs it;
// the database lock lets one act at a time.
func (r *IssuerRuntime) StartRotation(ctx context.Context) {
	if !r.Enabled() {
		return
	}
	go r.Manager.Run(ctx, issuerRotationInterval)
}

// registerIssuerRoutes mounts the two anonymous routes. They are always registered so that a
// disabled issuer answers 404 itself instead of falling through to another handler.
func registerIssuerRoutes(mux *runtimeLogMux, runtime *IssuerRuntime) {
	mux.HandleFunc("GET "+issuerDiscoveryPath, runtime.serveDiscovery)
	mux.HandleFunc("GET "+issuerJWKSPath, runtime.serveJWKS)
}

func (r *IssuerRuntime) serveDiscovery(w http.ResponseWriter, req *http.Request) {
	if !r.admit(w, req) {
		return
	}
	writeIssuerDocument(w, req, r.discovery, r.discoveryETag)
}

func (r *IssuerRuntime) serveJWKS(w http.ResponseWriter, req *http.Request) {
	if !r.admit(w, req) {
		return
	}
	body, etag, err := r.JWKS.Document(req.Context())
	if err != nil {
		slog.Error("serve JWKS", "error", err)
		http.Error(w, "signing keys are temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	writeIssuerDocument(w, req, body, etag)
}

// admit answers 404 while the issuer is disabled and 429 over the per-client budget.
func (r *IssuerRuntime) admit(w http.ResponseWriter, req *http.Request) bool {
	if !r.Enabled() {
		http.NotFound(w, req)
		return false
	}
	if !r.limiter.allow(requestIPAddress(req), time.Now()) {
		w.Header().Set("Retry-After", strconv.Itoa(int(issuerRateInterval/time.Second)))
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return false
	}
	return true
}

func writeIssuerDocument(w http.ResponseWriter, req *http.Request, body []byte, etag string) {
	header := w.Header()
	header.Set("Cache-Control", issuerCacheControl)
	header.Set("ETag", etag)
	header.Set("Access-Control-Allow-Origin", "*")
	if etagMatches(req.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	header.Set("Content-Type", "application/json")
	header.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if req.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// etagMatches implements the weak comparison of RFC 9110 section 13.1.2 for If-None-Match.
func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

// clientLimiter is a token bucket per client address with a bounded table, so a flood of
// distinct addresses cannot grow memory without limit.
type clientLimiter struct {
	burst    float64
	interval time.Duration
	maxHosts int

	mu      sync.Mutex
	buckets map[string]limiterBucket
}

type limiterBucket struct {
	tokens float64
	last   time.Time
}

func newClientLimiter(burst int, interval time.Duration, maxHosts int) *clientLimiter {
	return &clientLimiter{burst: float64(burst), interval: interval, maxHosts: maxHosts, buckets: map[string]limiterBucket{}}
}

func (l *clientLimiter) allow(client string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket, ok := l.buckets[client]
	if !ok {
		if len(l.buckets) >= l.maxHosts {
			l.buckets = map[string]limiterBucket{}
		}
		bucket = limiterBucket{tokens: l.burst, last: now}
	}
	bucket.tokens = min(l.burst, bucket.tokens+float64(now.Sub(bucket.last))/float64(l.interval))
	bucket.last = now
	allowed := bucket.tokens >= 1
	if allowed {
		bucket.tokens--
	}
	l.buckets[client] = bucket
	return allowed
}
