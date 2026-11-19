package upstreamauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/tokenissuer"
)

const (
	// AudienceGlobal, AudienceUSGov and AudienceChina are the audiences of Entra federated
	// credentials per cloud (ADR 0020).
	AudienceGlobal = "api://AzureADTokenExchange"
	AudienceUSGov  = "api://AzureADTokenExchangeUSGov"
	AudienceChina  = "api://AzureADTokenExchangeChina"

	assertionType      = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	assertionLifetime  = 5 * time.Minute
	tokenRefreshMargin = 5 * time.Minute
	exchangeTimeout    = 20 * time.Second
	maxExchangeBody    = 1 << 20
)

// Errors of the entra-workload scheme. None of them carries a token or an upstream body.
var (
	ErrEntraNotConfigured = errors.New("entra-workload is not configured: the OIDC issuer is disabled")
	ErrEntraAlgorithm     = errors.New("entra-workload requires an RS256 signing key")
	ErrEntraExchange      = errors.New("entra token exchange failed")
)

// AssertionSigner signs the client assertion in-process. tokenissuer.Signer satisfies it.
type AssertionSigner interface {
	Algorithm() string
	Sign(ctx context.Context, claims tokenissuer.Claims) (compact string, kid string, err error)
}

// EntraExchange is what one self-signed exchange reports to the audit hook. It never contains
// a token.
type EntraExchange struct {
	Subject, TenantID, ClientID, Repository, Scope, KeyID, JTI string
	// Registry is set for the ACR exchange steps.
	Registry string
	// Operation is "entra.token" or "acr.refresh" or "acr.access".
	Operation string
	// Succeeded reports whether the token service answered with a usable token. The event is
	// emitted either way, once the exchange finished.
	Succeeded bool
}

// Entra exchanges notarx assertions for Entra access tokens and caches the results.
type Entra struct {
	Signer       AssertionSigner
	IssuerURL    string
	DeploymentID string
	// AuthorityHost is the validated https origin of the Entra login host. Token requests go
	// to this host and no other.
	AuthorityHost string
	// Audience overrides the audience derived from AuthorityHost (NOTARX_ENTRA_AUDIENCE).
	Audience string
	// HTTPClient is used for every exchange; nil selects a client with a timeout and no
	// redirects. Tests inject a client for an httptest server.
	HTTPClient *http.Client
	// ACRHostAllowed decides which registry hosts may receive an Entra token; nil allows
	// *.azurecr.io, *.azurecr.us and *.azurecr.cn.
	ACRHostAllowed func(host string) bool
	// MaxLifetime is NOTARX_ISSUER_MAX_TOKEN_LIFETIME; assertions never outlive it. Zero means the
	// hard maximum.
	MaxLifetime time.Duration
	// Do sends every exchange request; nil uses HTTPClient. The runtime installs the proxy egress
	// path (pinned DNS, reserved-address refusal, environment proxy) here.
	Do func(*http.Request) (*http.Response, error)
	// Audit receives one event per exchange, after it finished, with its outcome.
	Audit func(ctx context.Context, event EntraExchange)
	Now   func() time.Time

	flight singleflight.Group
	mu     sync.Mutex
	tokens map[string]cachedToken
}

type cachedToken struct {
	value   string
	refresh time.Time // reuse until this instant
}

var entraProvider atomic.Pointer[Entra]

// SetEntra installs the process-wide provider used by Apply; nil removes it. The runtime
// calls it once at startup when the issuer is enabled.
func SetEntra(provider *Entra) { entraProvider.Store(provider) }

