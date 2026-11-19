package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/jose"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

const providerResourcePrefix = "authentication/providers/"

func (h identityManagementHandler) providersAvailable(w http.ResponseWriter) bool {
	if h.identityProviders == nil {
		writeHostedProblem(w, http.StatusServiceUnavailable, "not_supported", "the identity provider registry is unavailable")
		return false
	}
	return true
}

func (h identityManagementHandler) ListIdentityProviders(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r); !ok || !h.providersAvailable(w) {
		return
	}
	providers, err := h.identityProviders.List(r.Context())
	if err != nil {
		writeHostedProblem(w, http.StatusServiceUnavailable, "identity_providers_unavailable", "load identity providers failed")
		return
	}
	items := make([]adminopenapi.IdentityProvider, 0, len(providers))
	for _, provider := range providers {
		items = append(items, h.providerResponse(provider))
	}
	writeNativeMavenJSON(w, http.StatusOK, adminopenapi.IdentityProviderList{Items: items})
}

func (h identityManagementHandler) GetIdentityProvider(w http.ResponseWriter, r *http.Request, providerID openapi_types.UUID) {
	if _, ok := h.authorize(w, r); !ok || !h.providersAvailable(w) {
		return
	}
	provider, err := h.identityProviders.Get(r.Context(), providerID.String())
	if err != nil {
		writeIdentityProviderError(w, err, "load identity provider failed")
		return
	}
	h.writeIdentityProvider(w, http.StatusOK, provider)
}

func (h identityManagementHandler) CreateIdentityProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r); !ok || !h.providersAvailable(w) {
		return
	}
	var request adminopenapi.CreateIdentityProvider
	if !decodeProviderRequest(w, r, &request) {
		return
	}
	input := IdentityProviderInput{
		Slug: request.Slug, Kind: identity.Kind(request.Kind), DisplayName: request.DisplayName, Enabled: request.Enabled,
		AttributeMapping: attributeMappingFromRequest(request.AttributeMapping),
	}
	if !applyProviderSettings(w, &input, request.Oidc, request.Saml) {
		return
	}
	if request.ProvisioningMode != nil {
		input.ProvisioningMode = identity.ProvisioningMode(*request.ProvisioningMode)
	}
	if request.JitDefaultRole != nil {
		input.JITDefaultRole = string(*request.JitDefaultRole)
	}
	input.EmailLinkingEnabled = request.EmailLinkingEnabled != nil && *request.EmailLinkingEnabled
	// The id is created inside the runtime, so the audit names the collection and the
	// stored record carries the slug.
	provider, err := h.identityProviders.Create(r.Context(), input, h.auditRecords(r, providerResourcePrefix+input.Slug, "authentication.provider.create", http.StatusCreated)...)
	if err != nil {
		writeIdentityProviderError(w, err, "create identity provider failed")
		return
	}
	h.writeIdentityProvider(w, http.StatusCreated, provider)
}

func (h identityManagementHandler) ReplaceIdentityProvider(w http.ResponseWriter, r *http.Request, providerID openapi_types.UUID, params adminopenapi.ReplaceIdentityProviderParams) {
	if _, ok := h.authorize(w, r); !ok || !h.providersAvailable(w) {
		return
	}
	version, ok := parseProviderVersion(string(params.IfMatch))
	if !ok {
		writeHostedProblem(w, http.StatusPreconditionFailed, "version_conflict", "If-Match does not match the current identity provider version")
		return
	}
	var request adminopenapi.ReplaceIdentityProvider
	if !decodeProviderRequest(w, r, &request) {
		return
	}
	input := IdentityProviderInput{
		DisplayName: request.DisplayName, Enabled: request.Enabled,
		AttributeMapping: attributeMappingFromRequest(&request.AttributeMapping), ProvisioningMode: identity.ProvisioningMode(request.ProvisioningMode),
		EmailLinkingEnabled: request.EmailLinkingEnabled, JITDefaultRole: string(request.JitDefaultRole),
	}
	id := providerID.String()
	// The kind is fixed at creation, so the stored provider decides which settings object applies.
	current, err := h.identityProviders.Get(r.Context(), id)
	if err != nil {
		writeIdentityProviderError(w, err, "load identity provider failed")
		return
	}
	input.Kind = current.Kind
	if !applyProviderSettings(w, &input, request.Oidc, request.Saml) {
		return
	}
	provider, err := h.identityProviders.Replace(r.Context(), id, input, version, h.auditRecords(r, providerResourcePrefix+id, "authentication.provider.update", http.StatusOK)...)
	if err != nil {
		writeIdentityProviderError(w, err, "replace identity provider failed")
		return
	}
	h.writeIdentityProvider(w, http.StatusOK, provider)
}

