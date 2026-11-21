package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/remoteinstance"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

const (
	remoteInstanceBodyLimit  = 64 << 10
	remoteInstanceCheckLimit = 15 * time.Second
)

// remoteInstanceHandler owns the /remote-instances management resources (ADR 0021, "Remote targets"). Every
// operation is platform-administrator only. The static token is accepted on write, sealed with internal/secrets
// and never returned, logged, audited or published in an event.
type remoteInstanceHandler struct {
	managementScope
	store      repository.RemoteInstanceStore
	discoverer remoteinstance.Discoverer
}

func newRemoteInstanceHandler(scope managementScope, store repository.RemoteInstanceStore, discoverer remoteinstance.Discoverer) remoteInstanceHandler {
	return remoteInstanceHandler{managementScope: scope, store: store, discoverer: discoverer}
}

func (h remoteInstanceHandler) auditRecords(principal Principal, resource, operation string, status int) []repository.AuditRecord {
	if h.audit == nil {
		return nil
	}
	return []repository.AuditRecord{managementAudit(repository.AuditRecord{Actor: principal.Actor, Resource: resource, Operation: operation, Status: status})}
}

func remoteInstanceResponse(value repository.RemoteInstance) adminopenapi.RemoteInstance {
	response := adminopenapi.RemoteInstance{
		Id: uuid.MustParse(value.ID), Name: value.Name, BaseUrl: value.BaseURL, InstanceId: uuid.MustParse(value.InstanceID),
		AuthKind: adminopenapi.RemoteInstanceAuthKind(value.AuthKind), Audience: value.Audience, TokenConfigured: value.TokenSealed != "",
		TrustEvidence: value.TrustEvidence, State: adminopenapi.RemoteInstanceState(value.State), Version: value.Version,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
	if !value.LastCheckedAt.IsZero() {
		response.LastCheckedAt = &value.LastCheckedAt
	}
	return response
}

// remoteInstanceInput is the validated common part of a create and an update request.
type remoteInstanceInput struct {
	name, baseURL, authKind, audience, token string
	trustEvidence                            bool
}

func validateRemoteInstanceInput(name, baseURL string, authKind adminopenapi.RemoteInstanceAuthKind, audience, token *string, trustEvidence *bool) (remoteInstanceInput, error) {
	input := remoteInstanceInput{name: strings.TrimSpace(name), authKind: string(authKind)}
	if input.name == "" || utf8.RuneCountInString(input.name) > 128 || strings.ContainsAny(input.name, "\r\n\x00") {
		return input, errors.New("remote instance name is invalid")
	}
	normalized, err := remoteinstance.NormalizeBaseURL(baseURL)
	if err != nil {
		return input, err
	}
	input.baseURL = normalized
	if !repository.ValidRemoteInstanceAuthKind(input.authKind) {
		return input, errors.New("remote instance authKind is invalid")
	}
	if audience != nil {
		input.audience = strings.TrimSpace(*audience)
	}
	if len(input.audience) > 512 || strings.ContainsAny(input.audience, "\r\n\x00") {
		return input, errors.New("remote instance audience is invalid")
	}
	if token != nil {
		input.token = *token
	}
	if trustEvidence != nil {
		input.trustEvidence = *trustEvidence
	}
	switch input.authKind {
	case repository.RemoteInstanceAuthWorkloadIdentity:
		if input.audience == "" {
			return input, errors.New("audience is required for workload_identity")
		}
		if input.token != "" {
			return input, errors.New("token is not accepted for workload_identity")
		}
	case repository.RemoteInstanceAuthStaticToken:
		if input.token != "" && (len(input.token) < 8 || len(input.token) > 4096 || strings.ContainsAny(input.token, "\r\n\x00")) {
			return input, errors.New("token must be between 8 and 4096 bytes without line breaks")
		}
	}
	return input, nil
}

func decodeRemoteInstanceBody(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, remoteInstanceBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "remote instance payload is invalid")
		return false
	}
	return true
}

