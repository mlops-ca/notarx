package app

import (
	"context"
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
	if created.Code != http.StatusCreated || !strings.HasPrefix(created.Header().Get("Location"), "/scim/v2/Users/") || created.Header().Get("ETag") != `W/"1"` {
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
	replaced := h.scim(http.MethodPut, "/Users/"+id, `{"userName":"ADA@example.com"}`)
	body := decodeSCIM(t, replaced)
	// A change of letter case only is ignored: the stored name is kept.
	if replaced.Code != http.StatusOK || body["userName"] != "Ada@Example.com" || body["externalId"] != "ext-ada" || body["active"] != true || body["displayName"] != nil || body["emails"] != nil || replaced.Header().Get("ETag") != `W/"2"` {
		t.Fatalf("replace = %d %s", replaced.Code, replaced.Body.String())
	}
	// The account name is the identity grants and sessions are keyed on: SCIM cannot rename.
	expectSCIMError(t, h.scim(http.MethodPut, "/Users/"+id, `{"userName":"ada.l@example.com"}`), http.StatusBadRequest, "mutability")
	expectSCIMError(t, h.scim(http.MethodPatch, "/Users/"+id, `{"Operations":[{"op":"replace","path":"userName","value":"ada.l@example.com"}]}`), http.StatusBadRequest, "mutability")
	if user, _ := h.store.GetUser(t.Context(), id); user.Name != "Ada@Example.com" {
		t.Fatalf("the account was renamed through SCIM: %q", user.Name)
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
	provisioned := h.createUser("root@example.com", "ext-root")
	current, err := h.store.GetUser(t.Context(), provisioned)
	if err != nil {
		t.Fatal(err)
	}
	role := "admin"
	admin, err := h.store.UpdateUser(t.Context(), repository.UserUpdate{ID: provisioned, Role: &role}, current.Version)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.scim(http.MethodGet, "/Users/"+admin.ID, ""); got.Code != http.StatusOK {
		t.Fatalf("reading an administrator = %d", got.Code)
	}
	for name, request := range map[string][3]string{
		"deactivate": {http.MethodPatch, "/Users/" + admin.ID, `{"Operations":[{"op":"replace","path":"active","value":false}]}`},
		"rename":     {http.MethodPatch, "/Users/" + admin.ID, `{"Operations":[{"op":"replace","path":"displayName","value":"x"}]}`},
		"replace":    {http.MethodPut, "/Users/" + admin.ID, `{"userName":"root@example.com"}`},
		"delete":     {http.MethodDelete, "/Users/" + admin.ID, ""},
	} {
		expectSCIMError(t, h.scim(request[0], request[1], request[2]), http.StatusForbidden, "mutability")
		if after, _ := h.store.GetUser(t.Context(), admin.ID); after.State != repository.UserActive || after.Name != "root@example.com" || after.DisplayName != admin.DisplayName || after.SessionVersion != admin.SessionVersion {
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
	// An externalId the body leaves out is left alone: clearing it would make the IdP's next lookup find nothing.
	if kept := decodeSCIM(t, withMembers); kept["externalId"] != "g-eng" {
		t.Fatalf("a replace without externalId cleared it: %v", kept)
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
	// A write is audited once: its record commits with the change and the response does not add a second one.
	for _, operation := range []string{"scim.user.create", "scim.user.patch", "scim.user.delete"} {
		if got := len(byOperation[operation]); got != 1 {
			t.Errorf("%s was audited %d times, want once", operation, got)
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

func TestSCIMDeleteHidesTheAccountAndOnlyTheSameProviderCanCreateItAgain(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	other, err := h.runtime.Create(t.Context(), scimProviderInput("okta"))
	if err != nil {
		t.Fatal(err)
	}
	otherToken := h.createToken(other.ID, "okta", time.Hour)
	id := h.createUser("leaver@example.com", "ext-leaver")
	if _, err := h.store.LinkProviderIdentity(t.Context(), repository.ProviderIdentityLink{ProviderID: h.provider.ID, UserID: id, Subject: "leaver-sub", Issuer: "https://login.example.test", Kind: repository.UserIdentityOIDC}); err != nil {
		t.Fatal(err)
	}
	if got := h.scim(http.MethodDelete, "/Users/"+id, ""); got.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", got.Code, got.Body.String())
	}
	// SCIM no longer sees the account, even though the login subject stays bound and the row is kept.
	expectSCIMError(t, h.scim(http.MethodGet, "/Users/"+id, ""), http.StatusNotFound, "")
	expectSCIMError(t, h.scim(http.MethodDelete, "/Users/"+id, ""), http.StatusNotFound, "")
	expectSCIMError(t, h.scim(http.MethodPatch, "/Users/"+id, `{"Operations":[{"op":"replace","path":"active","value":true}]}`), http.StatusNotFound, "")
	if list := decodeSCIM(t, h.scim(http.MethodGet, "/Users?filter="+url.QueryEscape(`userName eq "leaver@example.com"`), "")); list["totalResults"] != float64(0) {
		t.Fatalf("a deleted account is still found: %v", list)
	}
	if user, _ := h.store.GetUser(t.Context(), id); user.State != repository.UserDisabled || user.DisabledReason != repository.DisabledBySCIM {
		t.Fatalf("the account after DELETE = %+v", user)
	}
	// Another provider cannot take it over; the same provider brings it back with a create.
	expectSCIMError(t, h.scimAs(otherToken, http.MethodPost, "/Users", `{"userName":"leaver@example.com"}`, nil), http.StatusConflict, "uniqueness")
	back := h.scim(http.MethodPost, "/Users", `{"userName":"Leaver@Example.com","externalId":"ext-leaver-2","displayName":"Back again"}`)
	body := decodeSCIM(t, back)
	if back.Code != http.StatusCreated || body["id"] != id || body["active"] != true || body["externalId"] != "ext-leaver-2" || body["displayName"] != "Back again" {
		t.Fatalf("recreate = %d %s", back.Code, back.Body.String())
	}
	if user, _ := h.store.GetUser(t.Context(), id); user.State != repository.UserActive || user.DisabledReason != "" {
		t.Fatalf("the revived account = %+v", user)
	}
}

func TestSCIMIfMatchHeaderParsing(t *testing.T) {
	for header, want := range map[string][]string{
		"":                 nil,
		"*":                nil,
		`W/"3"`:            {"3"},
		`"3"`:              {"3"},
		`W/"3", "4",W/"5"`: {"3", "4", "5"},
		"garbage":          {"garbage"},
		`""`:               {""},
		" , ":              {""},
	} {
		request := httptest.NewRequest(http.MethodPut, "/scim/v2/Users/x", nil)
		if header != "" {
			request.Header.Set("If-Match", header)
		}
		got := scimIfMatchVersions(request)
		if strings.Join(got, "|") != strings.Join(want, "|") || (got == nil) != (want == nil) {
			t.Errorf("If-Match %q = %#v, want %#v", header, got, want)
		}
	}
	if scimVersionMatches([]string{""}, "1") || !scimVersionMatches(nil, "1") || !scimVersionMatches([]string{"0", "1"}, "1") {
		t.Error("scimVersionMatches does not follow the If-Match list")
	}
}

// countingSCIMStore records how the server reads: one member query per page of groups, and no count on the pages that
// are only read to find a cursor.
type countingSCIMStore struct {
	scimStore
	singleMemberReads, batchMemberReads int
	userPages, keysOnly                 []bool
}

func (c *countingSCIMStore) ListSCIMGroupMembers(ctx context.Context, providerID, groupID string, limit int) ([]repository.UserGroupMember, error) {
	c.singleMemberReads++
	return c.scimStore.ListSCIMGroupMembers(ctx, providerID, groupID, limit)
}

func (c *countingSCIMStore) ListSCIMGroupsMembers(ctx context.Context, providerID string, groupIDs []string, perGroup int) (map[string][]repository.UserGroupMember, error) {
	c.batchMemberReads++
	return c.scimStore.ListSCIMGroupsMembers(ctx, providerID, groupIDs, perGroup)
}

func (c *countingSCIMStore) ListSCIMUsers(ctx context.Context, query repository.SCIMUserQuery) (repository.SCIMUserPage, error) {
	c.userPages = append(c.userPages, query.SkipTotal)
	c.keysOnly = append(c.keysOnly, query.KeysOnly)
	return c.scimStore.ListSCIMUsers(ctx, query)
}

func TestSCIMListsReadMembersInOnePassAndSkipTheCountWhileHopping(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true, MaxPage: 3})
	member := h.createUser("m@example.com", "")
	for index := 0; index < 5; index++ {
		h.createGroup("G"+strconv.Itoa(index), "g-"+strconv.Itoa(index), member)
	}
	for index := 0; index < 7; index++ {
		h.createUser("u"+strconv.Itoa(index)+"@example.com", "")
	}
	counting := &countingSCIMStore{scimStore: h.store}
	server := newSCIMServer(SCIMOptions{Enabled: true, MaxPage: 3}, counting, nil)
	call := func(path string) map[string]any {
		request := httptest.NewRequest(http.MethodGet, "https://notarx.example.test/scim/v2"+path, nil)
		request.Header.Set("Authorization", "Bearer "+h.token)
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", path, recorder.Code, recorder.Body.String())
		}
		return decodeSCIM(t, recorder)
	}
	list := call("/Groups")
	if resources := list["Resources"].([]any); len(resources) != 3 || len(resources[0].(map[string]any)["members"].([]any)) != 1 {
		t.Fatalf("groups = %v", list)
	}
	if counting.batchMemberReads != 1 || counting.singleMemberReads != 0 {
		t.Fatalf("members were read %d times in batches and %d times one group at a time", counting.batchMemberReads, counting.singleMemberReads)
	}
	call("/Groups/" + list["Resources"].([]any)[0].(map[string]any)["id"].(string))
	if counting.singleMemberReads != 1 {
		t.Fatalf("a single group read %d member queries", counting.singleMemberReads)
	}
	call("/Users?startIndex=7")
	// Hops skip the total and read only the keys; the page that is served carries both.
	if len(counting.userPages) < 2 || !counting.userPages[0] || counting.userPages[len(counting.userPages)-1] || !counting.keysOnly[0] || counting.keysOnly[len(counting.keysOnly)-1] {
		t.Fatalf("user pages read with SkipTotal = %v and KeysOnly = %v", counting.userPages, counting.keysOnly)
	}
}

func TestSCIMRejectsReservedExternalIDsAndHidesTombstonesFromTheAdminAPI(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	for _, reserved := range []string{repository.SCIMDeletedExternalIDPrefix + "x", repository.SCIMGeneratedExternalIDPrefix + "x"} {
		expectSCIMError(t, h.scim(http.MethodPost, "/Users", `{"userName":"r1","externalId":"`+reserved+`"}`), http.StatusBadRequest, "invalidValue")
		expectSCIMError(t, h.scim(http.MethodPost, "/Groups", `{"displayName":"R","externalId":"`+reserved+`"}`), http.StatusBadRequest, "invalidValue")
	}
	id := h.createUser("tomb@example.com", "ext-tomb")
	expectSCIMError(t, h.scim(http.MethodPatch, "/Users/"+id, `{"Operations":[{"op":"replace","path":"externalId","value":"`+repository.SCIMDeletedExternalIDPrefix+`z"}]}`), http.StatusBadRequest, "invalidValue")
	if got := h.scim(http.MethodDelete, "/Users/"+id, ""); got.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", got.Code)
	}
	identities := h.admin(http.MethodGet, "/users/"+id+"/identities", "")
	if identities.Code != http.StatusOK || strings.Contains(identities.Body.String(), "notarx-scim") {
		t.Fatalf("the admin identities list leaks a store marker: %d %s", identities.Code, identities.Body.String())
	}
}

func TestSCIMGroupsShowOnlyTheirSCIMManagedMembersOfTheProvider(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	managed := h.createUser("managed@example.com", "")
	group := h.createGroup("Mixed", "g-mixed", managed)
	local, err := h.store.CreateUser(t.Context(), repository.User{ID: uuid.NewString(), Name: "local-member", Role: "member"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.AddUserGroupMember(t.Context(), group, local.ID, repository.UserGroupSourceLocal); err != nil {
		t.Fatal(err)
	}
	got := decodeSCIM(t, h.scim(http.MethodGet, "/Groups/"+group, ""))
	members := got["members"].([]any)
	if len(members) != 1 || members[0].(map[string]any)["value"] != managed {
		t.Fatalf("members = %v, want only the SCIM-managed one", members)
	}
	if strings.Contains(h.scim(http.MethodGet, "/Groups", "").Body.String(), "local-member") {
		t.Fatal("a local member of a SCIM group is shown to the identity provider")
	}
	// Deleting the account removes the memberships SCIM wrote for it.
	if got := h.scim(http.MethodDelete, "/Users/"+managed, ""); got.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", got.Code)
	}
	if memberships, _ := h.store.ListUserGroupMemberships(t.Context(), managed); len(memberships) != 0 {
		t.Fatalf("a deleted account kept its memberships: %+v", memberships)
	}
}

func TestSCIMFailureLimiterIgnoresForwardedForHeaders(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	for attempt := 0; attempt <= samlRateBurst; attempt++ {
		request := httptest.NewRequest(http.MethodGet, "https://notarx.example.test/scim/v2/Users", nil)
		request.Header.Set("Authorization", "Bearer "+scimTokenPrefix+"guess")
		// A rotating forwarded address must not buy a fresh budget.
		request.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(attempt%250))
		recorder := httptest.NewRecorder()
		h.handler.ServeHTTP(recorder, request)
		if attempt == samlRateBurst && recorder.Code != http.StatusTooManyRequests {
			t.Fatalf("after the budget, with a rotating X-Forwarded-For = %d", recorder.Code)
		}
	}
	long := strings.Repeat("a", 5000)
	h.scimAs("", http.MethodGet, "/Users?x=1", "", nil)
	request := httptest.NewRequest(http.MethodGet, "https://notarx.example.test/scim/v2/Unknown/"+long, nil)
	request.RemoteAddr = "203.0.113.50:1"
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, request)
	for _, record := range h.store.Audits {
		if len(record.Resource) > scimMaxAuditResource {
			t.Fatalf("an audit resource of %d bytes was written", len(record.Resource))
		}
	}
}

func TestSCIMOnlySeesAccountsItProvisionedAndTakesOverUnprivilegedLinkedOnes(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	link := func(name, role, hash string) string {
		t.Helper()
		account, err := h.store.CreateUser(t.Context(), repository.User{ID: uuid.NewString(), Name: name, Role: role, SecretHash: hash})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.LinkProviderIdentity(t.Context(), repository.ProviderIdentityLink{ProviderID: h.provider.ID, UserID: account.ID, Subject: "sub-" + name, Issuer: "https://login.example.test", Kind: repository.UserIdentityOIDC}); err != nil {
			t.Fatal(err)
		}
		return account.ID
	}
	member, password, jit := link("member-user", "member", ""), link("password-user", "none", "bcrypt"), link("jit-user", "none", "")
	for name, id := range map[string]string{"member": member, "password": password, "jit": jit} {
		expectSCIMError(t, h.scim(http.MethodGet, "/Users/"+id, ""), http.StatusNotFound, "")
		expectSCIMError(t, h.scim(http.MethodPatch, "/Users/"+id, `{"Operations":[{"op":"replace","path":"active","value":false}]}`), http.StatusNotFound, "")
		expectSCIMError(t, h.scim(http.MethodDelete, "/Users/"+id, ""), http.StatusNotFound, "")
		if user, _ := h.store.GetUser(t.Context(), id); user.State != repository.UserActive {
			t.Fatalf("%s: SCIM changed an account it did not provision: %+v", name, user)
		}
	}
	expectSCIMError(t, h.scim(http.MethodPost, "/Users", `{"userName":"member-user"}`), http.StatusConflict, "uniqueness")
	expectSCIMError(t, h.scim(http.MethodPost, "/Users", `{"userName":"password-user"}`), http.StatusConflict, "uniqueness")
	// An unprivileged linked account is how a provider in jit mode correlates its users: the create takes it over.
	taken := h.scim(http.MethodPost, "/Users", `{"userName":"JIT-user","externalId":"ext-jit","displayName":"JIT"}`)
	body := decodeSCIM(t, taken)
	if taken.Code != http.StatusCreated || body["id"] != jit || body["externalId"] != "ext-jit" || body["active"] != true {
		t.Fatalf("take over = %d %s", taken.Code, taken.Body.String())
	}
	if got := h.scim(http.MethodGet, "/Users/"+jit, ""); got.Code != http.StatusOK {
		t.Fatalf("a taken over account is not visible: %d", got.Code)
	}
}

func TestSCIMTokenShapeIsCheckedBeforeAnyLookup(t *testing.T) {
	valid := scimTokenPrefix + strings.Repeat("A", 43)
	if !scimTokenShape(valid) || !scimTokenShape(scimTokenPrefix+strings.Repeat("a-_0", 10)+"abc") {
		t.Fatal("a token of the issued shape was refused")
	}
	for _, bad := range []string{"", scimTokenPrefix, scimTokenPrefix + strings.Repeat("A", 42), scimTokenPrefix + strings.Repeat("A", 44), "nxscim" + strings.Repeat("A", 43), scimTokenPrefix + strings.Repeat("A", 42) + "=", scimTokenPrefix + strings.Repeat("A", 42) + " "} {
		if scimTokenShape(bad) {
			t.Errorf("%q passed the shape check", bad)
		}
	}
	// A token of the issued shape that is unknown is looked up and refused; anything else never reaches the store.
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	counting := &countingSCIMStore{scimStore: h.store}
	server := newSCIMServer(SCIMOptions{Enabled: true}, counting, nil)
	for _, token := range []string{"garbage", scimTokenPrefix + "short"} {
		request := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%q = %d", token, recorder.Code)
		}
	}
}

func TestSCIMLocationsNeverTrustTheRequestHost(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	request := httptest.NewRequest(http.MethodPost, "https://attacker.example/scim/v2/Users", strings.NewReader(`{"userName":"host-test"}`))
	request.Host = "attacker.example"
	request.Header.Set("Authorization", "Bearer "+h.token)
	request.Header.Set("Content-Type", "application/scim+json")
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-Host", "attacker.example")
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || strings.Contains(recorder.Body.String(), "attacker.example") || strings.Contains(recorder.Header().Get("Location"), "attacker.example") ||
		!strings.HasPrefix(recorder.Header().Get("Location"), "/scim/v2/Users/") {
		t.Fatalf("create = %d location=%q body=%s", recorder.Code, recorder.Header().Get("Location"), recorder.Body.String())
	}
}

func TestSCIMGroupListSharesOneMemberBudgetAmongItsGroups(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true, MaxPage: 4})
	members := make([]string, 0, 3)
	for index := 0; index < 3; index++ {
		members = append(members, h.createUser("budget"+strconv.Itoa(index)+"@example.com", ""))
	}
	for index := 0; index < 4; index++ {
		h.createGroup("Budget "+strconv.Itoa(index), "b-"+strconv.Itoa(index), members...)
	}
	counting := &budgetSCIMStore{scimStore: h.store}
	server := newSCIMServer(SCIMOptions{Enabled: true, MaxPage: 4}, counting, nil)
	request := httptest.NewRequest(http.MethodGet, "/scim/v2/Groups", nil)
	request.Header.Set("Authorization", "Bearer "+h.token)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	// One more than the share is read, to tell a group that fits from one that does not.
	if recorder.Code != http.StatusOK || counting.perGroup != scimMaxListedMembers/4+1 {
		t.Fatalf("list = %d with a per-group read of %d, want %d", recorder.Code, counting.perGroup, scimMaxListedMembers/4+1)
	}
}

