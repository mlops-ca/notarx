package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/authorization/workloadpolicy"
	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

// AuthenticationWorkloadFederation marks a principal derived from a verified workload JWT
// (ADR 0019, "Authentication flow and integration"). Its CredentialID is the trust policy id.
const AuthenticationWorkloadFederation AuthenticationKind = "workload_federation"

// WorkloadBasicUsername is the Basic username that runs the workload path without pinning a policy.
const WorkloadBasicUsername = repository.WorkloadBasicUsernameAlias

// Bounded failure reasons of a workload authentication: they label metrics and audit events.
const (
	WorkloadAuthenticated    = "authenticated"
	WorkloadIssuerDisabled   = "issuer_disabled"
	WorkloadVerifyFailed     = "verification_failed"
	WorkloadPolicyNotFound   = "policy_not_found"
	WorkloadNoPolicyMatch    = "no_policy_match"
	WorkloadAmbiguousPolicy  = "ambiguous_policy"
	WorkloadAccountInactive  = "account_inactive"
	WorkloadReplay           = "replay"
	WorkloadMissingJTI       = "missing_jti"
	WorkloadStoreUnavailable = "store_error"
	WorkloadKeysUnavailable  = "keys_unavailable"
)

const (
	maxWorkloadTokenBytes = 16 << 10
	// maxWorkloadClaimSummary bounds the claims retained on a principal for audit.
	maxWorkloadClaimSummary = workloadpolicy.MaxConditions
)

// WorkloadClaims is the bounded summary of a verified workload token kept on the principal for
// audit: the issuer, the subject and the claims named by the matched policy's conditions. It never
// holds the token.
type WorkloadClaims struct {
	Issuer     string
	Subject    string
	PolicyName string
	JTIHash    string
	Referenced map[string]string
}

// WorkloadStore is the issuer, policy and jti storage the authenticator reads. The production
// wiring passes the read-cached store, so issuers and policies are not queried per request.
type WorkloadStore interface {
	GetWorkloadIssuer(context.Context, string) (repository.WorkloadIssuer, error)
	GetWorkloadIssuerByIssuer(context.Context, string) (repository.WorkloadIssuer, error)
	GetWorkloadTrustPolicy(context.Context, string) (repository.WorkloadTrustPolicy, error)
	ListActiveWorkloadTrustPolicies(ctx context.Context, issuerID string, now time.Time) ([]repository.WorkloadTrustPolicy, error)
	RecordWorkloadJTI(ctx context.Context, issuerID string, jtiHash []byte, expiresAt time.Time) (bool, error)
}

// WorkloadAccounts reads the service account a policy maps to.
type WorkloadAccounts interface {
	GetServiceAccount(context.Context, string) (repository.ServiceAccount, error)
}

// WorkloadEvent describes one authentication decision for metrics and audit. Detail is for
// operators only (the failing condition) and must never reach a client.
type WorkloadEvent struct {
	Success          bool
	Reason           string
	Detail           string
	Issuer           string
	IssuerID         string
	PolicyID         string
	PolicyName       string
	ServiceAccountID string
	JTIHash          string
	Subject          string
	Pinned           bool
	Claims           map[string]string
}

// WorkloadObserver receives every workload authentication success and failure.
type WorkloadObserver interface {
	ObserveWorkload(context.Context, WorkloadEvent)
}

// WorkloadOptions configures NewWorkloadAuthenticator.
type WorkloadOptions struct {
	Store    WorkloadStore
	Accounts WorkloadAccounts
	Verifier jose.TokenVerifier
	Observer WorkloadObserver
	// AllowPrivateNetwork lets every issuer fetch keys from private addresses (NOTARX_WORKLOAD_ALLOW_PRIVATE_NETWORK).
	AllowPrivateNetwork bool
	// ClockSkew extends the lifetime of a single-use jti marker past exp.
	ClockSkew time.Duration
	// Cache holds the verified-token cache and sets its revocation window (NOTARX_AUTHZ_CACHE_TTL);
	// nil disables caching, so every request verifies the token.
	Cache  *readcache.Registry
	Logger *slog.Logger
	Now    func() time.Time
}

// WorkloadAuthenticator maps a verified workload JWT onto a service-account principal.
type WorkloadAuthenticator struct {
	opts     WorkloadOptions
	registry *readcache.Registry
	verified *readcache.Cache[string, workloadDecision]
}

// workloadDecision is the cached outcome of verifying one token against the issuer and policies.
type workloadDecision struct {
	principal Principal
	issuerID  string
	singleUse bool
	jti       string
	// tokenExp is the exp of the token; a cached decision is never served at or after it.
	tokenExp time.Time
}

