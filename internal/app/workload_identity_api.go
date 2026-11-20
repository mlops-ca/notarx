package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/authorization/workloadpolicy"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/repository"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// workloadIdentityStore is the persistence of the workload identity management API (ADR 0019): the
// issuer and policy tables plus the two reads that validate a policy against its service account.
type workloadIdentityStore interface {
	repository.WorkloadStore
	GetServiceAccount(context.Context, string) (repository.ServiceAccount, error)
	ListRepositoriesGrantedToPrincipal(ctx context.Context, principal, scope string) ([]repository.HostedRepository, error)
}

// workloadHumanProviders is the human IdP registry, read to keep the two issuer registries apart.
type workloadHumanProviders interface {
	List(context.Context) ([]identity.Provider, error)
}

type workloadIdentityOptions struct {
	Scope     managementScope
	Store     workloadIdentityStore
	Providers workloadHumanProviders
	// AllowPrivateNetwork is NOTARX_WORKLOAD_ALLOW_PRIVATE_NETWORK, applied to every probe.
	AllowPrivateNetwork bool
}

// workloadIdentityHandler owns the /workload-identity management resources. Every operation is
// administrator-only: issuers and policies decide who may act as a service account.
type workloadIdentityHandler struct {
	managementScope
	store               workloadIdentityStore
	providers           workloadHumanProviders
	allowPrivateNetwork bool
}

func newWorkloadIdentityHandler(options workloadIdentityOptions) workloadIdentityHandler {
	return workloadIdentityHandler{managementScope: options.Scope, store: options.Store, providers: options.Providers, allowPrivateNetwork: options.AllowPrivateNetwork}
}

const (
	workloadBodyLimit  = 1 << 20
	workloadProbeLimit = 15 * time.Second
)

func (h workloadIdentityHandler) auditRecords(r *http.Request, resource, operation string, status int) []repository.AuditRecord {
	if h.audit == nil {
		return nil
	}
	return []repository.AuditRecord{managementAudit(repository.AuditRecord{Actor: h.auditActor(r), Resource: resource, Operation: operation, Status: status})}
}

// admit authorizes the caller as platform administrator and reports whether the registry exists.
func (h workloadIdentityHandler) admit(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := h.authorize(w, r); !ok {
		return false
	}
	if h.store == nil {
		writeHostedProblem(w, http.StatusServiceUnavailable, "not_supported", "workload identity federation is unavailable")
		return false
	}
	return true
}

func decodeWorkloadBody(w http.ResponseWriter, r *http.Request, into any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, workloadBodyLimit))
	if err != nil {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "the request body is unreadable or too large")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(into); err != nil || decoder.More() {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "the request body is not valid for this operation")
		return false
	}
	return true
}

func (h workloadIdentityHandler) pageBounds(w http.ResponseWriter, pageSizeParam *int, pageToken *string, endpoint string) (int, string, bool) {
	pageSize := 50
	if pageSizeParam != nil {
		pageSize = *pageSizeParam
	}
	if pageSize < 1 || pageSize > 200 {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "pageSize must be between 1 and 200")
		return 0, "", false
	}
	if pageToken == nil || *pageToken == "" {
		return pageSize, "", true
	}
	var cursor serviceAccountPageCursor
	if decodeSignedCursor(h.authenticator.AdminToken, *pageToken, &cursor) != nil || cursor.Endpoint != endpoint || cursor.ID == "" || time.Now().UTC().Unix() >= cursor.ExpiresAt {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_page_token", "page token is invalid or expired")
		return 0, "", false
	}
	return pageSize, cursor.ID, true
}

func (h workloadIdentityHandler) nextPageToken(endpoint, id string) *string {
	token := encodeSignedCursor(h.authenticator.AdminToken, serviceAccountPageCursor{Endpoint: endpoint, ID: id, ExpiresAt: time.Now().UTC().Add(15 * time.Minute).Unix()})
	return &token
}

// writeWorkloadStoreError maps a store failure onto the problem the contract documents.
func writeWorkloadStoreError(w http.ResponseWriter, err error, what string) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeHostedProblem(w, http.StatusNotFound, "not_found", what+" not found")
	case errors.Is(err, repository.ErrVersionConflict):
		writeHostedProblem(w, http.StatusPreconditionFailed, "version_conflict", "If-Match does not match the current "+what+" version")
	case errors.Is(err, repository.ErrWorkloadInvalid):
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, repository.ErrWorkloadIssuerExists), errors.Is(err, repository.ErrNameExists):
		writeHostedProblem(w, http.StatusConflict, "already_exists", "a "+what+" with this name or issuer URL already exists")
	case errors.Is(err, repository.ErrWorkloadIssuerInUse):
		writeHostedProblem(w, http.StatusConflict, "in_use", "trust policies still reference this issuer")
	case errors.Is(err, repository.ErrWorkloadReference):
		writeHostedProblem(w, http.StatusNotFound, "not_found", "the issuer or the service account does not exist")
	default:
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "workload identity operation failed")
	}
}