type budgetSCIMStore struct {
	scimStore
	perGroup int
}

func (b *budgetSCIMStore) ListSCIMGroupsMembers(ctx context.Context, providerID string, groupIDs []string, perGroup int) (map[string][]repository.UserGroupMember, error) {
	b.perGroup = perGroup
	return b.scimStore.ListSCIMGroupsMembers(ctx, providerID, groupIDs, perGroup)
}

func TestSCIMGroupListLeavesOutTheMembersOfAGroupThatExceedsItsShare(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true, MaxPage: 200})
	small := h.createUser("small@example.com", "")
	big := make([]string, 0, 3)
	for index := 0; index < 3; index++ {
		big = append(big, h.createUser("big"+strconv.Itoa(index)+"@example.com", ""))
	}
	h.createGroup("Small", "g-small", small)
	bigID := h.createGroup("Big", "g-big", big...)
	// Fill the page with groups so that each one's share of the budget is one member: the group of three exceeds it.
	for index := 0; index < scimMaxListedMembers-2; index++ {
		h.store.CreateSCIMGroup(t.Context(), repository.SCIMGroupCreate{ProviderID: h.provider.ID, DisplayName: "Filler " + strconv.Itoa(index)})
	}
	server := newSCIMServer(SCIMOptions{Enabled: true, MaxPage: scimMaxListedMembers}, h.store, nil)
	request := httptest.NewRequest(http.MethodGet, "/scim/v2/Groups?count="+strconv.Itoa(scimMaxListedMembers), nil)
	request.Header.Set("Authorization", "Bearer "+h.token)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list = %d %s", recorder.Code, recorder.Body.String())
	}
	for _, resource := range decodeSCIM(t, recorder)["Resources"].([]any) {
		group := resource.(map[string]any)
		switch group["displayName"] {
		case "Big":
			if group["members"] != nil {
				t.Fatalf("a partial member list was returned for a group that exceeds its share: %v", group["members"])
			}
		case "Small":
			if len(group["members"].([]any)) != 1 {
				t.Fatalf("a group that fits lost its members: %v", group)
			}
		}
	}
	// The group read still returns all of its members.
	if got := decodeSCIM(t, h.scim(http.MethodGet, "/Groups/"+bigID, "")); len(got["members"].([]any)) != 3 {
		t.Fatalf("group read = %v", got)
	}
}

