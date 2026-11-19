package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/access"
	"github.com/mlops-ca/notarx/internal/repository"
)

type visibilityFixture struct {
	t       *testing.T
	store   *repository.MemoryStore
	handler http.Handler
}

func newVisibilityFixture(t *testing.T, mode access.GroupMode) visibilityFixture {
	t.Helper()
	store := repository.NewMemoryStore()
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator(), GroupVisibilityMode: mode})
	return visibilityFixture{t: t, store: store, handler: handler}
}

func (f visibilityFixture) repo(name string, visibility repository.Visibility, proxy bool) repository.HostedRepository {
	f.t.Helper()
	repo := repository.HostedRepository{ID: uuid.NewString(), Name: name, Format: repository.FormatRaw, Visibility: visibility}
	if proxy {
		repo.Type, repo.Endpoint, repo.AllowedHosts = repository.RepositoryTypeProxy, "https://upstream.example", []string{"upstream.example"}
	}
	created, err := f.store.CreateHostedRepository(context.Background(), repo)
	if err != nil {
		f.t.Fatal(err)
	}
	return created
}

func (f visibilityFixture) call(method, path, body, ifMatch string) *httptest.ResponseRecorder {
	f.t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	authorize(request, "admin-secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", uuid.NewString())
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func groupBody(name string, visibility repository.Visibility, repos ...repository.HostedRepository) string {
	members := make([]string, 0, len(repos))
	for position, repo := range repos {
		members = append(members, `{"repositoryId":"`+repo.ID+`","position":`+string(rune('0'+position))+`}`)
	}
	return `{"name":"` + name + `","format":"raw","visibility":"` + string(visibility) + `","members":[` + strings.Join(members, ",") + `]}`
}

type conflictBody struct {
	Code    string `json:"code"`
	Members []struct {
		RepositoryID string `json:"repositoryId"`
		Name         string `json:"name"`
		Visibility   string `json:"visibility"`
	} `json:"members"`
}

func decodeConflict(t *testing.T, response *httptest.ResponseRecorder) conflictBody {
	t.Helper()
	var body conflictBody
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("conflict body: %v %s", err, response.Body.String())
	}
	return body
}

func TestStrictGroupModeRefusesAGroupMoreVisibleThanItsMembers(t *testing.T) {
	f := newVisibilityFixture(t, access.GroupStrict)
	public := f.repo("public-repo", repository.VisibilityPublic, false)
	authenticated := f.repo("authenticated-repo", repository.VisibilityAuthenticated, false)
	private := f.repo("private-repo", repository.VisibilityPrivate, false)
	privateProxy := f.repo("private-proxy", repository.VisibilityPrivate, true)

	for name, tc := range map[string]struct {
		visibility repository.Visibility
		members    []repository.HostedRepository
		conflicts  []string
	}{
		"public over public":           {repository.VisibilityPublic, []repository.HostedRepository{public}, nil},
		"authenticated over public":    {repository.VisibilityAuthenticated, []repository.HostedRepository{public, authenticated}, nil},
		"private over anything":        {repository.VisibilityPrivate, []repository.HostedRepository{private, public}, nil},
		"public over authenticated":    {repository.VisibilityPublic, []repository.HostedRepository{public, authenticated}, []string{"authenticated-repo"}},
		"public over private hosted":   {repository.VisibilityPublic, []repository.HostedRepository{public, private}, []string{"private-repo"}},
		"public over a private proxy":  {repository.VisibilityPublic, []repository.HostedRepository{privateProxy}, []string{"private-proxy"}},
		"authenticated over a private": {repository.VisibilityAuthenticated, []repository.HostedRepository{private, privateProxy}, []string{"private-repo", "private-proxy"}},
	} {
		t.Run(name, func(t *testing.T) {
			response := f.call(http.MethodPost, "/api/v2/groups", groupBody(strings.ReplaceAll(name, " ", "-"), tc.visibility, tc.members...), "")
			if tc.conflicts == nil {
				if response.Code != http.StatusCreated {
					t.Fatalf("create = %d %s", response.Code, response.Body.String())
				}
				return
			}
			if response.Code != http.StatusConflict {
				t.Fatalf("create = %d %s", response.Code, response.Body.String())
			}
			body := decodeConflict(t, response)
			if body.Code != "visibility_conflict" || len(body.Members) != len(tc.conflicts) {
				t.Fatalf("conflict body = %+v, want members %v", body, tc.conflicts)
			}
			for i, name := range tc.conflicts {
				if body.Members[i].Name != name || body.Members[i].RepositoryID == "" || body.Members[i].Visibility == "" {
					t.Fatalf("conflict member %d = %+v, want %s", i, body.Members[i], name)
				}
			}
		})
	}
}

