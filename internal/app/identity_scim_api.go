package app

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/repository"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// scimTokenPrefix starts every SCIM bearer token, so the server can refuse a malformed credential without a lookup.
const scimTokenPrefix = "nxscim_"

// scimTokenBytes is the entropy of a token: 256 bits.
const scimTokenBytes = 32

// scimAvailable reports whether the token store and the provider registry exist; it answers 503 when one is missing.
func (h identityManagementHandler) scimAvailable(w http.ResponseWriter) bool {
	if h.scimTokens == nil {
		writeHostedProblem(w, http.StatusServiceUnavailable, "not_supported", "SCIM tokens are unavailable")
		return false
	}
	return h.providersAvailable(w)
}

// scimProvider loads the provider a SCIM route addresses; an unknown one is 404.
func (h identityManagementHandler) scimProvider(w http.ResponseWriter, r *http.Request, providerID openapi_types.UUID) (identity.Provider, bool) {
	provider, err := h.identityProviders.Get(r.Context(), providerID.String())
	if err != nil {
		writeIdentityProviderError(w, err, "load identity provider failed")
		return identity.Provider{}, false
	}
	return provider, true
}

func scimTokenState(token repository.SCIMToken, now time.Time) adminopenapi.SCIMTokenState {
	switch {
	case token.RevokedAt != nil:
		return adminopenapi.SCIMTokenStateRevoked
	case !now.Before(token.ExpiresAt):
		return adminopenapi.SCIMTokenStateExpired
	}
	return adminopenapi.SCIMTokenStateActive
}

func scimTokenResponse(token repository.SCIMToken, now time.Time) adminopenapi.SCIMToken {
	return adminopenapi.SCIMToken{
		Id: uuid.MustParse(token.ID), ProviderId: uuid.MustParse(token.ProviderID), Name: token.Name,
		State: scimTokenState(token, now), CreatedAt: token.CreatedAt, ExpiresAt: token.ExpiresAt, RevokedAt: token.RevokedAt,
	}
}

func scimTokenAuditResource(providerID, tokenID string) string {
	return providerResourcePrefix + providerID + "/scim-tokens/" + tokenID
}

func (h identityManagementHandler) ListSCIMTokens(w http.ResponseWriter, r *http.Request, providerID openapi_types.UUID) {
	if _, ok := h.authorize(w, r); !ok || !h.scimAvailable(w) {
		return
	}
	if _, ok := h.scimProvider(w, r, providerID); !ok {
		return
	}
	tokens, err := h.scimTokens.ListSCIMTokens(r.Context(), providerID.String())
	if err != nil {
		writeHostedProblem(w, http.StatusServiceUnavailable, "scim_tokens_unavailable", "load SCIM tokens failed")
		return
	}
	now := time.Now().UTC()
	items := make([]adminopenapi.SCIMToken, 0, len(tokens))
	for _, token := range tokens {
		items = append(items, scimTokenResponse(token, now))
	}
	writeNativeMavenJSON(w, http.StatusOK, adminopenapi.SCIMTokenList{Items: items})
}

