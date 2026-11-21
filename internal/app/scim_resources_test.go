package app

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/repository"
)

func (h *scimHarness) createUser(userName, externalID string) string {
	h.t.Helper()
	body := `{"userName":"` + userName + `","displayName":"Test ` + userName + `","emails":[{"value":"` + userName + `","primary":true,"type":"work"}]`
	if externalID != "" {
		body += `,"externalId":"` + externalID + `"`
	}
	created := h.scim(http.MethodPost, "/Users", body+"}")
	if created.Code != http.StatusCreated {
		h.t.Fatalf("create %s = %d %s", userName, created.Code, created.Body.String())
	}
	return decodeSCIM(h.t, created)["id"].(string)
}

func (h *scimHarness) createGroup(displayName, externalID string, members ...string) string {
	h.t.Helper()
	list := make([]string, 0, len(members))
	for _, member := range members {
		list = append(list, `{"value":"`+member+`"}`)
	}
	created := h.scim(http.MethodPost, "/Groups", `{"displayName":"`+displayName+`","externalId":"`+externalID+`","members":[`+strings.Join(list, ",")+`]}`)
	if created.Code != http.StatusCreated {
		h.t.Fatalf("create group %s = %d %s", displayName, created.Code, created.Body.String())
	}
	return decodeSCIM(h.t, created)["id"].(string)
}