// issuerOverlapsHumanProvider reports the slug of a human identity provider that uses the same issuer URL.
func (h workloadIdentityHandler) issuerOverlapsHumanProvider(ctx context.Context, issuerURL string) (string, error) {
	if h.providers == nil {
		return "", nil
	}
	registered, err := h.providers.List(ctx)
	if err != nil {
		return "", err
	}
	want := strings.TrimRight(issuerURL, "/")
	for _, provider := range registered {
		if provider.Kind != identity.KindOIDC {
			continue
		}
		if settings, err := provider.OIDC(); err == nil && strings.TrimRight(settings.Issuer, "/") == want {
			return provider.Slug, nil
		}
	}
	return "", nil
}

func workloadIssuerResponse(issuer repository.WorkloadIssuer) adminopenapi.WorkloadIssuer {
	id, _ := parseUUID(issuer.ID)
	algs := make([]adminopenapi.WorkloadAlgorithm, 0, len(issuer.AllowedAlgs))
	for _, alg := range issuer.AllowedAlgs {
		algs = append(algs, adminopenapi.WorkloadAlgorithm(alg))
	}
	response := adminopenapi.WorkloadIssuer{
		Id: id, Name: issuer.Name, Kind: adminopenapi.WorkloadIssuerKind(issuer.Kind), Issuer: issuer.Issuer,
		Audiences: slices.Clone(issuer.Audiences), AllowedAlgs: algs, JwksMode: adminopenapi.WorkloadJWKSMode(issuer.JWKSMode),
		AllowPrivateNetwork: issuer.AllowPrivateNetwork, State: adminopenapi.WorkloadState(issuer.State), Version: issuer.Version,
		CreatedAt: issuer.CreatedAt, UpdatedAt: issuer.UpdatedAt,
	}
	if issuer.JWKSURI != "" {
		response.JwksUri = &issuer.JWKSURI
	}
	if issuer.CABundlePEM != "" {
		response.CaBundlePem = &issuer.CABundlePEM
	}
	if len(issuer.StaticJWKS) != 0 {
		var jwks map[string]interface{}
		if json.Unmarshal(issuer.StaticJWKS, &jwks) == nil {
			response.StaticJwks = &jwks
		}
	}
	return response
}

func workloadPolicyResponse(policy repository.WorkloadTrustPolicy) adminopenapi.WorkloadTrustPolicy {
	id, _ := parseUUID(policy.ID)
	issuerID, _ := parseUUID(policy.IssuerID)
	accountID, _ := parseUUID(policy.ServiceAccountID)
	conditions := []adminopenapi.WorkloadCondition{}
	var stored []struct {
		Claim  []string `json:"claim"`
		Op     string   `json:"op"`
		Values []string `json:"values"`
	}
	if json.Unmarshal(policy.Conditions, &stored) == nil {
		for _, condition := range stored {
			item := adminopenapi.WorkloadCondition{Op: adminopenapi.WorkloadConditionOp(condition.Op), Values: condition.Values}
			_ = item.Claim.FromWorkloadConditionClaim1(condition.Claim)
			conditions = append(conditions, item)
		}
	}
	response := adminopenapi.WorkloadTrustPolicy{
		Id: id, Name: policy.Name, IssuerId: issuerID, ServiceAccountId: accountID, Conditions: conditions,
		MaxTokenTtlSeconds: policy.MaxTokenTTLSeconds, SingleUse: policy.SingleUse, State: adminopenapi.WorkloadState(policy.State),
		Version: policy.Version, CreatedAt: policy.CreatedAt, UpdatedAt: policy.UpdatedAt,
	}
	if policy.ExpiresAt != nil {
		expires := policy.ExpiresAt.UTC()
		response.ExpiresAt = &expires
	}
	return response
}

func parseUUID(value string) (openapi_types.UUID, error) {
	var id openapi_types.UUID
	err := id.UnmarshalText([]byte(value))
	return id, err
}

