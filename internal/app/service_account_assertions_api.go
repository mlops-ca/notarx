package app

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/tokenissuer"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// Problem codes of the assertion API (ADR 0020).
const (
	problemAssertionAudienceNotAllowed = "assertion_audience_not_allowed"
	problemAssertionPrincipal          = "assertion_principal_not_allowed"
	problemAssertionLifetime           = "assertion_lifetime_exceeded"
	problemAssertionSubjectMissing     = "assertion_subject_missing"
	problemAssertionSubjectReserved    = "assertion_subject_reserved"
	problemIssuerDisabled              = "issuer_disabled"
	problemSigningUnavailable          = "signing_unavailable"
)

// validAssertionText reports whether value is usable as an audience or a subject: printable, trimmed and short enough
// for the key columns that hold it.
func validAssertionText(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || utf8.RuneCountInString(value) > repository.MaxAssertionAudienceLength || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// MintServiceAccountAssertion implements POST /service-accounts/{id}/assertions (ADR 0020): a service account, through
// one of its API keys or as a federated workload principal, mints a short-lived JWT for an audience on its own
// allow-list. Human users and the static administrator token are refused, so a human session cannot be turned into a
// workload assertion.
func (h identityManagementHandler) MintServiceAccountAssertion(w http.ResponseWriter, r *http.Request, serviceAccountID openapi_types.UUID) {
	principal, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var credential string
	switch principal.AuthenticationKind {
	case authorization.AuthenticationServiceAccountCredential:
		credential = tokenissuer.CredentialAPIKey
	case authorization.AuthenticationWorkloadFederation:
		credential = tokenissuer.CredentialWorkloadFederation
	default:
		writeHostedProblem(w, http.StatusForbidden, problemAssertionPrincipal, "only a service account or a federated workload principal may mint assertions")
		return
	}
	accountID := serviceAccountID.String()
	if principal.Actor != "service-account:"+accountID {
		writeHostedProblem(w, http.StatusForbidden, problemAssertionPrincipal, "a service account may mint assertions only for itself")
		return
	}
	if !h.issuer.Enabled() || h.serviceAccounts == nil {
		writeHostedProblem(w, http.StatusNotFound, problemIssuerDisabled, "the OIDC issuer is disabled")
		return
	}
	var request adminopenapi.MintServiceAccountAssertion
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || !validAssertionText(request.Audience) {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "an audience of 1 to 255 printable characters is required")
		return
	}
	lifetime := time.Duration(0)
	if request.LifetimeSeconds != nil {
		seconds := *request.LifetimeSeconds
		if seconds < 1 || seconds > repository.MaxAssertionLifetimeSeconds {
			writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "lifetimeSeconds must be between 1 and 600")
			return
		}
		lifetime = time.Duration(seconds) * time.Second
	}
	account, err := h.serviceAccounts.GetServiceAccount(r.Context(), accountID)
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "service account not found")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "read service account failed")
		return
	}
	if account.State != repository.ServiceAccountActive {
		writeHostedProblem(w, http.StatusForbidden, "service_account_disabled", "service account is disabled")
		return
	}
	if account.AssertionSubject == "" {
		writeHostedProblem(w, http.StatusConflict, problemAssertionSubjectMissing, "the service account has no assertion subject")
		return
	}
	entry, err := h.serviceAccounts.GetAssertionAudience(r.Context(), accountID, request.Audience)
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusForbidden, problemAssertionAudienceNotAllowed, "the audience is not on the assertion allow-list of the service account")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "read assertion allow-list failed")
		return
	}
	limit := min(time.Duration(entry.MaxLifetimeSeconds)*time.Second, h.issuer.Config.MaxTokenLifetime)
	if lifetime > limit {
		writeHostedProblem(w, http.StatusBadRequest, problemAssertionLifetime, "the lifetime exceeds the maximum of "+limit.String()+" for this audience")
		return
	}
	claims, err := tokenissuer.ClaimBuilder{Issuer: h.issuer.Config.URL, MaxLifetime: limit}.Build(tokenissuer.ClaimRequest{
		Subject: account.AssertionSubject, Audiences: []string{request.Audience}, Lifetime: lifetime,
		ServiceAccount: accountID, Credential: credential,
	})
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "build assertion claims failed")
		return
	}
	token, kid, err := h.issuer.Signer.Sign(r.Context(), claims)
	if err != nil {
		slog.Error("sign service account assertion", "error", err)
		writeHostedProblem(w, http.StatusServiceUnavailable, problemSigningUnavailable, "no signing key is available")
		return
	}
	// The mint is recorded before the token leaves: an assertion that cannot be audited is not handed out. The record
	// holds the identifiers of the token, never the token.
	if h.audit != nil {
		record := repository.AuditRecord{
			Class: repository.AuditClassSync, Actor: principal.Actor, Resource: "service-accounts/" + accountID,
			Operation: "service_account.assertion.mint", Status: http.StatusOK, AuthorizationSource: credential,
			Evidence: map[string]string{
				"serviceAccountId": accountID, "audience": request.Audience, "subject": account.AssertionSubject,
				"jti": claims.JTI, "kid": kid, "credentialId": principal.CredentialID, "credentialKind": credential,
			},
		}
		if !recordUnchangedAudit(w, h.audit, r.Context(), record) {
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeNativeMavenJSON(w, http.StatusOK, adminopenapi.ServiceAccountAssertion{
		Token: token, TokenType: adminopenapi.JWT, Audience: claims.Audience, Subject: claims.Subject,
		ExpiresAt: time.Unix(claims.Expiry, 0).UTC(), Jti: claims.JTI, Kid: kid,
	})
}

func assertionAudienceResponse(entry repository.AssertionAudience) adminopenapi.ServiceAccountAssertionAudience {
	return adminopenapi.ServiceAccountAssertionAudience{
		ServiceAccountId: uuid.MustParse(entry.ServiceAccountID), Audience: entry.Audience,
		MaxLifetimeSeconds: entry.MaxLifetimeSeconds, CreatedAt: entry.CreatedAt,
	}
}

func (h identityManagementHandler) ListServiceAccountAssertionAudiences(w http.ResponseWriter, r *http.Request, serviceAccountID openapi_types.UUID) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	entries, err := h.serviceAccounts.ListAssertionAudiences(r.Context(), serviceAccountID.String())
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "service account not found")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list assertion audiences failed")
		return
	}
	items := make([]adminopenapi.ServiceAccountAssertionAudience, 0, len(entries))
	for _, entry := range entries {
		items = append(items, assertionAudienceResponse(entry))
	}
	writeNativeMavenJSON(w, http.StatusOK, adminopenapi.ServiceAccountAssertionAudienceList{Items: items})
}