func (h identityManagementHandler) CreateSCIMToken(w http.ResponseWriter, r *http.Request, providerID openapi_types.UUID) {
	if _, ok := h.authorize(w, r); !ok || !h.scimAvailable(w) {
		return
	}
	var request adminopenapi.CreateSCIMToken
	if !decodeProviderRequest(w, r, &request) {
		return
	}
	if _, ok := h.scimProvider(w, r, providerID); !ok {
		return
	}
	now := time.Now().UTC()
	name := strings.TrimSpace(request.Name)
	if length := utf8.RuneCountInString(name); length == 0 || length > repository.MaxSCIMTokenNameLength {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "name is required and at most 128 characters")
		return
	}
	if !request.ExpiresAt.After(now) || request.ExpiresAt.After(now.Add(repository.MaxSCIMTokenLifetime)) {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "expiresAt is required, in the future and at most 365 days from now")
		return
	}
	raw := make([]byte, scimTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "generate SCIM token failed")
		return
	}
	plaintext := scimTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	id := providerID.String()
	tokenID := ids.New()
	// Only the hash is stored; the plaintext exists in this response alone and is neither logged nor audited.
	created, err := h.scimTokens.CreateSCIMToken(r.Context(), repository.SCIMToken{
		ID: tokenID, ProviderID: id, Name: name, SecretHash: authorization.HashAPIKey(plaintext), ExpiresAt: request.ExpiresAt,
	}, h.auditRecords(r, scimTokenAuditResource(id, tokenID), "authentication.scim-token.create", http.StatusCreated)...)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeHostedProblem(w, http.StatusNotFound, "not_found", "identity provider not found")
		return
	case errors.Is(err, repository.ErrSCIMTokenInvalid):
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "the SCIM token is invalid")
		return
	case err != nil:
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "create SCIM token failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeNativeMavenJSON(w, http.StatusCreated, adminopenapi.CreatedSCIMToken{
		Id: uuid.MustParse(created.ID), ProviderId: uuid.MustParse(created.ProviderID), Name: created.Name,
		State: adminopenapi.CreatedSCIMTokenState(scimTokenState(created, now)), CreatedAt: created.CreatedAt, ExpiresAt: created.ExpiresAt,
		RevokedAt: created.RevokedAt, Token: plaintext,
	})
}

func (h identityManagementHandler) RevokeSCIMToken(w http.ResponseWriter, r *http.Request, providerID, tokenID openapi_types.UUID) {
	if _, ok := h.authorize(w, r); !ok || !h.scimAvailable(w) {
		return
	}
	pid, tid := providerID.String(), tokenID.String()
	token, err := h.scimTokens.RevokeSCIMToken(r.Context(), pid, tid, h.auditRecords(r, scimTokenAuditResource(pid, tid), "authentication.scim-token.revoke", http.StatusOK)...)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeHostedProblem(w, http.StatusNotFound, "not_found", "SCIM token not found")
		return
	case err != nil:
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "revoke SCIM token failed")
		return
	}
	writeNativeMavenJSON(w, http.StatusOK, scimTokenResponse(token, time.Now().UTC()))
}

// providerIssuerText is the informational issuer a linked identity records: the OIDC issuer or the SAML entity id.
func providerIssuerText(provider identity.Provider) string {
	switch provider.Kind {
	case identity.KindOIDC:
		if settings, err := provider.OIDC(); err == nil {
			return settings.Issuer
		}
	case identity.KindSAML:
		if settings, err := provider.SAML(); err == nil {
			return settings.EntityID
		}
	}
	return "scim:" + provider.Slug
}

func (h identityManagementHandler) LinkIdentityProviderAccount(w http.ResponseWriter, r *http.Request, providerID openapi_types.UUID) {
	if _, ok := h.authorize(w, r); !ok || !h.providersAvailable(w) {
		return
	}
	if h.links == nil {
		writeHostedProblem(w, http.StatusServiceUnavailable, "not_supported", "account linking is unavailable")
		return
	}
	var request adminopenapi.CreateProviderLink
	if !decodeProviderRequest(w, r, &request) {
		return
	}
	provider, ok := h.scimProvider(w, r, providerID)
	if !ok {
		return
	}
	subject := strings.TrimSpace(request.Subject)
	if subject == "" || len(subject) > 512 {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "subject is required and at most 512 characters")
		return
	}
	id := providerID.String()
	linked, err := h.links.LinkProviderIdentity(r.Context(), repository.ProviderIdentityLink{
		ProviderID: id, UserID: request.UserId.String(), Subject: subject, Issuer: providerIssuerText(provider), Kind: string(provider.Kind),
	}, h.auditRecords(r, providerResourcePrefix+id+"/links/"+request.UserId.String(), "authentication.provider.link", http.StatusOK)...)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeHostedProblem(w, http.StatusNotFound, "not_found", "identity provider or account not found")
		return
	case errors.Is(err, repository.ErrIdentityExists):
		writeHostedProblem(w, http.StatusConflict, "identity_exists", "the subject or the account is already linked at this provider")
		return
	case errors.Is(err, repository.ErrSCIMInvalid):
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "the link is not valid")
		return
	case err != nil:
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "link the account failed")
		return
	}
	writeNativeMavenJSON(w, http.StatusOK, userIdentityResponse(linked))
}
