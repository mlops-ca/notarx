package notarxctl

import (
	"net/http"
	"strings"
	"testing"
)

const (
	userGroupID   = "00000000-0000-4000-e000-000000000001"
	userGroupBase = "/api/v2/user-groups"
	memberUserID  = "00000000-0000-4000-d000-000000000009"
	providerUUID  = "00000000-0000-4000-b000-000000000001"
)

func userGroupJSON(name, role, version string) string {
	return `{"id":"` + userGroupID + `","displayName":"` + name + `","source":"local","globalRole":"` + role + `","version":"` + version + `","createdAt":"2026-11-01T08:00:00Z","updatedAt":"2026-11-01T08:00:00Z"}`
}

func TestUserGroupReadCommands(t *testing.T) {
	api := newRouteAPI(t, "")
	expectUsage(t, api,
		[]string{"user-group", "show", "not-a-uuid"},
		[]string{"user-group", "list", "--page-size", "0"},
		[]string{"user-group", "member", "list", "x"},
	)
	api.onFunc("GET", userGroupBase, func(r *http.Request) (int, string) {
		if r.URL.Query().Get("pageToken") == "t2" {
			return 200, `{"items":[],"limit":1}`
		}
		return 200, `{"items":[` + userGroupJSON("ops", "none", "v1") + `],"limit":1,"nextPageToken":"t2"}`
	})
	code, stdout, stderr := runCLI(t, "user-group", "list", "--page-size", "1")
	if code != ExitOK || !strings.Contains(stdout, "ops") || !strings.Contains(stderr, "--cursor t2") {
		t.Fatalf("list: %d %s %s", code, stdout, stderr)
	}
	if got := api.recorded()[0].Query; !strings.Contains(got, "limit=1") || strings.Contains(got, "pageSize") {
		t.Fatalf("the list must use the limit parameter: %q", got)
	}
	if code, stdout, _ := runCLI(t, "user-group", "list", "--all", "--json"); code != ExitOK || !strings.Contains(stdout, "ops") {
		t.Fatalf("list --all: %d %s", code, stdout)
	}
	api.on("GET", userGroupBase+"/"+userGroupID, 200, userGroupJSON("ops", "member", "v1"))
	if code, stdout, _ := runCLI(t, "user-group", "show", userGroupID); code != ExitOK || !strings.Contains(stdout, "global role") || !strings.Contains(stdout, "member") {
		t.Fatalf("show: %d %s", code, stdout)
	}
	api.on("GET", userGroupBase+"/"+userGroupID+"/members", 200, `{"items":[{"userId":"`+memberUserID+`","userName":"alice","source":"local","createdAt":"2026-11-01T08:00:00Z"}],"limit":50}`)
	if code, stdout, _ := runCLI(t, "user-group", "member", "list", userGroupID); code != ExitOK || !strings.Contains(stdout, "alice") {
		t.Fatalf("members: %d %s", code, stdout)
	}
	api.on("GET", userGroupBase+"/"+userGroupID+"/members", 200, `{"items":[],"limit":50}`)
	if code, stdout, _ := runCLI(t, "user-group", "member", "list", userGroupID); code != ExitOK || !strings.Contains(stdout, "no members") {
		t.Fatalf("empty members: %d %s", code, stdout)
	}
	api.on("GET", userGroupBase, 200, `{"items":[],"limit":50}`)
	if code, stdout, _ := runCLI(t, "user-group", "list"); code != ExitOK || !strings.Contains(stdout, "no user groups") {
		t.Fatalf("empty list: %d %s", code, stdout)
	}
}

