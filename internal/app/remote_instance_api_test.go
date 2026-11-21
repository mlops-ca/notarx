package app

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mlops-ca/notarx/internal/config"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/remoteinstance"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

const remoteTestToken = "static-token-value-0123456789"

// fakeDiscoverer answers instance documents by base URL.
type fakeDiscoverer struct {
	documents map[string]remoteinstance.Document
	err       error
	calls     atomic.Int32
}

func (f *fakeDiscoverer) Discover(_ context.Context, baseURL string) (remoteinstance.Document, error) {
	f.calls.Add(1)
	if f.err != nil {
		return remoteinstance.Document{}, f.err
	}
	document, ok := f.documents[baseURL]
	if !ok {
		return remoteinstance.Document{}, errors.New("remote instance request failed: connection refused")
	}
	return document, nil
}

func remoteDocument(id string) remoteinstance.Document {
	return remoteinstance.Document{InstanceID: id, APIVersion: remoteinstance.APIVersion, Formats: []string{"raw"}, Limits: remoteinstance.DefaultLimits()}
}

type remoteAPI struct {
	t       *testing.T
	handler http.Handler
}

func (a remoteAPI) do(method, path, token, body, ifMatch string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		authorize(r, token)
	}
	if ifMatch != "" {
		r.Header.Set("If-Match", ifMatch)
	}
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, r)
	return w
}