// applyProviderSettings copies the settings object that matches the kind into the input and rejects a
// request that omits it or sends the other kind's object.
func applyProviderSettings(w http.ResponseWriter, input *IdentityProviderInput, oidc *adminopenapi.IdentityProviderOIDCInput, samlInput *adminopenapi.IdentityProviderSAMLInput) bool {
	switch input.Kind {
	case identity.KindOIDC:
		if oidc == nil || samlInput != nil {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "an OIDC provider takes the oidc settings object and no saml object")
			return false
		}
		input.OIDC = oidcSettingsFromInput(*oidc)
		input.ClientSecret = oidc.ClientSecret
		input.ClearClientSecret = oidc.ClearClientSecret != nil && *oidc.ClearClientSecret
	case identity.KindSAML:
		if samlInput == nil || oidc != nil {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "a SAML provider takes the saml settings object and no oidc object")
			return false
		}
		input.SAML = samlSettingsFromInput(samlInput)
	default:
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "kind must be oidc or saml")
		return false
	}
	return true
}

func samlSettingsFromInput(input *adminopenapi.IdentityProviderSAMLInput) *identity.SAMLSettings {
	settings := &identity.SAMLSettings{EntityID: input.EntityId, SSOURL: input.SsoUrl}
	for _, certificate := range input.Certificates {
		entry := identity.SAMLCertificate{PEM: certificate.Pem}
		if certificate.State != nil {
			entry.State = string(*certificate.State)
		}
		settings.Certificates = append(settings.Certificates, entry)
	}
	if input.NameIdFormat != nil {
		settings.NameIDFormat = string(*input.NameIdFormat)
	}
	settings.AllowIdPInitiated = input.AllowIdpInitiated != nil && *input.AllowIdpInitiated
	if input.RelayStateAllowlist != nil {
		settings.RelayStateAllowlist = *input.RelayStateAllowlist
	}
	settings.SignRequests = input.SignRequests != nil && *input.SignRequests
	if input.MetadataUrl != nil {
		settings.MetadataURL = *input.MetadataUrl
	}
	settings.MetadataRefresh = input.MetadataRefresh != nil && *input.MetadataRefresh
	return settings
}

func samlSettingsResponse(settings identity.SAMLSettings, view SAMLView) *adminopenapi.IdentityProviderSAMLSettings {
	certificates := make([]adminopenapi.IdentityProviderSAMLCertificate, 0, len(settings.Certificates))
	for _, certificate := range settings.Certificates {
		certificates = append(certificates, adminopenapi.IdentityProviderSAMLCertificate{
			Pem: certificate.PEM, NotAfter: certificate.NotAfter, State: adminopenapi.IdentityProviderSAMLCertificateState(certificate.State),
		})
	}
	keys := make([]adminopenapi.IdentityProviderSAMLKey, 0, len(view.Keys))
	for _, key := range view.Keys {
		if id, err := uuid.Parse(key.ID); err == nil {
			keys = append(keys, adminopenapi.IdentityProviderSAMLKey{Id: id, NotAfter: key.NotAfter})
		}
	}
	return &adminopenapi.IdentityProviderSAMLSettings{
		EntityId: settings.EntityID, SsoUrl: settings.SSOURL, Certificates: certificates,
		NameIdFormat:      adminopenapi.IdentityProviderSAMLSettingsNameIdFormat(settings.NameIDFormat),
		AllowIdpInitiated: settings.AllowIdPInitiated, RelayStateAllowlist: nonNil(settings.RelayStateAllowlist),
		SignRequests: settings.SignRequests, MetadataUrl: optionalString(settings.MetadataURL), MetadataRefresh: settings.MetadataRefresh,
		SpEntityId: view.Endpoints.EntityID, AcsUrl: view.Endpoints.ACSURL, MetadataEndpoint: view.Endpoints.MetadataURL, SpKeys: keys,
	}
}

func (h identityManagementHandler) ImportIdentityProviderMetadata(w http.ResponseWriter, r *http.Request, providerID openapi_types.UUID) {
	if _, ok := h.authorize(w, r); !ok || !h.providersAvailable(w) {
		return
	}
	var request adminopenapi.ImportIdentityProviderMetadata
	if !decodeProviderRequest(w, r, &request) {
		return
	}
	source := SAMLMetadataSource{}
	if request.Url != nil {
		source.URL = *request.Url
	}
	if request.Xml != nil {
		source.XML = *request.Xml
	}
	id := providerID.String()
	provider, err := h.identityProviders.ImportSAMLMetadata(r.Context(), id, source, h.auditRecords(r, providerResourcePrefix+id, "authentication.provider.import-metadata", http.StatusOK)...)
	if err != nil {
		writeIdentityProviderError(w, err, "import SAML metadata failed")
		return
	}
	h.writeIdentityProvider(w, http.StatusOK, provider)
}