func TestUserGroupCreateCommand(t *testing.T) {
	api := newRouteAPI(t, "")
	expectUsage(t, api,
		[]string{"user-group", "create", " "},
		[]string{"user-group", "create", "ops", "--source", "scim"},
		[]string{"user-group", "create", "ops", "--provider", providerUUID},
		[]string{"user-group", "create", "ops", "--source", "claims"},
	)
	if code, stdout, _ := runCLI(t, "user-group", "create", "ops", "--source", "claims", "--provider", providerUUID, "--external-id", "ops-team", "--dry-run"); code != ExitOK || !strings.Contains(stdout, "ops-team") || len(api.recorded()) != 0 {
		t.Fatalf("dry run: %d %s", code, stdout)
	}
	api.on("POST", userGroupBase, 201, userGroupJSON("ops", "none", "v1"))
	if code, stdout, stderr := runCLI(t, "user-group", "create", "ops", "--json"); code != ExitOK || !strings.Contains(stdout, userGroupID) {
		t.Fatalf("create: %d %s %s", code, stdout, stderr)
	}
	if body := api.recorded()[0].Body; !strings.Contains(body, `"displayName":"ops"`) || strings.Contains(body, "source") {
		t.Fatalf("body = %q", body)
	}
	if code, _, _ := runCLI(t, "user-group", "create", "ops", "--source", "claims", "--provider", providerUUID, "--external-id", "ops-team"); code != ExitOK {
		t.Fatalf("claims create: %d", code)
	}
	api.on("POST", userGroupBase, 409, `{"code":"name_exists","message":"user group name already exists"}`)
	if code, _, stderr := runCLI(t, "user-group", "create", "ops"); code != ExitConflict || !strings.Contains(stderr, "name_exists") {
		t.Fatalf("conflict: %d %s", code, stderr)
	}
	api.on("POST", userGroupBase, 201, `{"id":"`+userGroupID+`"}`)
	if code, stdout, _ := runCLI(t, "user-group", "create", "ops", "--json"); code == ExitOK || !strings.Contains(stdout, "unknown") {
		t.Fatalf("incomplete response must leave the outcome unknown: %d %s", code, stdout)
	}
}

func TestUserGroupUpdateCommands(t *testing.T) {
	api := newRouteAPI(t, "")
	expectUsage(t, api,
		[]string{"user-group", "rename", userGroupID, " "},
		[]string{"user-group", "rename", "x", "ops"},
		[]string{"user-group", "set-role", userGroupID, "--role", "root"},
		[]string{"user-group", "set-role", userGroupID, "--role", "admin"},
		[]string{"user-group", "set-role", userGroupID, "--role", "member", "--if-match", " "},
	)
	itemPath := userGroupBase + "/" + userGroupID
	api.on("GET", itemPath, 200, userGroupJSON("ops", "none", "v1"))
	api.on("PUT", itemPath, 200, userGroupJSON("platform", "none", "v2"))
	api.on("PUT", itemPath+"/global-role", 200, userGroupJSON("ops", "admin", "v2"))
	if code, stdout, stderr := runCLI(t, "user-group", "rename", userGroupID, "platform"); code != ExitOK || !strings.Contains(stdout, "platform") {
		t.Fatalf("rename: %d %s %s", code, stdout, stderr)
	}
	var put recordedRequest
	for _, request := range api.recorded() {
		if request.Method == "PUT" {
			put = request
		}
	}
	if put.IfMatch != "v1" || !strings.Contains(put.Body, `"displayName":"platform"`) {
		t.Fatalf("the rename must send the version it read: %+v", put)
	}
	if code, stdout, _ := runCLI(t, "user-group", "set-role", userGroupID, "--role", "admin", "--yes", "--if-match", "v9"); code != ExitOK || !strings.Contains(stdout, "admin") {
		t.Fatalf("set-role: %d %s", code, stdout)
	}
	if code, stdout, _ := runCLI(t, "user-group", "set-role", userGroupID, "--role", "admin", "--dry-run", "--json"); code != ExitOK || !strings.Contains(stdout, "global-role") {
		t.Fatalf("dry run: %d %s", code, stdout)
	}
	puts := api.count("PUT")
	api.on("PUT", itemPath, 412, `{"code":"version_conflict","message":"If-Match does not match current user group version"}`)
	if code, stdout, _ := runCLI(t, "user-group", "rename", userGroupID, "other", "--json"); code != ExitConflict || !strings.Contains(stdout, "conflict") || api.count("PUT") != puts+1 {
		t.Fatalf("stale version: %d %s", code, stdout)
	}
	api.on("PUT", itemPath, 200, `{"id":"`+userGroupID+`"}`)
	if code, stdout, _ := runCLI(t, "user-group", "rename", userGroupID, "other", "--json"); code == ExitOK || !strings.Contains(stdout, "unknown") {
		t.Fatalf("incomplete response: %d %s", code, stdout)
	}
	api.on("GET", itemPath, 404, `{"code":"not_found","message":"user group not found"}`)
	if code, _, _ := runCLI(t, "user-group", "rename", userGroupID, "other"); code != ExitNotFound {
		t.Fatalf("missing group: %d", code)
	}
	api.on("GET", itemPath, 200, `{"id":"`+userGroupID+`"}`)
	if code, _, _ := runCLI(t, "user-group", "rename", userGroupID, "other"); code == ExitOK {
		t.Fatal("an incomplete read must stop the write")
	}
}

