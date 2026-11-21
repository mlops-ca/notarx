package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/repository"
)

// ruleFixture is a Raw source with three published assets, an empty Raw target, a run-as service account and a
// registered remote instance; "promoter" administers both repositories and "reader" only reads the source.
type ruleFixture struct {
	t              *testing.T
	store          *repository.MemoryStore
	handler        http.Handler
	source, target repository.HostedRepository
	versioned      repository.HostedRepository
	pypi           repository.HostedRepository
	account        repository.ServiceAccount
	remote         repository.RemoteInstance
	assets         []repository.RawAsset
	promoter       string
	reader         string
}

func newRuleFixture(t *testing.T, options ...func(*GatewayOptions)) ruleFixture {
	t.Helper()
	ctx := context.Background()
	store := repository.NewMemoryStore()
	create := func(name string, format repository.Format, layout repository.RawLayout) repository.HostedRepository {
		repo, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: name, Format: format, RawLayout: layout})
		if err != nil {
			t.Fatal(err)
		}
		return repo
	}
	f := ruleFixture{t: t, store: store,
		source: create("rule-source", repository.FormatRaw, ""), target: create("rule-target", repository.FormatRaw, ""),
		versioned: create("rule-versioned", repository.FormatRaw, repository.RawLayoutVersioned), pypi: create("rule-pypi", repository.FormatPyPI, "")}
	for _, path := range []string{"releases/app-1.0.0.tgz", "releases/app-1.1.0.tgz", "snapshots/app-2.0.0-rc1.tgz"} {
		sum := sha256.Sum256([]byte(path))
		asset := repository.RawAsset{RepositoryID: f.source.ID, Path: path, Digest: fmt.Sprintf("sha256:%x", sum), ObjectKey: fmt.Sprintf("native/raw/sha256/%x", sum), Size: int64(len(path)), ContentType: "application/gzip"}
		if _, err := store.PutRawAsset(ctx, asset); err != nil {
			t.Fatal(err)
		}
		f.assets = append(f.assets, asset)
	}
	var err error
	if f.account, err = store.CreateServiceAccount(ctx, repository.ServiceAccount{ID: uuid.NewString(), Name: "rule-runner"}); err != nil {
		t.Fatal(err)
	}
	if f.remote, err = store.CreateRemoteInstance(ctx, repository.RemoteInstance{ID: uuid.NewString(), Name: "rule-peer", BaseURL: "https://peer.example.test", InstanceID: uuid.NewString(),
		AuthKind: repository.RemoteInstanceAuthWorkloadIdentity, Audience: "aud", State: repository.RemoteInstanceStateActive}); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []repository.HostedRepository{f.source, f.target, f.versioned, f.pypi} {
		grants := []repository.RepositoryGrant{{Principal: "promoter", Scopes: []string{"repositories:admin"}}, {Principal: "reader", Scopes: []string{"repositories:read"}}}
		if _, err = store.ReplaceRepositoryGrants(ctx, repo.ID, grants, "1"); err != nil {
			t.Fatal(err)
		}
	}
	authenticator := testAuthenticator()
	gateway := GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: authenticator}
	for _, option := range options {
		option(&gateway)
	}
	f.handler = newTestGateway(t, gateway)
	f.promoter, f.reader = authenticator.IssueToken("promoter"), authenticator.IssueToken("reader")
	return f
}

func (f ruleFixture) do(method, path, token, body string, headers ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		authorize(request, token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

// ruleBody renders a create request; match is the JSON of the match document.
func (f ruleFixture) ruleBody(name, source, target, match string, extra ...string) string {
	body := fmt.Sprintf(`{"name":%q,"sourceRepositoryId":%q,"target":{"kind":"local","repositoryId":%q},"match":%s,"runAsServiceAccountId":%q`, name, source, target, match, f.account.ID)
	for _, member := range extra {
		body += "," + member
	}
	return body + "}"
}

const rulesPath = "/api/v2/promotion-rules"

func (f ruleFixture) create(token, body string) *httptest.ResponseRecorder {
	return f.do(http.MethodPost, rulesPath, token, body)
}

func decodeRule(t *testing.T, response *httptest.ResponseRecorder) adminopenapi.PromotionRule {
	t.Helper()
	var rule adminopenapi.PromotionRule
	if err := json.Unmarshal(response.Body.Bytes(), &rule); err != nil {
		t.Fatalf("decode %s: %v", response.Body.String(), err)
	}
	return rule
}

func ruleProblemCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var problem struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &problem)
	return problem.Code
}