func (h identityManagementHandler) RotateIdentityProviderSPKey(w http.ResponseWriter, r *http.Request, providerID openapi_types.UUID) {
	if _, ok := h.authorize(w, r); !ok || !h.providersAvailable(w) {
		return
	}
	id := providerID.String()
	provider, err := h.identityProviders.RotateSAMLSPKey(r.Context(), id, h.auditRecords(r, providerResourcePrefix+id, "authentication.provider.rotate-sp-key", http.StatusOK)...)
	if err != nil {
		writeIdentityProviderError(w, err, "rotate the service provider key failed")
		return
	}
	h.writeIdentityProvider(w, http.StatusOK, provider)
}

func (h identityManagementHandler) DeleteIdentityProvider(w http.ResponseWriter, r *http.Request, providerID openapi_types.UUID, params adminopenapi.DeleteIdentityProviderParams) {
	if _, ok := h.authorize(w, r); !ok || !h.providersAvailable(w) {
		return
	}
	version, ok := parseProviderVersion(string(params.IfMatch))
	if !ok {
		writeHostedProblem(w, http.StatusPreconditionFailed, "version_conflict", "If-Match does not match the current identity provider version")
		return
	}
	id := providerID.String()
	if err := h.identityProviders.Delete(r.Context(), id, version, h.auditRecords(r, providerResourcePrefix+id, "authentication.provider.delete", http.StatusNoContent)...); err != nil {
		writeIdentityProviderError(w, err, "delete identity provider failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h identityManagementHandler) TestIdentityProvider(w http.ResponseWriter, r *http.Request, providerID openapi_types.UUID) {
	principal, ok := h.authorize(w, r)
	if !ok || !h.providersAvailable(w) {
		return
	}
	id := providerID.String()
	result, err := h.identityProviders.Test(r.Context(), id)
	status, outcome := http.StatusOK, repository.AuditResolved
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeHostedProblem(w, http.StatusNotFound, "not_found", "identity provider not found")
		return
	case errors.Is(err, errOIDCNotEnabled):
		status, outcome = http.StatusBadRequest, repository.AuditAccessDenied
	case err != nil:
		status, outcome = http.StatusServiceUnavailable, repository.AuditUpstreamError
	}
	if h.audit != nil {
		// The test changes nothing: its audit stays best-effort so the admin still
		// sees the real discovery outcome.
		_ = h.audit.RecordAudit(r.Context(), repository.AuditRecord{
			Actor: principal.Actor, Outcome: outcome, OccurredAt: time.Now().UTC(), Format: "management",
			Resource: providerResourcePrefix + id, Operation: "authentication.provider.test", Status: status, CacheDisposition: "bypass",
		})
	}
	if err != nil {
		message := "OIDC provider discovery failed"
		if errors.Is(err, errOIDCNotEnabled) {
			message = "the provider has no OIDC issuer to test"
		}
		writeHostedProblem(w, status, "oidc_unavailable", message)
		return
	}
	writeNativeMavenJSON(w, http.StatusOK, adminopenapi.OIDCConnectionTest{
		Reachable: result.Reachable, Issuer: result.Issuer,
		AuthorizationEndpoint: optionalString(result.AuthorizationEndpoint),
		TokenEndpoint:         optionalString(result.TokenEndpoint), JwksUrl: optionalString(result.JWKSURL),
		LatencyMs: int(result.LatencyMs), CheckedAt: result.CheckedAt,
	})
}

func decodeProviderRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "identity provider request is invalid")
		return false
	}
	return true
}

// parseProviderVersion reads an If-Match value: the bare version or the quoted ETag.
func parseProviderVersion(value string) (int64, bool) {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "W/"))
	value = strings.Trim(value, `"`)
	version, err := strconv.ParseInt(value, 10, 64)
	return version, err == nil && version > 0
}

func writeIdentityProviderError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, identity.ErrInvalid):
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, repository.ErrNotFound):
		writeHostedProblem(w, http.StatusNotFound, "not_found", "identity provider not found")
	case errors.Is(err, repository.ErrVersionConflict):
		writeHostedProblem(w, http.StatusPreconditionFailed, "version_conflict", "If-Match does not match the current identity provider version")
	case errors.Is(err, repository.ErrIdentityProviderSlugExists):
		writeHostedProblem(w, http.StatusConflict, "slug_exists", "an identity provider with this slug already exists")
	case errors.Is(err, repository.ErrIdentityProviderInUse):
		writeHostedProblem(w, http.StatusConflict, "provider_in_use", "unlink the identities of this provider before deleting it")
	case errors.Is(err, secrets.ErrKeyNotConfigured), errors.Is(err, secrets.ErrInvalidKey):
		writeHostedProblem(w, http.StatusServiceUnavailable, "encryption_key_unavailable", secrets.KeyEnv+" is required to persist identity provider credentials")
	default:
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", fallback)
	}
}