// workloadDenial is a cacheable negative outcome. Store failures are never wrapped in it.
type workloadDenial struct{ reason string }

func (d *workloadDenial) Error() string { return "workload authentication denied: " + d.reason }

// NewWorkloadAuthenticator returns the authenticator. The verifier and store are required.
func NewWorkloadAuthenticator(opts WorkloadOptions) (*WorkloadAuthenticator, error) {
	if opts.Store == nil || opts.Verifier == nil || opts.Accounts == nil {
		return nil, errors.New("workload authenticator needs a store, an account reader and a verifier")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	registry := opts.Cache
	if registry == nil {
		registry = readcache.NewRegistry(0)
	}
	w := &WorkloadAuthenticator{opts: opts, registry: registry}
	w.verified = readcache.New[string, workloadDecision](registry, readcache.Options[workloadDecision]{
		Name: "workload_verified_tokens", Scope: repository.CacheScopeWorkloadPolicy,
		Valid: func(value workloadDecision, now time.Time) bool { return now.Before(value.tokenExp) },
		IsNegative: func(err error) bool {
			var denial *workloadDenial
			return errors.As(err, &denial)
		},
	})
	return w, nil
}

// LooksLikeJWT is the cheap detection of ADR 0019: three base64url segments whose first decodes to
// a JSON object with an alg member. Anything else skips the workload path.
func LooksLikeJWT(token string) bool {
	if len(token) < 8 || len(token) > maxWorkloadTokenBytes || strings.Count(token, ".") != 2 {
		return false
	}
	header, rest, _ := strings.Cut(token, ".")
	payload, signature, _ := strings.Cut(rest, ".")
	if header == "" || payload == "" || signature == "" {
		return false
	}
	for _, segment := range []string{payload, signature} {
		if !base64URL(segment) {
			return false
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil {
		return false
	}
	var parsed struct {
		Alg string `json:"alg"`
	}
	return json.Unmarshal(raw, &parsed) == nil && parsed.Alg != ""
}

func base64URL(segment string) bool {
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// exactUnverifiedIssuer reads iss without verification. It is only used to find the issuer row,
// which then verifies the token, so a forged value can at most select an issuer that rejects it.
func exactUnverifiedIssuer(token string) string {
	_, rest, _ := strings.Cut(token, ".")
	payload, _, _ := strings.Cut(rest, ".")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}
	var claims struct {
		Issuer string `json:"iss"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return ""
	}
	return claims.Issuer
}

// Authenticate maps token to a service-account principal. pin, when set, is the name of the only
// policy allowed to match (the Basic username of ADR 0019). It reports false for anything that is
// not a workload token of a registered issuer, so other credential paths proceed unchanged.
func (w *WorkloadAuthenticator) Authenticate(ctx context.Context, token, pin string) (Principal, bool) {
	principal, commit, ok := w.AuthenticateDeferred(ctx, token, pin)
	if !ok || !commit() {
		return Principal{}, false
	}
	return principal, true
}

// AuthenticateDeferred is Authenticate with the single-use marker left unconsumed: the returned commit
// records the jti of a single-use policy and reports false on a replay (or when the marker cannot be
// written), and is a no-op for other policies. A caller that can still refuse the request after the token
// verified (the token exchange checks the audience first) calls commit last, so a refusal never burns the token.
func (w *WorkloadAuthenticator) AuthenticateDeferred(ctx context.Context, token, pin string) (Principal, func() bool, bool) {
	none := func() bool { return false }
	if w == nil || !LooksLikeJWT(token) {
		return Principal{}, none, false
	}
	issuerURL := exactUnverifiedIssuer(token)
	if issuerURL == "" {
		return Principal{}, none, false
	}
	issuer, err := w.opts.Store.GetWorkloadIssuerByIssuer(ctx, issuerURL)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			w.observe(ctx, WorkloadEvent{Reason: WorkloadStoreUnavailable, Issuer: truncate(issuerURL, 512), Pinned: pin != "", Detail: err.Error()})
		}
		return Principal{}, none, false
	}
	event := WorkloadEvent{Issuer: issuer.Issuer, IssuerID: issuer.ID, Pinned: pin != ""}
	if issuer.State != repository.WorkloadActive {
		event.Reason = WorkloadIssuerDisabled
		w.observe(ctx, event)
		return Principal{}, none, false
	}
	digest := sha256.Sum256([]byte(token + "\x00" + pin))
	// Any bump of an issuer, policy or service-account scope turns a hit into a miss, even when an
	// eviction notification was lost (ADR 0023 section 6).
	version := w.registry.Epoch(repository.CacheScopeWorkloadIssuer) + w.registry.Epoch(repository.CacheScopeWorkloadPolicy) + w.registry.Epoch(repository.CacheScopeServiceAccount)
	decision, err := w.verified.GetVersioned(ctx, string(digest[:]), version, func(ctx context.Context) (workloadDecision, error) {
		return w.decide(ctx, token, pin, issuer)
	})
	if err != nil {
		return Principal{}, none, false
	}
	commit := func() bool {
		if !decision.singleUse {
			return true
		}
		// Replay protection is a write and never uses the cache.
		hash := sha256.Sum256([]byte(decision.jti))
		fresh, recordErr := w.opts.Store.RecordWorkloadJTI(ctx, decision.issuerID, hash[:], decision.tokenExp.Add(w.opts.ClockSkew))
		claims := decision.principal.WorkloadClaims
		failure := WorkloadEvent{Issuer: issuer.Issuer, IssuerID: issuer.ID, PolicyID: decision.principal.CredentialID, ServiceAccountID: strings.TrimPrefix(decision.principal.Actor, "service-account:"), JTIHash: claims.JTIHash, Subject: claims.Subject, Pinned: pin != ""}
		switch {
		case recordErr != nil:
			failure.Reason, failure.Detail = WorkloadStoreUnavailable, recordErr.Error()
			w.observe(ctx, failure)
			return false
		case !fresh:
			failure.Reason = WorkloadReplay
			w.observe(ctx, failure)
			return false
		}
		return true
	}
	return decision.principal, commit, true
}

func (w *WorkloadAuthenticator) observe(ctx context.Context, event WorkloadEvent) {
	event.Success = event.Reason == WorkloadAuthenticated
	if w.opts.Observer != nil {
		w.opts.Observer.ObserveWorkload(ctx, event)
	}
}

func (w *WorkloadAuthenticator) denied(ctx context.Context, event WorkloadEvent, reason, detail string) (workloadDecision, error) {
	event.Reason, event.Detail = reason, detail
	w.observe(ctx, event)
	return workloadDecision{}, &workloadDenial{reason: reason}
}

// decide verifies token and evaluates the issuer's policies. Its outcome is what the cache stores.
func (w *WorkloadAuthenticator) decide(ctx context.Context, token, pin string, issuer repository.WorkloadIssuer) (workloadDecision, error) {
	now := w.opts.Now()
	event := WorkloadEvent{Issuer: issuer.Issuer, IssuerID: issuer.ID, Pinned: pin != ""}
	trusted := JoseIssuer(issuer)
	trusted.AllowPrivateNetwork = trusted.AllowPrivateNetwork || w.opts.AllowPrivateNetwork
	verified, err := w.opts.Verifier.Verify(ctx, token, trusted)
	if errors.Is(err, jose.ErrKeySourceUnavailable) || errors.Is(err, jose.ErrInvalidConfig) {
		// The issuer's keys could not be read: a transient outage must not be remembered as a verdict on the
		// token, so this failure is observed but never cached.
		event.Reason, event.Detail = WorkloadKeysUnavailable, err.Error()
		w.observe(ctx, event)
		return workloadDecision{}, err
	}
	if err != nil {
		// The jose error names the failed check but never holds the token.
		return w.denied(ctx, event, WorkloadVerifyFailed, err.Error())
	}
	subject, _ := verified.Claims["sub"].(string)
	event.Subject = truncate(subject, 256)
	records, err := w.opts.Store.ListActiveWorkloadTrustPolicies(ctx, issuer.ID, now)
	if err != nil {
		w.observe(ctx, WorkloadEvent{Reason: WorkloadStoreUnavailable, Issuer: issuer.Issuer, IssuerID: issuer.ID, Subject: truncate(subject, 256), Pinned: pin != "", Detail: err.Error()})
		return workloadDecision{}, err
	}
	byID := make(map[string]repository.WorkloadTrustPolicy, len(records))
	policies := make([]workloadpolicy.Policy, 0, len(records))
	for _, record := range records {
		if pin != "" && record.Name != pin {
			continue
		}
		policy, parseErr := workloadpolicy.NewPolicy(workloadpolicy.Kind(issuer.Kind), record.ID, record.Name, record.ServiceAccountID, record.Conditions)
		if parseErr != nil {
			w.opts.Logger.Error("stored workload trust policy is invalid", "policy", record.Name, "error", parseErr)
			continue
		}
		byID[record.ID] = record
		policies = append(policies, policy)
	}
	if len(policies) == 0 {
		return w.denied(ctx, event, WorkloadPolicyNotFound, "no active policy applies to this issuer"+pinnedDetail(pin))
	}
	matched, err := workloadpolicy.Evaluate(policies, verified.Claims)
	if err != nil {
		var ambiguous *workloadpolicy.AmbiguousError
		var noMatch *workloadpolicy.NoMatchError
		switch {
		case errors.As(err, &ambiguous):
			w.opts.Logger.Warn("workload token matches policies of different service accounts", "issuer", issuer.Name, "policies", ambiguous.Policies)
			return w.denied(ctx, event, WorkloadAmbiguousPolicy, ambiguous.Error())
		case errors.As(err, &noMatch):
			parts := make([]string, 0, len(noMatch.Failures))
			for _, failure := range noMatch.Failures {
				parts = append(parts, failure.String())
			}
			return w.denied(ctx, event, WorkloadNoPolicyMatch, strings.Join(parts, "; "))
		}
		return w.denied(ctx, event, WorkloadNoPolicyMatch, err.Error())
	}
	record := byID[matched.ID]
	event.PolicyID, event.PolicyName, event.ServiceAccountID = matched.ID, matched.Name, matched.ServiceAccountID
	referenced := workloadpolicy.ReferencedClaims(matched, verified.Claims)
	event.Claims = referenced
	account, err := w.opts.Accounts.GetServiceAccount(ctx, matched.ServiceAccountID)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return w.denied(ctx, event, WorkloadAccountInactive, "the service account does not exist")
	case err != nil:
		event.Reason, event.Detail = WorkloadStoreUnavailable, err.Error()
		w.observe(ctx, event)
		return workloadDecision{}, err
	case account.State != repository.ServiceAccountActive:
		return w.denied(ctx, event, WorkloadAccountInactive, "the service account is disabled")
	}
	exp, ok := claimTime(verified.Claims["exp"])
	if !ok {
		return w.denied(ctx, event, WorkloadVerifyFailed, "exp is missing")
	}
	jti, _ := verified.Claims["jti"].(string)
	var jtiHash string
	if jti != "" {
		digest := sha256.Sum256([]byte(jti))
		jtiHash = base64.RawURLEncoding.EncodeToString(digest[:])
	}
	if record.SingleUse && jti == "" {
		return w.denied(ctx, event, WorkloadMissingJTI, "the policy requires single use and the token has no jti")
	}
	event.JTIHash = jtiHash
	event.Reason = WorkloadAuthenticated
	w.observe(ctx, event)
	return workloadDecision{
		principal: Principal{
			Actor: "service-account:" + account.ID, AuthenticationKind: AuthenticationWorkloadFederation, CredentialID: matched.ID,
			WorkloadClaims: &WorkloadClaims{Issuer: issuer.Issuer, Subject: truncate(subject, 256), PolicyName: matched.Name, JTIHash: jtiHash, Referenced: capClaims(referenced)},
		},
		issuerID: issuer.ID, singleUse: record.SingleUse, jti: jti, tokenExp: exp,
	}, nil
}

// VerifyForIssuer verifies token against a registered issuer with the verifier and network policy
// of the authenticator. The dry-run policy tester uses it; it consumes no single-use marker.
func (w *WorkloadAuthenticator) VerifyForIssuer(ctx context.Context, token string, issuer repository.WorkloadIssuer) (*jose.Verified, error) {
	if w == nil {
		return nil, errors.New("workload federation is disabled")
	}
	trusted := JoseIssuer(issuer)
	trusted.AllowPrivateNetwork = trusted.AllowPrivateNetwork || w.opts.AllowPrivateNetwork
	return w.opts.Verifier.Verify(ctx, token, trusted)
}

// LookupIssuer returns the registered issuer a workload token names, active or not. The token
// is not verified. It reports false for anything that is not a workload JWT of a registered issuer.
func (w *WorkloadAuthenticator) LookupIssuer(ctx context.Context, token string) (repository.WorkloadIssuer, bool) {
	if w == nil || !LooksLikeJWT(token) {
		return repository.WorkloadIssuer{}, false
	}
	issuerURL := exactUnverifiedIssuer(token)
	if issuerURL == "" {
		return repository.WorkloadIssuer{}, false
	}
	issuer, err := w.opts.Store.GetWorkloadIssuerByIssuer(ctx, issuerURL)
	return issuer, err == nil
}

// UnverifiedClaims decodes the payload of a JWT without verifying it, keeping numbers exact. It is
// for tooling that shows or tests claims (the policy tester), never for authentication.
func UnverifiedClaims(token string) (map[string]any, error) {
	if !LooksLikeJWT(token) {
		return nil, errors.New("the value is not a JWT")
	}
	_, rest, _ := strings.Cut(token, ".")
	payload, _, _ := strings.Cut(rest, ".")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, errors.New("the JWT payload is not base64url")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var claims map[string]any
	if err := decoder.Decode(&claims); err != nil || claims == nil {
		return nil, errors.New("the JWT payload is not a JSON object")
	}
	return claims, nil
}

func pinnedDetail(pin string) string {
	if pin == "" {
		return ""
	}
	return " named " + pin
}

func capClaims(claims map[string]string) map[string]string {
	if len(claims) <= maxWorkloadClaimSummary {
		return claims
	}
	out := make(map[string]string, maxWorkloadClaimSummary)
	for key, value := range claims {
		if len(out) == maxWorkloadClaimSummary {
			break
		}
		out[key] = value
	}
	return out
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit], "")
}

// claimTime reads a NumericDate claim.
func claimTime(raw any) (time.Time, bool) {
	switch v := raw.(type) {
	case json.Number:
		if seconds, err := v.Float64(); err == nil {
			return time.Unix(int64(seconds), 0), true
		}
	case float64:
		return time.Unix(int64(v), 0), true
	}
	return time.Time{}, false
}

// JoseIssuer is the verifier-facing view of a registered issuer; every caller that verifies or probes an
// issuer builds it here, so the mode mapping has one definition.
func JoseIssuer(issuer repository.WorkloadIssuer) jose.Issuer {
	mode := jose.ModeDiscovery
	if issuer.JWKSMode == repository.WorkloadJWKSStatic {
		mode = jose.ModeStatic
	}
	return jose.Issuer{
		Issuer: issuer.Issuer, Audiences: issuer.Audiences, Algorithms: issuer.AllowedAlgs, Mode: mode,
		JWKSURI: issuer.JWKSURI, StaticJWKS: issuer.StaticJWKS, CABundlePEM: issuer.CABundlePEM, AllowPrivateNetwork: issuer.AllowPrivateNetwork,
	}
}

// policyNameExists reports whether name is the name of an active policy of the issuer the token names.
// The Basic username of ADR 0019 pins a policy only when it is an existing policy name.
func (w *WorkloadAuthenticator) policyNameExists(ctx context.Context, token, name string) bool {
	issuer, err := w.opts.Store.GetWorkloadIssuerByIssuer(ctx, exactUnverifiedIssuer(token))
	if err != nil {
		return false
	}
	records, err := w.opts.Store.ListActiveWorkloadTrustPolicies(ctx, issuer.ID, w.opts.Now())
	if err != nil {
		return false
	}
	for _, record := range records {
		if record.Name == name {
			return true
		}
	}
	return false
}

// AuthenticateBasic serves the Basic password path: the workload path runs when password is a JWT
// and username is "oidc" or an existing policy name, which then pins that policy.
func (w *WorkloadAuthenticator) AuthenticateBasic(ctx context.Context, username, password string) (Principal, bool) {
	if w == nil || !LooksLikeJWT(password) {
		return Principal{}, false
	}
	if username == WorkloadBasicUsername {
		return w.Authenticate(ctx, password, "")
	}
	if !w.policyNameExists(ctx, password, username) {
		return Principal{}, false
	}
	return w.Authenticate(ctx, password, username)
}

// Revalidate reports whether the policy, its issuer and its service account still allow a
// principal token issued for a workload. It reads through the caches, so a disabled policy ends
// the token within the revocation window (NOTARX_AUTHZ_CACHE_TTL, 5 seconds by default).
func (w *WorkloadAuthenticator) Revalidate(ctx context.Context, policyID, actor string) bool {
	if w == nil || policyID == "" {
		return false
	}
	policy, err := w.opts.Store.GetWorkloadTrustPolicy(ctx, policyID)
	if err != nil || policy.State != repository.WorkloadActive || "service-account:"+policy.ServiceAccountID != actor {
		return false
	}
	if policy.ExpiresAt != nil && !w.opts.Now().Before(*policy.ExpiresAt) {
		return false
	}
	issuer, err := w.opts.Store.GetWorkloadIssuer(ctx, policy.IssuerID)
	if err != nil || issuer.State != repository.WorkloadActive {
		return false
	}
	account, err := w.opts.Accounts.GetServiceAccount(ctx, policy.ServiceAccountID)
	return err == nil && account.State == repository.ServiceAccountActive
}