func algorithmsFromRequest(algs *[]adminopenapi.WorkloadAlgorithm) []string {
	if algs == nil {
		return nil
	}
	out := make([]string, 0, len(*algs))
	for _, alg := range *algs {
		out = append(out, string(alg))
	}
	return out
}

func staticJWKSFromRequest(jwks *map[string]interface{}) (json.RawMessage, bool) {
	if jwks == nil {
		return nil, true
	}
	raw, err := json.Marshal(*jwks)
	return raw, err == nil
}

func (h workloadIdentityHandler) ListWorkloadIssuers(w http.ResponseWriter, r *http.Request, params adminopenapi.ListWorkloadIssuersParams) {
	if !h.admit(w, r) {
		return
	}
	pageSize, afterID, ok := h.pageBounds(w, params.PageSize, params.PageToken, "workload-issuers")
	if !ok {
		return
	}
	issuers, err := h.store.ListWorkloadIssuers(r.Context(), pageSize+1, afterID)
	if err != nil {
		writeWorkloadStoreError(w, err, "workload issuer")
		return
	}
	var next *string
	if len(issuers) > pageSize {
		issuers = issuers[:pageSize]
		next = h.nextPageToken("workload-issuers", issuers[len(issuers)-1].ID)
	}
	items := make([]adminopenapi.WorkloadIssuer, 0, len(issuers))
	for _, issuer := range issuers {
		items = append(items, workloadIssuerResponse(issuer))
	}
	writeNativeMavenJSON(w, http.StatusOK, adminopenapi.WorkloadIssuerList{Items: items, NextPageToken: next})
}

func (h workloadIdentityHandler) GetWorkloadIssuer(w http.ResponseWriter, r *http.Request, issuerID openapi_types.UUID) {
	if !h.admit(w, r) {
		return
	}
	issuer, err := h.store.GetWorkloadIssuer(r.Context(), issuerID.String())
	if err != nil {
		writeWorkloadStoreError(w, err, "workload issuer")
		return
	}
	writeNativeMavenJSON(w, http.StatusOK, workloadIssuerResponse(issuer))
}

func (h workloadIdentityHandler) CreateWorkloadIssuer(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w, r) {
		return
	}
	var request adminopenapi.CreateWorkloadIssuer
	if !decodeWorkloadBody(w, r, &request) {
		return
	}
	staticJWKS, ok := staticJWKSFromRequest(request.StaticJwks)
	if !ok {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "staticJwks is not valid JSON")
		return
	}
	issuer := repository.WorkloadIssuer{
		Name: request.Name, Kind: repository.WorkloadIssuerKind(request.Kind), Issuer: request.Issuer, Audiences: request.Audiences,
		AllowedAlgs: algorithmsFromRequest(request.AllowedAlgs), JWKSMode: repository.WorkloadJWKSMode(request.JwksMode), StaticJWKS: staticJWKS,
	}
	if request.JwksUri != nil {
		issuer.JWKSURI = *request.JwksUri
	}
	if request.CaBundlePem != nil {
		issuer.CABundlePEM = *request.CaBundlePem
	}
	issuer.AllowPrivateNetwork = request.AllowPrivateNetwork != nil && *request.AllowPrivateNetwork
	if request.State != nil {
		issuer.State = repository.WorkloadState(*request.State)
	}
	if slug, err := h.issuerOverlapsHumanProvider(r.Context(), issuer.Issuer); err != nil {
		writeHostedProblem(w, http.StatusServiceUnavailable, "identity_providers_unavailable", "load identity providers failed")
		return
	} else if slug != "" {
		writeHostedProblem(w, http.StatusConflict, "issuer_conflict", "the issuer URL is already an identity provider ("+slug+"); the human and workload registries must not overlap")
		return
	}
	created, err := h.store.CreateWorkloadIssuer(r.Context(), issuer, h.auditRecords(r, "workload-identity/issuers/"+issuer.Name, "workload_identity.issuer.create", http.StatusCreated)...)
	if err != nil {
		writeWorkloadStoreError(w, err, "workload issuer")
		return
	}
	writeNativeMavenJSON(w, http.StatusCreated, workloadIssuerResponse(created))
}