func (e *Entra) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Entra) client() *http.Client {
	if e.HTTPClient != nil {
		return e.HTTPClient
	}
	return &http.Client{Timeout: exchangeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// audience derives the assertion audience from the authority host.
func (e *Entra) audience() (string, error) {
	if e.Audience != "" {
		return e.Audience, nil
	}
	parsed, err := url.Parse(e.AuthorityHost)
	if err != nil {
		return "", ErrEntraExchange
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "login.microsoftonline.com":
		return AudienceGlobal, nil
	case "login.microsoftonline.us":
		return AudienceUSGov, nil
	case "login.partner.microsoftonline.cn":
		return AudienceChina, nil
	}
	return "", fmt.Errorf("%w: set NOTARX_ENTRA_AUDIENCE for authority host %s", ErrEntraExchange, parsed.Hostname())
}

// validate checks the configuration without any network call; Validate uses it on every cache hit.
func (e *Entra) validate(auth *repository.UpstreamAuth) error {
	if e == nil || e.Signer == nil || e.IssuerURL == "" || e.AuthorityHost == "" {
		return ErrEntraNotConfigured
	}
	if e.Signer.Algorithm() != jose.RS256 {
		return ErrEntraAlgorithm
	}
	if _, err := e.audience(); err != nil {
		return err
	}
	return auth.Validate()
}

func (e *Entra) subject(auth *repository.UpstreamAuth) string {
	if auth.Subject != "" {
		return auth.Subject
	}
	return "notarx:instance:" + e.DeploymentID
}

func (e *Entra) cached(key string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	token, ok := e.tokens[key]
	if !ok || !e.now().Before(token.refresh) {
		return "", false
	}
	return token.value, true
}

func (e *Entra) store(key, value string, refresh time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tokens == nil {
		e.tokens = map[string]cachedToken{}
	}
	now := e.now()
	for name, token := range e.tokens { // drop what can no longer be reused
		if !now.Before(token.refresh) {
			delete(e.tokens, name)
		}
	}
	e.tokens[key] = cachedToken{value: value, refresh: refresh}
}

// Invalidate drops the cached tokens of auth. The proxy calls it after the upstream answered 401,
// then retries once with a fresh exchange.
func (e *Entra) Invalidate(auth *repository.UpstreamAuth) {
	if e == nil || auth == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	prefix := e.tokenBase(auth) + "\x01"
	for key := range e.tokens {
		if strings.HasPrefix(key, prefix) {
			delete(e.tokens, key)
		}
	}
}

// tokenBase identifies the Entra application, scope and assertion subject every cached token of
// auth derives from; the fields cannot contain the 0x01 separator (validated) so prefixes never
// collide. The subject is part of the key because the federated credential that accepts a token
// is matched on it.
func (e *Entra) tokenBase(auth *repository.UpstreamAuth) string {
	return auth.TenantID + "\x00" + auth.ClientID + "\x00" + auth.Scope + "\x00" + e.subject(auth)
}

// Invalidate drops the cached Entra tokens of auth on the installed provider.
func Invalidate(auth *repository.UpstreamAuth) { entraProvider.Load().Invalidate(auth) }

// AccessToken returns an Entra access token for auth, exchanging a fresh assertion when the cache
// has none. Concurrent callers share one exchange.
func (e *Entra) AccessToken(ctx context.Context, auth *repository.UpstreamAuth) (string, error) {
	if err := e.validate(auth); err != nil {
		return "", err
	}
	key := e.tokenBase(auth) + "\x01aad"
	if token, ok := e.cached(key); ok {
		return token, nil
	}
	value, err, _ := e.flight.Do(key, func() (any, error) {
		if token, ok := e.cached(key); ok {
			return token, nil
		}
		// The exchange must outlive one caller that gives up, or every waiter would fail with it.
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), exchangeTimeout)
		defer cancel()
		token, expires, err := e.exchange(work, auth)
		if err != nil {
			return "", err
		}
		e.store(key, token, expires.Add(-tokenRefreshMargin))
		return token, nil
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func (e *Entra) exchange(ctx context.Context, auth *repository.UpstreamAuth) (string, time.Time, error) {
	audience, err := e.audience()
	if err != nil {
		return "", time.Time{}, err
	}
	subject := e.subject(auth)
	lifetime := assertionLifetime
	if e.MaxLifetime > 0 {
		lifetime = min(lifetime, e.MaxLifetime)
	}
	claims, err := tokenissuer.ClaimBuilder{Issuer: e.IssuerURL, MaxLifetime: lifetime, Now: e.now}.Build(tokenissuer.ClaimRequest{
		Subject: subject, Audiences: []string{audience}, Lifetime: lifetime,
	})
	if err != nil {
		return "", time.Time{}, ErrEntraExchange
	}
	assertion, kid, err := e.Signer.Sign(ctx, claims)
	if err != nil {
		return "", time.Time{}, ErrEntraExchange
	}
	event := EntraExchange{Operation: "entra.token", Subject: subject, TenantID: auth.TenantID, ClientID: auth.ClientID, Repository: auth.Repository, Scope: auth.Scope, KeyID: kid, JTI: claims.JTI}
	endpoint, err := url.JoinPath(e.AuthorityHost, url.PathEscape(auth.TenantID), "oauth2", "v2.0", "token")
	if err != nil {
		return "", time.Time{}, ErrEntraExchange
	}
	form := url.Values{
		"grant_type": {"client_credentials"}, "client_id": {auth.ClientID}, "scope": {auth.Scope},
		"client_assertion_type": {assertionType}, "client_assertion": {assertion},
	}
	var result struct {
		AccessToken string          `json:"access_token"`
		TokenType   string          `json:"token_type"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := e.postForm(ctx, endpoint, e.AuthorityHost, form, &result); err != nil {
		e.audit(ctx, event)
		return "", time.Time{}, err
	}
	seconds, ok := parseSeconds(result.ExpiresIn)
	if result.AccessToken == "" || !ok || (result.TokenType != "" && !strings.EqualFold(result.TokenType, "Bearer")) {
		e.audit(ctx, event)
		return "", time.Time{}, ErrEntraExchange
	}
	event.Succeeded = true
	e.audit(ctx, event)
	return result.AccessToken, e.now().Add(time.Duration(seconds) * time.Second), nil
}

// parseSeconds accepts the number or the numeric string some token services return.
func parseSeconds(raw json.RawMessage) (int64, bool) {
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, false
	}
	seconds, err := number.Int64()
	return seconds, err == nil && seconds > 0
}

// postForm posts a form to endpoint, which must be on the allowed origin, and decodes the JSON
// answer. Redirects are refused and the body is bounded; neither the request nor an error
// body is ever returned to the caller.
func (e *Entra) postForm(ctx context.Context, endpoint, allowedOrigin string, form url.Values, into any) error {
	target, err := url.Parse(endpoint)
	allowed, allowedErr := url.Parse(allowedOrigin)
	if err != nil || allowedErr != nil || !SameOrigin(target, allowed) {
		return ErrEntraExchange
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return ErrEntraExchange
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	do := e.Do
	if do == nil {
		do = e.client().Do
	}
	response, err := do(request)
	if err != nil {
		return ErrEntraExchange
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxExchangeBody+1))
	if err != nil || len(body) > maxExchangeBody || response.StatusCode != http.StatusOK {
		return ErrEntraExchange
	}
	if json.Unmarshal(body, into) != nil {
		return ErrEntraExchange
	}
	return nil
}

var acrDefaultHost = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)*\.azurecr\.(io|us|cn)$`)

func (e *Entra) acrHostAllowed(host string) bool {
	if e.ACRHostAllowed != nil {
		return e.ACRHostAllowed(host)
	}
	return acrDefaultHost.MatchString(strings.ToLower(host))
}

var acrRepositoryPath = regexp.MustCompile(`^/v2/([a-z0-9][a-z0-9._/-]{0,254})/(?:manifests|blobs|tags|referrers)(?:/|$)`)

// acrScope maps a registry request path to the ACR token scope it needs. An empty scope means the
// request needs no token (the /v2/ ping).
func acrScope(path string) (string, bool) {
	if path == "/v2/_catalog" {
		return "registry:catalog:*", true
	}
	if match := acrRepositoryPath.FindStringSubmatch(path); match != nil {
		return "repository:" + match[1] + ":pull", true
	}
	return "", path == "/v2/" || path == "/v2"
}

// ACRToken returns an ACR access token for the registry request target: the Entra token is
// exchanged at /oauth2/exchange for a refresh token, which is exchanged at /oauth2/token for an
// access token scoped to the repository. Both steps use the same cache and singleflight.
func (e *Entra) ACRToken(ctx context.Context, auth *repository.UpstreamAuth, target *url.URL) (string, error) {
	if err := e.validate(auth); err != nil {
		return "", err
	}
	if target == nil || target.Scheme != "https" || !e.acrHostAllowed(target.Hostname()) {
		return "", ErrEntraExchange
	}
	scope, ok := acrScope(target.Path)
	if !ok {
		return "", ErrEntraExchange
	}
	if scope == "" {
		return "", nil
	}
	origin := "https://" + target.Host
	refreshKey := e.tokenBase(auth) + "\x01acr-refresh\x00" + target.Host
	accessKey := e.tokenBase(auth) + "\x01acr-access\x00" + target.Host + "\x00" + scope
	if token, ok := e.cached(accessKey); ok {
		return token, nil
	}
	value, err, _ := e.flight.Do(accessKey, func() (any, error) {
		if token, ok := e.cached(accessKey); ok {
			return token, nil
		}
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), exchangeTimeout)
		defer cancel()
		refresh, ok := e.cached(refreshKey)
		if !ok {
			aad, err := e.AccessToken(work, auth)
			if err != nil {
				return "", err
			}
			var exchanged struct {
				RefreshToken string `json:"refresh_token"`
			}
			form := url.Values{"grant_type": {"access_token"}, "service": {target.Host}, "tenant": {auth.TenantID}, "access_token": {aad}}
			if err := e.postForm(work, origin+"/oauth2/exchange", origin, form, &exchanged); err != nil || exchanged.RefreshToken == "" {
				e.auditACR(work, auth, target.Host, "acr.refresh", false)
				return "", ErrEntraExchange
			}
			refresh = exchanged.RefreshToken
			e.store(refreshKey, refresh, e.tokenRefreshTime(refresh))
			e.auditACR(work, auth, target.Host, "acr.refresh", true)
		}
		var issued struct {
			AccessToken string `json:"access_token"`
		}
		form := url.Values{"grant_type": {"refresh_token"}, "service": {target.Host}, "scope": {scope}, "refresh_token": {refresh}}
		if err := e.postForm(work, origin+"/oauth2/token", origin, form, &issued); err != nil || issued.AccessToken == "" {
			// A refresh token the registry refuses must not be retried until it would expire.
			e.dropKey(refreshKey)
			e.auditACR(work, auth, target.Host, "acr.access", false)
			return "", ErrEntraExchange
		}
		e.store(accessKey, issued.AccessToken, e.tokenRefreshTime(issued.AccessToken))
		e.auditACR(work, auth, target.Host, "acr.access", true)
		return issued.AccessToken, nil
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func (e *Entra) dropKey(key string) {
	e.mu.Lock()
	delete(e.tokens, key)
	e.mu.Unlock()
}

func (e *Entra) audit(ctx context.Context, event EntraExchange) {
	if e.Audit != nil {
		e.Audit(ctx, event)
	}
}

func (e *Entra) auditACR(ctx context.Context, auth *repository.UpstreamAuth, host, operation string, succeeded bool) {
	e.audit(ctx, EntraExchange{Operation: operation, Subject: e.subject(auth), TenantID: auth.TenantID, ClientID: auth.ClientID, Repository: auth.Repository, Scope: auth.Scope, Registry: host, Succeeded: succeeded})
}

// tokenRefreshTime is when a registry token should be replaced: five minutes before the exp claim
// of the JWT, or one minute from now when the token is opaque or carries no usable exp.
func (e *Entra) tokenRefreshTime(token string) time.Time {
	now := e.now()
	parts := strings.Split(token, ".")
	if len(parts) == 3 {
		if payload, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var claims struct {
				Exp json.Number `json:"exp"`
			}
			if json.Unmarshal(payload, &claims) == nil {
				if seconds, err := claims.Exp.Int64(); err == nil && seconds > 0 {
					if refresh := time.Unix(seconds, 0).Add(-tokenRefreshMargin); refresh.After(now) {
						return refresh
					}
					return now
				}
			}
		}
	}
	return now.Add(time.Minute)
}