func TestSCIMUserLifecycle(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true, MaxPage: 50})
	created := h.scim(http.MethodPost, "/Users", `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"Ada@Example.com","externalId":"ext-ada","displayName":"Ada Lovelace",
		"name":{"givenName":"Ada","familyName":"Lovelace"},"emails":[{"value":"ada@example.com","primary":true,"type":"work"}],"active":true}`)
	if created.Code != http.StatusCreated || !strings.HasPrefix(created.Header().Get("Location"), "https://notarx.example.test/scim/v2/Users/") || created.Header().Get("ETag") != `W/"1"` {
		t.Fatalf("create = %d %v %s", created.Code, created.Header(), created.Body.String())
	}
	user := decodeSCIM(t, created)
	id := user["id"].(string)
	if user["userName"] != "Ada@Example.com" || user["externalId"] != "ext-ada" || user["active"] != true || user["displayName"] != "Ada Lovelace" ||
		user["emails"].([]any)[0].(map[string]any)["value"] != "ada@example.com" || user["meta"].(map[string]any)["resourceType"] != "User" {
		t.Fatalf("user = %v", user)
	}
	stored, err := h.store.GetUser(t.Context(), id)
	if err != nil || stored.Role != "none" || stored.State != repository.UserActive || stored.SecretHash != "" {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	identities, err := h.store.ListUserIdentities(t.Context(), id)
	if err != nil || len(identities) != 1 || identities[0].Subject != "" || identities[0].SCIMExternalID != "ext-ada" || identities[0].ProviderID != h.provider.ID {
		t.Fatalf("identities = %+v, %v", identities, err)
	}

	// A userName matching any account is a uniqueness conflict, case-insensitively; so is a local account.
	expectSCIMError(t, h.scim(http.MethodPost, "/Users", `{"userName":"ada@example.COM"}`), http.StatusConflict, "uniqueness")
	if _, err := h.store.CreateUser(t.Context(), repository.User{ID: uuid.NewString(), Name: "local-user", Role: "member", SecretHash: "bcrypt"}); err != nil {
		t.Fatal(err)
	}
	expectSCIMError(t, h.scim(http.MethodPost, "/Users", `{"userName":"Local-User"}`), http.StatusConflict, "uniqueness")
	expectSCIMError(t, h.scim(http.MethodPost, "/Users", `{"userName":"other","externalId":"ext-ada"}`), http.StatusConflict, "uniqueness")

	fetched := h.scim(http.MethodGet, "/Users/"+id, "")
	if fetched.Code != http.StatusOK || fetched.Header().Get("ETag") != `W/"1"` {
		t.Fatalf("get = %d %v", fetched.Code, fetched.Header())
	}
	for filter, want := range map[string]float64{
		`userName eq "ADA@example.com"`: 1, `externalId eq "ext-ada"`: 1, `userName eq "nobody"`: 0,
	} {
		list := decodeSCIM(t, h.scim(http.MethodGet, "/Users?filter="+url.QueryEscape(filter), ""))
		if list["totalResults"] != want || list["startIndex"] != float64(1) {
			t.Errorf("filter %q = %v", filter, list)
		}
	}
	for _, filter := range []string{`userName ne "a"`, `userName co "a"`, `userName eq "a" and active eq true`, `emails.value eq "a"`, `displayName eq "a"`} {
		expectSCIMError(t, h.scim(http.MethodGet, "/Users?filter="+url.QueryEscape(filter), ""), http.StatusBadRequest, "invalidFilter")
	}

	// Replace: a missing displayName and email are cleared, an absent externalId and active stay.
	replaced := h.scim(http.MethodPut, "/Users/"+id, `{"userName":"ada.l@example.com"}`)
	body := decodeSCIM(t, replaced)
	if replaced.Code != http.StatusOK || body["userName"] != "ada.l@example.com" || body["externalId"] != "ext-ada" || body["active"] != true || body["displayName"] != nil || body["emails"] != nil || replaced.Header().Get("ETag") != `W/"2"` {
		t.Fatalf("replace = %d %s", replaced.Code, replaced.Body.String())
	}
	// If-Match guards the write.
	expectSCIMError(t, h.scimAs(h.token, http.MethodPut, "/Users/"+id, `{"userName":"x"}`, map[string]string{"If-Match": `W/"1"`}), http.StatusPreconditionFailed, "invalidVers")
	if matched := h.scimAs(h.token, http.MethodPatch, "/Users/"+id, `{"Operations":[{"op":"replace","path":"displayName","value":"Ada L"}]}`, map[string]string{"If-Match": `W/"2"`}); matched.Code != http.StatusOK {
		t.Fatalf("matching If-Match = %d %s", matched.Code, matched.Body.String())
	}
	if star := h.scimAs(h.token, http.MethodPatch, "/Users/"+id, `{"Operations":[{"op":"replace","path":"displayName","value":"Ada M"}]}`, map[string]string{"If-Match": "*"}); star.Code != http.StatusOK {
		t.Fatalf("If-Match * = %d", star.Code)
	}

	// Request validation.
	expectSCIMError(t, h.scim(http.MethodPost, "/Users", `{"displayName":"no name"}`), http.StatusBadRequest, "invalidValue")
	expectSCIMError(t, h.scim(http.MethodPost, "/Users", `nope`), http.StatusBadRequest, "invalidSyntax")
	expectSCIMError(t, h.scim(http.MethodPost, "/Users", `{"userName":"`+strings.Repeat("u", 200)+`"}`), http.StatusBadRequest, "invalidValue")
	expectSCIMError(t, h.scim(http.MethodPost, "/Users", `{"userName":"bad-mail","emails":[{"value":"not an address"}]}`), http.StatusBadRequest, "invalidValue")
	expectSCIMError(t, h.scim(http.MethodPost, "/Users", `{"userName":"long","displayName":"`+strings.Repeat("d", 200)+`"}`), http.StatusBadRequest, "invalidValue")
	expectSCIMError(t, h.scim(http.MethodPost, "/Users", `{"userName":"long","externalId":"`+strings.Repeat("e", 300)+`"}`), http.StatusBadRequest, "invalidValue")
	wrong := httptest.NewRequest(http.MethodPost, "/scim/v2/Users", strings.NewReader(`{"userName":"x"}`))
	wrong.Header.Set("Authorization", "Bearer "+h.token)
	wrong.Header.Set("Content-Type", "text/plain")
	wrongResponse := httptest.NewRecorder()
	h.handler.ServeHTTP(wrongResponse, wrong)
	expectSCIMError(t, wrongResponse, http.StatusUnsupportedMediaType, "invalidSyntax")
	for _, contentType := range []string{"application/json", "application/scim+json; charset=utf-8"} {
		request := httptest.NewRequest(http.MethodPost, "/scim/v2/Users", strings.NewReader(`{"userName":"ct-`+strconv.Itoa(len(contentType))+`"}`))
		request.Header.Set("Authorization", "Bearer "+h.token)
		request.Header.Set("Content-Type", contentType)
		recorder := httptest.NewRecorder()
		h.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusCreated {
			t.Errorf("content type %q = %d %s", contentType, recorder.Code, recorder.Body.String())
		}
	}
}

