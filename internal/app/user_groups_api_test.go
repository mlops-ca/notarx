package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/identity/saml/samltest"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

func userGroupCaller(handler http.Handler) func(method, path, body, token, ifMatch string) *httptest.ResponseRecorder {
	return func(method, path, body, token, ifMatch string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			authorize(r, token)
		}
		if ifMatch != "" {
			r.Header.Set("If-Match", ifMatch)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
}

func TestUserGroupManagementHTTP(t *testing.T) {
	store := repository.NewMemoryStore()
	user, err := store.CreateUser(t.Context(), repository.User{ID: uuid.NewString(), Name: "ada", Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	request := userGroupCaller(handler)

	if got := request(http.MethodGet, "/api/v2/user-groups", "", "", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list = %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodGet, "/api/v2/user-groups", "", "writer-secret", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("non-admin list = %d %s", got.Code, got.Body.String())
	}
	created := request(http.MethodPost, "/api/v2/user-groups", `{"displayName":"Platform"}`, "admin-secret", "")
	if created.Code != http.StatusCreated || created.Header().Get("ETag") != "1" {
		t.Fatalf("create = %d etag=%q %s", created.Code, created.Header().Get("ETag"), created.Body.String())
	}
	var group struct {
		ID         string `json:"id"`
		Source     string `json:"source"`
		GlobalRole string `json:"globalRole"`
		Version    string `json:"version"`
	}
	if err := json.NewDecoder(created.Body).Decode(&group); err != nil || group.ID == "" || group.Source != "local" || group.GlobalRole != "none" {
		t.Fatalf("group = %#v err=%v", group, err)
	}
	if got := request(http.MethodPost, "/api/v2/user-groups", `{"displayName":"platform"}`, "admin-secret", ""); got.Code != http.StatusConflict {
		t.Fatalf("duplicate name = %d %s", got.Code, got.Body.String())
	}
	// SCIM groups are created by the SCIM server, never through this API, and a claims group needs its provider.
	for name, body := range map[string]string{
		"scim source":          `{"displayName":"s","source":"scim"}`,
		"claims without ids":   `{"displayName":"c","source":"claims"}`,
		"local with an ext id": `{"displayName":"l","externalId":"x"}`,
		"unknown field":        `{"displayName":"l","globalRole":"admin"}`,
		"empty name":           `{"displayName":"  "}`,
	} {
		if got := request(http.MethodPost, "/api/v2/user-groups", body, "admin-secret", ""); got.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, got.Code, got.Body.String())
		}
	}

	path := "/api/v2/user-groups/" + group.ID
	if got := request(http.MethodGet, path, "", "admin-secret", ""); got.Code != http.StatusOK || got.Header().Get("ETag") != "1" {
		t.Fatalf("get = %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodGet, "/api/v2/user-groups/"+uuid.NewString(), "", "admin-secret", ""); got.Code != http.StatusNotFound {
		t.Fatalf("unknown group = %d", got.Code)
	}
	renamed := request(http.MethodPut, path, `{"displayName":"Platform Team"}`, "admin-secret", "1")
	if renamed.Code != http.StatusOK || renamed.Header().Get("ETag") != "2" || !strings.Contains(renamed.Body.String(), "Platform Team") {
		t.Fatalf("rename = %d %s", renamed.Code, renamed.Body.String())
	}
	if got := request(http.MethodPut, path, `{"displayName":"Stale"}`, "admin-secret", "1"); got.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale rename = %d %s", got.Code, got.Body.String())
	}
	flagged := request(http.MethodPut, path+"/global-role", `{"globalRole":"member"}`, "admin-secret", "2")
	if flagged.Code != http.StatusOK || !strings.Contains(flagged.Body.String(), `"globalRole":"member"`) {
		t.Fatalf("set global role = %d %s", flagged.Code, flagged.Body.String())
	}
	if got := request(http.MethodPut, path+"/global-role", `{"globalRole":"owner"}`, "admin-secret", "3"); got.Code != http.StatusBadRequest {
		t.Fatalf("unknown role = %d %s", got.Code, got.Body.String())
	}

	members := path + "/members"
	if got := request(http.MethodPost, members, `{"userId":"`+user.ID+`"}`, "admin-secret", ""); got.Code != http.StatusNoContent {
		t.Fatalf("add member = %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodPost, members, `{"userId":"`+user.ID+`"}`, "admin-secret", ""); got.Code != http.StatusConflict {
		t.Fatalf("duplicate member = %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodPost, members, `{"userId":"`+uuid.NewString()+`"}`, "admin-secret", ""); got.Code != http.StatusNotFound {
		t.Fatalf("unknown user = %d %s", got.Code, got.Body.String())
	}
	listed := request(http.MethodGet, members, "", "admin-secret", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"userName":"ada"`) || !strings.Contains(listed.Body.String(), `"source":"local"`) {
		t.Fatalf("members = %d %s", listed.Code, listed.Body.String())
	}
	if got := request(http.MethodGet, "/api/v2/user-groups?limit=1", "", "admin-secret", ""); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "Platform Team") {
		t.Fatalf("list = %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodGet, "/api/v2/user-groups?pageToken=garbage", "", "admin-secret", ""); got.Code != http.StatusBadRequest {
		t.Fatalf("bad page token = %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodDelete, members+"/"+user.ID, "", "admin-secret", ""); got.Code != http.StatusNoContent {
		t.Fatalf("remove member = %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodDelete, members+"/"+user.ID, "", "admin-secret", ""); got.Code != http.StatusNotFound {
		t.Fatalf("second removal = %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodDelete, path, "", "admin-secret", ""); got.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", got.Code, got.Body.String())
	}

	audits := map[string]repository.AuditRecord{}
	for _, record := range store.Audits {
		audits[record.Operation] = record
	}
	for operation, status := range map[string]int{
		"user_group.create": http.StatusCreated, "user_group.rename": http.StatusOK, "user_group.set_global_role": http.StatusOK,
		"user_group.member_add": http.StatusNoContent, "user_group.member_remove": http.StatusNoContent, "user_group.delete": http.StatusNoContent,
	} {
		record, ok := audits[operation]
		if !ok || record.Status != status || record.Actor != "alice" || !strings.HasPrefix(record.Resource, "user-groups/") {
			t.Errorf("audit %s = %#v ok=%v", operation, record, ok)
		}
	}
	if got := audits["user_group.set_global_role"].AuthorizationReason; got != "global_role_member" {
		t.Errorf("the flag is audited with the role it sets, got %q", got)
	}
}

func TestGroupPrincipalGrantsAreValidatedAndSimulatedWithGroups(t *testing.T) {
	store := repository.NewMemoryStore()
	ctx := context.Background()
	repo, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "group-raw", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(ctx, repository.User{ID: uuid.NewString(), Name: "ada", Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateUserGroup(ctx, repository.UserGroup{DisplayName: "writers", Source: repository.UserGroupSourceLocal})
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	request := userGroupCaller(handler)
	grants := "/api/v2/repositories/" + repo.ID + "/grants"

	for _, principal := range []string{"group:not-a-uuid", "group:", "group:" + strings.ToUpper(group.ID)} {
		body, _ := json.Marshal(map[string]any{"principal": principal, "scopes": []string{"repositories:write"}})
		if got := request(http.MethodPost, grants, string(body), "admin-secret", ""); got.Code != http.StatusBadRequest {
			t.Errorf("principal %q = %d %s", principal, got.Code, got.Body.String())
		}
	}
	body, _ := json.Marshal(map[string]any{"principal": "group:" + group.ID, "scopes": []string{"repositories:write"}})
	if got := request(http.MethodPost, grants, string(body), "admin-secret", ""); got.Code != http.StatusOK {
		t.Fatalf("group grant = %d %s", got.Code, got.Body.String())
	}

	simulate := func(actor string) string {
		got := request(http.MethodGet, "/api/v2/repositories/"+repo.ID+"/effective-access?actor="+url.QueryEscape(actor), "", "admin-secret", "")
		if got.Code != http.StatusOK {
			t.Fatalf("simulate %s = %d %s", actor, got.Code, got.Body.String())
		}
		return got.Body.String()
	}
	if body := simulate("user:ada"); !strings.Contains(body, `"write":{"allowed":false`) || strings.Contains(body, "groupIds") {
		t.Fatalf("a non-member simulation = %s", body)
	}
	if err := store.AddUserGroupMember(ctx, group.ID, user.ID, repository.UserGroupSourceLocal); err != nil {
		t.Fatal(err)
	}
	if body := simulate("user:ada"); !strings.Contains(body, `"write":{"allowed":true,"reason":"scope_granted","source":"repository_grants"}`) || !strings.Contains(body, group.ID) {
		t.Fatalf("the simulation does not include the group: %s", body)
	}
	// An actor that merely spells the group principal gets nothing from it.
	if body := simulate("group:" + group.ID); !strings.Contains(body, `"write":{"allowed":false`) {
		t.Fatalf("a spoofed actor was granted through the group: %s", body)
	}
}

func TestSAMLLoginPopulatesClaimsGroupsOfTheProviderOnly(t *testing.T) {
	f := newSAMLFixture(t, nil)
	f.handler.groups = f.store
	engineers, err := f.store.CreateUserGroup(t.Context(), repository.UserGroup{DisplayName: "Engineers", Source: repository.UserGroupSourceClaims, ProviderID: f.provider.ID, ExternalID: "engineers"})
	if err != nil {
		t.Fatal(err)
	}
	admins, err := f.store.CreateUserGroup(t.Context(), repository.UserGroup{DisplayName: "Admins", Source: repository.UserGroupSourceLocal, GlobalRole: repository.UserGroupRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	signIn := func(attributes string, assertionID string) {
		t.Helper()
		login := f.begin("/")
		params := redirectParams(t, f.post(&login, f.response(samltest.Response{RequestID: login.requestID, NameID: "persistent-1", AssertionID: assertionID, Attributes: attributes}), login.relay))
		if params.Get("oidc") != "success" {
			t.Fatalf("sign-in params = %v", params)
		}
	}
	signIn(samltest.Attribute("groups", "engineers", admins.ID, "Admins"), "_first")
	user, _, err := f.store.GetUserByProviderIdentity(t.Context(), f.provider.ID, "persistent-1")
	if err != nil {
		t.Fatal(err)
	}
	memberships, err := f.store.ListUserGroupMemberships(t.Context(), user.ID)
	if err != nil || len(memberships) != 1 || memberships[0].GroupID != engineers.ID {
		t.Fatalf("memberships = %#v err=%v, want only the claims group", memberships, err)
	}
	signIn(samltest.Attribute("groups", "other"), "_second")
	if memberships, _ = f.store.ListUserGroupMemberships(t.Context(), user.ID); len(memberships) != 0 {
		t.Fatalf("memberships after the group was withdrawn = %#v", memberships)
	}
}

func completeStubLogin(t *testing.T, handler http.Handler, idp *stubOIDCProvider, slug string) *httptest.ResponseRecorder {
	t.Helper()
	flowCookie, state := startStubLogin(t, handler, idp, slug, "/auth/oidc/"+slug+"/login")
	callback := httptest.NewRequest(http.MethodGet, "/auth/oidc/"+slug+"/callback?code=authorization-code&state="+url.QueryEscape(state), nil)
	callback.AddCookie(flowCookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, callback)
	if response.Code != http.StatusFound || !strings.Contains(response.Header().Get("Location"), "oidc=success") {
		t.Fatalf("callback = %d location=%q", response.Code, response.Header().Get("Location"))
	}
	return response
}

func TestOIDCLoginPopulatesClaimsGroupsAndTreatsEntraOverageAsNoGroups(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	idp := newStubOIDCProvider(t, "groups-key", "gitlab-user", nil)
	store := repository.NewMemoryStore()
	runtime := NewIdentityRuntime(store)
	corp, err := runtime.Create(t.Context(), corpProviderInput("corp", idp))
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.CreateUser(t.Context(), repository.User{ID: uuid.NewString(), Name: "local-user", Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateUserIdentity(t.Context(), repository.UserIdentity{ID: uuid.NewString(), UserID: user.ID, Kind: repository.UserIdentityOIDC, ProviderID: corp.ID, Issuer: idp.URL, Subject: "gitlab-user"}); err != nil {
		t.Fatal(err)
	}
	engineers, err := store.CreateUserGroup(t.Context(), repository.UserGroup{DisplayName: "Engineers", Source: repository.UserGroupSourceClaims, ProviderID: corp.ID, ExternalID: "engineers"})
	if err != nil {
		t.Fatal(err)
	}
	admins, err := store.CreateUserGroup(t.Context(), repository.UserGroup{DisplayName: "Admins", Source: repository.UserGroupSourceLocal, GlobalRole: repository.UserGroupRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{IdentityRuntime: runtime}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})

	// The token asserts the claims group, the id and the name of a privileged local group.
	idp.setExtraClaims(map[string]any{"groups": []string{"engineers", admins.ID, "Admins"}})
	completeStubLogin(t, handler, idp, "corp")
	memberships, err := store.ListUserGroupMemberships(t.Context(), user.ID)
	if err != nil || len(memberships) != 1 || memberships[0].GroupID != engineers.ID || memberships[0].MemberSource != repository.UserGroupSourceClaims {
		t.Fatalf("memberships after a login = %#v err=%v, want only the claims group", memberships, err)
	}

	// The groups claim overflowed: no groups, never a partial list, and an audit event.
	idp.setExtraClaims(map[string]any{"hasgroups": true, "_claim_names": map[string]any{"groups": "src1"}, "groups": []string{"engineers"}})
	completeStubLogin(t, handler, idp, "corp")
	if memberships, _ = store.ListUserGroupMemberships(t.Context(), user.ID); len(memberships) != 0 {
		t.Fatalf("memberships after an overage login = %#v, want none", memberships)
	}
	overage := 0
	for _, record := range store.Audits {
		if record.Operation == "auth.oidc.groups_overage" && record.AuthorizationReason == "groups_overage" && record.Actor == "oidc:corp" && record.Resource == "auth/oidc/corp" {
			overage++
		}
	}
	if overage != 1 {
		t.Fatalf("overage audit events = %d, want 1", overage)
	}
}
