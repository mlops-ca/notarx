package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/rubygemspublication"
)

// The conformance harness (ADR 0029): every format in docs/protocol-compatibility.md
// is driven through the real gateway with anonymous and authenticated callers, a
// private, an authenticated and a public repository, the global gate on and off,
// and a group over them. The access decision itself is exhaustively tabled in
// internal/access; this harness proves each protocol handler routes through it and
// shapes a denial identically for a private name and an absent one.

type conformanceFormat struct {
	name   repository.Format
	path   string // anonymous read path; %s is the repository or group name
	proxy  bool   // the format only serves Proxy repositories here
	groups bool   // V2 groups resolve for this format
}

var conformanceFormats = []conformanceFormat{
	{name: repository.FormatOCI, path: "/v2/%s/app/manifests/latest", groups: true},
	{name: repository.FormatRaw, path: "/raw/%s/a.txt", groups: true},
	{name: repository.FormatMaven, path: "/maven/%s/org/example/w/1.0/w-1.0.pom", groups: true},
	{name: repository.FormatConan, path: "/conan/%s/v2/conans/pkg/1.0/user/channel/latest", groups: true},
	{name: repository.FormatNPM, path: "/npm/%s/left-pad", groups: true},
	{name: repository.FormatPyPI, path: "/pypi/%s/simple/left-pad/", groups: true},
	{name: repository.FormatGo, path: "/go/%s/example.com/m/@v/list", groups: true},
	{name: repository.FormatCargo, path: "/cargo/%s/config.json", groups: true},
	{name: repository.FormatAPT, path: "/apt/%s/dists/stable/InRelease", proxy: true, groups: true},
	{name: repository.FormatNuGet, path: "/nuget/%s/v3/index.json"},
	{name: repository.FormatHelm, path: "/helm/%s/index.yaml"},
	{name: repository.FormatRubyGems, path: "/rubygems/%s/versions"},
}

func conformanceRepository(format conformanceFormat, name string, visibility repository.Visibility) repository.HostedRepository {
	repo := repository.HostedRepository{ID: uuid.NewString(), Name: name, Format: format.name, Visibility: visibility}
	if format.proxy {
		repo.Type = repository.RepositoryTypeProxy
		repo.Endpoint = "https://upstream.example"
		repo.AllowedHosts = []string{"upstream.example"}
	}
	return repo
}

// shape is everything a client can observe about a response except values that
// differ per request.
func shape(response *httptest.ResponseRecorder) string {
	headers := make([]string, 0, len(response.Header()))
	for key, values := range response.Header() {
		switch strings.ToLower(key) {
		case "date", "x-request-id", "x-trace-id":
			continue
		}
		headers = append(headers, key+"="+strings.Join(values, ","))
	}
	sort.Strings(headers)
	return response.Result().Status + "|" + strings.Join(headers, ";") + "|" + response.Body.String()
}

// conformanceGateway composes the gateway for a format. The NuGet, Helm and
// RubyGems preview protocols are separate format modules that only mount when
// enabled.
func conformanceGateway(t *testing.T, store *repository.MemoryStore, format repository.Format) http.Handler {
	t.Helper()
	options := GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()}
	switch format {
	case repository.FormatNuGet:
		options.NuGetPreviewEnabled, options.NuGetStore, options.NuGetObjects, options.NuGetSources = true, store, objectstore.NewMemoryStore(), store
	case repository.FormatHelm:
		options.HelmPreviewEnabled, options.HelmStore, options.HelmGovernance, options.HelmSources, options.HelmObjects = true, store, store, store, objectstore.NewMemoryStore()
	case repository.FormatRubyGems:
		options.RubyGemsPreviewEnabled, options.RubyGemsStore, options.RubyGemsObjects = true, rubygemspublication.NewMemory(store, store), objectstore.NewMemoryStore()
	}
	return newTestGateway(t, options)
}

func anonymousGet(handler http.Handler, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	return response
}

