package huggingface

import (
	"context"
	"crypto/sha1" //nolint:gosec // git object ids are sha1 by definition
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/egress"
)

// fakeHub is a Hugging Face Hub on a TLS test server. The Hub and its CDN are one server
// (the test certificate is valid for example.com only); the CDN lives under /cdn/.
type fakeHub struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	refs     map[string]string // revision -> commit sha
	commits  map[string]map[string]*hubFile
	down     bool
	pageSize int
	requests map[string]int
	auth     map[string][]string // path prefix -> Authorization values seen
	cdnSlow  chan struct{}       // when set, CDN reads wait on it
	// failAfter makes a CDN body end after n bytes (a truncated upstream).
	failAfter int
}

type hubFile struct {
	Content []byte
	LFS     bool
	// Xet advertises X-Xet-Hash on resolve; XetOnly additionally withholds the redirect.
	Xet, XetOnly bool
	// Corrupt serves different bytes than the digest says.
	Corrupt bool
}

func (f *hubFile) sha256() string {
	sum := sha256.Sum256(f.Content)
	return hex.EncodeToString(sum[:])
}

func (f *hubFile) gitBlob() string {
	h := sha1.New() //nolint:gosec
	fmt.Fprintf(h, "blob %d\x00", len(f.Content))
	h.Write(f.Content)
	return hex.EncodeToString(h.Sum(nil))
}

const hubModel = "acme/widget"

func commitID(n int) string { return fmt.Sprintf("%040x", n) }

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	hub := &fakeHub{t: t, refs: map[string]string{}, commits: map[string]map[string]*hubFile{}, pageSize: 1000, requests: map[string]int{}, auth: map[string][]string{}}
	hub.server = httptest.NewTLSServer(http.HandlerFunc(hub.serve))
	t.Cleanup(hub.server.Close)
	return hub
}

// addCommit registers the files of a commit and points the revision (when not empty) at it.
func (h *fakeHub) addCommit(sha, rev string, files map[string]*hubFile) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.commits[sha] = files
	if rev != "" {
		h.refs[rev] = sha
	}
}

func (h *fakeHub) setRef(rev, sha string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.refs[rev] = sha
}

func (h *fakeHub) setDown(down bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.down = down
}

func (h *fakeHub) count(prefix string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for key, value := range h.requests {
		if strings.HasPrefix(key, prefix) {
			n += value
		}
	}
	return n
}

func (h *fakeHub) authSeen(prefix string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for key, values := range h.auth {
		if strings.HasPrefix(key, prefix) {
			out = append(out, values...)
		}
	}
	return out
}

// upstream returns an Upstream wired to the hub: the dial hook connects to the test
// server whatever address the egress policy resolved.
func (h *fakeHub) upstream() *Upstream {
	pool := x509.NewCertPool()
	pool.AddCert(h.server.Certificate())
	hooks := egress.Hooks{
		LookupIP: func(context.Context, string, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, h.server.Listener.Addr().String())
		},
		ProxyFromEnvironment: func(*http.Request) (*url.URL, error) { return nil, nil },
	}
	return &Upstream{
		Endpoint:     "https://example.com",
		Token:        "upstream-secret",
		Hooks:        hooks,
		Client:       &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}},
		CDNAllowlist: []string{"example.com"},
	}
}

func (h *fakeHub) record(r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	h.requests[key]++
	h.auth[r.URL.Path] = append(h.auth[r.URL.Path], r.Header.Get("Authorization"))
}