func TestPromotionRuleLifecycleVersionsAndAudit(t *testing.T) {
	f := newRuleFixture(t)
	match := `{"coordinate":{"glob":"releases/**"},"version":{"range":">=1.0.0"}}`
	created := f.create(f.promoter, f.ruleBody("nightly", f.source.ID, f.target.ID, match, `"conditions":{"minAge":"72h","securityPolicy":"pass"}`))
	if created.Code != http.StatusCreated || created.Header().Get("ETag") != "1" {
		t.Fatalf("create = %d etag=%q %s", created.Code, created.Header().Get("ETag"), created.Body.String())
	}
	rule := decodeRule(t, created)
	if rule.Version != "1" || rule.CreatedBy != "promoter" || rule.UpdatedBy != "promoter" || rule.Mode != "copy" || !rule.Enabled || rule.State != "active" || rule.StateReason != nil || rule.Target.Kind != "local" ||
		rule.Match.Scope == nil || *rule.Match.Scope != "coordinate" || rule.Conditions.MinAge == nil || *rule.Conditions.MinAge != "72h" || rule.RunAsServiceAccountId.String() != f.account.ID {
		t.Fatalf("rule = %+v", rule)
	}
	path := rulesPath + "/" + rule.Id.String()
	if got := f.do(http.MethodGet, path, f.promoter, ""); got.Code != http.StatusOK || got.Header().Get("ETag") != "1" {
		t.Fatalf("get = %d %s", got.Code, got.Body.String())
	}

	// Edits need the current version, increment it and record the editor in the rule and the audit log.
	update := func(version, name, token string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"name":%q,"target":{"kind":"local","repositoryId":%q},"match":{"coordinate":{"regex":"releases/app-.*"}},"runAsServiceAccountId":%q,"enabled":false}`, name, f.target.ID, f.account.ID)
		return f.do(http.MethodPut, path, token, body, "If-Match", version)
	}
	for i, name := range []string{"edited-once", "edited-twice", "edited-thrice"} {
		got := update(fmt.Sprint(i+1), name, f.promoter)
		if got.Code != http.StatusOK || got.Header().Get("ETag") != fmt.Sprint(i+2) {
			t.Fatalf("update %d = %d etag=%q %s", i+1, got.Code, got.Header().Get("ETag"), got.Body.String())
		}
	}
	edited := decodeRule(t, f.do(http.MethodGet, path, f.promoter, ""))
	if edited.Version != "4" || edited.Name != "edited-thrice" || edited.Enabled || edited.UpdatedBy != "promoter" || edited.CreatedBy != "promoter" || edited.Match.Coordinate.Regex == nil || edited.Conditions.MinAge != nil {
		t.Fatalf("edited = %+v", edited)
	}
	if got := update("1", "stale", f.promoter); got.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale update = %d %s", got.Code, got.Body.String())
	}
	if got := update("4", "by-admin", "admin-secret"); got.Code != http.StatusOK {
		t.Fatalf("administrator update = %d %s", got.Code, got.Body.String())
	}
	if byAdmin := decodeRule(t, f.do(http.MethodGet, path, "admin-secret", "")); byAdmin.UpdatedBy != "alice" || byAdmin.CreatedBy != "promoter" || byAdmin.Version != "5" {
		t.Fatalf("by admin = %+v", byAdmin)
	}
	records, err := f.store.ListAudits(context.Background(), repository.AuditQuery{Repository: f.source.Name, Operation: "promotion_rule.update", Limit: 50})
	if err != nil || len(records) != 4 {
		t.Fatalf("update audit records = %d, %v", len(records), err)
	}
	actors := map[string]int{}
	for _, record := range records {
		actors[record.Actor]++
		if record.Resource != "promotion-rules/"+rule.Id.String() || record.Evidence["updatedBy"] != record.Actor || record.Evidence["version"] == "" {
			t.Fatalf("audit record = %+v", record)
		}
	}
	if actors["promoter"] != 3 || actors["alice"] != 1 {
		t.Fatalf("editors = %v", actors)
	}
	if creates, _ := f.store.ListAudits(context.Background(), repository.AuditQuery{Operation: "promotion_rule.create", Limit: 10}); len(creates) != 1 {
		t.Fatalf("create audit records = %d", len(creates))
	}

	page := f.do(http.MethodGet, rulesPath, f.promoter, "")
	var list adminopenapi.PromotionRulePage
	if err = json.Unmarshal(page.Body.Bytes(), &list); err != nil || page.Code != http.StatusOK || len(list.Items) != 1 || list.Items[0].Id != rule.Id {
		t.Fatalf("list = %d %s", page.Code, page.Body.String())
	}
	if got := f.do(http.MethodDelete, path, f.promoter, ""); got.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", got.Code, got.Body.String())
	}
	if got := f.do(http.MethodGet, path, f.promoter, ""); got.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d", got.Code)
	}
	if deletes, _ := f.store.ListAudits(context.Background(), repository.AuditQuery{Operation: "promotion_rule.delete", Limit: 10}); len(deletes) != 1 {
		t.Fatalf("delete audit records = %d", len(deletes))
	}
}

func TestPromotionRuleAuthorization(t *testing.T) {
	f := newRuleFixture(t)
	body := f.ruleBody("auth", f.source.ID, f.target.ID, `{"coordinate":{"glob":"**"}}`)
	if got := f.create("", body); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", got.Code)
	}
	// A reader cannot create, and a caller with no grant on the target cannot aim a rule at it.
	if got := f.create(f.reader, body); got.Code != http.StatusForbidden {
		t.Fatalf("reader = %d %s", got.Code, got.Body.String())
	}
	other, err := f.store.CreateHostedRepository(context.Background(), repository.HostedRepository{ID: uuid.NewString(), Name: "rule-ungranted", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.create(f.promoter, f.ruleBody("auth", f.source.ID, other.ID, `{"coordinate":{"glob":"**"}}`)); got.Code != http.StatusForbidden {
		t.Fatalf("no administration of the target = %d %s", got.Code, got.Body.String())
	}
	if got := f.create(f.promoter, f.ruleBody("auth", other.ID, f.target.ID, `{"coordinate":{"glob":"**"}}`)); got.Code != http.StatusForbidden {
		t.Fatalf("no administration of the source = %d %s", got.Code, got.Body.String())
	}
	created := f.create(f.promoter, body)
	if created.Code != http.StatusCreated {
		t.Fatal(created.Body.String())
	}
	path := rulesPath + "/" + decodeRule(t, created).Id.String()
	for _, call := range []struct{ method, path, body string }{
		{http.MethodGet, path, ""}, {http.MethodPut, path, "{}"}, {http.MethodDelete, path, ""}, {http.MethodPost, path + "/dry-run", ""},
	} {
		if got := f.do(call.method, call.path, f.reader, call.body, "If-Match", "1"); got.Code != http.StatusForbidden {
			t.Fatalf("reader %s %s = %d %s", call.method, call.path, got.Code, got.Body.String())
		}
	}
	// A list shows a caller the rules of the repositories it administers and nothing else; an administrator sees all.
	if got := f.do(http.MethodGet, rulesPath, f.reader, ""); got.Code != http.StatusOK || strings.Contains(got.Body.String(), "auth") {
		t.Fatalf("reader list = %d %s", got.Code, got.Body.String())
	}
	if got := f.do(http.MethodGet, rulesPath+"?sourceRepositoryId="+f.source.ID, f.reader, ""); got.Code != http.StatusForbidden {
		t.Fatalf("reader list of a source = %d", got.Code)
	}
	for _, token := range []string{f.promoter, "admin-secret"} {
		if got := f.do(http.MethodGet, rulesPath+"?sourceRepositoryId="+f.source.ID, token, ""); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"name":"auth"`) {
			t.Fatalf("list = %d %s", got.Code, got.Body.String())
		}
	}
	if got := f.do(http.MethodGet, rulesPath+"?limit=0", "admin-secret", ""); got.Code != http.StatusBadRequest {
		t.Fatalf("limit 0 = %d", got.Code)
	}
}

