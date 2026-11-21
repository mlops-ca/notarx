package app

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/scim"
)

// SCIMOptions configures the SCIM 2.0 server of ADR 0018 section 4.
type SCIMOptions struct {
	// Enabled mounts nothing visible while false: every /scim/v2/ route answers 404 (NOTARX_SCIM_ENABLED).
	Enabled bool
	// MaxPage caps the count of a list request (NOTARX_SCIM_MAX_PAGE).
	MaxPage int
	// PublicURL is NOTARX_PUBLIC_URL; the locations of resources derive from it, or from the request without it.
	PublicURL *url.URL
}

const (
	scimBasePath = "/scim/v2"
	// scimBodyLimit caps one SCIM request body.
	scimBodyLimit = 1 << 20
	// scimMaxAuditResource bounds the resource of an audit record: a request path is client-controlled.
	scimMaxAuditResource = 256
	// scimMaxReturnedMembers bounds the members one Group resource returns; clients that need larger groups
	// exclude the members, as Entra ID does.
	scimMaxReturnedMembers = 10000
	// scimMaxListedMembers bounds the members of each group in a list response, which reads a whole page of groups in
	// one query.
	scimMaxListedMembers = 1000
)

// scimStore is the part of the repository the SCIM server uses.
type scimStore interface {
	repository.SCIMStore
	repository.SCIMTokenStore
	GetIdentityProvider(context.Context, string) (identity.Provider, error)
}

// scimServer is the root-mounted SCIM 2.0 server. It answers every /scim/v2/ route with 404 while disabled and
// authenticates every other request with the bearer token of one provider, which scopes everything it can see.
type scimServer struct {
	options SCIMOptions
	store   scimStore
	audit   repository.AuditRecorder
	// failures rate limits the failed token authentications of one peer address (the limiter of the anonymous
	// routes, ADR 0018 section 1). It is spent on failures only, so a valid token is never throttled and someone
	// guessing tokens cannot lock the identity provider out, even behind a proxy that gives every client one address.
	failures *samlRateLimiter
	started  time.Time
}

func newSCIMServer(options SCIMOptions, store scimStore, audit repository.AuditRecorder) *scimServer {
	if options.MaxPage <= 0 {
		options.MaxPage = config.DefaultSCIMMaxPage
	}
	return &scimServer{options: options, store: store, audit: audit, failures: &samlRateLimiter{}, started: time.Now().UTC()}
}

// scimCaller is the authenticated provider of a request.
type scimCaller struct {
	ProviderID string
	Slug       string
}

func (c scimCaller) actor() string { return "scim:" + c.Slug }