// sealRemoteToken seals a static token for the instance id and answers the problem itself on failure.
func sealRemoteToken(w http.ResponseWriter, id, token string) (string, bool) {
	sealed, err := secrets.Seal(remoteinstance.TokenPurpose(id), token)
	if errors.Is(err, secrets.ErrKeyNotConfigured) || errors.Is(err, secrets.ErrInvalidKey) {
		writeHostedProblem(w, http.StatusServiceUnavailable, "encryption_key_unavailable", secrets.KeyEnv+" is required to persist remote instance tokens")
		return "", false
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "encrypt remote instance token failed")
		return "", false
	}
	return sealed, true
}

// checkRemote fetches the instance document of baseURL and rejects this deployment's own id. A failure is
// answered as 422 with the reason (the reason never carries a credential: the request is anonymous).
func (h remoteInstanceHandler) checkRemote(ctx context.Context, w http.ResponseWriter, baseURL string) (remoteinstance.Document, bool) {
	document, reason := h.discover(ctx, baseURL)
	if reason != "" {
		code := "remote_check_failed"
		if reason == errSelfInstance {
			code = "self_instance"
		}
		writeHostedProblem(w, http.StatusUnprocessableEntity, code, reason)
		return remoteinstance.Document{}, false
	}
	return document, true
}

const errSelfInstance = "the remote instance reports this instance's own id"

// discover returns the validated document or a non-empty reason. The reason is bounded and carries no credential.
func (h remoteInstanceHandler) discover(ctx context.Context, baseURL string) (remoteinstance.Document, string) {
	ctx, cancel := context.WithTimeout(ctx, remoteInstanceCheckLimit)
	defer cancel()
	document, err := h.discoverer.Discover(ctx, baseURL)
	if err != nil {
		return remoteinstance.Document{}, boundedReason("remote check failed: " + err.Error())
	}
	local, err := h.store.LocalInstanceID(ctx)
	if err != nil {
		return remoteinstance.Document{}, "read the local instance id failed"
	}
	if document.InstanceID == local {
		return remoteinstance.Document{}, errSelfInstance
	}
	return document, ""
}

func boundedReason(reason string) string {
	if len(reason) > 1024 {
		return reason[:1024]
	}
	return reason
}

func (h remoteInstanceHandler) ListRemoteInstances(w http.ResponseWriter, r *http.Request, params adminopenapi.ListRemoteInstancesParams) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	if !validListLimit(w, params.Limit, 500) {
		return
	}
	query := repository.RemoteInstanceQuery{}
	if params.Limit != nil {
		query.Limit = *params.Limit
	}
	if params.PageToken != nil {
		query.PageToken = *params.PageToken
	}
	page, err := h.store.PageRemoteInstances(r.Context(), query)
	if errors.Is(err, repository.ErrInvalidPageToken) {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "pageToken is not valid for this listing")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "list remote instances failed")
		return
	}
	response := adminopenapi.RemoteInstancePage{Items: make([]adminopenapi.RemoteInstance, 0, len(page.Items)), NextPageToken: optionalString(page.NextPageToken)}
	for _, value := range page.Items {
		response.Items = append(response.Items, remoteInstanceResponse(value))
	}
	writeNativeMavenJSON(w, http.StatusOK, response)
}

func (h remoteInstanceHandler) CreateRemoteInstance(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.authorize(w, r)
	if !ok {
		return
	}
	var request adminopenapi.CreateRemoteInstance
	if !decodeRemoteInstanceBody(w, r, &request) {
		return
	}
	input, err := validateRemoteInstanceInput(request.Name, request.BaseUrl, request.AuthKind, request.Audience, request.Token, request.TrustEvidence)
	if err != nil {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if input.authKind == repository.RemoteInstanceAuthStaticToken && input.token == "" {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "token is required for static_token")
		return
	}
	id := ids.New()
	sealed := ""
	if input.token != "" {
		if sealed, ok = sealRemoteToken(w, id, input.token); !ok {
			return
		}
	}
	document, ok := h.checkRemote(r.Context(), w, input.baseURL)
	if !ok {
		return
	}
	created, err := h.store.CreateRemoteInstance(r.Context(), repository.RemoteInstance{
		ID: id, Name: input.name, BaseURL: input.baseURL, InstanceID: document.InstanceID, AuthKind: input.authKind, Audience: input.audience,
		TokenSealed: sealed, TrustEvidence: input.trustEvidence, State: repository.RemoteInstanceStateActive, LastCheckedAt: time.Now().UTC(),
	}, h.auditRecords(principal, id, "remote_instance.create", http.StatusCreated)...)
	if errors.Is(err, repository.ErrRemoteInstanceNameExists) {
		writeHostedProblem(w, http.StatusConflict, "name_exists", "remote instance name already exists")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "create remote instance failed")
		return
	}
	w.Header().Set("ETag", created.Version)
	writeNativeMavenJSON(w, http.StatusCreated, remoteInstanceResponse(created))
}