func TestPromotionRuleValidation(t *testing.T) {
	f := newRuleFixture(t)
	post := func(source, target repository.HostedRepository, match string, extra ...string) *httptest.ResponseRecorder {
		return f.create(f.promoter, f.ruleBody("v-"+uuid.NewString(), source.ID, target.ID, match, extra...))
	}
	long := strings.Repeat("a", 513)
	atLimit := strings.Repeat("a", 512)
	for name, tc := range map[string]struct {
		source, target repository.HostedRepository
		match          string
		extra          []string
		status         int
		code           string
	}{
		"valid glob":                 {f.source, f.target, `{"coordinate":{"glob":"releases/*"}}`, nil, 201, ""},
		"valid regex":                {f.source, f.target, `{"coordinate":{"regex":"releases/app-[0-9.]+\\.tgz"}}`, nil, 201, ""},
		"regex at the limit":         {f.source, f.target, `{"coordinate":{"regex":"` + atLimit + `"}}`, nil, 201, ""},
		"regex over the limit":       {f.source, f.target, `{"coordinate":{"regex":"` + long + `"}}`, nil, 400, "invalid_request"},
		"glob over the limit":        {f.source, f.target, `{"coordinate":{"glob":"` + long + `"}}`, nil, 400, "invalid_request"},
		"lookahead is not RE2":       {f.source, f.target, `{"coordinate":{"regex":"(?=a)a"}}`, nil, 400, "invalid_request"},
		"backreference":              {f.source, f.target, `{"coordinate":{"regex":"(a)\\1"}}`, nil, 400, "invalid_request"},
		"glob and regex":             {f.source, f.target, `{"coordinate":{"glob":"a","regex":"a"}}`, nil, 400, "invalid_request"},
		"no coordinate pattern":      {f.source, f.target, `{"coordinate":{}}`, nil, 400, "invalid_request"},
		"bad semver range":           {f.source, f.target, `{"coordinate":{"glob":"**"},"version":{"range":"not a range"}}`, nil, 400, "invalid_request"},
		"unknown scope":              {f.source, f.target, `{"coordinate":{"glob":"**"},"scope":"galaxy"}`, nil, 400, "invalid_request"},
		"unknown match member":       {f.source, f.target, `{"coordinate":{"glob":"**"},"colour":"red"}`, nil, 400, "invalid_request"},
		"properties, not versioned":  {f.source, f.target, `{"coordinate":{"glob":"**"},"properties":{"channel":"stable"}}`, nil, 422, "unsupported_match"},
		"refs without tags":          {f.source, f.target, `{"coordinate":{"glob":"**"},"refs":["latest"]}`, nil, 422, "unsupported_match"},
		"release on Raw":             {f.source, f.target, `{"coordinate":{"glob":"**"},"scope":"release"}`, nil, 201, ""},
		"conditions: unknown member": {f.source, f.target, `{"coordinate":{"glob":"**"}}`, []string{`"conditions":{"vibes":true}`}, 400, "invalid_request"},
		"conditions: bad minAge":     {f.source, f.target, `{"coordinate":{"glob":"**"}}`, []string{`"conditions":{"minAge":"soon"}`}, 400, "invalid_request"},
		"conditions: bad severity":   {f.source, f.target, `{"coordinate":{"glob":"**"}}`, []string{`"conditions":{"scan":{"completed":true,"maxSeverity":"scary"}}`}, 400, "invalid_request"},
		"mode move":                  {f.source, f.target, `{"coordinate":{"glob":"**"}}`, []string{`"mode":"move"`}, 400, "invalid_request"},
		"mode copy":                  {f.source, f.target, `{"coordinate":{"glob":"**"}}`, []string{`"mode":"copy"`}, 201, ""},
	} {
		got := post(tc.source, tc.target, tc.match, tc.extra...)
		if got.Code != tc.status || (tc.code != "" && ruleProblemCode(t, got) != tc.code) {
			t.Errorf("%s = %d %s, want %d %s", name, got.Code, got.Body.String(), tc.status, tc.code)
		}
	}
}