func TestAccessConformanceDenialShapingAndGate(t *testing.T) {
	for _, format := range conformanceFormats {
		t.Run(string(format.name), func(t *testing.T) {
			ctx := context.Background()
			store := repository.NewMemoryStore()
			for name, visibility := range map[string]repository.Visibility{"private-feed": repository.VisibilityPrivate, "public-feed": repository.VisibilityPublic} {
				if _, err := store.CreateHostedRepository(ctx, conformanceRepository(format, name, visibility)); err != nil {
					t.Fatal(err)
				}
			}
			handler := conformanceGateway(t, store, format.name)
			path := func(name string) string { return strings.Replace(format.path, "%s", name, 1) }

			// Gate closed: a public feed answers like a private one and like a missing one.
			closedPublic, closedPrivate, absent := anonymousGet(handler, path("public-feed")), anonymousGet(handler, path("private-feed")), anonymousGet(handler, path("absent-feed"))
			if closedPublic.Code != http.StatusUnauthorized && closedPublic.Code != http.StatusNotFound {
				t.Fatalf("gate closed: public feed answered %d, want a denial", closedPublic.Code)
			}
			if shape(closedPrivate) != shape(absent) {
				t.Fatalf("private and absent names answer differently\nprivate: %s\nabsent:  %s", shape(closedPrivate), shape(absent))
			}
			if shape(closedPublic) != shape(absent) {
				t.Fatalf("a public feed behind a closed gate answers differently from an absent one\npublic: %s\nabsent: %s", shape(closedPublic), shape(absent))
			}

			enableAnonymousAccess(t, store)
			openPrivate, openAbsent, openPublic := anonymousGet(handler, path("private-feed")), anonymousGet(handler, path("absent-feed")), anonymousGet(handler, path("public-feed"))
			if shape(openPrivate) != shape(openAbsent) {
				t.Fatalf("gate open: private and absent names answer differently\nprivate: %s\nabsent:  %s", shape(openPrivate), shape(openAbsent))
			}
			if openPublic.Code == http.StatusUnauthorized || openPublic.Code == http.StatusForbidden {
				t.Fatalf("gate open: public feed denied anonymous read with %d %s", openPublic.Code, openPublic.Body.String())
			}
			// Anonymous callers never write.
			write := httptest.NewRecorder()
			handler.ServeHTTP(write, httptest.NewRequest(http.MethodPut, path("public-feed"), strings.NewReader("x")))
			if write.Code < 400 || write.Code == http.StatusCreated || write.Code == http.StatusOK {
				t.Fatalf("anonymous write to a public feed answered %d", write.Code)
			}
		})
	}
}

func TestAccessConformanceAuthenticatedTier(t *testing.T) {
	for _, format := range conformanceFormats {
		t.Run(string(format.name), func(t *testing.T) {
			ctx := context.Background()
			store := repository.NewMemoryStore()
			for name, visibility := range map[string]repository.Visibility{"authenticated-feed": repository.VisibilityAuthenticated, "private-feed": repository.VisibilityPrivate} {
				repo, err := store.CreateHostedRepository(ctx, conformanceRepository(format, name, visibility))
				if err != nil {
					t.Fatal(err)
				}
				// A managed grant set pins the repository to explicit grants, so an
				// identity without one can read it only through its visibility.
				if _, err := store.ReplaceRepositoryGrants(ctx, repo.ID, []repository.RepositoryGrant{{Principal: "owner", Scopes: []string{"repositories:read"}}}, "1"); err != nil {
					t.Fatal(err)
				}
			}
			enableAnonymousAccess(t, store)
			authenticator := testAuthenticator()
			handler := conformanceGateway(t, store, format.name)
			read := func(name string) *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodGet, strings.Replace(format.path, "%s", name, 1), nil)
				authorize(request, authenticator.IssueToken("stranger"))
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				return response
			}
			if response := read("authenticated-feed"); response.Code == http.StatusUnauthorized || response.Code == http.StatusForbidden {
				t.Fatalf("an authenticated identity was denied an authenticated-tier feed: %d %s", response.Code, response.Body.String())
			}
			if response := read("private-feed"); response.Code != http.StatusUnauthorized && response.Code != http.StatusForbidden && response.Code != http.StatusNotFound {
				t.Fatalf("an identity without a grant read a private feed: %d %s", response.Code, response.Body.String())
			}
			if anonymous := anonymousGet(handler, strings.Replace(format.path, "%s", "authenticated-feed", 1)); anonymous.Code != http.StatusUnauthorized && anonymous.Code != http.StatusNotFound {
				t.Fatalf("an authenticated-tier feed answered an anonymous caller with %d", anonymous.Code)
			}
		})
	}
}