func (h *fakeHub) serve(w http.ResponseWriter, r *http.Request) {
	h.record(r)
	h.mu.Lock()
	down := h.down
	h.mu.Unlock()
	if down {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/models/"+hubModel+"/revision/"):
		h.serveRevision(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/models/"+hubModel+"/tree/"):
		h.serveTree(w, r)
	case strings.HasPrefix(r.URL.Path, "/"+hubModel+"/resolve/"):
		h.serveResolve(w, r)
	case strings.HasPrefix(r.URL.Path, "/cdn/"):
		h.serveCDN(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *fakeHub) serveRevision(w http.ResponseWriter, r *http.Request) {
	rev := strings.TrimPrefix(r.URL.Path, "/api/models/"+hubModel+"/revision/")
	h.mu.Lock()
	sha, ok := h.refs[rev]
	if !ok {
		if _, isCommit := h.commits[rev]; isCommit {
			sha, ok = rev, true
		}
	}
	h.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":"Revision Not Found"}`, http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"sha": sha, "lastModified": "2026-01-02T03:04:05.000Z"})
}

func (h *fakeHub) serveTree(w http.ResponseWriter, r *http.Request) {
	sha := strings.TrimPrefix(r.URL.Path, "/api/models/"+hubModel+"/tree/")
	h.mu.Lock()
	files, ok := h.commits[sha]
	page := h.pageSize
	h.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	start, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	end := min(start+page, len(names))
	entries := []map[string]any{{"type": "directory", "oid": commitID(1), "path": "dir"}}
	for _, name := range names[start:end] {
		file := files[name]
		entry := map[string]any{"type": "file", "oid": file.gitBlob(), "size": len(file.Content), "path": name}
		if file.LFS {
			entry["lfs"] = map[string]any{"oid": file.sha256(), "size": len(file.Content), "pointerSize": 134}
		}
		entries = append(entries, entry)
	}
	if end < len(names) {
		w.Header().Set("Link", fmt.Sprintf("<https://example.com%s?recursive=true&expand=false&cursor=%d>; rel=\"next\"", r.URL.Path, end))
	}
	_ = json.NewEncoder(w).Encode(entries)
}

func (h *fakeHub) serveResolve(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/"+hubModel+"/resolve/")
	rev, path, _ := strings.Cut(rest, "/")
	h.mu.Lock()
	sha, ok := h.refs[rev]
	if !ok {
		sha = rev
	}
	file, found := h.commits[sha][path]
	h.mu.Unlock()
	if !found {
		http.Error(w, "missing", http.StatusNotFound)
		return
	}
	w.Header().Set("X-Repo-Commit", sha)
	if file.Xet {
		w.Header().Set("X-Xet-Hash", strings.Repeat("a", 64))
		w.Header().Set("X-Xet-Refresh-Route", "/api/models/"+hubModel+"/xet-read-token/"+sha)
	}
	if file.XetOnly {
		w.Header().Set("ETag", `"`+file.gitBlob()+`"`)
		return
	}
	if file.LFS {
		w.Header().Set("ETag", `"`+file.sha256()+`"`)
		w.Header().Set("X-Linked-Etag", `"`+file.sha256()+`"`)
		w.Header().Set("X-Linked-Size", strconv.Itoa(len(file.Content)))
		w.Header().Set("Location", "https://example.com/cdn/"+sha+"/"+path)
		w.WriteHeader(http.StatusFound)
		return
	}
	w.Header().Set("ETag", `"`+file.gitBlob()+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(file.Content)))
	if r.Method == http.MethodGet {
		body := file.Content
		if file.Corrupt {
			body = append([]byte(nil), body...)
			body[len(body)/2] ^= 0xff
		}
		_, _ = w.Write(body)
	}
}

func (h *fakeHub) serveCDN(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/cdn/")
	sha, path, _ := strings.Cut(rest, "/")
	h.mu.Lock()
	file := h.commits[sha][path]
	slow := h.cdnSlow
	failAfter := h.failAfter
	h.mu.Unlock()
	if file == nil {
		http.NotFound(w, r)
		return
	}
	body := file.Content
	if file.Corrupt {
		body = append([]byte(nil), body...)
		body[len(body)/2] ^= 0xff
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Content-Type", "application/octet-stream")
	if r.Method == http.MethodHead {
		return
	}
	if slow != nil {
		half := len(body) / 2
		_, _ = w.Write(body[:half])
		w.(http.Flusher).Flush()
		<-slow
		body = body[half:]
	}
	if failAfter > 0 && failAfter < len(body) {
		_, _ = w.Write(body[:failAfter])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	_, _ = w.Write(body)
}