// Properties are valid on a versioned generic source and release scope needs the ReleaseSet capability: both checked
// against a source whose target is legal, so only the capability decides.
func TestPromotionRuleCapabilityChecksAgainstTheSourceFormat(t *testing.T) {
	f := newRuleFixture(t)
	ctx := context.Background()
	versionedTarget, err := f.store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "rule-versioned-target", Format: repository.FormatRaw, RawLayout: repository.RawLayoutVersioned})
	if err != nil {
		t.Fatal(err)
	}
	pypiTarget, err := f.store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "rule-pypi-target", Format: repository.FormatPyPI})
	if err != nil {
		t.Fatal(err)
	}
	nugetSource, err := f.store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "rule-nuget", Format: repository.FormatNuGet})
	if err != nil {
		t.Fatal(err)
	}
	for _, repo := range []repository.HostedRepository{versionedTarget, pypiTarget, nugetSource} {
		if _, err = f.store.ReplaceRepositoryGrants(ctx, repo.ID, []repository.RepositoryGrant{{Principal: "promoter", Scopes: []string{"repositories:admin"}}}, "1"); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct {
		source, target repository.HostedRepository
		match          string
		status         int
		code           string
	}{
		"properties on a versioned generic source":  {f.versioned, versionedTarget, `{"coordinate":{"glob":"**"},"properties":{"channel":"stable"}}`, 201, ""},
		"properties on a plain Raw source":          {f.source, f.target, `{"coordinate":{"glob":"**"},"properties":{"channel":"stable"}}`, 422, "unsupported_match"},
		"properties on a PyPI source":               {f.pypi, pypiTarget, `{"coordinate":{"glob":"**"},"properties":{"channel":"stable"}}`, 422, "unsupported_match"},
		"release on a PyPI source (no ReleaseSet)":  {f.pypi, pypiTarget, `{"coordinate":{"glob":"**"},"scope":"release"}`, 422, "unsupported_format"},
		"release on a Raw source (ReleaseSet)":      {f.source, f.target, `{"coordinate":{"glob":"**"},"scope":"release"}`, 201, ""},
		"any rule on a format without promotion":    {nugetSource, nugetSource, `{"coordinate":{"glob":"**"}}`, 422, "unsupported_format"},
		"release rejection beats a pattern problem": {f.pypi, pypiTarget, `{"coordinate":{"glob":"**"},"scope":"release","properties":{"k":"v"}}`, 422, ""},
	} {
		got := f.create(f.promoter, f.ruleBody("cap-"+uuid.NewString(), tc.source.ID, tc.target.ID, tc.match))
		if got.Code != tc.status || (tc.code != "" && ruleProblemCode(t, got) != tc.code) {
			t.Errorf("%s = %d %s, want %d %s", name, got.Code, got.Body.String(), tc.status, tc.code)
		}
	}
}