func TestAccessConformanceGroups(t *testing.T) {
	for _, format := range conformanceFormats {
		if !format.groups {
			continue
		}
		t.Run(string(format.name), func(t *testing.T) {
			ctx := context.Background()
			store := repository.NewMemoryStore()
			publicRepo, err := store.CreateHostedRepository(ctx, conformanceRepository(format, "public-feed", repository.VisibilityPublic))
			if err != nil {
				t.Fatal(err)
			}
			privateRepo, err := store.CreateHostedRepository(ctx, conformanceRepository(format, "private-feed", repository.VisibilityPrivate))
			if err != nil {
				t.Fatal(err)
			}
			create := func(name string, visibility repository.Visibility, members ...repository.HostedRepository) {
				group := repository.HostedGroup{ID: uuid.NewString(), Name: name, Format: format.name, Visibility: visibility}
				for position, member := range members {
					group.Members = append(group.Members, repository.GroupMember{RepositoryID: member.ID, Position: position})
				}
				if _, _, err := store.CreateHostedGroupIdempotently(ctx, group, "admin", name, name); err != nil {
					t.Fatal(err)
				}
			}
			create("public-group", repository.VisibilityPublic, publicRepo)
			create("private-group", repository.VisibilityPrivate, publicRepo)
			create("mixed-group", repository.VisibilityPublic, privateRepo)
			handler := conformanceGateway(t, store, format.name)
			path := func(name string) string { return strings.Replace(format.path, "%s", name, 1) }

			absent := anonymousGet(handler, path("absent-group"))
			enableAnonymousAccess(t, store)
			if private, absentOpen := anonymousGet(handler, path("private-group")), anonymousGet(handler, path("absent-group")); shape(private) != shape(absentOpen) {
				t.Fatalf("a private group and an absent one answer differently\nprivate: %s\nabsent:  %s", shape(private), shape(absentOpen))
			}
			if response := anonymousGet(handler, path("public-group")); response.Code == http.StatusUnauthorized || response.Code == http.StatusForbidden {
				t.Fatalf("a public group over a public member denied anonymous read: %d %s", response.Code, response.Body.String())
			}
			// The group is a ceiling and members are filtered before any lookup: a
			// public group over only a private member exposes nothing and answers a
			// denial, never a member-specific response.
			if response := anonymousGet(handler, path("mixed-group")); response.Code != http.StatusUnauthorized && response.Code != http.StatusNotFound {
				t.Fatalf("a public group over a private member answered %d %s", response.Code, response.Body.String())
			}
			_ = absent
		})
	}
}