func TestRemoteInstanceAPIAdministratorsOnlyWriteOnlyTokenAndCredentialHygiene(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	ctx := context.Background()
	store := repository.NewMemoryStore()
	local, err := store.LocalInstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	peerID := "0197c0de-0000-7000-8000-000000000001"
	discoverer := &fakeDiscoverer{documents: map[string]remoteinstance.Document{
		"https://peer.example.test":  remoteDocument(peerID),
		"https://other.example.test": remoteDocument("0197c0de-0000-7000-8000-000000000002"),
		"https://self.example.test":  remoteDocument(local),
	}}
	api := remoteAPI{t: t, handler: newTestGateway(t, GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator(), RemoteDiscoverer: discoverer})}
	path := "/api/v2/remote-instances"
	body := func(name, base string) string {
		return fmt.Sprintf(`{"name":%q,"baseUrl":%q,"authKind":"static_token","token":%q,"trustEvidence":true}`, name, base, remoteTestToken)
	}

	// Platform administrators only, on every operation.
	for _, call := range []struct{ method, path, body string }{
		{http.MethodGet, path, ""}, {http.MethodPost, path, body("p", "https://peer.example.test")},
		{http.MethodGet, path + "/" + peerID, ""}, {http.MethodPut, path + "/" + peerID, "{}"},
		{http.MethodDelete, path + "/" + peerID, ""}, {http.MethodPost, path + "/" + peerID + "/test", ""},
	} {
		if got := api.do(call.method, call.path, "resolver-secret", call.body, "1"); got.Code != http.StatusForbidden {
			t.Fatalf("non-admin %s %s = %d %s", call.method, call.path, got.Code, got.Body.String())
		}
		if got := api.do(call.method, call.path, "", call.body, "1"); got.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s %s = %d", call.method, call.path, got.Code)
		}
	}

	created := api.do(http.MethodPost, path, "admin-secret", body("peer", "https://peer.example.test/"), "")
	if created.Code != http.StatusCreated || created.Header().Get("ETag") != "1" {
		t.Fatalf("create = %d etag=%q %s", created.Code, created.Header().Get("ETag"), created.Body.String())
	}
	var instance map[string]any
	if err = json.Unmarshal(created.Body.Bytes(), &instance); err != nil {
		t.Fatal(err)
	}
	if instance["tokenConfigured"] != true || instance["instanceId"] != peerID || instance["state"] != "active" || instance["baseUrl"] != "https://peer.example.test" || instance["lastCheckedAt"] == nil {
		t.Fatalf("created = %v", instance)
	}
	for key := range instance {
		if strings.Contains(strings.ToLower(key), "token") && key != "tokenConfigured" {
			t.Fatalf("response exposes %q", key)
		}
	}
	id, _ := instance["id"].(string)
	stored, err := store.GetRemoteInstance(ctx, id)
	if err != nil || stored.TokenSealed == "" || stored.TokenSealed == remoteTestToken || strings.Contains(stored.TokenSealed, remoteTestToken) {
		t.Fatalf("stored = %#v err=%v", stored, err)
	}
	if plaintext, openErr := secrets.Open(remoteinstance.TokenPurpose(id), stored.TokenSealed); openErr != nil || plaintext != remoteTestToken {
		t.Fatalf("sealed token opens to %q err=%v", plaintext, openErr)
	}
	if _, openErr := secrets.Open(remoteinstance.TokenPurpose("another-id"), stored.TokenSealed); openErr == nil {
		t.Fatal("the sealed token opened under another instance purpose")
	}

	// Reads never return the token.
	for _, read := range []string{path, path + "/" + id} {
		got := api.do(http.MethodGet, read, "admin-secret", "", "")
		if got.Code != http.StatusOK || strings.Contains(got.Body.String(), remoteTestToken) || !strings.Contains(got.Body.String(), `"tokenConfigured":true`) {
			t.Fatalf("read %s = %d %s", read, got.Code, got.Body.String())
		}
	}

	// Conflicts and validation.
	if dup := api.do(http.MethodPost, path, "admin-secret", body("peer", "https://other.example.test"), ""); dup.Code != http.StatusConflict {
		t.Fatalf("duplicate name = %d %s", dup.Code, dup.Body.String())
	}
	if self := api.do(http.MethodPost, path, "admin-secret", body("self", "https://self.example.test"), ""); self.Code != http.StatusUnprocessableEntity || !strings.Contains(self.Body.String(), "self_instance") {
		t.Fatalf("self instance = %d %s", self.Code, self.Body.String())
	}
	if down := api.do(http.MethodPost, path, "admin-secret", body("down", "https://down.example.test"), ""); down.Code != http.StatusUnprocessableEntity || !strings.Contains(down.Body.String(), "remote_check_failed") {
		t.Fatalf("unreachable = %d %s", down.Code, down.Body.String())
	}
	for name, invalid := range map[string]string{
		"http":              body("n", "http://peer.example.test"),
		"credentials":       body("n", "https://u:p@peer.example.test"),
		"no token":          `{"name":"n","baseUrl":"https://peer.example.test","authKind":"static_token"}`,
		"short token":       `{"name":"n","baseUrl":"https://peer.example.test","authKind":"static_token","token":"short"}`,
		"workload no aud":   `{"name":"n","baseUrl":"https://peer.example.test","authKind":"workload_identity"}`,
		"workload w/ token": `{"name":"n","baseUrl":"https://peer.example.test","authKind":"workload_identity","audience":"a","token":"` + remoteTestToken + `"}`,
		"unknown kind":      `{"name":"n","baseUrl":"https://peer.example.test","authKind":"basic"}`,
		"unknown field":     `{"name":"n","baseUrl":"https://peer.example.test","authKind":"workload_identity","audience":"a","extra":1}`,
		"empty name":        body(" ", "https://peer.example.test"),
	} {
		if got := api.do(http.MethodPost, path, "admin-secret", invalid, ""); got.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", name, got.Code, got.Body.String())
		}
	}

	// Update: an omitted token keeps the sealed one, a new token replaces it, workload_identity removes it.
	renamed := `{"name":"peer-renamed","baseUrl":"https://peer.example.test","authKind":"static_token","enabled":true}`
	if missing := api.do(http.MethodPut, path+"/"+id, "admin-secret", renamed, ""); missing.Code != http.StatusBadRequest {
		t.Fatalf("missing If-Match = %d", missing.Code)
	}
	updated := api.do(http.MethodPut, path+"/"+id, "admin-secret", renamed, "1")
	if updated.Code != http.StatusOK || updated.Header().Get("ETag") != "2" {
		t.Fatalf("update = %d %s", updated.Code, updated.Body.String())
	}
	if kept, _ := store.GetRemoteInstance(ctx, id); kept.TokenSealed != stored.TokenSealed {
		t.Fatal("an omitted token must keep the sealed token")
	}
	if stale := api.do(http.MethodPut, path+"/"+id, "admin-secret", renamed, "1"); stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale = %d", stale.Code)
	}
	replaced := api.do(http.MethodPut, path+"/"+id, "admin-secret", `{"name":"peer-renamed","baseUrl":"https://peer.example.test","authKind":"static_token","token":"replacement-token-9876543210","enabled":true}`, "2")
	if replaced.Code != http.StatusOK {
		t.Fatalf("replace token = %d %s", replaced.Code, replaced.Body.String())
	}
	if now, _ := store.GetRemoteInstance(ctx, id); now.TokenSealed == stored.TokenSealed || now.TokenSealed == "" {
		t.Fatal("a new token must be sealed again")
	}
	if moved := api.do(http.MethodPut, path+"/"+id, "admin-secret", `{"name":"peer-renamed","baseUrl":"https://other.example.test","authKind":"static_token","enabled":true}`, "3"); moved.Code != http.StatusUnprocessableEntity || !strings.Contains(moved.Body.String(), "token_required") {
		t.Fatalf("a static token must not follow a changed baseUrl = %d %s", moved.Code, moved.Body.String())
	}
	if now, _ := store.GetRemoteInstance(ctx, id); now.BaseURL != "https://peer.example.test" {
		t.Fatalf("a refused update changed the instance: %s", now.BaseURL)
	}
	if noAudience := api.do(http.MethodPut, path+"/"+id, "admin-secret", `{"name":"peer-renamed","baseUrl":"https://peer.example.test","authKind":"workload_identity","enabled":true}`, "3"); noAudience.Code != http.StatusBadRequest {
		t.Fatalf("workload without audience = %d", noAudience.Code)
	}
	workload := api.do(http.MethodPut, path+"/"+id, "admin-secret", `{"name":"peer-renamed","baseUrl":"https://peer.example.test","authKind":"workload_identity","audience":"https://peer.example.test","enabled":false}`, "3")
	if workload.Code != http.StatusOK || !strings.Contains(workload.Body.String(), `"tokenConfigured":false`) || !strings.Contains(workload.Body.String(), `"state":"disabled"`) {
		t.Fatalf("to workload_identity = %d %s", workload.Code, workload.Body.String())
	}
	if now, _ := store.GetRemoteInstance(ctx, id); now.TokenSealed != "" {
		t.Fatal("workload_identity must not keep a token")
	}

	// Test endpoint: records the outcome, a disabled instance stays disabled, another instance id is unreachable.
	tested := api.do(http.MethodPost, path+"/"+id+"/test", "admin-secret", "", "")
	if tested.Code != http.StatusOK || !strings.Contains(tested.Body.String(), `"reachable":true`) || !strings.Contains(tested.Body.String(), `"state":"disabled"`) {
		t.Fatalf("test = %d %s", tested.Code, tested.Body.String())
	}
	discoverer.documents["https://peer.example.test"] = remoteDocument("0197c0de-0000-7000-8000-000000000003")
	enable := api.do(http.MethodPut, path+"/"+id, "admin-secret", `{"name":"peer-renamed","baseUrl":"https://other.example.test","authKind":"workload_identity","audience":"a","enabled":true}`, "4")
	if enable.Code != http.StatusOK || !strings.Contains(enable.Body.String(), `"state":"active"`) {
		t.Fatalf("enable = %d %s", enable.Code, enable.Body.String())
	}
	discoverer.documents["https://other.example.test"] = remoteDocument(local)
	if self := api.do(http.MethodPost, path+"/"+id+"/test", "admin-secret", "", ""); !strings.Contains(self.Body.String(), `"reachable":false`) || !strings.Contains(self.Body.String(), `"state":"unreachable"`) {
		t.Fatalf("test of a remote now reporting this instance = %d %s", self.Code, self.Body.String())
	}
	discoverer.documents["https://other.example.test"] = remoteDocument("0197c0de-0000-7000-8000-000000000009")
	if moved := api.do(http.MethodPost, path+"/"+id+"/test", "admin-secret", "", ""); !strings.Contains(moved.Body.String(), "another instance id") {
		t.Fatalf("test of a remote reporting another id = %d %s", moved.Code, moved.Body.String())
	}
	discoverer.err = errors.New("remote instance request failed: timeout")
	if down := api.do(http.MethodPost, path+"/"+id+"/test", "admin-secret", "", ""); down.Code != http.StatusOK || !strings.Contains(down.Body.String(), `"state":"unreachable"`) {
		t.Fatalf("test of an unreachable remote = %d %s", down.Code, down.Body.String())
	}
	if after, _ := store.GetRemoteInstance(ctx, id); after.State != repository.RemoteInstanceStateUnreachable || after.LastCheckedAt.IsZero() {
		t.Fatalf("the test outcome was not recorded: %#v", after)
	}

	// Delete.
	if got := api.do(http.MethodDelete, path+"/"+id, "admin-secret", "", ""); got.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", got.Code)
	}
	for _, call := range []string{http.MethodGet, http.MethodDelete} {
		if got := api.do(call, path+"/"+id, "admin-secret", "", ""); got.Code != http.StatusNotFound {
			t.Fatalf("%s after delete = %d", call, got.Code)
		}
	}
	if got := api.do(http.MethodPost, path+"/"+id+"/test", "admin-secret", "", ""); got.Code != http.StatusNotFound {
		t.Fatalf("test after delete = %d", got.Code)
	}

	// Credentials never reach the logs, the audit log or the stored audit records.
	audits, err := store.ListAudits(ctx, repository.AuditQuery{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(audits)
	sealedValues := []string{remoteTestToken, "replacement-token-9876543210", stored.TokenSealed}
	for _, secret := range sealedValues {
		if strings.Contains(logs.String(), secret) || strings.Contains(string(encoded), secret) {
			t.Fatalf("credential %q leaked into the logs or audit records", secret)
		}
	}
	if !strings.Contains(string(encoded), "remote_instance.create") || !strings.Contains(string(encoded), "remote_instance.delete") {
		t.Fatalf("remote instance mutations are not audited: %s", encoded)
	}
}

func TestRemoteInstanceAPIRequiresTheEncryptionKeyForStaticTokens(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "")
	store := repository.NewMemoryStore()
	discoverer := &fakeDiscoverer{documents: map[string]remoteinstance.Document{"https://peer.example.test": remoteDocument("0197c0de-0000-7000-8000-000000000001")}}
	api := remoteAPI{t: t, handler: newTestGateway(t, GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator(), RemoteDiscoverer: discoverer})}
	got := api.do(http.MethodPost, "/api/v2/remote-instances", "admin-secret",
		`{"name":"peer","baseUrl":"https://peer.example.test","authKind":"static_token","token":"`+remoteTestToken+`"}`, "")
	if got.Code != http.StatusServiceUnavailable || !strings.Contains(got.Body.String(), "encryption_key_unavailable") || strings.Contains(got.Body.String(), remoteTestToken) {
		t.Fatalf("create without key = %d %s", got.Code, got.Body.String())
	}
	if discoverer.calls.Load() != 0 {
		t.Fatal("the remote was contacted before the token could be sealed")
	}
	// A workload_identity instance needs no key.
	if ok := api.do(http.MethodPost, "/api/v2/remote-instances", "admin-secret",
		`{"name":"peer","baseUrl":"https://peer.example.test","authKind":"workload_identity","audience":"https://peer.example.test"}`, ""); ok.Code != http.StatusCreated {
		t.Fatalf("workload create = %d %s", ok.Code, ok.Body.String())
	}
}

