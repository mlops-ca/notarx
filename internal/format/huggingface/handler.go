package huggingface

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mlops-ca/notarx/internal/access"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/capacity"
	"github.com/mlops-ca/notarx/internal/egress"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/spool"
	"github.com/mlops-ca/notarx/internal/upstreamauth"
)

// Hub error codes (the X-Error-Code header and the code of the JSON body). The first three are
// the codes huggingface_hub maps to its exceptions; the others are the stable problem codes of
// ADR 0027.
const (
	hubRepoNotFound      = "RepoNotFound"
	hubRevisionNotFound  = "RevisionNotFound"
	hubEntryNotFound     = "EntryNotFound"
	codeUnsupportedType  = "unsupported_repo_type"
	codeXetUnsupported   = "xet_not_supported"
	codeNotFound         = "not_found"
	codeMethodNotAllowed = "method_not_allowed"
	codeUnauthorized     = "authentication_required"
	codeAccessDenied     = "access_denied"
	codeQuota            = "quota_exceeded"
	codeTooLarge         = "object_too_large"
	codeBusy             = "fetch_capacity"
	codeInProgress       = "fetch_in_progress"
	codeGroupUnsupported = "group_not_supported"
	codeDigest           = "upstream_digest_mismatch"
	codeInternal         = "internal_error"
	headerStale          = "X-Notarx-Stale"
)

// RepositoryLookup finds the notarx repository a request addresses.
type RepositoryLookup interface {
	GetHostedRepositoryByName(context.Context, string) (repository.HostedRepository, error)
}

// GroupLookup finds a group, so a request for one is answered honestly until group reads exist.
type GroupLookup interface {
	GetHostedGroupByName(context.Context, string) (repository.HostedGroup, error)
}

// AuditRecorder stores audit records.
type AuditRecorder interface {
	RecordAudit(context.Context, repository.AuditRecord) error
}

// AnonymousPolicy supplies the public access policy (ADR 0029).
type AnonymousPolicy interface {
	GetPublicAccessPolicy(context.Context) (repository.PublicAccessPolicy, error)
}

// ProtocolMetrics receives protocol outcomes. Implementations must not block.
type ProtocolMetrics interface {
	Metrics
	// RecordHuggingFaceXetOnly counts a file refused as Xet-only (notarx_hf_xet_only_total).
	RecordHuggingFaceXetOnly()
}

// ReadOptions configures the read protocol (resolve and HEAD) of the module.
type ReadOptions struct {
	Repositories RepositoryLookup
	// Groups is optional; with it a Hugging Face group answers 501 instead of looking absent.
	Groups        GroupLookup
	Authenticator authorization.Authenticator
	Authorizer    authorization.RepositoryAuthorizer
	Audit         AuditRecorder
	Anonymous     AnonymousPolicy
	Config        Config
	// Hooks and Client are the egress hooks and the base HTTP client of upstream requests.
	Hooks  egress.Hooks
	Client *http.Client
	// Locker serializes one object fetch across nodes; the zero value is a no-op.
	Locker  spool.Locker
	Metrics ProtocolMetrics
	// Hub overrides the upstream of a repository (tests); nil builds it from the repository.
	Hub func(repository.HostedRepository) Hub
	// Upstream overrides the byte fetch upstream of a repository (tests).
	Upstream func(repository.HostedRepository) *Upstream
}

type reader struct {
	ReadOptions
	fetcher *Fetcher
	misses  missCache
}

// newReader validates the options of the read protocol.
func newReader(options ReadOptions, store *Store, objects objectstore.Store) (*reader, error) {
	if store.Dialect == MySQL {
		return nil, ErrDialectUnsupported
	}
	if options.Repositories == nil || options.Audit == nil || options.Anonymous == nil || options.Authorizer.Grants == nil {
		return nil, errors.New("huggingface read protocol requires repository, authorization, audit and anonymous-policy ports")
	}
	if objects == nil {
		return nil, errors.New("huggingface read protocol requires an object store")
	}
	if options.Config.MaxObjectBytes == 0 {
		options.Config = DefaultConfig()
	}
	fetcher := NewFetcher(store, objects, options.Config)
	fetcher.Locker = options.Locker
	if options.Metrics != nil {
		fetcher.Metrics = options.Metrics
	}
	return &reader{ReadOptions: options, fetcher: fetcher}, nil
}