func (h workloadIdentityHandler) UpdateWorkloadIssuer(w http.ResponseWriter, r *http.Request, issuerID openapi_types.UUID, params adminopenapi.UpdateWorkloadIssuerParams) {
	if !h.admit(w, r) {
		return
	}
	var request adminopenapi.UpdateWorkloadIssuer
	if !decodeWorkloadBody(w, r, &request) {
		return
	}
	id := issuerID.String()
	issuer, err := h.store.GetWorkloadIssuer(r.Context(), id)
	if err != nil {
		writeWorkloadStoreError(w, err, "workload issuer")
		return
	}
	if request.Audiences != nil {
		issuer.Audiences = *request.Audiences
	}
	if request.AllowedAlgs != nil {
		issuer.AllowedAlgs = algorithmsFromRequest(request.AllowedAlgs)
	}
	if request.JwksMode != nil {
		issuer.JWKSMode = repository.WorkloadJWKSMode(*request.JwksMode)
		if issuer.JWKSMode == repository.WorkloadJWKSStatic {
			issuer.JWKSURI = ""
		} else {
			issuer.StaticJWKS = nil
		}
	}
	if request.JwksUri != nil {
		issuer.JWKSURI = *request.JwksUri
	}
	if request.StaticJwks != nil {
		static, ok := staticJWKSFromRequest(request.StaticJwks)
		if !ok {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "staticJwks is not valid JSON")
			return
		}
		issuer.StaticJWKS = static
	}
	if request.CaBundlePem != nil {
		issuer.CABundlePEM = *request.CaBundlePem
	}
	if request.AllowPrivateNetwork != nil {
		issuer.AllowPrivateNetwork = *request.AllowPrivateNetwork
	}
	if request.State != nil {
		issuer.State = repository.WorkloadState(*request.State)
	}
	updated, err := h.store.UpdateWorkloadIssuer(r.Context(), issuer, params.IfMatch, h.auditRecords(r, "workload-identity/issuers/"+id, "workload_identity.issuer.update", http.StatusOK)...)
	if err != nil {
		writeWorkloadStoreError(w, err, "workload issuer")
		return
	}
	writeNativeMavenJSON(w, http.StatusOK, workloadIssuerResponse(updated))
}

func (h workloadIdentityHandler) DeleteWorkloadIssuer(w http.ResponseWriter, r *http.Request, issuerID openapi_types.UUID, params adminopenapi.DeleteWorkloadIssuerParams) {
	if !h.admit(w, r) {
		return
	}
	id := issuerID.String()
	if err := h.store.DeleteWorkloadIssuer(r.Context(), id, params.IfMatch, h.auditRecords(r, "workload-identity/issuers/"+id, "workload_identity.issuer.delete", http.StatusNoContent)...); err != nil {
		writeWorkloadStoreError(w, err, "workload issuer")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h workloadIdentityHandler) ProbeWorkloadIssuer(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w, r) {
		return
	}
	var request adminopenapi.ProbeWorkloadIssuerRequest
	if !decodeWorkloadBody(w, r, &request) {
		return
	}
	var target jose.Issuer
	switch {
	case request.IssuerId != nil:
		issuer, err := h.store.GetWorkloadIssuer(r.Context(), request.IssuerId.String())
		if err != nil {
			writeWorkloadStoreError(w, err, "workload issuer")
			return
		}
		target = authorization.JoseIssuer(issuer)
	case request.Issuer != nil && request.JwksMode != nil:
		target = jose.Issuer{Issuer: *request.Issuer, Mode: jose.JWKSMode(*request.JwksMode)}
		if !strings.HasPrefix(target.Issuer, "https://") || target.Mode != jose.ModeDiscovery && target.Mode != jose.ModeStatic {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "issuer must be an https URL and jwksMode discovery or static")
			return
		}
		if request.JwksUri != nil {
			target.JWKSURI = *request.JwksUri
		}
		if request.CaBundlePem != nil {
			target.CABundlePEM = *request.CaBundlePem
		}
		target.AllowPrivateNetwork = request.AllowPrivateNetwork != nil && *request.AllowPrivateNetwork
		static, ok := staticJWKSFromRequest(request.StaticJwks)
		if !ok {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "staticJwks is not valid JSON")
			return
		}
		target.StaticJWKS = static
	default:
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "name a registered issuer with issuerId, or give issuer and jwksMode")
		return
	}
	target.AllowPrivateNetwork = target.AllowPrivateNetwork || h.allowPrivateNetwork
	ctx, cancel := context.WithTimeout(r.Context(), workloadProbeLimit)
	defer cancel()
	result, err := jose.Probe(ctx, target, nil)
	response := adminopenapi.WorkloadIssuerProbe{Keys: []adminopenapi.WorkloadIssuerProbeKey{}, Algorithms: []string{}}
	if err != nil {
		message := err.Error()
		response.Error = &message
	} else {
		response.Reachable = true
		response.Algorithms = append(response.Algorithms, result.Algorithms...)
		if result.JWKSURI != "" {
			response.JwksUri = &result.JWKSURI
		}
		for _, key := range result.Keys {
			item := adminopenapi.WorkloadIssuerProbeKey{KeyType: key.KeyType}
			if key.KeyID != "" {
				item.KeyId = &key.KeyID
			}
			if key.Algorithm != "" {
				item.Algorithm = &key.Algorithm
			}
			response.Keys = append(response.Keys, item)
		}
	}
	writeNativeMavenJSON(w, http.StatusOK, response)
}