func TestRemoteInstanceAPIListPagesByName(t *testing.T) {
	store := repository.NewMemoryStore()
	discoverer := &fakeDiscoverer{documents: map[string]remoteinstance.Document{"https://peer.example.test": remoteDocument("0197c0de-0000-7000-8000-000000000001")}}
	api := remoteAPI{t: t, handler: newTestGateway(t, GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator(), RemoteDiscoverer: discoverer})}
	for _, name := range []string{"b-peer", "a-peer", "c-peer"} {
		got := api.do(http.MethodPost, "/api/v2/remote-instances", "admin-secret",
			fmt.Sprintf(`{"name":%q,"baseUrl":"https://peer.example.test","authKind":"workload_identity","audience":"a"}`, name), "")
		if got.Code != http.StatusCreated {
			t.Fatalf("create %s = %d %s", name, got.Code, got.Body.String())
		}
	}
	var names []string
	token := ""
	for page := 0; page < 5; page++ {
		target := "/api/v2/remote-instances?limit=2"
		if token != "" {
			target += "&pageToken=" + token
		}
		got := api.do(http.MethodGet, target, "admin-secret", "", "")
		var decoded struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &decoded) != nil {
			t.Fatalf("list = %d %s", got.Code, got.Body.String())
		}
		for _, item := range decoded.Items {
			names = append(names, item.Name)
		}
		if token = decoded.NextPageToken; token == "" {
			break
		}
	}
	if strings.Join(names, ",") != "a-peer,b-peer,c-peer" {
		t.Fatalf("names = %v", names)
	}
	if bad := api.do(http.MethodGet, "/api/v2/remote-instances?pageToken=bogus", "admin-secret", "", ""); bad.Code != http.StatusBadRequest {
		t.Fatalf("bad page token = %d", bad.Code)
	}
}