func TestPromotionRuleReferencesAndRemoteTargets(t *testing.T) {
	f := newRuleFixture(t)
	match := `{"coordinate":{"glob":"**"}}`
	remote := func(instance, repo string) string {
		return fmt.Sprintf(`{"name":"r-%s","sourceRepositoryId":%q,"target":{"kind":"remote","remoteInstanceId":%q,"remoteRepository":%q},"match":%s,"runAsServiceAccountId":%q}`, uuid.NewString(), f.source.ID, instance, repo, match, f.account.ID)
	}
	if got := f.create(f.promoter, remote(f.remote.ID, "releases")); got.Code != http.StatusForbidden {
		t.Fatalf("a remote target needs a platform administrator: %d %s", got.Code, got.Body.String())
	}
	got := f.create("admin-secret", remote(f.remote.ID, "releases"))
	if got.Code != http.StatusCreated {
		t.Fatalf("remote create = %d %s", got.Code, got.Body.String())
	}
	rule := decodeRule(t, got)
	if rule.Target.Kind != "remote" || rule.Target.RemoteInstanceId == nil || rule.Target.RemoteInstanceId.String() != f.remote.ID || rule.Target.RemoteRepository == nil || *rule.Target.RemoteRepository != "releases" || rule.Target.RepositoryId != nil {
		t.Fatalf("remote rule = %+v", rule.Target)
	}
	if got = f.create("admin-secret", remote(uuid.NewString(), "releases")); got.Code != http.StatusUnprocessableEntity || ruleProblemCode(t, got) != "unknown_remote_instance" {
		t.Fatalf("unknown instance = %d %s", got.Code, got.Body.String())
	}
	if got = f.create("admin-secret", remote(f.remote.ID, "")); got.Code != http.StatusBadRequest {
		t.Fatalf("empty remote repository = %d %s", got.Code, got.Body.String())
	}
	mixed := strings.Replace(remote(f.remote.ID, "r"), `"remoteRepository":"r"`, `"remoteRepository":"r","repositoryId":"`+f.target.ID+`"`, 1)
	if got = f.create("admin-secret", mixed); got.Code != http.StatusBadRequest {
		t.Fatalf("remote target with a local repository = %d %s", got.Code, got.Body.String())
	}
	unknown := strings.Replace(f.ruleBody("x", f.source.ID, f.target.ID, match), f.account.ID, uuid.NewString(), 1)
	if got = f.create(f.promoter, unknown); got.Code != http.StatusUnprocessableEntity || ruleProblemCode(t, got) != "unknown_service_account" {
		t.Fatalf("unknown service account = %d %s", got.Code, got.Body.String())
	}
	disabled := repository.ServiceAccountDisabled
	if _, err := f.store.UpdateServiceAccount(context.Background(), repository.ServiceAccountUpdate{ID: f.account.ID, State: &disabled}, f.account.Version); err != nil {
		t.Fatal(err)
	}
	if got = f.create(f.promoter, f.ruleBody("x", f.source.ID, f.target.ID, match)); got.Code != http.StatusUnprocessableEntity || ruleProblemCode(t, got) != "service_account_disabled" {
		t.Fatalf("disabled service account = %d %s", got.Code, got.Body.String())
	}
	if got = f.create(f.promoter, f.ruleBody("x", uuid.NewString(), f.target.ID, match)); got.Code != http.StatusNotFound {
		t.Fatalf("unknown source = %d", got.Code)
	}
	if got = f.create(f.promoter, f.ruleBody("x", f.source.ID, f.source.ID, match)); got.Code != http.StatusConflict || ruleProblemCode(t, got) != "invalid_target" {
		t.Fatalf("a rule onto its own source = %d %s", got.Code, got.Body.String())
	}
	if got = f.create(f.promoter, f.ruleBody("x", f.source.ID, f.pypi.ID, match)); got.Code != http.StatusConflict {
		t.Fatalf("a target of another format = %d %s", got.Code, got.Body.String())
	}
}

func TestPromotionRuleNameAndCap(t *testing.T) {
	f := newRuleFixture(t, func(o *GatewayOptions) { o.PromotionMaxRulesPerRepository = 2 })
	match := `{"coordinate":{"glob":"**"}}`
	for _, name := range []string{"one", "two"} {
		if got := f.create(f.promoter, f.ruleBody(name, f.source.ID, f.target.ID, match)); got.Code != http.StatusCreated {
			t.Fatalf("%s = %d %s", name, got.Code, got.Body.String())
		}
	}
	if got := f.create(f.promoter, f.ruleBody("one", f.source.ID, f.target.ID, match)); got.Code != http.StatusConflict || ruleProblemCode(t, got) != "name_exists" {
		t.Fatalf("duplicate = %d %s", got.Code, got.Body.String())
	}
	if got := f.create(f.promoter, f.ruleBody("three", f.source.ID, f.target.ID, match)); got.Code != http.StatusConflict || ruleProblemCode(t, got) != "rule_limit_reached" {
		t.Fatalf("over the cap = %d %s", got.Code, got.Body.String())
	}
	// The cap is per source repository.
	if got := f.create(f.promoter, f.ruleBody("three", f.target.ID, f.source.ID, match)); got.Code != http.StatusCreated {
		t.Fatalf("another source = %d %s", got.Code, got.Body.String())
	}
}