func (h workloadIdentityHandler) ListWorkloadTrustPolicies(w http.ResponseWriter, r *http.Request, params adminopenapi.ListWorkloadTrustPoliciesParams) {
	if !h.admit(w, r) {
		return
	}
	pageSize, afterID, ok := h.pageBounds(w, params.PageSize, params.PageToken, "workload-policies")
	if !ok {
		return
	}
	policies, err := h.store.ListWorkloadTrustPolicies(r.Context(), pageSize+1, afterID)
	if err != nil {
		writeWorkloadStoreError(w, err, "workload trust policy")
		return
	}
	var next *string
	if len(policies) > pageSize {
		policies = policies[:pageSize]
		next = h.nextPageToken("workload-policies", policies[len(policies)-1].ID)
	}
	items := make([]adminopenapi.WorkloadTrustPolicy, 0, len(policies))
	for _, policy := range policies {
		items = append(items, workloadPolicyResponse(policy))
	}
	writeNativeMavenJSON(w, http.StatusOK, adminopenapi.WorkloadTrustPolicyList{Items: items, NextPageToken: next})
}

func (h workloadIdentityHandler) GetWorkloadTrustPolicy(w http.ResponseWriter, r *http.Request, policyID openapi_types.UUID) {
	if !h.admit(w, r) {
		return
	}
	policy, err := h.store.GetWorkloadTrustPolicy(r.Context(), policyID.String())
	if err != nil {
		writeWorkloadStoreError(w, err, "workload trust policy")
		return
	}
	writeNativeMavenJSON(w, http.StatusOK, workloadPolicyResponse(policy))
}

// validateWorkloadConditions validates the request conditions for the issuer kind and returns the canonical form.
func validateWorkloadConditions(w http.ResponseWriter, kind repository.WorkloadIssuerKind, conditions []adminopenapi.WorkloadCondition, allowAnyRef bool) (json.RawMessage, bool) {
	raw, err := json.Marshal(conditions)
	if err != nil {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "conditions are not valid JSON")
		return nil, false
	}
	canonical, _, err := workloadpolicy.Validate(workloadpolicy.Kind(kind), raw, workloadpolicy.ValidateOptions{AllowAnyRef: allowAnyRef})
	if err != nil {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_policy", err.Error())
		return nil, false
	}
	return canonical, true
}

// checkWorkloadAdminMapping refuses a mapping onto a service account that holds an administrator
// grant unless the request acknowledges it (ADR 0019, "Security considerations").
func (h workloadIdentityHandler) checkWorkloadAdminMapping(w http.ResponseWriter, r *http.Request, accountID string, allowAdmin bool) bool {
	if allowAdmin {
		return true
	}
	administered, err := h.store.ListRepositoriesGrantedToPrincipal(r.Context(), "service-account:"+accountID, authorization.GrantScopeAdmin)
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "read the service account grants failed")
		return false
	}
	if len(administered) > 0 {
		writeHostedProblem(w, http.StatusBadRequest, "admin_mapping_requires_allow_admin", "the service account holds an administrator grant; set allowAdmin to map a workload onto it")
		return false
	}
	return true
}

// checkSingleUse refuses single_use=true when a token of the issuer has no jti: replay protection
// cannot work without one. GitHub Actions, GitLab CI and Kubernetes tokens always carry a jti; for a
// generic issuer the request must bring a sample token (never stored) that does.
func checkSingleUse(w http.ResponseWriter, kind repository.WorkloadIssuerKind, sampleToken string) bool {
	if sampleToken != "" {
		claims, err := authorization.UnverifiedClaims(sampleToken)
		if err != nil {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "sampleToken is not a JWT")
			return false
		}
		if jti, _ := claims["jti"].(string); jti == "" {
			writeHostedProblem(w, http.StatusBadRequest, "single_use_requires_jti", "the issuer's sample token has no jti, so single_use cannot be enforced")
			return false
		}
		return true
	}
	if kind == repository.WorkloadKindGeneric {
		writeHostedProblem(w, http.StatusBadRequest, "single_use_requires_jti", "single_use on a generic issuer needs a sampleToken that carries a jti")
		return false
	}
	return true
}