// Routes mounts /hf/ when the read protocol is configured.
func (m *Module) Routes() []formatmodule.Route {
	if m.read == nil {
		return nil
	}
	return []formatmodule.Route{{Pattern: "/hf/", Handler: m}}
}

// ServeHTTP serves /hf/{repository}/...: the Hugging Face Hub protocol under a notarx
// repository (HF_ENDPOINT=https://host/hf/{repository}).
func (m *Module) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m.read == nil {
		hubError(w, http.StatusNotFound, codeNotFound, "Not Found")
		return
	}
	m.read.serve(w, r)
}

// route is a parsed request path.
type route struct {
	repository string
	repoType   string
	repoName   string
	rev        string
	path       string
}

// parseRoute splits /hf/{repository}/{[datasets/]org/name}/resolve/{rev}/{path...}. The path is
// split before it is unescaped, so an encoded slash in a revision (refs%2Fpr%2F1) stays one segment.
func parseRoute(escapedPath string) (route, string, bool) {
	rest, ok := strings.CutPrefix(escapedPath, "/hf/")
	if !ok {
		return route{}, codeNotFound, false
	}
	parts := strings.Split(rest, "/")
	segments := make([]string, len(parts))
	for i, part := range parts {
		unescaped, err := url.PathUnescape(part)
		if err != nil || strings.ContainsAny(unescaped, "\x00\r\n") {
			return route{}, codeNotFound, false
		}
		segments[i] = unescaped
	}
	if len(segments) < 2 || segments[0] == "" {
		return route{}, codeNotFound, false
	}
	parsed := route{repository: segments[0], repoType: RepoTypeModel}
	segments = segments[1:]
	switch segments[0] {
	case "api":
		// /api/{models|datasets|spaces}/{id}/...: Spaces are unsupported, Xet tokens are never issued,
		// and the rest of the API belongs to later slices.
		if len(segments) >= 2 && segments[1] == "spaces" {
			return route{}, codeUnsupportedType, false
		}
		for _, segment := range segments {
			if segment == "xet-read-token" || segment == "xet-write-token" {
				return route{}, codeXetUnsupported, false
			}
		}
		return route{}, codeNotFound, false
	case "spaces":
		return route{}, codeUnsupportedType, false
	case "datasets":
		parsed.repoType = RepoTypeDataset
		segments = segments[1:]
	}
	// {org}/{name}/resolve/{rev}/{path...}
	if len(segments) < 5 || segments[2] != "resolve" {
		return route{}, codeNotFound, false
	}
	parsed.repoName = segments[0] + "/" + segments[1]
	parsed.rev = segments[3]
	parsed.path = strings.Join(segments[4:], "/")
	if !ValidRepoName(parsed.repoName) || parsed.rev == "" || !ValidPath(parsed.path) {
		return route{}, codeNotFound, false
	}
	return parsed, "", true
}