func (h identityManagementHandler) PutServiceAccountAssertionAudience(w http.ResponseWriter, r *http.Request, serviceAccountID openapi_types.UUID) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	var request adminopenapi.PutServiceAccountAssertionAudience
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || !validAssertionText(request.Audience) {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "an audience of 1 to 255 printable characters is required")
		return
	}
	seconds := repository.MaxAssertionLifetimeSeconds
	if request.MaxLifetimeSeconds != nil {
		seconds = *request.MaxLifetimeSeconds
	}
	if seconds < repository.MinAssertionLifetimeSeconds || seconds > repository.MaxAssertionLifetimeSeconds {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "maxLifetimeSeconds must be between 60 and 600")
		return
	}
	accountID := serviceAccountID.String()
	// An audience that is not a plain identifier is stored as given: Entra and notarx peers match it byte for byte.
	account, err := h.serviceAccounts.GetServiceAccount(r.Context(), accountID)
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "service account not found")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "read service account failed")
		return
	}
	if account.AssertionSubject == "" {
		writeHostedProblem(w, http.StatusConflict, problemAssertionSubjectMissing, "the service account has no assertion subject; recreate it with a valid name or a custom subject")
		return
	}
	entry, created, err := h.serviceAccounts.PutAssertionAudience(r.Context(), repository.AssertionAudience{
		ServiceAccountID: accountID, Audience: request.Audience, MaxLifetimeSeconds: seconds,
	}, h.assertionAudienceAudit(r, accountID, request.Audience, "service_account.assertion_audience.put", seconds)...)
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "service account not found")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "save assertion audience failed")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeNativeMavenJSON(w, status, assertionAudienceResponse(entry))
}

func (h identityManagementHandler) DeleteServiceAccountAssertionAudience(w http.ResponseWriter, r *http.Request, serviceAccountID openapi_types.UUID, params adminopenapi.DeleteServiceAccountAssertionAudienceParams) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	if !validAssertionText(params.Audience) {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "an audience of 1 to 255 printable characters is required")
		return
	}
	accountID := serviceAccountID.String()
	err := h.serviceAccounts.DeleteAssertionAudience(r.Context(), accountID, params.Audience,
		h.assertionAudienceAudit(r, accountID, params.Audience, "service_account.assertion_audience.delete", 0)...)
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "assertion audience not found")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "remove assertion audience failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// assertionAudienceAudit is the audit of a change to an allow-list, written in the transaction of the change.
func (h identityManagementHandler) assertionAudienceAudit(r *http.Request, accountID, audience, operation string, maxLifetimeSeconds int) []repository.AuditRecord {
	records := h.auditRecords(r, "service-accounts/"+accountID, operation, http.StatusOK)
	for i := range records {
		records[i].Class = repository.AuditClassSync
		records[i].Evidence = map[string]string{"serviceAccountId": accountID, "audience": audience}
		if maxLifetimeSeconds > 0 {
			records[i].Evidence["maxLifetimeSeconds"] = strconv.Itoa(maxLifetimeSeconds)
		}
	}
	return records
}