// TestAccessConformanceOCIAnonymousPullFlows drives the two anonymous pull paths of
// ADR 0029 with the request sequences of Docker, containerd, Podman and ORAS: the
// standard /v2/ challenge with an anonymous token, and a direct unauthenticated
// GET or HEAD. (The clients' own binaries run in the registry e2e; this proves the
// gateway side of both sequences, which is the only thing that differs per client.)
func TestAccessConformanceOCIAnonymousPullFlows(t *testing.T) {
	store := repository.NewMemoryStore()
	for _, name := range []string{"public", "private"} {
		visibility := repository.VisibilityPrivate
		if name == "public" {
			visibility = repository.VisibilityPublic
		}
		if _, err := store.CreateHostedRepository(context.Background(), repository.HostedRepository{ID: uuid.NewString(), Name: name, Format: repository.FormatOCI, Visibility: visibility}); err != nil {
			t.Fatal(err)
		}
	}
	handler := conformanceGateway(t, store, repository.FormatOCI)
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	for _, name := range []string{"public", "private"} {
		put := httptest.NewRequest(http.MethodPut, "/v2/"+name+"/app/manifests/latest", bytes.NewReader(manifest))
		put.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		authorize(put, "resolver-secret")
		if published := httptest.NewRecorder(); func() bool { handler.ServeHTTP(published, put); return published.Code != http.StatusCreated }() {
			t.Fatalf("publish %s failed", name)
		}
	}
	do := func(userAgent, method, target, bearer string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, target, nil)
		request.Header.Set("User-Agent", userAgent)
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	token := func(userAgent, scope string) (int, string) {
		response := do(userAgent, http.MethodGet, "/auth/token?service=artifact-gateway&scope="+scope, "")
		var body struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		return response.Code, body.Token
	}
	for _, client := range []struct {
		name, userAgent string
		directFirst     bool // ORAS tries an unauthenticated read before the token flow
	}{
		{"docker", "docker/27.0 go/go1.22 git-commit/abc kernel/6.1 os/linux arch/amd64", false},
		{"containerd", "containerd/v2.0.0", false},
		{"podman", "libpod/5.0 (podman)", false},
		{"oras", "oras-go/2.5", true},
	} {
		t.Run(client.name, func(t *testing.T) {
			for _, gateOpen := range []bool{true, false} {
				if gateOpen {
					enableAnonymousAccess(t, store)
				} else {
					current, err := store.GetPublicAccessPolicy(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					if _, err := store.ReplacePublicAccessPolicy(context.Background(), repository.PublicAccessPolicy{Enabled: false}, current.Version); err != nil {
						t.Fatal(err)
					}
				}
				if client.directFirst {
					for _, method := range []string{http.MethodGet, http.MethodHead} {
						direct := do(client.userAgent, method, "/v2/public/app/manifests/latest", "")
						if gateOpen && direct.Code != http.StatusOK || !gateOpen && direct.Code != http.StatusUnauthorized {
							t.Fatalf("gate=%v direct %s = %d", gateOpen, method, direct.Code)
						}
					}
				}
				// The challenge, then the anonymous token, then the pull.
				ping := do(client.userAgent, http.MethodGet, "/v2/", "")
				if ping.Code != http.StatusUnauthorized || !strings.Contains(ping.Header().Get("WWW-Authenticate"), "Bearer realm=") {
					t.Fatalf("ping = %d %q", ping.Code, ping.Header().Get("WWW-Authenticate"))
				}
				code, bearer := token(client.userAgent, "repository:public/app:pull")
				if code != http.StatusOK || bearer == "" {
					t.Fatalf("anonymous token = %d", code)
				}
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					pulled := do(client.userAgent, method, "/v2/public/app/manifests/latest", bearer)
					if gateOpen && pulled.Code != http.StatusOK || !gateOpen && pulled.Code != http.StatusUnauthorized {
						t.Fatalf("gate=%v token pull %s = %d %s", gateOpen, method, pulled.Code, pulled.Body.String())
					}
				}
				// The same token reads nothing a bare anonymous request cannot, and never writes.
				if private := do(client.userAgent, http.MethodGet, "/v2/private/app/manifests/latest", bearer); private.Code != http.StatusUnauthorized {
					t.Fatalf("the anonymous token read a private repository: %d", private.Code)
				}
				absent := do(client.userAgent, http.MethodGet, "/v2/absent/app/manifests/latest", bearer)
				if shape(absent) != shape(do(client.userAgent, http.MethodGet, "/v2/private/app/manifests/latest", bearer)) {
					t.Fatal("a private name and an absent one answer differently to a token holder")
				}
				write := httptest.NewRequest(http.MethodPut, "/v2/public/app/manifests/other", bytes.NewReader(manifest))
				write.Header.Set("Authorization", "Bearer "+bearer)
				if response := httptest.NewRecorder(); func() bool { handler.ServeHTTP(response, write); return response.Code < 400 }() {
					t.Fatal("the anonymous token wrote a manifest")
				}
			}
			// Credentials and push scopes still need a login.
			if code, _ := token(client.userAgent, "repository:public/app:pull,push"); code != http.StatusUnauthorized {
				t.Fatalf("a push scope without credentials = %d, want 401", code)
			}
			if code, _ := token(client.userAgent, ""); code != http.StatusUnauthorized {
				t.Fatalf("a token request without a scope = %d, want 401", code)
			}
		})
	}
}

// TestAccessConformanceCoversProtocolCompatibilityFormats keeps the harness honest:
// every format the compatibility baseline names must be driven above.
func TestAccessConformanceCoversProtocolCompatibilityFormats(t *testing.T) {
	data, err := os.ReadFile("../../docs/protocol-compatibility.md")
	if err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for _, format := range conformanceFormats {
		covered[strings.ToLower(string(format.name))] = true
	}
	row := regexp.MustCompile(`(?m)^\| ([A-Za-z]+)[^|]*\|`)
	skip := map[string]bool{"format": true, "protocol": true}
	var missing []string
	for _, match := range row.FindAllStringSubmatch(string(data), -1) {
		name := strings.ToLower(match[1])
		if skip[name] || strings.HasPrefix(match[0], "| ---") {
			continue
		}
		if !covered[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("protocol-compatibility.md names formats the access conformance harness does not drive: %v", missing)
	}
}
