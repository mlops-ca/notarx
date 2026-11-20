package huggingface

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/egress"
	"github.com/mlops-ca/notarx/internal/repository"
)

// Stable problem codes of upstream failures (ADR 0027).
const (
	CodeUpstreamNotFound     = "upstream_not_found"
	CodeUpstreamGated        = "upstream_gated"
	CodeUpstreamUnavailable  = "upstream_unavailable"
	CodeUpstreamInvalid      = "upstream_invalid_response"
	CodeTreeTooLarge         = "upstream_tree_too_large"
	CodePathTooLong          = "upstream_path_too_long"
	CodeXetOnly              = "upstream_xet_only"
	CodeRedirectRejected     = "upstream_redirect_rejected"
	maxAPIResponseBytes      = 64 << 20
	maxTreePages             = 10000
	defaultUpstreamTimeout   = 60 * time.Second
	upstreamUserAgent        = "notarx-huggingface-proxy"
	headerRepoCommit         = "X-Repo-Commit"
	headerLinkedETag         = "X-Linked-Etag"
	headerLinkedSize         = "X-Linked-Size"
	headerXetHash            = "X-Xet-Hash"
	headerXetLink            = "X-Xet-Refresh-Route"
	treeEntryTypeFile        = "file"
	treeEntryTypeDirectory   = "directory"
	upstreamErrorBodyPreview = 256
)

// UpstreamError is a failure of the upstream Hub with a stable problem code and, when
// upstream answered, its HTTP status. Gated models are not bypassed: a 401 or 403 is
// relayed.
type UpstreamError struct {
	Code   string
	Status int
	Err    error
}

func (e *UpstreamError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Code, e.Err)
	}
	return e.Code
}

func (e *UpstreamError) Unwrap() error { return e.Err }

// Errors callers match with errors.Is.
var (
	// ErrTreeTooLarge means the tree of a commit has more files than allowed.
	ErrTreeTooLarge = &UpstreamError{Code: CodeTreeTooLarge}
	// ErrXetOnly means the file is served only through the Xet protocol, which notarx does not
	// implement.
	ErrXetOnly = &UpstreamError{Code: CodeXetOnly}
)

// Is makes every UpstreamError with the same code equal to its sentinel.
func (e *UpstreamError) Is(target error) bool {
	t, ok := target.(*UpstreamError)
	return ok && t.Code == e.Code && (t.Status == 0 || t.Status == e.Status)
}

func upstreamStatusError(status int) *UpstreamError {
	switch {
	case status == http.StatusNotFound || status == http.StatusGone:
		return &UpstreamError{Code: CodeUpstreamNotFound, Status: http.StatusNotFound}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &UpstreamError{Code: CodeUpstreamGated, Status: status}
	default:
		return &UpstreamError{Code: CodeUpstreamUnavailable, Status: status}
	}
}

// Upstream talks to a Hugging Face Hub (or a private mirror). It performs the metadata
// requests of the proxy and the byte fetch; redirects are never followed by the client
// itself: the single permitted hop goes through egress.FollowOnce.
type Upstream struct {
	// Endpoint is the https origin of the Hub.
	Endpoint string
	// Credential applies the upstream credential to a request to Endpoint; nil sends none. The
	// credential is never sent to a redirect target: the hop request is built without it.
	Credential func(*http.Request) error
	// Proxy is the repository egress configuration; nil keeps the environment default.
	Proxy *repository.EgressProxy
	Hooks egress.Hooks
	// Client is the base HTTP client (timeouts, TLS roots); nil uses a default.
	Client *http.Client
	// CDNAllowlist lists the hosts the single redirect may reach.
	CDNAllowlist []string
}

// Revision is a ref resolved to a commit.
type Revision struct {
	SHA          string
	LastModified time.Time
}

// FileMeta is what the first upstream response of a resolve request tells about a file.
type FileMeta struct {
	Status     int
	Commit     string
	ETag       string
	LinkedETag string
	LinkedSize int64
	Size       int64
	Location   string
	// XetOnly is true when upstream advertises a Xet hash but offers no plain HTTP route
	// to the bytes.
	XetOnly bool
}