// The creator may not delegate more than it holds: a service account that holds an operation on a repository of the
// rule that the creator does not hold is refused, unless the creator administers every service account.
func TestPromotionRuleConfusedDeputyRefusal(t *testing.T) {
	f := newRuleFixture(t)
	ctx := context.Background()
	authenticator := testAuthenticator()
	// Authority outside the grant sets comes from this fallback: the service account may write, "promoter" may do
	// everything, and "ops-lead" may administer but not write. The repositories below have no managed grant set, so the
	// fallback decides for all three.
	fallback := func(principal Principal, _ repository.HostedRepository, operation RepositoryOperation) AuthorizationDecision {
		switch {
		case strings.HasPrefix(principal.Actor, "service-account:") && operation == RepositoryWrite:
			return AuthorizationDecision{Allowed: true, Source: "legacy_static", Reason: "write_pattern_granted"}
		case principal.Actor == "promoter":
			return AuthorizationDecision{Allowed: true, Source: "legacy_static", Reason: "all_granted"}
		case principal.Actor == "ops-lead" && operation == RepositoryAdmin:
			return AuthorizationDecision{Allowed: true, Source: "legacy_static", Reason: "admin_pattern_granted"}
		}
		return AuthorizationDecision{Source: "legacy_static", Reason: "scope_not_granted"}
	}
	handler := newPromotionRuleHandler(newManagementScope(f.store, f.store, f.store, f.store, authenticator, nil), f.store, generatedRepositoryAPIAdapter{}, 0)
	handler.authorizer = authorization.RepositoryAuthorizer{Grants: f.store, Legacy: authenticator, LegacyFallback: fallback}
	// Repositories without a managed grant set, so the fallback decides.
	deputySource, err := f.store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "deputy-source", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	deputyTarget, err := f.store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "deputy-target", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	repositories := []repository.HostedRepository{deputySource, deputyTarget}
	if _, _, denied := handler.confusedDeputy(ctx, Principal{Actor: "promoter"}, f.account.ID, repositories); denied {
		t.Fatal("a creator whose admin grant implies write holds everything the service account holds")
	}
	repo, operation, denied := handler.confusedDeputy(ctx, Principal{Actor: "ops-lead"}, f.account.ID, repositories)
	if !denied || repo.ID != deputySource.ID || operation != RepositoryWrite {
		t.Fatalf("a creator without write was not refused: %v %s %s", denied, repo.Name, operation)
	}
	if _, _, denied = handler.confusedDeputy(ctx, Principal{Actor: "alice", Admin: true}, f.account.ID, repositories); denied {
		t.Fatal("a platform administrator administers every service account")
	}

	// End to end: ops-lead administers both repositories, so only the guard stands between it and the rule.
	create := func(name string, token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, rulesPath, strings.NewReader(f.ruleBody(name, deputySource.ID, deputyTarget.ID, `{"coordinate":{"glob":"**"}}`)))
		authorize(request, token)
		response := httptest.NewRecorder()
		handler.CreatePromotionRule(response, request)
		return response
	}
	refused := create("deputy", authenticator.IssueToken("ops-lead"))
	if refused.Code != http.StatusForbidden || ruleProblemCode(t, refused) != "confused_deputy" {
		t.Fatalf("delegation of more than the creator holds = %d %s", refused.Code, refused.Body.String())
	}
	if page, err := f.store.PagePromotionRules(ctx, repository.PromotionRuleQuery{}); err != nil || len(page.Items) != 0 {
		t.Fatalf("a refused rule was stored: %d rules, %v", len(page.Items), err)
	}
	if allowed := create("covered", authenticator.IssueToken("promoter")); allowed.Code != http.StatusCreated {
		t.Fatalf("a creator that holds what the service account holds = %d %s", allowed.Code, allowed.Body.String())
	}
	if byAdmin := create("by-admin", "admin-secret"); byAdmin.Code != http.StatusCreated {
		t.Fatalf("a platform administrator = %d %s", byAdmin.Code, byAdmin.Body.String())
	}
}