func TestUserGroupDeleteAndMemberCommands(t *testing.T) {
	api := newRouteAPI(t, "")
	itemPath := userGroupBase + "/" + userGroupID
	expectUsage(t, api,
		[]string{"user-group", "delete", userGroupID},
		[]string{"user-group", "member", "add", userGroupID, "nobody"},
		[]string{"user-group", "member", "remove", userGroupID, memberUserID},
	)
	api.on("DELETE", itemPath, 204, "")
	if code, stdout, _ := runCLI(t, "user-group", "delete", userGroupID, "--yes", "--json"); code != ExitOK || !strings.Contains(stdout, `"deleted": true`) {
		t.Fatalf("delete: %d %s", code, stdout)
	}
	if code, stdout, _ := runCLI(t, "user-group", "delete", userGroupID, "--dry-run"); code != ExitOK || !strings.Contains(stdout, "DELETE") {
		t.Fatalf("delete dry run: %d %s", code, stdout)
	}
	api.on("DELETE", itemPath, 200, "")
	if code, stdout, _ := runCLI(t, "user-group", "delete", userGroupID, "--yes", "--json"); code == ExitOK || !strings.Contains(stdout, "unknown") {
		t.Fatalf("unexpected status: %d %s", code, stdout)
	}
	api.on("DELETE", itemPath, 404, `{"code":"not_found","message":"user group not found"}`)
	if code, _, _ := runCLI(t, "user-group", "delete", userGroupID, "--yes"); code != ExitNotFound {
		t.Fatalf("missing: %d", code)
	}

	members := itemPath + "/members"
	api.on("POST", members, 204, "")
	if code, stdout, _ := runCLI(t, "user-group", "member", "add", userGroupID, memberUserID, "--json"); code != ExitOK || !strings.Contains(stdout, `"changed": true`) {
		t.Fatalf("add: %d %s", code, stdout)
	}
	if code, stdout, _ := runCLI(t, "user-group", "member", "add", userGroupID, memberUserID, "--dry-run"); code != ExitOK || !strings.Contains(stdout, "POST") {
		t.Fatalf("add dry run: %d %s", code, stdout)
	}
	api.on("POST", members, 409, `{"code":"member_exists","message":"the user is already a member of the group"}`)
	if code, stdout, _ := runCLI(t, "user-group", "member", "add", userGroupID, memberUserID); code != ExitOK || !strings.Contains(stdout, "already in") {
		t.Fatalf("repeated add is idempotent: %d %s", code, stdout)
	}
	api.on("POST", members, 404, `{"code":"not_found","message":"user not found"}`)
	if code, _, _ := runCLI(t, "user-group", "member", "add", userGroupID, memberUserID); code != ExitNotFound {
		t.Fatalf("unknown user: %d", code)
	}
	api.on("POST", members, 200, "")
	if code, stdout, _ := runCLI(t, "user-group", "member", "add", userGroupID, memberUserID, "--json"); code == ExitOK || !strings.Contains(stdout, "unknown") {
		t.Fatalf("unexpected status: %d %s", code, stdout)
	}

	member := members + "/" + memberUserID
	api.on("DELETE", member, 204, "")
	if code, stdout, _ := runCLI(t, "user-group", "member", "remove", userGroupID, memberUserID, "--yes", "--json"); code != ExitOK || !strings.Contains(stdout, `"changed": true`) {
		t.Fatalf("remove: %d %s", code, stdout)
	}
	if code, stdout, _ := runCLI(t, "user-group", "member", "remove", userGroupID, memberUserID, "--dry-run"); code != ExitOK || !strings.Contains(stdout, "DELETE") {
		t.Fatalf("remove dry run: %d %s", code, stdout)
	}
	api.on("DELETE", member, 404, `{"code":"not_found","message":"the user is not a member of the group"}`)
	if code, stdout, _ := runCLI(t, "user-group", "member", "remove", userGroupID, memberUserID, "--yes"); code != ExitOK || !strings.Contains(stdout, "is not in") {
		t.Fatalf("repeated remove is idempotent: %d %s", code, stdout)
	}
	api.on("DELETE", member, 404, `{"code":"not_found","message":"user group not found"}`)
	if code, _, _ := runCLI(t, "user-group", "member", "remove", userGroupID, memberUserID, "--yes"); code != ExitNotFound {
		t.Fatalf("missing group: %d", code)
	}
}