func TestStrictGroupModeChecksReplacementsAndMemberChanges(t *testing.T) {
	f := newVisibilityFixture(t, access.GroupStrict)
	public := f.repo("public-repo", repository.VisibilityPublic, false)
	private := f.repo("private-repo", repository.VisibilityPrivate, false)
	created := f.call(http.MethodPost, "/api/v2/groups", groupBody("feed", repository.VisibilityPublic, public), "")
	var group repository.HostedGroup
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &group) != nil {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	// Adding a private member to a public group is refused, by whole replacement
	// and by member replacement alike.
	replace := f.call(http.MethodPut, "/api/v2/groups/"+group.ID, groupBody("feed", repository.VisibilityPublic, public, private), group.Version)
	if replace.Code != http.StatusConflict || decodeConflict(t, replace).Code != "visibility_conflict" {
		t.Fatalf("replace = %d %s", replace.Code, replace.Body.String())
	}
	members := f.call(http.MethodPut, "/api/v2/groups/"+group.ID+"/members", `[{"repositoryId":"`+public.ID+`","position":0},{"repositoryId":"`+private.ID+`","position":1}]`, group.Version)
	if members.Code != http.StatusConflict || decodeConflict(t, members).Code != "visibility_conflict" {
		t.Fatalf("members = %d %s", members.Code, members.Body.String())
	}
	// Raising the group above its members fails; lowering the group is fine.
	raise := f.call(http.MethodPut, "/api/v2/groups/"+group.ID, groupBody("feed", repository.VisibilityPublic, public), group.Version)
	if raise.Code != http.StatusOK {
		t.Fatalf("an unchanged valid replacement = %d %s", raise.Code, raise.Body.String())
	}
}

func TestStrictGroupModeRefusesLoweringAMemberBelowItsGroup(t *testing.T) {
	f := newVisibilityFixture(t, access.GroupStrict)
	public := f.repo("public-repo", repository.VisibilityPublic, false)
	other := f.repo("other-repo", repository.VisibilityPublic, false)
	if created := f.call(http.MethodPost, "/api/v2/groups", groupBody("feed", repository.VisibilityPublic, public), ""); created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	lower := f.call(http.MethodPatch, "/api/v2/repositories/"+public.ID, `{"visibility":"authenticated"}`, public.Version)
	if lower.Code != http.StatusConflict {
		t.Fatalf("lowering a member below its group = %d %s", lower.Code, lower.Body.String())
	}
	body := decodeConflict(t, lower)
	if body.Code != "visibility_conflict" || len(body.Members) != 1 || body.Members[0].Name != "public-repo" || body.Members[0].Visibility != "authenticated" {
		t.Fatalf("conflict body = %+v", body)
	}
	if stored, _ := f.store.GetHostedRepository(context.Background(), public.ID); stored.Visibility != repository.VisibilityPublic {
		t.Fatalf("a refused change was stored: %q", stored.Visibility)
	}
	// A repository outside any group, and raising a member, are unaffected.
	if free := f.call(http.MethodPatch, "/api/v2/repositories/"+other.ID, `{"visibility":"private"}`, other.Version); free.Code != http.StatusOK {
		t.Fatalf("lowering a repository outside every group = %d %s", free.Code, free.Body.String())
	}
	if same := f.call(http.MethodPatch, "/api/v2/repositories/"+public.ID, `{"visibility":"public"}`, public.Version); same.Code != http.StatusOK {
		t.Fatalf("an unchanged visibility = %d %s", same.Code, same.Body.String())
	}
}

func TestLenientGroupModeKeepsComputedFilteringOnly(t *testing.T) {
	f := newVisibilityFixture(t, access.GroupLenient)
	public := f.repo("public-repo", repository.VisibilityPublic, false)
	private := f.repo("private-repo", repository.VisibilityPrivate, false)
	if created := f.call(http.MethodPost, "/api/v2/groups", groupBody("feed", repository.VisibilityPublic, public, private), ""); created.Code != http.StatusCreated {
		t.Fatalf("lenient create = %d %s", created.Code, created.Body.String())
	}
	if lower := f.call(http.MethodPatch, "/api/v2/repositories/"+public.ID, `{"visibility":"private"}`, public.Version); lower.Code != http.StatusOK {
		t.Fatalf("lenient lowering = %d %s", lower.Code, lower.Body.String())
	}
	// The anonymous view of that group still hides the private members.
	enableAnonymousAccess(t, f.store)
	group, err := f.store.GetHostedGroupByName(context.Background(), "feed")
	if err != nil {
		t.Fatal(err)
	}
	members := make([]repository.Member, 0, len(group.Members))
	for _, member := range group.Members {
		repo, _ := f.store.GetHostedRepository(context.Background(), member.RepositoryID)
		members = append(members, access.BindMember(repository.Member{Name: repo.Name, RepositoryID: repo.ID}, repo))
	}
	if got := access.AnonymousGroupMembers(context.Background(), f.store, group, members); len(got) != 0 {
		t.Fatalf("anonymous members of a lenient group = %v", got)
	}
}