func TestSCIMDeactivationRevokesSessionsAndReactivationRules(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	id := h.createUser("grace@example.com", "ext-grace")
	session, err := h.store.CreateUserSession(t.Context(), repository.UserSession{ID: uuid.NewString(), UserID: id, Kind: repository.UserSessionOIDC, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := h.store.GetUser(t.Context(), id)
	off := h.scim(http.MethodPatch, "/Users/"+id, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"active","value":"False"}]}`)
	if off.Code != http.StatusOK || decodeSCIM(t, off)["active"] != false {
		t.Fatalf("deactivate = %d %s", off.Code, off.Body.String())
	}
	user, _ := h.store.GetUser(t.Context(), id)
	if user.State != repository.UserDisabled || user.DisabledReason != repository.DisabledBySCIM || user.SessionVersion != before.SessionVersion+1 {
		t.Fatalf("user = %+v", user)
	}
	if sessions, _ := h.store.ListUserSessions(t.Context(), id, true); len(sessions) != 1 || sessions[0].ID != session.ID || sessions[0].RevokedAt == nil {
		t.Fatalf("sessions = %+v", sessions)
	}
	// The grants and the identity row are kept, so re-activation restores access.
	if identities, _ := h.store.ListUserIdentities(t.Context(), id); len(identities) != 1 {
		t.Fatalf("identities = %+v", identities)
	}
	on := h.scim(http.MethodPatch, "/Users/"+id, `{"Operations":[{"op":"replace","value":{"active":true}}]}`)
	if on.Code != http.StatusOK || decodeSCIM(t, on)["active"] != true {
		t.Fatalf("reactivate = %d %s", on.Code, on.Body.String())
	}

	// An account an administrator disabled stays disabled.
	manual := h.createUser("heidi@example.com", "ext-heidi")
	current, _ := h.store.GetUser(t.Context(), manual)
	disabled := repository.UserDisabled
	if _, err := h.store.UpdateUser(t.Context(), repository.UserUpdate{ID: manual, State: &disabled}, current.Version); err != nil {
		t.Fatal(err)
	}
	expectSCIMError(t, h.scim(http.MethodPatch, "/Users/"+manual, `{"Operations":[{"op":"replace","path":"active","value":true}]}`), http.StatusConflict, "mutability")
	if after, _ := h.store.GetUser(t.Context(), manual); after.State != repository.UserDisabled || after.DisabledReason != repository.DisabledByAdmin {
		t.Fatalf("a refused re-activation changed the account: %+v", after)
	}

	// DELETE removes the link and disables; the account row stays.
	removed := h.scim(http.MethodDelete, "/Users/"+id, "")
	if removed.Code != http.StatusNoContent || removed.Body.Len() != 0 {
		t.Fatalf("delete = %d %s", removed.Code, removed.Body.String())
	}
	expectSCIMError(t, h.scim(http.MethodGet, "/Users/"+id, ""), http.StatusNotFound, "")
	if kept, err := h.store.GetUser(t.Context(), id); err != nil || kept.State != repository.UserDisabled {
		t.Fatalf("the account after DELETE = %+v, %v", kept, err)
	}
	expectSCIMError(t, h.scim(http.MethodDelete, "/Users/"+id, ""), http.StatusNotFound, "")
}

func TestSCIMCannotTouchAPlatformAdministrator(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	admin, err := h.store.CreateUser(t.Context(), repository.User{ID: uuid.NewString(), Name: "root", Role: "admin", SecretHash: "bcrypt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.LinkProviderIdentity(t.Context(), repository.ProviderIdentityLink{ProviderID: h.provider.ID, UserID: admin.ID, Subject: "root-sub", Issuer: "https://login.example.test", Kind: repository.UserIdentityOIDC}); err != nil {
		t.Fatal(err)
	}
	if got := h.scim(http.MethodGet, "/Users/"+admin.ID, ""); got.Code != http.StatusOK {
		t.Fatalf("reading an administrator = %d", got.Code)
	}
	for name, request := range map[string][3]string{
		"deactivate": {http.MethodPatch, "/Users/" + admin.ID, `{"Operations":[{"op":"replace","path":"active","value":false}]}`},
		"rename":     {http.MethodPatch, "/Users/" + admin.ID, `{"Operations":[{"op":"replace","path":"displayName","value":"x"}]}`},
		"replace":    {http.MethodPut, "/Users/" + admin.ID, `{"userName":"root2"}`},
		"delete":     {http.MethodDelete, "/Users/" + admin.ID, ""},
	} {
		expectSCIMError(t, h.scim(request[0], request[1], request[2]), http.StatusForbidden, "mutability")
		if after, _ := h.store.GetUser(t.Context(), admin.ID); after.State != repository.UserActive || after.Name != "root" || after.DisplayName != "" || after.SessionVersion != admin.SessionVersion {
			t.Fatalf("%s changed the administrator: %+v", name, after)
		}
	}
}

func TestSCIMListPagingByIndexAndCursor(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true, MaxPage: 4})
	for index := 0; index < 10; index++ {
		h.createUser("user"+strconv.Itoa(index)+"@example.com", "")
	}
	page := func(query string) map[string]any {
		t.Helper()
		got := h.scim(http.MethodGet, "/Users?"+query, "")
		if got.Code != http.StatusOK {
			t.Fatalf("list %q = %d %s", query, got.Code, got.Body.String())
		}
		return decodeSCIM(t, got)
	}
	first := page("")
	if first["totalResults"] != float64(10) || first["itemsPerPage"] != float64(4) || first["startIndex"] != float64(1) || first["nextCursor"] == nil {
		t.Fatalf("first page = %v", first)
	}
	// count is capped at NOTARX_SCIM_MAX_PAGE.
	if capped := page("count=1000"); capped["itemsPerPage"] != float64(4) {
		t.Errorf("count above the cap = %v", capped["itemsPerPage"])
	}
	if zero := page("count=0"); zero["itemsPerPage"] != float64(0) || zero["totalResults"] != float64(10) || zero["nextCursor"] != nil {
		t.Errorf("count 0 = %v", zero)
	}
	if negative := page("count=-5"); negative["itemsPerPage"] != float64(0) {
		t.Errorf("negative count = %v", negative)
	}
	// Index paging walks the same order as cursor paging.
	var byIndex, byCursor []string
	for start := 1; start <= 10; start += 4 {
		for _, resource := range page("startIndex=" + strconv.Itoa(start))["Resources"].([]any) {
			byIndex = append(byIndex, resource.(map[string]any)["id"].(string))
		}
	}
	cursor := ""
	for {
		current := page("cursor=" + url.QueryEscape(cursor))
		for _, resource := range current["Resources"].([]any) {
			byCursor = append(byCursor, resource.(map[string]any)["id"].(string))
		}
		next, _ := current["nextCursor"].(string)
		if cursor = next; cursor == "" {
			break
		}
	}
	if len(byIndex) != 10 || strings.Join(byIndex, ",") != strings.Join(byCursor, ",") {
		t.Fatalf("by index %v, by cursor %v", byIndex, byCursor)
	}
	if past := page("startIndex=50"); past["itemsPerPage"] != float64(0) || past["totalResults"] != float64(10) || past["startIndex"] != float64(50) {
		t.Errorf("past the end = %v", past)
	}
	if exact := page("startIndex=11"); exact["itemsPerPage"] != float64(0) {
		t.Errorf("exactly past the end = %v", exact)
	}
	expectSCIMError(t, h.scim(http.MethodGet, "/Users?startIndex=20000", ""), http.StatusBadRequest, "invalidValue")
	expectSCIMError(t, h.scim(http.MethodGet, "/Users?cursor=garbage", ""), http.StatusBadRequest, "invalidValue")
	expectSCIMError(t, h.scim(http.MethodGet, "/Users?count=abc", ""), http.StatusBadRequest, "invalidValue")
	expectSCIMError(t, h.scim(http.MethodGet, "/Users?startIndex=abc", ""), http.StatusBadRequest, "invalidValue")
	if low := page("startIndex=-3"); low["startIndex"] != float64(1) {
		t.Errorf("a startIndex below 1 = %v", low["startIndex"])
	}
}

func TestSCIMGroupLifecycleAndRoleSafety(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	ada, grace := h.createUser("ada@example.com", "e-ada"), h.createUser("grace@example.com", "e-grace")
	// A request cannot name a role: unknown attributes are ignored and the group is created at none.
	created := h.scim(http.MethodPost, "/Groups", `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:Group"],"displayName":"Engineers","externalId":"g-eng","globalRole":"admin","roles":[{"value":"admin"}],"members":[{"value":"`+ada+`"}]}`)
	if created.Code != http.StatusCreated || created.Header().Get("ETag") != `W/"1"` {
		t.Fatalf("create = %d %v %s", created.Code, created.Header(), created.Body.String())
	}
	group := decodeSCIM(t, created)
	groupID := group["id"].(string)
	if group["displayName"] != "Engineers" || group["externalId"] != "g-eng" || len(group["members"].([]any)) != 1 || group["members"].([]any)[0].(map[string]any)["value"] != ada ||
		group["members"].([]any)[0].(map[string]any)["display"] != "ada@example.com" {
		t.Fatalf("group = %v", group)
	}
	stored, err := h.store.GetUserGroup(t.Context(), groupID)
	if err != nil || stored.GlobalRole != repository.UserGroupRoleNone || stored.Source != repository.UserGroupSourceSCIM || stored.ProviderID != h.provider.ID {
		t.Fatalf("stored group = %+v, %v", stored, err)
	}
	expectSCIMError(t, h.scim(http.MethodPost, "/Groups", `{"displayName":"Dup","externalId":"g-eng"}`), http.StatusConflict, "uniqueness")
	expectSCIMError(t, h.scim(http.MethodPost, "/Groups", `{"displayName":"Bad","members":[{"value":"`+uuid.NewString()+`"}]}`), http.StatusBadRequest, "invalidValue")
	expectSCIMError(t, h.scim(http.MethodPost, "/Groups", `{"externalId":"x"}`), http.StatusBadRequest, "invalidValue")

	// PATCH shapes of Entra ID and Okta.
	patch := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		got := h.scim(http.MethodPatch, "/Groups/"+groupID, body)
		if got.Code != http.StatusOK {
			t.Fatalf("patch %s = %d %s", body, got.Code, got.Body.String())
		}
		return got
	}
	members := func(response *httptest.ResponseRecorder) []string {
		var values []string
		for _, member := range decodeSCIM(t, response)["members"].([]any) {
			values = append(values, member.(map[string]any)["value"].(string))
		}
		return values
	}
	if got := members(patch(`{"Operations":[{"op":"Add","path":"members","value":[{"value":"` + grace + `"}]}]}`)); len(got) != 2 {
		t.Fatalf("after add = %v", got)
	}
	if got := members(patch(`{"Operations":[{"op":"Remove","path":"members[value eq \"` + ada + `\"]"}]}`)); len(got) != 1 || got[0] != grace {
		t.Fatalf("after selector remove = %v", got)
	}
	if got := members(patch(`{"Operations":[{"op":"add","path":"members","value":[{"value":"` + ada + `"}]},{"op":"remove","path":"members","value":[{"value":"` + grace + `"}]}]}`)); len(got) != 1 || got[0] != ada {
		t.Fatalf("after value-array remove = %v", got)
	}
	renamed := decodeSCIM(t, patch(`{"Operations":[{"op":"replace","value":{"displayName":"Platform","description":"ignored"}}]}`))
	if renamed["displayName"] != "Platform" {
		t.Fatalf("after pathless replace = %v", renamed)
	}
	if got := members(patch(`{"Operations":[{"op":"replace","path":"members","value":[{"value":"` + grace + `"}]}]}`)); len(got) != 1 || got[0] != grace {
		t.Fatalf("after members replace = %v", got)
	}
	if got := decodeSCIM(t, patch(`{"Operations":[{"op":"remove","path":"members"}]}`))["members"].([]any); len(got) != 0 {
		t.Fatalf("after removing every member = %v", got)
	}
	expectSCIMError(t, h.scim(http.MethodPatch, "/Groups/"+groupID, `{"Operations":[{"op":"replace","path":"id","value":"x"}]}`), http.StatusBadRequest, "invalidPath")
	expectSCIMError(t, h.scim(http.MethodPatch, "/Groups/"+groupID, `{"Operations":[{"op":"add","path":"members","value":[{"value":"`+uuid.NewString()+`"}]}]}`), http.StatusBadRequest, "invalidValue")
	expectSCIMError(t, h.scimAs(h.token, http.MethodPatch, "/Groups/"+groupID, `{"Operations":[{"op":"replace","path":"displayName","value":"x"}]}`, map[string]string{"If-Match": `W/"1"`}), http.StatusPreconditionFailed, "invalidVers")

	// excludedAttributes=members is honoured on a read and on a list; PUT replaces members only when named.
	if got := decodeSCIM(t, h.scim(http.MethodGet, "/Groups/"+groupID+"?excludedAttributes=members", "")); got["members"] != nil {
		t.Errorf("excluded members were returned: %v", got)
	}
	if got := decodeSCIM(t, h.scim(http.MethodGet, "/Groups/"+groupID, "")); got["members"] == nil {
		t.Errorf("members were not returned by default: %v", got)
	}
	list := decodeSCIM(t, h.scim(http.MethodGet, "/Groups?filter="+url.QueryEscape(`displayName eq "Platform"`)+"&excludedAttributes=members", ""))
	if list["totalResults"] != float64(1) || list["Resources"].([]any)[0].(map[string]any)["members"] != nil {
		t.Errorf("list = %v", list)
	}
	byExternal := decodeSCIM(t, h.scim(http.MethodGet, "/Groups?filter="+url.QueryEscape(`externalId eq "g-eng"`), ""))
	if byExternal["totalResults"] != float64(1) || byExternal["Resources"].([]any)[0].(map[string]any)["members"] == nil {
		t.Errorf("filter by externalId = %v", byExternal)
	}
	expectSCIMError(t, h.scim(http.MethodGet, "/Groups?filter="+url.QueryEscape(`userName eq "x"`), ""), http.StatusBadRequest, "invalidFilter")
	replaced := h.scim(http.MethodPut, "/Groups/"+groupID, `{"displayName":"Platform 2","externalId":"g-eng"}`)
	if replaced.Code != http.StatusOK || decodeSCIM(t, replaced)["displayName"] != "Platform 2" {
		t.Fatalf("replace = %d %s", replaced.Code, replaced.Body.String())
	}
	withMembers := h.scim(http.MethodPut, "/Groups/"+groupID, `{"displayName":"Platform 2","members":[{"value":"`+ada+`"}]}`)
	if got := members(withMembers); len(got) != 1 || got[0] != ada {
		t.Fatalf("replace with members = %v", got)
	}

	// The administrator's flag survives SCIM changes; delete and recreate starts at none again.
	current, _ := h.store.GetUserGroup(t.Context(), groupID)
	if _, err := h.store.SetUserGroupGlobalRole(t.Context(), groupID, repository.UserGroupRoleAdmin, current.Version); err != nil {
		t.Fatal(err)
	}
	patch(`{"Operations":[{"op":"replace","path":"displayName","value":"Platform 3"}]}`)
	if flagged, _ := h.store.GetUserGroup(t.Context(), groupID); flagged.GlobalRole != repository.UserGroupRoleAdmin {
		t.Fatalf("a SCIM change cleared the administrator's flag: %+v", flagged)
	}
	if removed := h.scim(http.MethodDelete, "/Groups/"+groupID, ""); removed.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", removed.Code, removed.Body.String())
	}
	expectSCIMError(t, h.scim(http.MethodGet, "/Groups/"+groupID, ""), http.StatusNotFound, "")
	recreated := decodeSCIM(t, h.scim(http.MethodPost, "/Groups", `{"displayName":"Platform 3","externalId":"g-eng"}`))
	if again, err := h.store.GetUserGroup(t.Context(), recreated["id"].(string)); err != nil || again.GlobalRole != repository.UserGroupRoleNone {
		t.Fatalf("recreated group = %+v, %v", again, err)
	}

	// SCIM reaches only the groups of source scim of its own provider.
	local, err := h.store.CreateUserGroup(t.Context(), repository.UserGroup{DisplayName: "Local", Source: repository.UserGroupSourceLocal})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		expectSCIMError(t, h.scim(method, "/Groups/"+local.ID, ""), http.StatusNotFound, "")
	}
	expectSCIMError(t, h.scim(http.MethodPatch, "/Groups/"+local.ID, `{"Operations":[{"op":"replace","path":"displayName","value":"x"}]}`), http.StatusNotFound, "")
}