func (u *Upstream) base() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return &http.Client{Timeout: defaultUpstreamTimeout}
}

func (u *Upstream) client(ctx context.Context) (*http.Client, error) {
	client, err := egress.ApplyContext(ctx, u.base(), u.Proxy, u.Endpoint, u.Hooks)
	if err != nil {
		return nil, &UpstreamError{Code: CodeUpstreamUnavailable, Err: err}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client, nil
}

func repoTypeSegment(repoType string) (string, bool) {
	switch repoType {
	case RepoTypeModel:
		return "models", true
	case RepoTypeDataset:
		return "datasets", true
	}
	return "", false
}

// apiPath is /api/{models|datasets}/{org}/{name}/{suffix...}.
func apiPath(repoType, repoName, suffix string) (string, error) {
	segment, ok := repoTypeSegment(repoType)
	if !ok || !ValidRepoName(repoName) {
		return "", ErrInvalid
	}
	return "/api/" + segment + "/" + repoName + suffix, nil
}

// resolvePath is the Hub path of a file: models live at the root, datasets under /datasets.
func resolvePath(repoType, repoName, rev, path string) (string, error) {
	if !ValidRepoName(repoName) || !ValidPath(path) || rev == "" || strings.ContainsAny(rev, "?#\\") {
		return "", ErrInvalid
	}
	prefix := ""
	switch repoType {
	case RepoTypeModel:
	case RepoTypeDataset:
		prefix = "/datasets"
	default:
		return "", ErrInvalid
	}
	escaped := make([]string, 0, strings.Count(path, "/")+1)
	for _, part := range strings.Split(path, "/") {
		escaped = append(escaped, url.PathEscape(part))
	}
	return prefix + "/" + repoName + "/resolve/" + url.PathEscape(rev) + "/" + strings.Join(escaped, "/"), nil
}

func (u *Upstream) newRequest(ctx context.Context, method, rawURL string, header http.Header) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, &UpstreamError{Code: CodeUpstreamInvalid, Err: err}
	}
	for name, values := range header {
		request.Header[name] = append([]string(nil), values...)
	}
	request.Header.Set("User-Agent", upstreamUserAgent)
	if u.Credential != nil {
		if err := u.Credential(request); err != nil {
			return nil, &UpstreamError{Code: CodeUpstreamInvalid, Err: fmt.Errorf("apply upstream credential: %w", err)}
		}
	}
	return request, nil
}

func (u *Upstream) endpointURL(path string) string {
	return strings.TrimRight(u.Endpoint, "/") + path
}

// getJSON performs a GET and decodes a bounded JSON body. It returns the response headers
// for pagination.
func (u *Upstream) getJSON(ctx context.Context, rawURL string, out any) (http.Header, error) {
	client, err := u.client(ctx)
	if err != nil {
		return nil, err
	}
	request, err := u.newRequest(ctx, http.MethodGet, rawURL, http.Header{"Accept": {"application/json"}})
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, &UpstreamError{Code: CodeUpstreamUnavailable, Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, upstreamErrorBodyPreview))
		return nil, upstreamStatusError(response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseBytes+1))
	if err != nil {
		return nil, &UpstreamError{Code: CodeUpstreamUnavailable, Err: err}
	}
	if len(body) > maxAPIResponseBytes {
		return nil, &UpstreamError{Code: CodeUpstreamInvalid, Err: errors.New("response too large")}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, &UpstreamError{Code: CodeUpstreamInvalid, Err: err}
	}
	return response.Header, nil
}

// Revision resolves a branch, tag or commit sha to a commit.
func (u *Upstream) Revision(ctx context.Context, repoType, repoName, rev string) (Revision, error) {
	if rev == "" || strings.ContainsAny(rev, "\\") {
		return Revision{}, ErrInvalid
	}
	path, err := apiPath(repoType, repoName, "/revision/"+url.PathEscape(rev))
	if err != nil {
		return Revision{}, err
	}
	var payload struct {
		SHA          string `json:"sha"`
		LastModified string `json:"lastModified"`
	}
	if _, err := u.getJSON(ctx, u.endpointURL(path), &payload); err != nil {
		return Revision{}, err
	}
	if !ValidCommit(payload.SHA) {
		return Revision{}, &UpstreamError{Code: CodeUpstreamInvalid, Err: errors.New("revision has no commit sha")}
	}
	revision := Revision{SHA: payload.SHA}
	if parsed, err := time.Parse(time.RFC3339, payload.LastModified); err == nil {
		revision.LastModified = parsed.UTC()
	}
	return revision, nil
}