func (rd *reader) serve(w http.ResponseWriter, r *http.Request) {
	parsed, code, ok := parseRoute(r.URL.EscapedPath())
	if !ok {
		switch code {
		case codeUnsupportedType:
			hubError(w, http.StatusNotFound, code, "Spaces are not supported")
		case codeXetUnsupported:
			hubError(w, http.StatusNotFound, code, "Xet is not supported; use plain HTTP downloads (HF_HUB_DISABLE_XET=1)")
		default:
			hubError(w, http.StatusNotFound, codeNotFound, "Not Found")
		}
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		hubError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, "Method Not Allowed")
		return
	}
	repo, err := rd.Repositories.GetHostedRepositoryByName(r.Context(), parsed.repository)
	absent := err != nil || repo.Format != repository.FormatHuggingFace || repo.State != repository.RepositoryActive ||
		(repo.Type != repository.RepositoryTypeHosted && repo.Type != repository.RepositoryTypeProxy)
	principal, allowed := rd.authorize(r, repo, parsed, absent)
	if absent && principal.Actor != "" {
		if group, ok := rd.huggingFaceGroup(r.Context(), parsed.repository); ok &&
			rd.Authorizer.AuthorizeResource(r.Context(), principal, group.AsRepository(repository.FormatHuggingFace), authorization.RepositoryRead, parsed.repoType+"/"+parsed.repoName).Allowed {
			// Groups are created and administered, but reading through one is a later slice (ADR 0027).
			// Only a caller who may read the group is told it exists.
			hubError(w, http.StatusNotImplemented, codeGroupUnsupported, "Reading through a Hugging Face group is not implemented yet")
			return
		}
	}
	if !allowed {
		if r.Header.Get("Authorization") == "" || principal.Actor == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="notarx Hugging Face"`)
			hubError(w, http.StatusUnauthorized, codeUnauthorized, "Invalid credentials in Authorization header")
			return
		}
		if absent {
			hubError(w, http.StatusNotFound, hubRepoNotFound, "Repository Not Found")
			return
		}
		rd.audit(r, repo, parsed, principal.Actor, repository.AuditAccessDenied, http.StatusForbidden, 0)
		hubError(w, http.StatusForbidden, codeAccessDenied, "Access denied")
		return
	}
	if absent {
		hubError(w, http.StatusNotFound, hubRepoNotFound, "Repository Not Found")
		return
	}
	rd.resolve(w, r, repo, parsed, principal)
}

// authorize returns the caller and whether it may read. An absent repository denies, so an
// unauthenticated caller cannot tell a private repository from a missing one.
func (rd *reader) authorize(r *http.Request, repo repository.HostedRepository, parsed route, absent bool) (authorization.Principal, bool) {
	principal, authenticated := rd.principal(r)
	if absent {
		return principal, false
	}
	if !authenticated {
		if r.Header.Get("Authorization") == "" && access.AnonymousRepositoryRead(r.Context(), rd.Anonymous, repo, r.Method) {
			return authorization.Principal{Actor: "anonymous"}, true
		}
		return authorization.Principal{}, false
	}
	decision := rd.Authorizer.AuthorizeResource(r.Context(), principal, repo, authorization.RepositoryRead, parsed.repoType+"/"+parsed.repoName)
	return principal, decision.Allowed
}

// principal resolves Authorization: Bearer <token> (the HF_TOKEN of huggingface_hub) and Basic
// with the token as the password, through the one authentication path of the other formats.
func (rd *reader) principal(r *http.Request) (authorization.Principal, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return authorization.Principal{}, false
	}
	header := values[0]
	if _, password, basic := r.BasicAuth(); basic {
		return rd.Authenticator.AuthenticateContext(r.Context(), "Bearer "+password)
	}
	if strings.HasPrefix(header, "Bearer ") {
		return rd.Authenticator.AuthenticateContext(r.Context(), header)
	}
	return authorization.Principal{}, false
}

func (rd *reader) huggingFaceGroup(ctx context.Context, name string) (repository.HostedGroup, bool) {
	if rd.Groups == nil {
		return repository.HostedGroup{}, false
	}
	group, err := rd.Groups.GetHostedGroupByName(ctx, name)
	return group, err == nil && group.Format == repository.FormatHuggingFace
}

func (rd *reader) hubFor(repo repository.HostedRepository) Hub {
	if rd.Hub != nil {
		return rd.Hub(repo)
	}
	return rd.upstreamFor(repo)
}

func (rd *reader) upstreamFor(repo repository.HostedRepository) *Upstream {
	if rd.Upstream != nil {
		return rd.Upstream(repo)
	}
	if repo.Type != repository.RepositoryTypeProxy {
		return nil
	}
	endpoint := repo.Endpoint
	return &Upstream{
		Endpoint:     endpoint,
		Proxy:        repo.EgressProxy,
		Hooks:        rd.Hooks,
		Client:       rd.Client,
		CDNAllowlist: rd.Config.CDNAllowlistFor(endpoint),
		Credential: func(request *http.Request) error {
			return upstreamauth.ApplyOrigin(request, endpoint, repo.UpstreamAuth)
		},
	}
}

// offlineHub is the upstream of a Hosted repository: it has none, so only stored commits exist.
type offlineHub struct{}

func (offlineHub) Revision(context.Context, string, string, string) (Revision, error) {
	return Revision{}, &UpstreamError{Code: CodeUpstreamNotFound, Status: http.StatusNotFound}
}

func (offlineHub) Tree(context.Context, string, string, string, int) ([]File, error) {
	return nil, &UpstreamError{Code: CodeUpstreamNotFound, Status: http.StatusNotFound}
}

func (rd *reader) ingest(repo repository.HostedRepository, store *Store) *Ingest {
	hub := rd.hubFor(repo)
	if hub == nil || isNilHub(hub) {
		hub = offlineHub{}
	}
	return &Ingest{Store: store, Hub: hub, Config: rd.Config}
}

func isNilHub(hub Hub) bool {
	upstream, ok := hub.(*Upstream)
	return ok && upstream == nil
}

// resolve answers GET and HEAD of a file: it resolves the revision to a commit, ingests the
// tree on first use, and answers HEAD from rows (so a cached commit works offline) or streams
// the bytes through the fetcher, always for the commit the headers name.
func (rd *reader) resolve(w http.ResponseWriter, r *http.Request, repo repository.HostedRepository, parsed route, principal authorization.Principal) {
	status, bytes, abort := rd.respond(w, r, repo, parsed, rd.fetcher.Store, r.Context())
	if abort != nil {
		// Bytes are on the wire: the only honest answer is a broken connection. A client that went
		// away is not an upstream failure.
		status = statusClientClosed
		if r.Context().Err() == nil {
			status = http.StatusBadGateway
		}
	}
	rd.audit(r, repo, parsed, principal.Actor, auditOutcome(status), status, bytes)
	if abort != nil {
		panic(http.ErrAbortHandler)
	}
}

// statusClientClosed is the audit status of a transfer the client abandoned (the nginx convention).
const statusClientClosed = 499

// auditOutcome classifies a response status for the audit log.
func auditOutcome(status int) repository.AuditOutcome {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return repository.AuditAccessDenied
	case status == http.StatusNotFound:
		return repository.AuditNotFound
	case status == http.StatusInternalServerError:
		return repository.AuditStorageError
	case status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge || status == http.StatusMethodNotAllowed || status == http.StatusInsufficientStorage:
		// Refused by policy (size limit, quota, method), not an upstream failure.
		return repository.AuditProxyDenied
	case status == statusClientClosed:
		// No outcome enum names an abandoned download; the 499 status in the record tells it apart.
		return repository.AuditResolved
	case status >= http.StatusInternalServerError:
		return repository.AuditUpstreamError
	}
	return repository.AuditResolved
}

// respond writes the response and returns its status, the bytes served and, when a transfer broke
// after bytes were committed, the error that broke it.
func (rd *reader) respond(w http.ResponseWriter, r *http.Request, repo repository.HostedRepository, parsed route, store *Store, ctx context.Context) (int, int64, error) {
	ingest := rd.ingest(repo, store)
	hfRepo, err := store.GetRepo(ctx, repo.ID, parsed.repoType, parsed.repoName)
	if errors.Is(err, ErrNotFound) && repo.Type == repository.RepositoryTypeProxy {
		// No row exists until upstream has confirmed the repo and the revision, so a request for a
		// name that does not exist can neither grow hf_repos nor go upstream again within a minute.
		hfRepo, err = rd.confirmUpstream(ctx, ingest, repo, parsed, store)
	}
	if errors.Is(err, ErrNotFound) {
		return hubError(w, http.StatusNotFound, hubRepoNotFound, "Repository Not Found"), 0, nil
	}
	if err != nil {
		return rd.fail(ctx, w, revisionError(err)), 0, nil
	}
	var resolution Resolution
	if r.Method == http.MethodGet {
		resolution, err = ingest.ResolveForRead(ctx, hfRepo, parsed.rev)
	} else {
		resolution, err = ingest.ResolveRevision(ctx, hfRepo, parsed.rev)
	}
	if err != nil {
		return rd.fail(ctx, w, revisionError(err)), 0, nil
	}
	commit, err := ingest.EnsureTree(ctx, hfRepo, resolution.Commit)
	if err != nil {
		return rd.fail(ctx, w, err), 0, nil
	}
	file, err := store.GetFile(ctx, commit.ID, parsed.path)
	if errors.Is(err, ErrNotFound) {
		return hubError(w, http.StatusNotFound, hubEntryNotFound, "Entry Not Found"), 0, nil
	}
	if err != nil {
		return rd.fail(ctx, w, err), 0, nil
	}
	headers := fileHeaders(commit, file)
	if resolution.Stale {
		headers.Set(headerStale, "true")
	}
	if r.Method == http.MethodHead {
		for name, values := range headers {
			w.Header()[name] = values
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
		w.WriteHeader(http.StatusOK)
		return http.StatusOK, 0, nil
	}
	upstream := rd.upstreamFor(repo)
	object := Object{RepositoryID: repo.ID, HFRepoID: hfRepo.ID, CommitID: commit.ID, Path: file.Path, Size: file.Size,
		Open: func(ctx context.Context) (*http.Response, error) {
			if upstream == nil {
				return nil, &UpstreamError{Code: CodeUpstreamUnavailable, Err: errors.New("repository has no upstream")}
			}
			return upstream.Open(ctx, hfRepo.RepoType, hfRepo.RepoName, commit.SHA, file.Path, nil)
		}}
	written, err := rd.fetcher.Serve(w, r, object, headers)
	if err != nil {
		if written {
			return http.StatusOK, 0, err
		}
		return rd.fail(ctx, w, err), 0, nil
	}
	return http.StatusOK, file.Size, nil
}

// confirmUpstream asks upstream whether the repo and revision exist and only then creates the repo
// row. A miss is remembered for a minute.
func (rd *reader) confirmUpstream(ctx context.Context, ingest *Ingest, repo repository.HostedRepository, parsed route, store *Store) (Repo, error) {
	key := repo.ID + "|" + parsed.repoType + "|" + parsed.repoName + "|" + parsed.rev
	if rd.misses.has(key) {
		return Repo{}, errRevisionNotFound
	}
	revision, err := ingest.Hub.Revision(ctx, parsed.repoType, parsed.repoName, parsed.rev)
	if err != nil {
		var failure *UpstreamError
		if errors.As(err, &failure) && failure.Code == CodeUpstreamNotFound {
			rd.misses.add(key)
		}
		return Repo{}, err
	}
	hfRepo, err := store.EnsureRepo(ctx, repo.ID, parsed.repoType, parsed.repoName)
	if err != nil {
		return Repo{}, err
	}
	// The revision just resolved is stored, so the resolution that follows does not ask upstream again.
	return hfRepo, ingest.Seed(ctx, hfRepo, parsed.rev, revision)
}

// missCache remembers upstream "not found" answers for a short time and a bounded number of keys.
type missCache struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

const (
	missTTL      = time.Minute
	missCapacity = 4096
)

func (c *missCache) has(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	expires, ok := c.entries[key]
	if ok && time.Now().Before(expires) {
		return true
	}
	delete(c.entries, key)
	return false
}

func (c *missCache) add(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= missCapacity {
		c.entries = make(map[string]time.Time, 64)
	}
	c.entries[key] = time.Now().Add(missTTL)
}

// fileHeaders are the headers huggingface_hub reads from the first response: the commit, the
// ETag (the sha256 of an LFS file, the git blob id of a regular one) and, for LFS files, the
// linked ETag and size. Upstream headers, Xet ones included, are never copied.
func fileHeaders(commit Commit, file File) http.Header {
	headers := http.Header{}
	headers.Set("X-Repo-Commit", commit.SHA)
	headers.Set("Content-Type", "application/octet-stream")
	if file.LFS && file.SHA256 != "" {
		headers.Set("ETag", `"`+file.SHA256+`"`)
		headers.Set("X-Linked-Etag", `"`+file.SHA256+`"`)
		headers.Set("X-Linked-Size", strconv.FormatInt(file.Size, 10))
	} else if file.GitBlobID != "" {
		headers.Set("ETag", `"`+file.GitBlobID+`"`)
	}
	return headers
}

// revisionError marks an upstream 404 of the revision lookup as a missing revision.
func revisionError(err error) error {
	var failure *UpstreamError
	if errors.As(err, &failure) && failure.Code == CodeUpstreamNotFound {
		return errRevisionNotFound
	}
	return err
}

var errRevisionNotFound = errors.New("revision not found")

// fail maps an error to the Hub-shaped response and returns the status.
func (rd *reader) fail(ctx context.Context, w http.ResponseWriter, err error) int {
	var upstream *UpstreamError
	switch {
	case errors.Is(err, context.Canceled) && ctx.Err() != nil:
		// The client went away: there is nobody to answer.
		return statusClientClosed
	case errors.Is(err, context.Canceled):
		// Cancelled for a server-side reason (a lost lock lease): the client is still there.
		return hubError(w, http.StatusGatewayTimeout, CodeUpstreamUnavailable, "The fetch was cancelled; retry")
	case errors.Is(err, errRevisionNotFound):
		return hubError(w, http.StatusNotFound, hubRevisionNotFound, "Invalid rev id")
	case errors.Is(err, ErrNotFound):
		return hubError(w, http.StatusNotFound, hubEntryNotFound, "Entry Not Found")
	case errors.Is(err, ErrInvalid):
		return hubError(w, http.StatusBadRequest, "invalid_request", "Invalid request")
	case errors.Is(err, capacity.ErrQuotaExceeded):
		return hubError(w, http.StatusInsufficientStorage, codeQuota, "Repository capacity quota exceeded")
	case errors.Is(err, ErrObjectTooLarge):
		return hubError(w, http.StatusRequestEntityTooLarge, codeTooLarge, "Object exceeds the size limit")
	case errors.Is(err, ErrFetchCapacity):
		w.Header().Set("Retry-After", "5")
		return hubError(w, http.StatusServiceUnavailable, codeBusy, "Too many downloads in progress")
	case errors.Is(err, ErrFetchInProgress):
		w.Header().Set("Retry-After", "5")
		return hubError(w, http.StatusServiceUnavailable, codeInProgress, "The file is being fetched by another node; retry")
	case errors.Is(err, ErrUnverifiable):
		return hubError(w, http.StatusBadGateway, CodeUpstreamInvalid, "Upstream did not name a digest for the file")
	case errors.Is(err, ErrDigestMismatch):
		return hubError(w, http.StatusBadGateway, codeDigest, "Upstream bytes do not match their digest")
	case errors.As(err, &upstream):
		switch upstream.Code {
		case CodeUpstreamNotFound:
			return hubError(w, http.StatusNotFound, hubEntryNotFound, "Entry Not Found")
		case CodeUpstreamGated:
			// Gated models are not bypassed: upstream 401 and 403 are relayed.
			return hubError(w, upstream.Status, CodeUpstreamGated, "Upstream denied access")
		case CodeUpstreamUnavailable:
			return hubError(w, http.StatusGatewayTimeout, CodeUpstreamUnavailable, "Upstream is unavailable and the file was never fetched")
		case CodeXetOnly:
			if rd.Metrics != nil {
				rd.Metrics.RecordHuggingFaceXetOnly()
			}
			return hubError(w, http.StatusBadGateway, CodeXetOnly, "The file is served only through Xet, which is not supported")
		default:
			return hubError(w, http.StatusBadGateway, upstream.Code, "Upstream error")
		}
	default:
		return hubError(w, http.StatusInternalServerError, codeInternal, "Internal error")
	}
}

func (rd *reader) audit(r *http.Request, repo repository.HostedRepository, parsed route, actor string, outcome repository.AuditOutcome, status int, bytes int64) {
	if repo.ID == "" {
		return
	}
	_ = rd.Audit.RecordAudit(context.WithoutCancel(r.Context()), repository.AuditRecord{
		Actor: actor, Repository: repo.Name, GroupName: repo.Name, Format: string(repository.FormatHuggingFace),
		Resource: parsed.repoType + "/" + parsed.repoName + "@" + parsed.rev + "/" + parsed.path, Operation: strings.ToLower(r.Method),
		Outcome: outcome, Status: status, Bytes: bytes, OccurredAt: time.Now().UTC(),
	})
}

// hubError writes the Hub error shape: {"error": ...} with X-Error-Code. It returns the status.
func hubError(w http.ResponseWriter, status int, code, message string) int {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Error-Code", code)
	w.Header().Set("X-Error-Message", message)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	return status
}