func TestSCIMGroupListPaging(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true, MaxPage: 3})
	for index := 0; index < 7; index++ {
		h.createGroup("Group "+strconv.Itoa(index), "g-"+strconv.Itoa(index))
	}
	var seen []string
	cursor := ""
	for {
		page := decodeSCIM(t, h.scim(http.MethodGet, "/Groups?excludedAttributes=members&cursor="+url.QueryEscape(cursor), ""))
		if page["totalResults"] != float64(7) {
			t.Fatalf("total = %v", page["totalResults"])
		}
		for _, resource := range page["Resources"].([]any) {
			seen = append(seen, resource.(map[string]any)["id"].(string))
		}
		next, _ := page["nextCursor"].(string)
		if cursor = next; cursor == "" {
			break
		}
	}
	if len(seen) != 7 {
		t.Fatalf("cursor paging saw %d groups", len(seen))
	}
	third := decodeSCIM(t, h.scim(http.MethodGet, "/Groups?excludedAttributes=members&startIndex=4", ""))
	if third["startIndex"] != float64(4) || third["Resources"].([]any)[0].(map[string]any)["id"] != seen[3] {
		t.Fatalf("startIndex 4 = %v", third)
	}
	if past := decodeSCIM(t, h.scim(http.MethodGet, "/Groups?startIndex=99", "")); past["itemsPerPage"] != float64(0) || past["totalResults"] != float64(7) {
		t.Fatalf("past the end = %v", past)
	}
	expectSCIMError(t, h.scim(http.MethodGet, "/Groups?cursor=garbage", ""), http.StatusBadRequest, "invalidValue")
}