func TestGroupVisibilityModeDefaultsToStrict(t *testing.T) {
	f := newVisibilityFixture(t, "")
	public := f.repo("public-repo", repository.VisibilityPublic, false)
	private := f.repo("private-repo", repository.VisibilityPrivate, false)
	if response := f.call(http.MethodPost, "/api/v2/groups", groupBody("feed", repository.VisibilityPublic, public, private), ""); response.Code != http.StatusConflict {
		t.Fatalf("a handler without a mode must be strict: %d %s", response.Code, response.Body.String())
	}
}

func TestStrictModeKeepsStatusOrderOnLowering(t *testing.T) {
	f := newVisibilityFixture(t, access.GroupStrict)
	public := f.repo("public-repo", repository.VisibilityPublic, false)
	if created := f.call(http.MethodPost, "/api/v2/groups", groupBody("feed", repository.VisibilityPublic, public), ""); created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	// A stale If-Match keeps its 412 and a malformed body its 400 ahead of the 409.
	if stale := f.call(http.MethodPatch, "/api/v2/repositories/"+public.ID, `{"visibility":"private"}`, "999"); stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("a stale If-Match = %d %s, want 412", stale.Code, stale.Body.String())
	}
	if bad := f.call(http.MethodPatch, "/api/v2/repositories/"+public.ID, `{"visibility":"hidden"}`, public.Version); bad.Code != http.StatusBadRequest {
		t.Fatalf("an invalid visibility = %d %s, want 400", bad.Code, bad.Body.String())
	}
}

// The lenient mode shows the members a group's callers cannot see on the read.
func TestLenientGroupReadsNameTheLessVisibleMembers(t *testing.T) {
	f := newVisibilityFixture(t, access.GroupLenient)
	public := f.repo("public-repo", repository.VisibilityPublic, false)
	private := f.repo("private-repo", repository.VisibilityPrivate, false)
	created := f.call(http.MethodPost, "/api/v2/groups", groupBody("feed", repository.VisibilityPublic, public, private), "")
	var group repository.HostedGroup
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &group) != nil || len(group.LessVisibleMembers) != 1 {
		t.Fatalf("lenient create = %d %s", created.Code, created.Body.String())
	}
	read := f.call(http.MethodGet, "/api/v2/groups/"+group.ID, "", "")
	var got repository.HostedGroup
	if read.Code != http.StatusOK || json.Unmarshal(read.Body.Bytes(), &got) != nil {
		t.Fatalf("read = %d %s", read.Code, read.Body.String())
	}
	if len(got.LessVisibleMembers) != 1 || got.LessVisibleMembers[0].Name != "private-repo" || got.LessVisibleMembers[0].Visibility != repository.VisibilityPrivate {
		t.Fatalf("lessVisibleMembers = %+v", got.LessVisibleMembers)
	}
	list := f.call(http.MethodGet, "/api/v2/groups", "", "")
	var page struct{ Items []repository.HostedGroup }
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &page) != nil || len(page.Items) != 1 || len(page.Items[0].LessVisibleMembers) != 1 {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	// A group whose members are all at least as visible carries no indicator.
	clean := f.call(http.MethodPost, "/api/v2/groups", groupBody("clean", repository.VisibilityPrivate, public, private), "")
	var cleanGroup repository.HostedGroup
	_ = json.Unmarshal(clean.Body.Bytes(), &cleanGroup)
	read = f.call(http.MethodGet, "/api/v2/groups/"+cleanGroup.ID, "", "")
	if strings.Contains(read.Body.String(), "lessVisibleMembers") {
		t.Fatalf("a clean group read = %s", read.Body.String())
	}
}

// Strict mode refuses such groups on write, so reads skip the member lookups.
func TestStrictGroupReadsSkipTheLessVisibleMembersIndicator(t *testing.T) {
	f := newVisibilityFixture(t, access.GroupStrict)
	public := f.repo("public-repo", repository.VisibilityPublic, false)
	private := f.repo("private-repo", repository.VisibilityPrivate, false)
	// A layout written before strict mode (no ceiling mark on the store write).
	group, _, err := f.store.CreateHostedGroupIdempotently(context.Background(), repository.HostedGroup{ID: uuid.NewString(), Name: "legacy", Format: repository.FormatRaw, Visibility: repository.VisibilityPublic, Members: []repository.GroupMember{{RepositoryID: public.ID}, {RepositoryID: private.ID, Position: 1}}}, "test", uuid.NewString(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if read := f.call(http.MethodGet, "/api/v2/groups/"+group.ID, "", ""); read.Code != http.StatusOK || strings.Contains(read.Body.String(), "lessVisibleMembers") {
		t.Fatalf("strict read = %d %s", read.Code, read.Body.String())
	}
}
