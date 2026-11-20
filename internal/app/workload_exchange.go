package app

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

// Token exchange (ADR 0019, "Token exchange endpoint"): POST /auth/token-exchange, RFC 8693.
const (
	workloadExchangePath         = "/auth/token-exchange"
	tokenExchangeGrantType       = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenExchangeJWTType         = "urn:ietf:params:oauth:token-type:jwt"
	tokenExchangeAccessTokenType = "urn:ietf:params:oauth:token-type:access_token"
	workloadExchangeBodyLimit    = 32 << 10
	workloadExchangeMaxHosts     = 10000
	// workloadInvalidGrantMessage is the same for every verification or policy failure, so the
	// endpoint is no oracle for which condition failed; the detail goes to the audit log.
	workloadInvalidGrantMessage = "the subject token is not valid for this service"
)

// WorkloadExchangeOptions configures the exchange endpoint.
type WorkloadExchangeOptions struct {
	// MaxTTL caps the lifetime of an exchanged token (NOTARX_WORKLOAD_EXCHANGE_MAX_TTL).
	MaxTTL time.Duration
	// Rate is the number of exchanges per minute per source address and per issuer
	// (NOTARX_WORKLOAD_EXCHANGE_RATE).
	Rate int
}

// workloadExchangeHandler serves the exchange. It answers 404 while workload federation is off.
type workloadExchangeHandler struct {
	authenticator Authenticator
	policies      repository.WorkloadStore
	maxTTL        time.Duration
	rate          int
	byAddress     *clientLimiter
	byIssuer      *clientLimiter
}

func newWorkloadExchangeHandler(authenticator Authenticator, policies repository.WorkloadStore, options WorkloadExchangeOptions) *workloadExchangeHandler {
	if options.MaxTTL <= 0 {
		options.MaxTTL = config.DefaultWorkloadExchangeMaxTTL
	}
	options.MaxTTL = min(options.MaxTTL, config.MaxWorkloadExchangeTTL)
	if options.Rate <= 0 {
		options.Rate = config.DefaultWorkloadExchangeRate
	}
	// A bucket of Rate tokens refilled evenly over a minute.
	refill := time.Minute / time.Duration(options.Rate)
	return &workloadExchangeHandler{
		authenticator: authenticator, policies: policies, maxTTL: options.MaxTTL, rate: options.Rate,
		byAddress: newClientLimiter(options.Rate, refill, workloadExchangeMaxHosts),
		byIssuer:  newClientLimiter(options.Rate, refill, workloadExchangeMaxHosts),
	}
}

func writeTokenExchangeError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": description})
}

func (h *workloadExchangeHandler) tooManyRequests(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(int(time.Minute/time.Duration(h.rate)/time.Second)+1))
	writeTokenExchangeError(w, http.StatusTooManyRequests, "temporarily_unavailable", "too many token exchanges; retry later")
}

func (h *workloadExchangeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.authenticator.Workload == nil {
		http.NotFound(w, r)
		return
	}
	now := time.Now()
	if !h.byAddress.allow(peerAddress(r), now) {
		h.tooManyRequests(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, workloadExchangeBodyLimit)
	if mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(mediaType) != "application/x-www-form-urlencoded" {
		writeTokenExchangeError(w, http.StatusBadRequest, "invalid_request", "the body must be application/x-www-form-urlencoded")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeTokenExchangeError(w, http.StatusBadRequest, "invalid_request", "the form body is malformed or too large")
		return
	}
	form := r.PostForm
	if form.Get("grant_type") != tokenExchangeGrantType {
		writeTokenExchangeError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be "+tokenExchangeGrantType)
		return
	}
	subject := form.Get("subject_token")
	if subject == "" || form.Get("subject_token_type") != tokenExchangeJWTType {
		writeTokenExchangeError(w, http.StatusBadRequest, "invalid_request", "subject_token and subject_token_type "+tokenExchangeJWTType+" are required")
		return
	}
	// Token minting reads through to the database: a disabled policy never gets a new token.
	ctx := readcache.Bypass(r.Context())
	principal, commit, ok := h.authenticator.Workload.AuthenticateDeferred(ctx, subject, form.Get("policy"))
	if !ok {
		writeTokenExchangeError(w, http.StatusBadRequest, "invalid_grant", workloadInvalidGrantMessage)
		return
	}
	// The per-issuer budget is charged only for a token that verified: an unauthenticated caller who merely names a
	// registered issuer in a forged token must not be able to exhaust it for the issuer's real workloads.
	issuer, _ := h.authenticator.Workload.LookupIssuer(ctx, subject)
	if !h.byIssuer.allow(issuer.ID, now) {
		h.tooManyRequests(w)
		return
	}
	if audience := form.Get("audience"); audience != "" && !slices.Contains(issuer.Audiences, audience) {
		writeTokenExchangeError(w, http.StatusBadRequest, "invalid_target", "the audience is not accepted by the issuer of the subject token")
		return
	}
	lifetime := h.maxTTL
	policy, err := h.policies.GetWorkloadTrustPolicy(ctx, principal.CredentialID)
	if err != nil {
		writeTokenExchangeError(w, http.StatusBadRequest, "invalid_grant", workloadInvalidGrantMessage)
		return
	}
	lifetime = min(lifetime, time.Duration(policy.MaxTokenTTLSeconds)*time.Second)
	// Every other refusal is behind us: only now is a single-use token consumed, so a refused exchange never burns it.
	if !commit() {
		writeTokenExchangeError(w, http.StatusBadRequest, "invalid_grant", workloadInvalidGrantMessage)
		return
	}
	token := h.authenticator.IssuePrincipalTokenFor(principal, lifetime)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": token, "issued_token_type": tokenExchangeAccessTokenType, "token_type": "Bearer", "expires_in": int(lifetime / time.Second),
	})
}