func TestSCIMEveryCallIsAuditedWithTheProviderActor(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	id := h.createUser("audited@example.com", "e-1")
	h.scim(http.MethodGet, "/Users/"+id, "")
	h.scim(http.MethodGet, "/ServiceProviderConfig", "")
	h.scim(http.MethodPatch, "/Users/"+id, `{"Operations":[{"op":"replace","path":"displayName","value":"Secret Name"},{"op":"replace","path":"title","value":"CEO"}]}`)
	h.scim(http.MethodGet, "/Users/"+uuid.NewString(), "")
	h.scimAs("", http.MethodGet, "/Users", "", nil)
	h.scim(http.MethodDelete, "/Users/"+id, "")
	byOperation := map[string][]repository.AuditRecord{}
	for _, record := range h.store.Audits {
		byOperation[record.Operation] = append(byOperation[record.Operation], record)
		if strings.Contains(record.Resource+record.AuthorizationReason, "Secret Name") || strings.Contains(strings.Join(mapValues(record.Evidence), ","), "Secret Name") || strings.Contains(strings.Join(mapValues(record.Evidence), ","), "CEO") {
			t.Fatalf("an attribute value reached the audit trail: %+v", record)
		}
	}
	for _, operation := range []string{"scim.user.create", "scim.user.read", "scim.discovery.read", "scim.user.patch", "scim.user.delete", "scim.authenticate"} {
		if len(byOperation[operation]) == 0 {
			t.Errorf("missing audit %s in %v", operation, byOperation)
		}
	}
	patchRecord := byOperation["scim.user.patch"][0]
	if patchRecord.Actor != "scim:entra" || patchRecord.Evidence["attributes"] != "displayName" || patchRecord.Evidence["ignored"] != "title" || patchRecord.Resource != "scim/Users/"+id || patchRecord.Status != http.StatusOK || patchRecord.AuthorizationSource != "scim" {
		t.Fatalf("patch audit = %+v", patchRecord)
	}
	if denied := byOperation["scim.authenticate"][0]; denied.Actor != "scim:unauthenticated" || denied.Outcome != repository.AuditAccessDenied || denied.Status != http.StatusUnauthorized {
		t.Fatalf("authentication audit = %+v", denied)
	}
	if missing := byOperation["scim.user.read"]; missing[len(missing)-1].Status != http.StatusNotFound {
		t.Fatalf("the not-found read audit = %+v", missing[len(missing)-1])
	}
	for _, record := range h.store.Audits {
		if strings.HasPrefix(record.Operation, "scim.") && record.Operation != "scim.authenticate" && record.Actor != "scim:entra" {
			t.Errorf("%s was audited as %q", record.Operation, record.Actor)
		}
	}
}