func TestPromotionRuleDryRunNeverEnqueuesAndReportsVerdicts(t *testing.T) {
	f := newRuleFixture(t)
	ctx := context.Background()
	// Quarantine one asset, give another a clean scan, and tighten the target policy to require a signature.
	quarantined, clean := f.assets[0], f.assets[1]
	if _, err := f.store.ReplaceArtifactQuarantine(ctx, repository.ArtifactQuarantine{RepositoryID: f.source.ID, Format: repository.FormatRaw, Coordinate: quarantined.Path, Digest: quarantined.Digest,
		State: repository.ArtifactQuarantineStateQuarantined, Reason: "suspicious", UpdatedBy: "alice"}, "0"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReplaceArtifactIntelligence(ctx, repository.ArtifactIntelligence{RepositoryID: f.source.ID, Format: repository.FormatRaw, Coordinate: clean.Path, Digest: clean.Digest,
		Vulnerability: &repository.ArtifactVulnerabilitySummary{Scanner: "ref", Status: "clean", ScannedAt: time.Now(), Medium: 1}, UpdatedBy: "ci"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReplaceRepositorySecurityPolicy(ctx, f.target.ID, repository.RepositorySecurityPolicy{Version: "1", Enabled: true, RequireSignature: true, MaxAllowedSeverity: "critical", AllowedLicenses: []string{}}, "1"); err != nil {
		t.Fatal(err)
	}
	conditions := `"conditions":{"securityPolicy":"pass","scan":{"completed":true,"maxSeverity":"low"},"minAge":"1ms","requireSbom":true}`
	created := f.create(f.promoter, f.ruleBody("dry", f.source.ID, f.target.ID, `{"coordinate":{"glob":"releases/**"}}`, conditions))
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	rule := decodeRule(t, created)
	time.Sleep(5 * time.Millisecond)
	run := f.do(http.MethodPost, rulesPath+"/"+rule.Id.String()+"/dry-run", f.promoter, "")
	if run.Code != http.StatusOK {
		t.Fatalf("dry run = %d %s", run.Code, run.Body.String())
	}
	var result adminopenapi.PromotionRuleDryRun
	if err := json.Unmarshal(run.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.RuleId != rule.Id || result.RuleVersion != "1" || result.Examined != 3 || result.Matched != 2 || result.Truncated || len(result.Items) != 2 {
		t.Fatalf("result = %+v", result)
	}
	byCoordinate := map[string]adminopenapi.PromotionRuleDryRunItem{}
	for _, item := range result.Items {
		byCoordinate[item.Coordinate] = item
	}
	verdicts := func(item adminopenapi.PromotionRuleDryRunItem) map[string]string {
		out := map[string]string{}
		for _, condition := range item.Conditions {
			reason := ""
			if condition.Reason != nil {
				reason = ":" + *condition.Reason
			}
			out[string(condition.Condition)] = string(condition.Verdict) + reason
		}
		return out
	}
	q := verdicts(byCoordinate[quarantined.Path])
	if byCoordinate[quarantined.Path].Outcome != "blocked" || q["notQuarantined"] != "fail:"+repository.ArtifactQuarantinedReason || q["scan"] != "fail:scan_not_completed" || q["minAge"] != "pass" {
		t.Fatalf("quarantined = %+v", q)
	}
	c := verdicts(byCoordinate[clean.Path])
	if byCoordinate[clean.Path].Outcome != "blocked" || c["notQuarantined"] != "pass" || c["scan"] != "fail:severity_above_low" || c["minAge"] != "pass" ||
		!strings.HasPrefix(c["securityPolicy"], "fail:") || !strings.Contains(c["securityPolicy"], "signature_required") || c["requireSbom"] != "fail:sbom_required" {
		t.Fatalf("clean = %+v", c)
	}
	if _, matched := byCoordinate[f.assets[2].Path]; matched {
		t.Fatal("snapshots/ does not match releases/**")
	}
	// Nothing was enqueued or recorded.
	if jobs, err := f.store.ListLifecycleJobs(ctx, f.target.ID, 50); err != nil || len(jobs) != 0 {
		t.Fatalf("target jobs = %d, %v", len(jobs), err)
	}
	if jobs, err := f.store.ListLifecycleJobs(ctx, f.source.ID, 50); err != nil || len(jobs) != 0 {
		t.Fatalf("source jobs = %d, %v", len(jobs), err)
	}
	if page, err := f.store.PageArtifactPromotions(ctx, repository.ArtifactPromotionQuery{Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("promotions = %d, %v", len(page.Items), err)
	}

	// limit cuts the answer and says so; a limit outside the bounds is refused.
	one := f.do(http.MethodPost, rulesPath+"/"+rule.Id.String()+"/dry-run?limit=1", f.promoter, "")
	var limited adminopenapi.PromotionRuleDryRun
	if err := json.Unmarshal(one.Body.Bytes(), &limited); err != nil || one.Code != http.StatusOK || len(limited.Items) != 1 || limited.Matched != 2 || !limited.Truncated {
		t.Fatalf("limited = %d %s", one.Code, one.Body.String())
	}
	if got := f.do(http.MethodPost, rulesPath+"/"+rule.Id.String()+"/dry-run?limit=501", f.promoter, ""); got.Code != http.StatusBadRequest {
		t.Fatalf("limit 501 = %d", got.Code)
	}
	if got := f.do(http.MethodPost, rulesPath+"/"+uuid.NewString()+"/dry-run", f.promoter, ""); got.Code != http.StatusNotFound {
		t.Fatalf("unknown rule = %d", got.Code)
	}

	// A rule with only the implied condition is eligible once the artifact is not quarantined; an early minAge keeps it back.
	plain := decodeRule(t, f.create(f.promoter, f.ruleBody("plain", f.source.ID, f.target.ID, `{"coordinate":{"regex":"releases/app-1\\.1\\.0\\.tgz"}}`, `"conditions":{"minAge":"72h"}`)))
	young := decodeDryRun(t, f.do(http.MethodPost, rulesPath+"/"+plain.Id.String()+"/dry-run", f.promoter, ""))
	if len(young.Items) != 1 || young.Items[0].Outcome != "blocked" || verdicts(young.Items[0])["minAge"] != "fail:younger_than_min_age" {
		t.Fatalf("young = %+v", young)
	}
}

func decodeDryRun(t *testing.T, response *httptest.ResponseRecorder) adminopenapi.PromotionRuleDryRun {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("dry run = %d %s", response.Code, response.Body.String())
	}
	var result adminopenapi.PromotionRuleDryRun
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPromotionRuleDryRunEligibleAndRemoteTargets(t *testing.T) {
	f := newRuleFixture(t)
	ctx := context.Background()
	for _, asset := range f.assets {
		if _, err := f.store.ReplaceArtifactIntelligence(ctx, repository.ArtifactIntelligence{RepositoryID: f.source.ID, Format: repository.FormatRaw, Coordinate: asset.Path, Digest: asset.Digest,
			Signatures: []repository.ArtifactSignature{{KeyID: "k", Algorithm: "ed25519", Identity: "ci", Signature: "c2ln", Verified: true}}, SBOMs: []repository.ArtifactSBOM{{MediaType: "application/spdx+json", Digest: "sha256:" + strings.Repeat("a", 64)}}, Provenance: &repository.ArtifactProvenance{Builder: "ci", BuildType: "build", SourceRepository: "https://git.example/r", SourceCommit: strings.Repeat("b", 40), BuildID: "1"},
			Vulnerability: &repository.ArtifactVulnerabilitySummary{Scanner: "ref", Status: "clean"}, UpdatedBy: "ci"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	all := `"conditions":{"scan":{"completed":true,"maxSeverity":"none"},"requireSignature":true,"requireSbom":true,"requireProvenance":true}`
	local := decodeRule(t, f.create(f.promoter, f.ruleBody("eligible", f.source.ID, f.target.ID, `{"coordinate":{"glob":"**"}}`, all)))
	result := decodeDryRun(t, f.do(http.MethodPost, rulesPath+"/"+local.Id.String()+"/dry-run", f.promoter, ""))
	if result.Matched != 3 || len(result.Items) != 3 {
		t.Fatalf("result = %+v", result)
	}
	for _, item := range result.Items {
		if item.Outcome != "eligible" {
			t.Fatalf("item = %+v", item)
		}
	}
	// A remote target's admission policy is the remote's: reported as not evaluated rather than guessed.
	remoteBody := fmt.Sprintf(`{"name":"remote-dry","sourceRepositoryId":%q,"target":{"kind":"remote","remoteInstanceId":%q,"remoteRepository":"r"},"match":{"coordinate":{"glob":"**"}},"conditions":{"securityPolicy":"pass"},"runAsServiceAccountId":%q}`, f.source.ID, f.remote.ID, f.account.ID)
	remote := decodeRule(t, f.create("admin-secret", remoteBody))
	if got := f.do(http.MethodPost, rulesPath+"/"+remote.Id.String()+"/dry-run", f.promoter, ""); got.Code != http.StatusForbidden {
		t.Fatalf("a remote rule is dry-run by an administrator only: %d", got.Code)
	}
	remoteResult := decodeDryRun(t, f.do(http.MethodPost, rulesPath+"/"+remote.Id.String()+"/dry-run", "admin-secret", ""))
	if len(remoteResult.Items) != 3 {
		t.Fatalf("remote result = %+v", remoteResult)
	}
	for _, item := range remoteResult.Items {
		found := false
		for _, condition := range item.Conditions {
			if condition.Condition == "securityPolicy" {
				found = condition.Verdict == "not_evaluated" && condition.Reason != nil && *condition.Reason == "remote_target_policy"
			}
		}
		if !found || item.Outcome != "undetermined" {
			t.Fatalf("remote item = %+v", item)
		}
	}
}