func TestWellKnownInstanceDocumentIsAnonymousAndStable(t *testing.T) {
	store := repository.NewMemoryStore()
	handler := newTestGateway(t, GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator(), ImportStaging: objectstore.NewMemoryStore()})
	get := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/.well-known/notarx", nil))
		return w
	}
	first := get()
	var document remoteinstance.Document
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &document) != nil || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("well-known = %d %s", first.Code, first.Body.String())
	}
	local, _ := store.LocalInstanceID(context.Background())
	if document.InstanceID != local || document.APIVersion != remoteinstance.APIVersion || document.Limits != remoteinstance.DefaultLimits() || len(document.Formats) != 1 || document.Formats[0] != "raw" {
		t.Fatalf("document = %#v local=%s", document, local)
	}
	if _, err := document.Validate(); err != nil {
		t.Fatalf("the served document does not validate: %v", err)
	}
	if again := get(); again.Body.String() != first.Body.String() {
		t.Fatal("the instance document changed between requests")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/.well-known/notarx", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", w.Code)
	}
}

// Two gateways over TLS: registering a peer goes through the real egress policy and instance protocol; a deployment
// cannot register itself, and private networks are refused unless the operator allows them.
func TestRemoteInstanceRegistrationOverTheInstanceProtocol(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	var peerHandler, selfHandler atomic.Pointer[http.Handler]
	serve := func(holder *atomic.Pointer[http.Handler]) *httptest.Server {
		return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { (*holder.Load()).ServeHTTP(w, r) }))
	}
	peer, self := serve(&peerHandler), serve(&selfHandler)
	defer peer.Close()
	defer self.Close()
	roots := x509.NewCertPool()
	roots.AddCert(peer.Certificate())
	roots.AddCert(self.Certificate())

	gateway := func(allowPrivate bool) (http.Handler, *repository.MemoryStore) {
		store := repository.NewMemoryStore()
		return newTestGateway(t, GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator(),
			RemoteDiscoverer: remoteinstance.NewDiscoverer(remoteinstance.Options{AllowPrivateNetworks: allowPrivate, RootCAs: roots})}), store
	}
	peerGateway, _ := gateway(true)
	ph := http.Handler(peerGateway)
	peerHandler.Store(&ph)
	selfGateway, _ := gateway(true)
	sh := http.Handler(selfGateway)
	selfHandler.Store(&sh)

	register := func(handler http.Handler, base string) *httptest.ResponseRecorder {
		return remoteAPI{t: t, handler: handler}.do(http.MethodPost, "/api/v2/remote-instances", "admin-secret",
			fmt.Sprintf(`{"name":"remote","baseUrl":%q,"authKind":"workload_identity","audience":"a"}`, base), "")
	}
	if got := register(selfGateway, peer.URL); got.Code != http.StatusCreated {
		t.Fatalf("register peer = %d %s", got.Code, got.Body.String())
	}
	if got := register(selfGateway, self.URL); got.Code != http.StatusUnprocessableEntity || !strings.Contains(got.Body.String(), "self_instance") {
		t.Fatalf("register self = %d %s", got.Code, got.Body.String())
	}
	strict, _ := gateway(false)
	if got := register(strict, peer.URL); got.Code != http.StatusUnprocessableEntity || !strings.Contains(got.Body.String(), "private") {
		t.Fatalf("private network without the allow flag = %d %s", got.Code, got.Body.String())
	}
}