func TestSCIMTakeoverAndCreateAreAuditedWithTheirAccount(t *testing.T) {
	h := newSCIMHarness(t, SCIMOptions{Enabled: true})
	created := h.createUser("fresh@example.com", "")
	linked, err := h.store.CreateUser(t.Context(), repository.User{ID: uuid.NewString(), Name: "linked@example.com", Role: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.LinkProviderIdentity(t.Context(), repository.ProviderIdentityLink{ProviderID: h.provider.ID, UserID: linked.ID, Subject: "linked-sub", Issuer: "https://login.example.test", Kind: repository.UserIdentityOIDC}); err != nil {
		t.Fatal(err)
	}
	if got := h.scim(http.MethodPost, "/Users", `{"userName":"linked@example.com"}`); got.Code != http.StatusCreated {
		t.Fatalf("take over = %d %s", got.Code, got.Body.String())
	}
	group := h.createGroup("Audited", "g-audited")
	results := map[string]string{}
	for _, record := range h.store.Audits {
		if record.Operation == "scim.user.create" {
			results[record.Resource] = record.Evidence["result"]
		}
		if record.Operation == "scim.group.create" && record.Resource != "scim/Groups/"+group {
			t.Errorf("the group create was audited as %q", record.Resource)
		}
	}
	if results["scim/Users/"+created] != "created" || results["scim/Users/"+linked.ID] != "taken_over" {
		t.Fatalf("create audits = %v", results)
	}
}

func TestSCIMOnlyKeepsTheBreakGlassSignInOfAdministratorListedSubjects(t *testing.T) {
	idp := newStubOIDCProvider(t, "scim-break-glass-key", "break-glass-subject", nil)
	store := repository.NewMemoryStore()
	runtime := NewIdentityRuntime(store)
	input := corpProviderInput("corp", idp)
	input.ProvisioningMode = identity.ProvisioningSCIMOnly
	input.OIDC.AdminSubjects = []string{"break-glass-subject"}
	if _, err := runtime.Create(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{IdentityRuntime: runtime}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	// The subject an administrator listed signs in as before, although no account is linked to it.
	if got := completeStubLogin(t, handler, idp, "corp"); got.Code != http.StatusFound {
		t.Fatalf("break-glass login = %d", got.Code)
	}
	// Any other unlinked subject is refused.
	other := newStubOIDCProvider(t, "scim-other-key", "someone-else", nil)
	otherInput := corpProviderInput("other", other)
	otherInput.ProvisioningMode = identity.ProvisioningSCIMOnly
	if _, err := runtime.Create(t.Context(), otherInput); err != nil {
		t.Fatal(err)
	}
	if got := attemptStubLogin(t, handler, other, "other"); !strings.Contains(got.Header().Get("Location"), "oidc_error=account_not_linked") {
		t.Fatalf("an unlinked subject of a scim_only provider = %q", got.Header().Get("Location"))
	}
}