func (h remoteInstanceHandler) GetRemoteInstance(w http.ResponseWriter, r *http.Request, remoteInstanceID adminopenapi.RemoteInstanceId) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	value, err := h.store.GetRemoteInstance(r.Context(), remoteInstanceID.String())
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "remote instance not found")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get remote instance failed")
		return
	}
	w.Header().Set("ETag", value.Version)
	writeNativeMavenJSON(w, http.StatusOK, remoteInstanceResponse(value))
}

func (h remoteInstanceHandler) UpdateRemoteInstance(w http.ResponseWriter, r *http.Request, remoteInstanceID adminopenapi.RemoteInstanceId, params adminopenapi.UpdateRemoteInstanceParams) {
	principal, ok := h.authorize(w, r)
	if !ok {
		return
	}
	current, err := h.store.GetRemoteInstance(r.Context(), remoteInstanceID.String())
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "remote instance not found")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get remote instance failed")
		return
	}
	var request adminopenapi.UpdateRemoteInstance
	if !decodeRemoteInstanceBody(w, r, &request) {
		return
	}
	input, err := validateRemoteInstanceInput(request.Name, request.BaseUrl, request.AuthKind, request.Audience, request.Token, request.TrustEvidence)
	if err != nil {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	sealed := ""
	switch {
	case input.authKind != repository.RemoteInstanceAuthStaticToken:
	case input.token != "":
		if sealed, ok = sealRemoteToken(w, current.ID, input.token); !ok {
			return
		}
	case current.TokenSealed != "" && (input.baseURL != current.BaseURL || input.audience != current.Audience):
		// A sealed credential belongs to the host it was issued for; it never follows a changed destination.
		writeHostedProblem(w, http.StatusUnprocessableEntity, "token_required", "a new token is required when baseUrl or audience of a static_token instance changes")
		return
	case current.TokenSealed != "":
		// Leave sealed empty: the store carries the stored token forward inside the write, so a rotation that
		// lands after the read above is never reverted.
	default:
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "token is required for static_token")
		return
	}
	next := repository.RemoteInstance{
		ID: current.ID, Name: input.name, BaseURL: input.baseURL, InstanceID: current.InstanceID, AuthKind: input.authKind, Audience: input.audience,
		TokenSealed: sealed, TrustEvidence: input.trustEvidence, State: repository.RemoteInstanceStateDisabled, LastCheckedAt: current.LastCheckedAt,
	}
	if request.Enabled {
		document, checked := h.checkRemote(r.Context(), w, input.baseURL)
		if !checked {
			return
		}
		if !strings.EqualFold(document.InstanceID, current.InstanceID) && input.authKind == repository.RemoteInstanceAuthStaticToken && input.token == "" {
			// The kept credential was issued for the instance registered, not for the one the host now reports; only a new
			// token in the same request may adopt the new id.
			writeHostedProblem(w, http.StatusUnprocessableEntity, "instance_changed", "the remote instance reports another instance id than the one registered; a new token is required to adopt it")
			return
		}
		next.InstanceID, next.State, next.LastCheckedAt = document.InstanceID, repository.RemoteInstanceStateActive, time.Now().UTC()
	}
	updated, err := h.store.UpdateRemoteInstance(r.Context(), next, string(params.IfMatch), h.auditRecords(principal, current.ID, "remote_instance.update", http.StatusOK)...)
	if errors.Is(err, repository.ErrVersionConflict) {
		writeHostedProblem(w, http.StatusPreconditionFailed, "version_conflict", "If-Match does not match current remote instance version")
		return
	}
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "remote instance not found")
		return
	}
	if errors.Is(err, repository.ErrRemoteInstanceNameExists) {
		writeHostedProblem(w, http.StatusConflict, "name_exists", "remote instance name already exists")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "update remote instance failed")
		return
	}
	w.Header().Set("ETag", updated.Version)
	writeNativeMavenJSON(w, http.StatusOK, remoteInstanceResponse(updated))
}