// Deleting a remote instance that promotion rules still target is a 409 in_use that names the rules.
func TestRemoteInstanceAPIDeleteConflictNamesTheBlockingRules(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	source, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: "0197c0de-0000-7000-8000-0000000000a1", Name: "dev", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.CreateServiceAccount(ctx, repository.ServiceAccount{ID: "0197c0de-0000-7000-8000-0000000000a2", Name: "runner"})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := store.CreateRemoteInstance(ctx, repository.RemoteInstance{ID: "0197c0de-0000-7000-8000-0000000000a3", Name: "peer", BaseURL: "https://peer.example.test", InstanceID: "0197c0de-0000-7000-8000-0000000000a4",
		AuthKind: repository.RemoteInstanceAuthStaticToken, TokenSealed: "v1:kid:sealed", State: repository.RemoteInstanceStateActive})
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"release", "nightly"} {
		if _, err = store.CreatePromotionRule(ctx, repository.PromotionRule{ID: fmt.Sprintf("0197c0de-0000-7000-8000-0000000000b%d", i), Name: name, SourceRepositoryID: source.ID, TargetKind: repository.PromotionTargetRemote,
			RemoteInstanceID: remote.ID, RemoteRepository: "releases", Match: json.RawMessage(`{"coordinate":{"glob":"**"}}`), Conditions: json.RawMessage(`{}`), RunAsServiceAccountID: account.ID, CreatedBy: "alice"}, 32); err != nil {
			t.Fatal(err)
		}
	}
	api := remoteAPI{t: t, handler: newTestGateway(t, GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})}
	got := api.do(http.MethodDelete, "/api/v2/remote-instances/"+remote.ID, "admin-secret", "", "")
	var problem struct {
		Code  string
		Rules []struct{ RuleID, Name string }
	}
	if got.Code != http.StatusConflict || json.Unmarshal(got.Body.Bytes(), &problem) != nil || problem.Code != "in_use" {
		t.Fatalf("delete = %d %s", got.Code, got.Body.String())
	}
	if len(problem.Rules) != 2 || problem.Rules[0].Name != "nightly" || problem.Rules[1].Name != "release" || problem.Rules[0].RuleID == "" {
		t.Fatalf("blocking rules = %+v", problem.Rules)
	}
	if _, err = store.GetRemoteInstance(ctx, remote.ID); err != nil {
		t.Fatalf("the refused delete removed the instance: %v", err)
	}
}

// The instance document advertises the import limits this instance enforces, so a sender never plans a push that the
// receiver's own configuration would refuse.
func TestWellKnownInstanceDocumentAdvertisesTheConfiguredImportLimits(t *testing.T) {
	limits := remoteinstance.Limits{MaxBlobBytes: 1 << 20, MaxSessionBytes: 8 << 20, MaxBlobsPerSession: 7, MaxOpenSessionsPerPrincipal: 2}
	handler := newTestGateway(t, GatewayOptions{Store: repository.NewMemoryStore(), Adapter: TestAdapter{}, Authenticator: testAuthenticator(), ImportConfig: config.ImportConfig{MaxBlobBytes: 1 << 20, MaxSessionBytes: 8 << 20, MaxBlobsPerSession: 7, MaxOpenSessionsPerPrincipal: 2}})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/.well-known/notarx", nil))
	var document remoteinstance.Document
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &document) != nil || document.Limits != limits {
		t.Fatalf("well-known = %d %s", w.Code, w.Body.String())
	}
}