type treeEntry struct {
	Type string `json:"type"`
	Oid  string `json:"oid"`
	Size int64  `json:"size"`
	Path string `json:"path"`
	LFS  *struct {
		Oid  string `json:"oid"`
		Size int64  `json:"size"`
	} `json:"lfs"`
}

// Tree lists every file of a commit, following the Link rel="next" pagination. A tree with
// more than maxFiles files is refused with ErrTreeTooLarge before the rest is read.
func (u *Upstream) Tree(ctx context.Context, repoType, repoName, sha string, maxFiles int) ([]File, error) {
	if !ValidCommit(sha) {
		return nil, ErrInvalid
	}
	path, err := apiPath(repoType, repoName, "/tree/"+sha)
	if err != nil {
		return nil, err
	}
	origin, err := url.Parse(u.Endpoint)
	if err != nil {
		return nil, &UpstreamError{Code: CodeUpstreamInvalid, Err: err}
	}
	next := u.endpointURL(path) + "?recursive=true&expand=false"
	var files []File
	for page := 0; next != ""; page++ {
		if page >= maxTreePages {
			return nil, ErrTreeTooLarge
		}
		var entries []treeEntry
		header, err := u.getJSON(ctx, next, &entries)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Type == treeEntryTypeDirectory {
				continue
			}
			if entry.Type != treeEntryTypeFile {
				return nil, &UpstreamError{Code: CodeUpstreamInvalid, Err: fmt.Errorf("unknown tree entry type %q", entry.Type)}
			}
			if len([]rune(entry.Path)) > MaxPathLength {
				return nil, &UpstreamError{Code: CodePathTooLong, Err: ErrPathTooLong}
			}
			if !ValidPath(entry.Path) || entry.Size < 0 {
				return nil, &UpstreamError{Code: CodeUpstreamInvalid, Err: fmt.Errorf("invalid tree path %q", entry.Path)}
			}
			file := File{Path: entry.Path, Size: entry.Size}
			if ValidCommit(entry.Oid) {
				file.GitBlobID = entry.Oid
			}
			if entry.LFS != nil {
				if !ValidDigest(entry.LFS.Oid) || entry.LFS.Size < 0 {
					return nil, &UpstreamError{Code: CodeUpstreamInvalid, Err: fmt.Errorf("invalid lfs pointer for %q", entry.Path)}
				}
				file.LFS, file.SHA256, file.Size = true, entry.LFS.Oid, entry.LFS.Size
			}
			files = append(files, file)
			if len(files) > maxFiles {
				return nil, ErrTreeTooLarge
			}
		}
		next, err = nextLink(header, origin)
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// nextLink returns the rel="next" URL of a Link header, or "". The target must stay on the
// upstream origin: the request carries the upstream credential.
func nextLink(header http.Header, origin *url.URL) (string, error) {
	for _, value := range header.Values("Link") {
		for _, part := range strings.Split(value, ",") {
			target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
			if !ok || !strings.Contains(strings.ReplaceAll(params, " ", ""), `rel="next"`) {
				continue
			}
			target = strings.Trim(strings.TrimSpace(target), "<>")
			resolved, err := origin.Parse(target)
			if err != nil || resolved.Scheme != origin.Scheme || !strings.EqualFold(resolved.Host, origin.Host) {
				return "", &UpstreamError{Code: CodeUpstreamInvalid, Err: errors.New("pagination link leaves the upstream origin")}
			}
			return resolved.String(), nil
		}
	}
	return "", nil
}

// Head performs the HEAD request of the proxy without following redirects and captures the
// metadata of the first response: commit, ETag, linked ETag and size, and the location of
// the bytes. The Xet headers are read to detect a Xet-only file and are never passed on.
func (u *Upstream) Head(ctx context.Context, repoType, repoName, rev, filePath string) (FileMeta, error) {
	path, err := resolvePath(repoType, repoName, rev, filePath)
	if err != nil {
		return FileMeta{}, err
	}
	client, err := u.client(ctx)
	if err != nil {
		return FileMeta{}, err
	}
	request, err := u.newRequest(ctx, http.MethodHead, u.endpointURL(path), nil)
	if err != nil {
		return FileMeta{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return FileMeta{}, &UpstreamError{Code: CodeUpstreamUnavailable, Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return FileMeta{}, upstreamStatusError(response.StatusCode)
	}
	meta := FileMeta{
		Status:     response.StatusCode,
		Commit:     response.Header.Get(headerRepoCommit),
		ETag:       strings.Trim(response.Header.Get("ETag"), `"`),
		LinkedETag: strings.Trim(response.Header.Get(headerLinkedETag), `"`),
		Location:   response.Header.Get("Location"),
		Size:       response.ContentLength,
	}
	if value := response.Header.Get(headerLinkedSize); value != "" {
		if _, err := fmt.Sscanf(value, "%d", &meta.LinkedSize); err != nil || meta.LinkedSize < 0 {
			return FileMeta{}, &UpstreamError{Code: CodeUpstreamInvalid, Err: errors.New("invalid X-Linked-Size")}
		}
	}
	if meta.Commit != "" && !ValidCommit(meta.Commit) {
		return FileMeta{}, &UpstreamError{Code: CodeUpstreamInvalid, Err: errors.New("invalid X-Repo-Commit")}
	}
	meta.XetOnly = (response.Header.Get(headerXetHash) != "" || response.Header.Get(headerXetLink) != "") && meta.Location == ""
	return meta, nil
}

// Open starts the byte fetch of a file at a commit. A redirect is followed exactly once, to
// an allowlisted CDN host, without the upstream credential; a second redirect is
// ErrRedirectRejected. The caller closes the body of the returned response.
func (u *Upstream) Open(ctx context.Context, repoType, repoName, rev, filePath string, header http.Header) (*http.Response, error) {
	path, err := resolvePath(repoType, repoName, rev, filePath)
	if err != nil {
		return nil, err
	}
	client, err := u.client(ctx)
	if err != nil {
		return nil, err
	}
	// Reading a multi-gigabyte body must not hit the overall client timeout; the caller bounds the
	// fetch with its context.
	client.Timeout = 0
	request, err := u.newRequest(ctx, http.MethodGet, u.endpointURL(path), header)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, &UpstreamError{Code: CodeUpstreamUnavailable, Err: err}
	}
	xet := response.Header.Get(headerXetHash) != "" || response.Header.Get(headerXetLink) != ""
	if xet && !egress.IsRedirect(response.StatusCode) {
		// A Xet hash without a plain HTTP route to the bytes: the file is unsupported (ADR 0027).
		_ = response.Body.Close()
		return nil, &UpstreamError{Code: CodeXetOnly, Status: response.StatusCode}
	}
	if egress.IsRedirect(response.StatusCode) {
		hopClient := *u.base()
		hopClient.Timeout = 0
		follower := egress.FollowOnce(u.CDNAllowlist, u.Proxy, u.Hooks, &hopClient)
		response, err = follower.Follow(ctx, request, response)
		if err != nil {
			if errors.Is(err, egress.ErrRedirectRejected) {
				return nil, &UpstreamError{Code: CodeRedirectRejected, Err: err}
			}
			return nil, &UpstreamError{Code: CodeUpstreamUnavailable, Err: err}
		}
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		_ = response.Body.Close()
		if xet && response.StatusCode >= 400 {
			// The Git LFS bridge stopped serving a Xet-backed file.
			return nil, &UpstreamError{Code: CodeXetOnly, Status: response.StatusCode}
		}
		return nil, upstreamStatusError(response.StatusCode)
	}
	return response, nil
}