func (h remoteInstanceHandler) DeleteRemoteInstance(w http.ResponseWriter, r *http.Request, remoteInstanceID adminopenapi.RemoteInstanceId) {
	principal, ok := h.authorize(w, r)
	if !ok {
		return
	}
	err := h.store.DeleteRemoteInstance(r.Context(), remoteInstanceID.String(), h.auditRecords(principal, remoteInstanceID.String(), "remote_instance.delete", http.StatusNoContent)...)
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "remote instance not found")
		return
	}
	if errors.Is(err, repository.ErrRemoteInstanceInUse) {
		writeRemoteInstanceInUse(w, err)
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "delete remote instance failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeRemoteInstanceInUse answers 409 in_use. The body names the promotion rules that still target the instance, so the
// Console can tell the administrator which rules to retarget or delete first.
func writeRemoteInstanceInUse(w http.ResponseWriter, err error) {
	rules := []repository.PromotionRuleRef{}
	var inUse *repository.RemoteInstanceInUseError
	if errors.As(err, &inUse) && inUse.Rules != nil {
		rules = inUse.Rules
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(http.StatusConflict), "status": http.StatusConflict, "code": "in_use",
		"message": "remote instance is still the target of a promotion rule", "requestId": "", "rules": rules})
}

func (h remoteInstanceHandler) TestRemoteInstance(w http.ResponseWriter, r *http.Request, remoteInstanceID adminopenapi.RemoteInstanceId) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	current, err := h.store.GetRemoteInstance(r.Context(), remoteInstanceID.String())
	if errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "remote instance not found")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get remote instance failed")
		return
	}
	checkedAt := time.Now().UTC()
	document, reason := h.discover(r.Context(), current.BaseURL)
	if reason == "" && document.InstanceID != current.InstanceID {
		reason = "the remote instance reports another instance id than the one registered"
	}
	state := current.State
	if state != repository.RemoteInstanceStateDisabled {
		state = repository.RemoteInstanceStateActive
		if reason != "" {
			state = repository.RemoteInstanceStateUnreachable
		}
	}
	if _, err = h.store.RecordRemoteInstanceCheck(r.Context(), current.ID, state, checkedAt, current.Version); errors.Is(err, repository.ErrNotFound) {
		writeHostedProblem(w, http.StatusNotFound, "not_found", "remote instance not found")
		return
	} else if errors.Is(err, repository.ErrVersionConflict) {
		// The instance was edited or disabled while the check ran: report the probe result without recording it.
		if latest, getErr := h.store.GetRemoteInstance(r.Context(), current.ID); getErr == nil {
			state = latest.State
		}
	} else if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "record remote instance check failed")
		return
	}
	response := adminopenapi.RemoteInstanceTestResult{Reachable: reason == "", State: adminopenapi.RemoteInstanceState(state), CheckedAt: checkedAt, Formats: []string{}}
	if reason != "" {
		response.Error = &reason
	} else {
		id := uuid.MustParse(document.InstanceID)
		limits := adminopenapi.InstanceLimits{MaxBlobBytes: document.Limits.MaxBlobBytes, MaxSessionBytes: document.Limits.MaxSessionBytes,
			MaxBlobsPerSession: document.Limits.MaxBlobsPerSession, MaxOpenSessionsPerPrincipal: document.Limits.MaxOpenSessionsPerPrincipal}
		response.InstanceId, response.ApiVersion, response.Formats, response.Limits = &id, &document.APIVersion, document.Formats, &limits
	}
	writeNativeMavenJSON(w, http.StatusOK, response)
}