func (h workloadIdentityHandler) CreateWorkloadTrustPolicy(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w, r) {
		return
	}
	var request adminopenapi.CreateWorkloadTrustPolicy
	if !decodeWorkloadBody(w, r, &request) {
		return
	}
	issuer, err := h.store.GetWorkloadIssuer(r.Context(), request.IssuerId.String())
	if err != nil {
		writeWorkloadStoreError(w, err, "workload issuer")
		return
	}
	accountID := request.ServiceAccountId.String()
	if _, err := h.store.GetServiceAccount(r.Context(), accountID); err != nil {
		writeWorkloadStoreError(w, err, "service account")
		return
	}
	canonical, ok := validateWorkloadConditions(w, issuer.Kind, request.Conditions, request.AllowAnyRef != nil && *request.AllowAnyRef)
	if !ok || !h.checkWorkloadAdminMapping(w, r, accountID, request.AllowAdmin != nil && *request.AllowAdmin) {
		return
	}
	policy := repository.WorkloadTrustPolicy{Name: request.Name, IssuerID: issuer.ID, ServiceAccountID: accountID, Conditions: canonical}
	if request.MaxTokenTtlSeconds != nil {
		policy.MaxTokenTTLSeconds = *request.MaxTokenTtlSeconds
	}
	if request.SingleUse != nil && *request.SingleUse {
		sample := ""
		if request.SampleToken != nil {
			sample = *request.SampleToken
		}
		if !checkSingleUse(w, issuer.Kind, sample) {
			return
		}
		policy.SingleUse = true
	}
	if request.State != nil {
		policy.State = repository.WorkloadState(*request.State)
	}
	if request.ExpiresAt != nil {
		expires := request.ExpiresAt.UTC()
		policy.ExpiresAt = &expires
	}
	created, err := h.store.CreateWorkloadTrustPolicy(r.Context(), policy, h.auditRecords(r, "workload-identity/policies/"+policy.Name, "workload_identity.policy.create", http.StatusCreated)...)
	if err != nil {
		writeWorkloadStoreError(w, err, "workload trust policy")
		return
	}
	writeNativeMavenJSON(w, http.StatusCreated, workloadPolicyResponse(created))
}

func (h workloadIdentityHandler) UpdateWorkloadTrustPolicy(w http.ResponseWriter, r *http.Request, policyID openapi_types.UUID, params adminopenapi.UpdateWorkloadTrustPolicyParams) {
	if !h.admit(w, r) {
		return
	}
	var request adminopenapi.UpdateWorkloadTrustPolicy
	if !decodeWorkloadBody(w, r, &request) {
		return
	}
	id := policyID.String()
	policy, err := h.store.GetWorkloadTrustPolicy(r.Context(), id)
	if err != nil {
		writeWorkloadStoreError(w, err, "workload trust policy")
		return
	}
	issuer, err := h.store.GetWorkloadIssuer(r.Context(), policy.IssuerID)
	if err != nil {
		writeWorkloadStoreError(w, err, "workload issuer")
		return
	}
	// The stored conditions were validated on write; they are validated again only when the request
	// changes them or restates the acknowledgement, because allow_any_ref is not stored.
	if request.Conditions != nil || request.AllowAnyRef != nil {
		conditions := policyConditionsForRevalidation(policy, request.Conditions)
		if conditions == nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "stored conditions are unreadable")
			return
		}
		canonical, ok := validateWorkloadConditions(w, issuer.Kind, conditions, request.AllowAnyRef != nil && *request.AllowAnyRef)
		if !ok {
			return
		}
		policy.Conditions = canonical
	}
	accountChanged := request.ServiceAccountId != nil && request.ServiceAccountId.String() != policy.ServiceAccountID
	// Re-enabling a policy re-checks the admin mapping: the account may have gained an administrator grant while the
	// policy was disabled, whatever else the request restates.
	reenabled := request.State != nil && *request.State == adminopenapi.WorkloadState(repository.WorkloadActive) && policy.State == repository.WorkloadDisabled
	if accountChanged {
		accountID := request.ServiceAccountId.String()
		if _, err := h.store.GetServiceAccount(r.Context(), accountID); err != nil {
			writeWorkloadStoreError(w, err, "service account")
			return
		}
		policy.ServiceAccountID = accountID
	}
	if (accountChanged || reenabled) && !h.checkWorkloadAdminMapping(w, r, policy.ServiceAccountID, request.AllowAdmin != nil && *request.AllowAdmin) {
		return
	}
	if request.MaxTokenTtlSeconds != nil {
		policy.MaxTokenTTLSeconds = *request.MaxTokenTtlSeconds
	}
	if request.SingleUse != nil {
		if *request.SingleUse && !policy.SingleUse {
			sample := ""
			if request.SampleToken != nil {
				sample = *request.SampleToken
			}
			if !checkSingleUse(w, issuer.Kind, sample) {
				return
			}
		}
		policy.SingleUse = *request.SingleUse
	}
	if request.State != nil {
		policy.State = repository.WorkloadState(*request.State)
	}
	switch {
	case request.ClearExpiresAt != nil && *request.ClearExpiresAt && request.ExpiresAt != nil:
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "expiresAt and clearExpiresAt are mutually exclusive")
		return
	case request.ClearExpiresAt != nil && *request.ClearExpiresAt:
		policy.ExpiresAt = nil
	case request.ExpiresAt != nil:
		expires := request.ExpiresAt.UTC()
		policy.ExpiresAt = &expires
	}
	updated, err := h.store.UpdateWorkloadTrustPolicy(r.Context(), policy, params.IfMatch, h.auditRecords(r, "workload-identity/policies/"+id, "workload_identity.policy.update", http.StatusOK)...)
	if err != nil {
		writeWorkloadStoreError(w, err, "workload trust policy")
		return
	}
	writeNativeMavenJSON(w, http.StatusOK, workloadPolicyResponse(updated))
}