func mapValues(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func TestSCIMOnlyFirstLoginBindsByExternalIDNeverByUserName(t *testing.T) {
	idp := newStubOIDCProvider(t, "scim-login-key", "idp-subject-1", nil)
	store := repository.NewMemoryStore()
	runtime := NewIdentityRuntime(store)
	input := corpProviderInput("corp", idp)
	input.ProvisioningMode = identity.ProvisioningSCIMOnly
	input.AttributeMapping = identity.AttributeMapping{ExternalID: "employee_id"}
	corp, err := runtime.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{IdentityRuntime: runtime}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	provisioned, err := store.CreateSCIMUser(t.Context(), repository.SCIMUserCreate{ProviderID: corp.ID, UserName: "preferred-user", ExternalID: "E-1001", Active: true})
	if err != nil {
		t.Fatal(err)
	}

	// The IdP asserts no externalId: the userName-like claim is not enough, whatever it says.
	idp.setExtraClaims(map[string]any{"preferred_username": "preferred-user"})
	denied := attemptStubLogin(t, handler, idp, "corp")
	if location := denied.Header().Get("Location"); !strings.Contains(location, "oidc_error=account_not_linked") {
		t.Fatalf("a login that matched only by userName: %d %q", denied.Code, location)
	}
	if identities, _ := store.ListUserIdentities(t.Context(), provisioned.User.ID); identities[0].Subject != "" {
		t.Fatalf("the subject was bound without an externalId: %+v", identities[0])
	}
	// A wrong externalId does not bind either.
	idp.setExtraClaims(map[string]any{"employee_id": "E-9999"})
	if got := attemptStubLogin(t, handler, idp, "corp"); !strings.Contains(got.Header().Get("Location"), "oidc_error=account_not_linked") {
		t.Fatalf("a wrong externalId: %q", got.Header().Get("Location"))
	}
	// The matching externalId binds the subject and signs the account in.
	idp.setExtraClaims(map[string]any{"employee_id": "E-1001"})
	ok := completeStubLogin(t, handler, idp, "corp")
	if location := ok.Header().Get("Location"); ok.Code != http.StatusFound || strings.Contains(location, "oidc_error") || !strings.Contains(location, "oidc=success") {
		t.Fatalf("login = %d %q", ok.Code, ok.Header().Get("Location"))
	}
	linked, link, err := store.GetUserByProviderIdentity(t.Context(), corp.ID, "idp-subject-1")
	if err != nil || linked.ID != provisioned.User.ID || link.SCIMExternalID != "E-1001" {
		t.Fatalf("bound identity = %+v %+v, %v", linked, link, err)
	}
	if again := completeStubLogin(t, handler, idp, "corp"); strings.Contains(again.Header().Get("Location"), "oidc_error") {
		t.Fatalf("the second login = %q", again.Header().Get("Location"))
	}
	var sessionCookie *http.Cookie
	for _, cookie := range ok.Result().Cookies() {
		if cookie.Name == webSessionCookieName && cookie.MaxAge > 0 {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie")
	}
	identityRequest := func() int {
		request := httptest.NewRequest(http.MethodGet, "/api/v2/identity", nil)
		request.AddCookie(sessionCookie)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	if got := identityRequest(); got != http.StatusOK {
		t.Fatalf("session before deactivation = %d", got)
	}
	// SCIM deactivation ends the session at once and blocks the next login.
	inactive := false
	if _, err := store.UpdateSCIMUser(t.Context(), corp.ID, provisioned.User.ID, repository.SCIMUserChange{Active: &inactive}); err != nil {
		t.Fatal(err)
	}
	if got := identityRequest(); got != http.StatusUnauthorized {
		t.Fatalf("session after SCIM deactivation = %d, want 401", got)
	}
	if blocked := attemptStubLogin(t, handler, idp, "corp"); !strings.Contains(blocked.Header().Get("Location"), "oidc_error=account_disabled") {
		t.Fatalf("login of a deactivated account = %q", blocked.Header().Get("Location"))
	}
}

// attemptStubLogin runs a browser login against the stub IdP and returns the callback response whatever it is, for the
// tests that expect a refusal.
func attemptStubLogin(t *testing.T, handler http.Handler, idp *stubOIDCProvider, slug string) *httptest.ResponseRecorder {
	t.Helper()
	flowCookie, state := startStubLogin(t, handler, idp, slug, "/auth/oidc/"+slug+"/login")
	callback := httptest.NewRequest(http.MethodGet, "/auth/oidc/"+slug+"/callback?code=authorization-code&state="+url.QueryEscape(state), nil)
	callback.AddCookie(flowCookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, callback)
	return response
}