func (h identityManagementHandler) writeIdentityProvider(w http.ResponseWriter, status int, provider identity.Provider) {
	w.Header().Set("ETag", `"`+strconv.FormatInt(provider.Version, 10)+`"`)
	writeNativeMavenJSON(w, status, h.providerResponse(provider))
}

// providerResponse adds the derived SAML fields (service provider URLs and published keys) that only the
// runtime knows to the stored provider.
func (h identityManagementHandler) providerResponse(provider identity.Provider) adminopenapi.IdentityProvider {
	response := identityProviderResponse(provider)
	if provider.Kind == identity.KindSAML {
		if settings, err := provider.SAML(); err == nil {
			response.Saml = samlSettingsResponse(settings, h.identityProviders.SAMLView(provider))
		}
	}
	return response
}

func oidcSettingsFromInput(input adminopenapi.IdentityProviderOIDCInput) identity.OIDCSettings {
	settings := identity.OIDCSettings{
		Issuer: input.Issuer, Audience: input.Audience, ClientID: input.ClientId, RedirectURL: input.RedirectUrl,
		Scopes: input.Scopes, AdminSubjects: input.AdminSubjects, MemberRoles: input.MemberRoles, AdminRoles: input.AdminRoles,
	}
	if input.JwksUrl != nil {
		settings.JWKSURL = *input.JwksUrl
	}
	if input.Algorithms != nil {
		for _, algorithm := range *input.Algorithms {
			settings.Algorithms = append(settings.Algorithms, string(algorithm))
		}
	}
	return settings
}

func attributeMappingFromRequest(input *adminopenapi.IdentityProviderAttributeMapping) identity.AttributeMapping {
	if input == nil {
		return identity.AttributeMapping{}
	}
	value := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return identity.AttributeMapping{
		Subject: value(input.Subject), Username: value(input.Username), Email: value(input.Email),
		DisplayName: value(input.DisplayName), Groups: value(input.Groups), ExternalID: value(input.ExternalId),
	}
}

func identityProviderResponse(provider identity.Provider) adminopenapi.IdentityProvider {
	mapping := provider.AttributeMapping
	response := adminopenapi.IdentityProvider{
		Id: uuid.MustParse(provider.ID), Slug: provider.Slug, Kind: adminopenapi.IdentityProviderKind(provider.Kind),
		DisplayName: provider.DisplayName, Enabled: provider.Enabled,
		AttributeMapping: adminopenapi.IdentityProviderAttributeMapping{
			Subject: optionalString(mapping.Subject), Username: optionalString(mapping.Username), Email: optionalString(mapping.Email),
			DisplayName: optionalString(mapping.DisplayName), Groups: optionalString(mapping.Groups), ExternalId: optionalString(mapping.ExternalID),
		},
		ProvisioningMode: adminopenapi.IdentityProviderProvisioningMode(provider.ProvisioningMode), EmailLinkingEnabled: provider.EmailLinkingEnabled,
		JitDefaultRole: adminopenapi.IdentityProviderJitDefaultRole(provider.JITDefaultRole),
		Version:        strconv.FormatInt(provider.Version, 10), CreatedAt: provider.CreatedAt, UpdatedAt: provider.UpdatedAt,
	}
	if provider.Kind == identity.KindOIDC {
		if settings, err := provider.OIDC(); err == nil {
			algorithms := make([]adminopenapi.IdentityProviderOIDCSettingsAlgorithms, 0, len(settings.Algorithms))
			for _, algorithm := range settings.Algorithms {
				if algorithm == jose.RS256 || algorithm == jose.ES256 || algorithm == jose.EdDSA {
					algorithms = append(algorithms, adminopenapi.IdentityProviderOIDCSettingsAlgorithms(algorithm))
				}
			}
			response.Oidc = &adminopenapi.IdentityProviderOIDCSettings{
				Issuer: settings.Issuer, Audience: settings.Audience, JwksUrl: optionalString(settings.JWKSURL),
				ClientId: settings.ClientID, ClientSecretConfigured: provider.SecretsSealed != "", RedirectUrl: settings.RedirectURL,
				Scopes: nonNil(settings.Scopes), AdminSubjects: nonNil(settings.AdminSubjects), MemberRoles: nonNil(settings.MemberRoles),
				AdminRoles: nonNil(settings.AdminRoles), Algorithms: algorithms,
			}
		}
	}
	return response
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