// policyConditionsForRevalidation returns the request conditions, or the stored ones when only the
// allow_any_ref acknowledgement is restated.
func policyConditionsForRevalidation(policy repository.WorkloadTrustPolicy, requested *[]adminopenapi.WorkloadCondition) []adminopenapi.WorkloadCondition {
	if requested != nil {
		return *requested
	}
	return workloadPolicyResponse(policy).Conditions
}

func (h workloadIdentityHandler) DeleteWorkloadTrustPolicy(w http.ResponseWriter, r *http.Request, policyID openapi_types.UUID, params adminopenapi.DeleteWorkloadTrustPolicyParams) {
	if !h.admit(w, r) {
		return
	}
	id := policyID.String()
	if err := h.store.DeleteWorkloadTrustPolicy(r.Context(), id, params.IfMatch, h.auditRecords(r, "workload-identity/policies/"+id, "workload_identity.policy.delete", http.StatusNoContent)...); err != nil {
		writeWorkloadStoreError(w, err, "workload trust policy")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TestWorkloadTrustPolicy is the dry run of ADR 0019: it evaluates a pasted token or claim set against one
// policy or every active policy of an issuer and reports the failing condition. It changes no state and
// never consumes a single-use marker.
func (h workloadIdentityHandler) TestWorkloadTrustPolicy(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w, r) {
		return
	}
	var request adminopenapi.TestWorkloadPolicyRequest
	if !decodeWorkloadBody(w, r, &request) {
		return
	}
	if request.Token == nil && request.Claims == nil || request.Token != nil && request.Claims != nil {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "give either a token or a claim set")
		return
	}
	var claims map[string]any
	var token string
	if request.Token != nil {
		token = strings.TrimSpace(*request.Token)
		parsed, err := authorization.UnverifiedClaims(token)
		if err != nil {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		claims = parsed
	} else {
		claims = *request.Claims
	}
	issuer, policies, ok := h.testTargets(w, r, request, claims)
	if !ok {
		return
	}
	response := adminopenapi.WorkloadPolicyTest{Results: []adminopenapi.WorkloadPolicyTestResult{}, Warnings: []string{}}
	if id, err := parseUUID(issuer.ID); err == nil {
		response.IssuerId = &id
	}
	switch {
	case token == "":
		response.Warnings = append(response.Warnings, "a claim set is not verified; only a pasted token is checked against the issuer keys")
	case h.authenticator.Workload == nil:
		message := "workload federation is disabled (NOTARX_WORKLOAD_ENABLED=false); the token was not verified"
		response.VerificationError = &message
	default:
		if _, err := h.authenticator.Workload.VerifyForIssuer(r.Context(), token, issuer); err != nil {
			message := err.Error()
			response.VerificationError = &message
		} else {
			response.SignatureVerified = true
		}
	}
	if jti, _ := claims["jti"].(string); jti == "" {
		response.Warnings = append(response.Warnings, "the token has no jti: a single_use policy would reject it, and single_use cannot be enabled for this issuer")
	}
	parsed := make([]workloadpolicy.Policy, 0, len(policies))
	for _, record := range policies {
		policy, err := workloadpolicy.NewPolicy(workloadpolicy.Kind(issuer.Kind), record.ID, record.Name, record.ServiceAccountID, record.Conditions)
		if err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "a stored policy is unreadable")
			return
		}
		parsed = append(parsed, policy)
		result := adminopenapi.WorkloadPolicyTestResult{PolicyName: record.Name}
		result.PolicyId, _ = parseUUID(record.ID)
		if _, evalErr := workloadpolicy.Evaluate([]workloadpolicy.Policy{policy}, claims); evalErr == nil {
			result.Matched = true
		} else {
			var noMatch *workloadpolicy.NoMatchError
			if errors.As(evalErr, &noMatch) && len(noMatch.Failures) > 0 {
				failure := noMatch.Failures[0]
				result.Failure = &adminopenapi.WorkloadPolicyTestFailure{ConditionIndex: failure.Condition, Claim: []string(failure.Claim), Reason: failure.Reason}
			}
		}
		if record.SingleUse && claims["jti"] == nil {
			response.Warnings = append(response.Warnings, "policy "+record.Name+" is single_use and this token has no jti")
		}
		response.Results = append(response.Results, result)
	}
	matched, err := workloadpolicy.Evaluate(parsed, claims)
	var ambiguous *workloadpolicy.AmbiguousError
	switch {
	case err == nil:
		id, _ := parseUUID(matched.ID)
		response.MatchedPolicyId = &id
	case errors.As(err, &ambiguous):
		yes := true
		response.Ambiguous = &yes
		response.Warnings = append(response.Warnings, "policies of different service accounts match, so authentication would be refused: "+strings.Join(ambiguous.Policies, ", "))
	}
	writeNativeMavenJSON(w, http.StatusOK, response)
}

