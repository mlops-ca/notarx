package app

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

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
	problemAssertionSubjectHeld        = "assertion_subject_held"
	problemIssuerDisabled              = "issuer_disabled"
	problemSigningUnavailable          = "signing_unavailable"
)

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
	if err := decoder.Decode(&request); err != nil || !repository.ValidAssertionText(request.Audience) {
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
		h.auditRefusedMint(r, principal, accountID, request.Audience, credential, problemAssertionAudienceNotAllowed)
		writeHostedProblem(w, http.StatusForbidden, problemAssertionAudienceNotAllowed, "the audience is not on the assertion allow-list of the service account")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "read assertion allow-list failed")
		return
	}
	limit := min(time.Duration(entry.MaxLifetimeSeconds)*time.Second, h.issuer.Config.MaxTokenLifetime)
	if lifetime > limit {
		h.auditRefusedMint(r, principal, accountID, request.Audience, credential, problemAssertionLifetime)
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

// auditRefusedMint records a mint that was refused for a policy reason, so a credential probing for allowed audiences
// leaves a trail. The request is refused either way, so a failure to write the record is logged, not surfaced.
func (h identityManagementHandler) auditRefusedMint(r *http.Request, principal Principal, accountID, audience, credential, reason string) {
	if h.audit == nil {
		return
	}
	record := managementAudit(repository.AuditRecord{
		Class: repository.AuditClassSync, Outcome: repository.AuditAccessDenied, Actor: principal.Actor, Resource: "service-accounts/" + accountID,
		Operation: "service_account.assertion.mint", Status: http.StatusForbidden, AuthorizationSource: credential, AuthorizationReason: reason,
		Evidence: map[string]string{
			"serviceAccountId": accountID, "audience": audience, "credentialId": principal.CredentialID, "credentialKind": credential, "reason": reason,
		},
	})
	if err := h.audit.RecordAudit(r.Context(), record); err != nil {
		slog.Error("record refused assertion mint", "error", err)
	}
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
	if err := decoder.Decode(&request); err != nil || !repository.ValidAssertionText(request.Audience) {
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
	// An audience is stored as given: Entra and notarx peers match it byte for byte.
	entry, created, err := h.serviceAccounts.PutAssertionAudience(r.Context(), repository.AssertionAudience{
		ServiceAccountID: accountID, Audience: request.Audience, MaxLifetimeSeconds: seconds,
	}, h.assertionAudienceAudit(r, accountID, request.Audience, "service_account.assertion_audience.put", seconds)...)
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "service account not found")
		return
	}
	if errors.Is(err, repository.ErrAssertionSubjectMissing) {
		writeHostedProblem(w, http.StatusConflict, problemAssertionSubjectMissing, "the service account has no assertion subject; create it again with a valid name or a custom subject")
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
	if !repository.ValidAssertionText(params.Audience) {
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

func assertionSubjectResponse(subject repository.AssertionSubject) adminopenapi.AssertionSubject {
	response := adminopenapi.AssertionSubject{Subject: subject.Subject, Orphaned: subject.Orphaned(), CreatedAt: subject.CreatedAt}
	if !subject.Orphaned() {
		id := uuid.MustParse(subject.ServiceAccountID)
		response.ServiceAccountId = &id
	}
	return response
}

// ListAssertionSubjects implements GET /assertion-subjects: every reserved subject, with a flag for the orphaned ones
// that an administrator may release (ADR 0020, "Subject stability").
func (h identityManagementHandler) ListAssertionSubjects(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	subjects, err := h.serviceAccounts.ListAssertionSubjects(r.Context())
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list assertion subjects failed")
		return
	}
	items := make([]adminopenapi.AssertionSubject, 0, len(subjects))
	for _, subject := range subjects {
		items = append(items, assertionSubjectResponse(subject))
	}
	writeNativeMavenJSON(w, http.StatusOK, adminopenapi.AssertionSubjectList{Items: items})
}

// ReleaseAssertionSubject implements DELETE /assertion-subjects/{subject}. Only a reservation without an owner can be
// released, so the Entra trust built on a live account's subject cannot be removed from under it. The release is
// audited in the transaction of the delete.
func (h identityManagementHandler) ReleaseAssertionSubject(w http.ResponseWriter, r *http.Request, subject string) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	if !repository.ValidAssertionText(subject) {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "a subject of 1 to 255 printable characters is required")
		return
	}
	records := h.auditRecords(r, "assertion-subjects", "service_account.assertion_subject.release", http.StatusNoContent)
	for i := range records {
		records[i].Class = repository.AuditClassSync
		records[i].Evidence = map[string]string{"subject": subject}
	}
	err := h.serviceAccounts.ReleaseAssertionSubject(r.Context(), subject, records...)
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "assertion subject not found")
		return
	}
	if errors.Is(err, repository.ErrAssertionSubjectHeld) {
		writeHostedProblem(w, http.StatusConflict, problemAssertionSubjectHeld, "the subject is held by a live service account and cannot be released")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "release assertion subject failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