// scimPeer is the address the connection came from. X-Forwarded-For is not used: the caller controls it, so keying
// the limiter on it would let anyone rotate the header for a fresh budget. Behind a reverse proxy the failures of every
// client share the proxy's budget, which only ever throttles failed authentications.
func scimPeer(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *scimServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.options.Enabled {
		http.NotFound(w, r)
		return
	}
	caller, failure := s.authenticate(r)
	if failure != nil {
		if failure.Status == http.StatusUnauthorized {
			if !s.failures.allow(scimPeer(r), time.Now().UTC()) {
				w.Header().Set("Retry-After", strconv.Itoa(int(samlRateWindow.Seconds())))
				scim.NewError(http.StatusTooManyRequests, "", "too many failed authentications; retry later").Write(w)
				return
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="notarx-scim"`)
			s.recordFailure(r, "scim:unauthenticated", failure)
		}
		failure.Write(w)
		return
	}
	s.route(w, r, caller)
}

// authenticate resolves the bearer token. Every credential failure is the same 401, so a caller cannot tell an unknown
// token from an expired, revoked, malformed or disabled-provider one; a store failure is 503 and never counts against
// the peer.
func (s *scimServer) authenticate(r *http.Request) (scimCaller, *scim.Error) {
	unauthorized := scim.NewError(http.StatusUnauthorized, "", "a valid SCIM bearer token is required")
	scheme, token, found := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	token = strings.TrimSpace(token)
	if !found || !strings.EqualFold(scheme, "Bearer") || !scimTokenShape(token) {
		return scimCaller{}, unauthorized
	}
	hash := authorization.HashAPIKey(token)
	unavailable := scim.NewError(http.StatusServiceUnavailable, "", "the SCIM server is temporarily unavailable")
	stored, err := s.store.FindActiveSCIMTokenByHash(r.Context(), hash)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return scimCaller{}, unauthorized
	case err != nil:
		slog.Error("look up the SCIM token failed", "error", err)
		return scimCaller{}, unavailable
	}
	if subtle.ConstantTimeCompare([]byte(stored.SecretHash), []byte(hash)) != 1 {
		return scimCaller{}, unauthorized
	}
	provider, err := s.store.GetIdentityProvider(r.Context(), stored.ProviderID)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return scimCaller{}, unauthorized
	case err != nil:
		slog.Error("load the identity provider of a SCIM token failed", "error", err)
		return scimCaller{}, unavailable
	}
	// A disabled provider provisions nothing: an administrator who switches an identity provider off expects its
	// logins and its provisioning to stop together.
	if !provider.Enabled {
		return scimCaller{}, unauthorized
	}
	return scimCaller{ProviderID: provider.ID, Slug: provider.Slug}, nil
}

// scimTokenShape reports whether a credential has the shape of a token this server issues (the prefix and 256 bits of
// base64url), so that arbitrary strings are refused without a database read.
func scimTokenShape(token string) bool {
	rest, ok := strings.CutPrefix(token, scimTokenPrefix)
	if !ok || len(rest) != base64.RawURLEncoding.EncodedLen(scimTokenBytes) {
		return false
	}
	for _, character := range rest {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_') {
			return false
		}
	}
	return true
}

// route dispatches an authenticated request and returns the status it answered.
func (s *scimServer) route(w http.ResponseWriter, r *http.Request, caller scimCaller) int {
	segments := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, scimBasePath), "/"), "/")
	resource, id := segments[0], ""
	if len(segments) > 2 {
		return s.respond(w, r, caller, "scim.unknown", scim.NewError(http.StatusNotFound, "", "unknown SCIM resource"))
	}
	if len(segments) == 2 {
		id = segments[1]
	}
	switch resource {
	case "ServiceProviderConfig":
		return s.discovery(w, r, caller, resource, id, func(base string) any {
			return scim.ServiceProviderConfig(scim.Capabilities{MaxResults: s.options.MaxPage}, base, s.started)
		})
	case "Schemas":
		return s.discoveryList(w, r, caller, resource, id, func(base string) []map[string]any { return scim.Schemas(base) })
	case "ResourceTypes":
		return s.discoveryList(w, r, caller, resource, id, func(base string) []map[string]any { return scim.ResourceTypes(base) })
	case "Users":
		return s.users(w, r, caller, id)
	case "Groups":
		return s.groups(w, r, caller, id)
	}
	return s.respond(w, r, caller, "scim.unknown", scim.NewError(http.StatusNotFound, "", "unknown SCIM resource"))
}

// base is the SCIM base URL of the locations in responses: absolute from NOTARX_PUBLIC_URL, and only the path without
// it. The request's Host and X-Forwarded-Proto are client-controlled, so they never build an absolute URL: an identity
// provider that follows a location it was handed must not be sent to another host with its bearer token.
func (s *scimServer) base(*http.Request) string {
	if s.options.PublicURL != nil {
		return strings.TrimRight(s.options.PublicURL.String(), "/") + scimBasePath
	}
	return scimBasePath
}

// respond writes an error, audits the call and returns the status.
func (s *scimServer) respond(w http.ResponseWriter, r *http.Request, caller scimCaller, operation string, failure *scim.Error) int {
	failure.Write(w)
	s.record(r, caller, operation, r.URL.Path, failure.Status, nil)
	return failure.Status
}

// writeJSON answers with a SCIM document. audit says whether the call is recorded here: a change whose audit record
// already committed with it in the store transaction is not recorded a second time.
func (s *scimServer) writeJSON(w http.ResponseWriter, r *http.Request, caller scimCaller, operation, resource string, status int, body any, headers map[string]string, audit bool) int {
	encoded, err := json.Marshal(body)
	if err != nil {
		return s.respond(w, r, caller, operation, scim.NewError(http.StatusInternalServerError, "", "the response could not be built"))
	}
	w.Header().Set("Content-Type", scim.ContentType)
	for name, value := range headers {
		w.Header().Set(name, value)
	}
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
	if audit {
		s.record(r, caller, operation, resource, status, nil)
	}
	return status
}

func (s *scimServer) discovery(w http.ResponseWriter, r *http.Request, caller scimCaller, resource, id string, build func(base string) any) int {
	if id != "" {
		return s.respond(w, r, caller, "scim.discovery.read", scim.NewError(http.StatusNotFound, "", "unknown SCIM resource"))
	}
	if r.Method != http.MethodGet {
		return s.methodNotAllowed(w, r, caller, "GET")
	}
	return s.writeJSON(w, r, caller, "scim.discovery.read", "/"+resource, http.StatusOK, build(s.base(r)), nil, true)
}

func (s *scimServer) discoveryList(w http.ResponseWriter, r *http.Request, caller scimCaller, resource, id string, build func(base string) []map[string]any) int {
	if r.Method != http.MethodGet {
		return s.methodNotAllowed(w, r, caller, "GET")
	}
	documents := build(s.base(r))
	if id == "" {
		items := make([]any, 0, len(documents))
		for _, document := range documents {
			items = append(items, document)
		}
		return s.writeJSON(w, r, caller, "scim.discovery.read", "/"+resource, http.StatusOK,
			scim.ListResponse{TotalResults: int64(len(items)), StartIndex: 1, Resources: items}, nil, true)
	}
	for _, document := range documents {
		if document["id"] == id || document["name"] == id {
			return s.writeJSON(w, r, caller, "scim.discovery.read", "/"+resource+"/"+id, http.StatusOK, document, nil, true)
		}
	}
	return s.respond(w, r, caller, "scim.discovery.read", scim.NewError(http.StatusNotFound, "", "unknown SCIM resource"))
}

func (s *scimServer) methodNotAllowed(w http.ResponseWriter, r *http.Request, caller scimCaller, allow string) int {
	w.Header().Set("Allow", allow)
	return s.respond(w, r, caller, "scim.method", scim.NewError(http.StatusMethodNotAllowed, "", "the method is not allowed for this resource"))
}

// readBody reads a JSON request body of a media type SCIM allows.
func (s *scimServer) readBody(w http.ResponseWriter, r *http.Request) ([]byte, *scim.Error) {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != scim.ContentType && mediaType != "application/json" {
		return nil, scim.NewError(http.StatusUnsupportedMediaType, scim.TypeInvalidSyntax, "the content type must be application/scim+json or application/json")
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, scimBodyLimit))
	if err != nil {
		return nil, scim.NewError(http.StatusRequestEntityTooLarge, scim.TypeInvalidSyntax, "the request body is unreadable or too large")
	}
	return body, nil
}

// listParameters are the paging parameters of a list request.
type scimListParameters struct {
	count      int
	startIndex int
	cursor     string
}

func (s *scimServer) listParameters(r *http.Request) (scimListParameters, *scim.Error) {
	query := r.URL.Query()
	parameters := scimListParameters{count: scim.DefaultPageSize, startIndex: 1, cursor: query.Get("cursor")}
	if parameters.count > s.options.MaxPage {
		parameters.count = s.options.MaxPage
	}
	if raw := query.Get("count"); raw != "" {
		count, err := strconv.Atoi(raw)
		if err != nil {
			return parameters, scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "count must be an integer")
		}
		parameters.count = min(max(count, 0), s.options.MaxPage)
	}
	if raw := query.Get("startIndex"); raw != "" {
		index, err := strconv.Atoi(raw)
		if err != nil {
			return parameters, scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "startIndex must be an integer")
		}
		parameters.startIndex = max(index, 1)
	}
	return parameters, nil
}

// scimCursorHop reads the keyset cursor that precedes startIndex without OFFSET: it hops pages of the same keyset
// query, discarding the rows, so the cost is bounded by repository.ListTotalCap rows. A client that needs to page
// further uses the cursor of the response (RFC 9865). ok is false when startIndex is past the last row.
func scimCursorHop(startIndex int, page func(cursor string, limit int) (count int, next string, err error)) (cursor string, ok bool, failure *scim.Error, err error) {
	skip := startIndex - 1
	if skip > repository.ListTotalCap {
		return "", false, scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "startIndex is too large to page by index; continue with the nextCursor of the previous page"), nil
	}
	for skip > 0 {
		step := min(skip, repository.SCIMMaxPageSize)
		count, next, err := page(cursor, step)
		if err != nil {
			return "", false, nil, err
		}
		if count < step || next == "" {
			return "", false, nil, nil
		}
		cursor, skip = next, skip-step
	}
	return cursor, true, nil, nil
}

// storeFailure maps a store error to a SCIM response; anything else is a logged 500.
func (s *scimServer) storeFailure(err error, kind string) *scim.Error {
	switch {
	case errors.Is(err, repository.ErrInvalidPageToken):
		return scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "the cursor is not valid for this request")
	case errors.Is(err, repository.ErrVersionConflict):
		return s.preconditionFailed()
	case errors.Is(err, repository.ErrNotFound):
		return scim.NewError(http.StatusNotFound, "", kind+" not found")
	case errors.Is(err, repository.ErrUserGroupProviderNotFound):
		return scim.NewError(http.StatusNotFound, "", "the identity provider of this token no longer exists")
	case errors.Is(err, repository.ErrSCIMUserNameImmutable):
		return scim.NewError(http.StatusBadRequest, scim.TypeMutability, "userName cannot be changed through SCIM: the account name is the identity its grants and sessions are keyed on")
	case errors.Is(err, repository.ErrSCIMAdminProtected):
		return scim.NewError(http.StatusForbidden, scim.TypeMutability, "SCIM cannot modify a platform administrator")
	case errors.Is(err, repository.ErrSCIMAdminDisabled):
		return scim.NewError(http.StatusConflict, scim.TypeMutability, "the account was disabled by an administrator and cannot be re-activated by SCIM")
	case errors.Is(err, repository.ErrNameExists), errors.Is(err, repository.ErrIdentityExists), errors.Is(err, repository.ErrUserGroupExternalID), errors.Is(err, repository.ErrUserGroupNameExists):
		return scim.NewError(http.StatusConflict, scim.TypeUniqueness, "a "+kind+" with this identifier already exists")
	case errors.Is(err, repository.ErrSCIMInvalid), errors.Is(err, repository.ErrUserGroupInvalid):
		return scim.NewError(http.StatusBadRequest, scim.TypeInvalidValue, "the request names an attribute or member the provider cannot use")
	}
	slog.Error("SCIM store failure", "kind", kind, "error", err)
	return scim.NewError(http.StatusInternalServerError, "", "the request could not be completed")
}

// scimIfMatchVersions reads the If-Match header into the versions the store compares under its row lock: absent or *
// is unconditional (nil); otherwise the weak or strong entity tags, whose opaque value is the version. A header that
// names no usable tag yields a list nothing can match, so the write is refused with 412.
func scimIfMatchVersions(r *http.Request) []string {
	header := strings.TrimSpace(r.Header.Get("If-Match"))
	if header == "" || header == "*" {
		return nil
	}
	versions := []string{}
	for _, candidate := range strings.Split(header, ",") {
		tag := strings.Trim(strings.TrimPrefix(strings.TrimSpace(candidate), "W/"), `"`)
		if tag != "" {
			versions = append(versions, tag)
		}
	}
	if len(versions) == 0 {
		return []string{""}
	}
	return versions
}

// scimVersionMatches reports whether the current version satisfies the If-Match versions. It is the early 412 of a
// request that changes nothing or whose resource is already read; the store's check under the row lock is the
// authoritative one.
func scimVersionMatches(versions []string, version string) bool {
	return len(versions) == 0 || slices.Contains(versions, version)
}

func (s *scimServer) preconditionFailed() *scim.Error {
	return scim.NewError(http.StatusPreconditionFailed, scim.TypeInvalidVers, "If-Match does not match the current version of the resource")
}

// audit writes the record of a call outside the transaction of a change (reads and refusals). Failures of the
// audit of a read are not surfaced; a write carries its audit into the store transaction instead.
func (s *scimServer) record(r *http.Request, caller scimCaller, operation, resource string, status int, evidence map[string]string) {
	if s.audit == nil {
		return
	}
	_ = s.audit.RecordAudit(r.Context(), s.auditRecord(r, caller.actor(), operation, resource, status, evidence))
}

func (s *scimServer) recordFailure(r *http.Request, actor string, failure *scim.Error) {
	if s.audit == nil {
		return
	}
	_ = s.audit.RecordAudit(r.Context(), s.auditRecord(r, actor, "scim.authenticate", r.URL.Path, failure.Status, nil))
}

// writeAudit is the audit record a change hands to the store, so the change and its audit commit together.
func (s *scimServer) writeAudit(r *http.Request, caller scimCaller, operation, resource string, status int, evidence map[string]string) []repository.AuditRecord {
	if s.audit == nil {
		return nil
	}
	return []repository.AuditRecord{s.auditRecord(r, caller.actor(), operation, resource, status, evidence)}
}

func (s *scimServer) auditRecord(r *http.Request, actor, operation, resource string, status int, evidence map[string]string) repository.AuditRecord {
	outcome := repository.AuditResolved
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests:
		outcome = repository.AuditAccessDenied
	case status >= 500:
		outcome = repository.AuditStorageError
	}
	return repository.AuditRecord{
		Class: repository.AuditClassSync, Actor: actor, Outcome: outcome, OccurredAt: time.Now().UTC(), Format: "management",
		Resource: boundedString("scim"+resourcePath(resource), scimMaxAuditResource), Operation: operation, Status: status, CacheDisposition: "bypass",
		AuthorizationSource: "scim", RequestID: r.Header.Get("X-Request-Id"), Evidence: evidence,
	}
}

func resourcePath(resource string) string {
	resource = strings.TrimPrefix(resource, scimBasePath)
	if !strings.HasPrefix(resource, "/") {
		resource = "/" + resource
	}
	return resource
}