// testTargets resolves the issuer and the policies a dry run evaluates.
func (h workloadIdentityHandler) testTargets(w http.ResponseWriter, r *http.Request, request adminopenapi.TestWorkloadPolicyRequest, claims map[string]any) (repository.WorkloadIssuer, []repository.WorkloadTrustPolicy, bool) {
	ctx := r.Context()
	switch {
	case request.PolicyId != nil:
		policy, err := h.store.GetWorkloadTrustPolicy(ctx, request.PolicyId.String())
		if err != nil {
			writeWorkloadStoreError(w, err, "workload trust policy")
			return repository.WorkloadIssuer{}, nil, false
		}
		issuer, err := h.store.GetWorkloadIssuer(ctx, policy.IssuerID)
		if err != nil {
			writeWorkloadStoreError(w, err, "workload issuer")
			return repository.WorkloadIssuer{}, nil, false
		}
		return issuer, []repository.WorkloadTrustPolicy{policy}, true
	case request.IssuerId != nil:
		return h.issuerPolicies(w, r, request.IssuerId.String(), "")
	}
	issuerURL, _ := claims["iss"].(string)
	if issuerURL == "" {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "name an issuer with issuerId or policyId, or include an iss claim")
		return repository.WorkloadIssuer{}, nil, false
	}
	return h.issuerPolicies(w, r, "", issuerURL)
}

func (h workloadIdentityHandler) issuerPolicies(w http.ResponseWriter, r *http.Request, id, issuerURL string) (repository.WorkloadIssuer, []repository.WorkloadTrustPolicy, bool) {
	var issuer repository.WorkloadIssuer
	var err error
	if id != "" {
		issuer, err = h.store.GetWorkloadIssuer(r.Context(), id)
	} else {
		issuer, err = h.store.GetWorkloadIssuerByIssuer(r.Context(), issuerURL)
	}
	if err != nil {
		writeWorkloadStoreError(w, err, "workload issuer")
		return repository.WorkloadIssuer{}, nil, false
	}
	policies, err := h.store.ListActiveWorkloadTrustPolicies(r.Context(), issuer.ID, time.Now().UTC())
	if err != nil {
		writeWorkloadStoreError(w, err, "workload trust policy")
		return repository.WorkloadIssuer{}, nil, false
	}
	return issuer, policies, true
}
