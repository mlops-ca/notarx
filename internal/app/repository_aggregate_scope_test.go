package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/repository"
)

// The cross-repository management views show a repository administrator its own
// repositories and nothing else, so a repository it does not administer
// contributes no rows and cannot be told apart from one that does not exist.
func TestRepositoryConsoleAggregatesFollowRepositoryAdministration(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	managed, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "aggregate-managed", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "aggregate-foreign", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	for repositoryID, principal := range map[string]string{managed.ID: "repo-admin", foreign.ID: "other-admin"} {
		if _, err := store.ReplaceRepositoryGrants(ctx, repositoryID, []repository.RepositoryGrant{
			{Principal: principal, Scopes: []string{"repositories:admin"}},
		}, "1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.PutRawAsset(ctx, repository.RawAsset{RepositoryID: managed.ID, Path: "release.txt", Digest: "sha256:scoped", Size: 11}); err != nil {
		t.Fatal(err)
	}
	job := repository.LifecycleJob{ID: uuid.NewString(), RepositoryID: managed.ID, Kind: repository.LifecycleJobRetention, IdempotencyKey: "scoped-retention"}
	if _, _, err := store.EnqueueLifecycleJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	authenticator := testAuthenticator()
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: authenticator})
	request := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			authorize(req, token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	grantRepositoryNames := func(token string) []string {
		t.Helper()
		response := request("/api/v2/repository-grants", token)
		if response.Code != http.StatusOK {
			t.Fatalf("grants=%d body=%s", response.Code, response.Body.String())
		}
		var records adminopenapi.RepositoryGrantRecordList
		if err := json.Unmarshal(response.Body.Bytes(), &records); err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(records))
		for _, record := range records {
			names = append(names, record.RepositoryName)
		}
		return names
	}
	jobRepositoryNames := func(token string) []string {
		t.Helper()
		response := request("/api/v2/lifecycle-jobs?limit=10", token)
		if response.Code != http.StatusOK {
			t.Fatalf("jobs=%d body=%s", response.Code, response.Body.String())
		}
		var jobs adminopenapi.RepositoryLifecycleJobPage
		if err := json.Unmarshal(response.Body.Bytes(), &jobs); err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(jobs.Items))
		for _, item := range jobs.Items {
			names = append(names, item.RepositoryName)
		}
		return names
	}
	capacityRepositoryCount := func(token string) int {
		t.Helper()
		response := request("/api/v2/repository-capacities", token)
		if response.Code != http.StatusOK {
			t.Fatalf("capacities=%d body=%s", response.Code, response.Body.String())
		}
		var capacities adminopenapi.RepositoryCapacityList
		if err := json.Unmarshal(response.Body.Bytes(), &capacities); err != nil {
			t.Fatal(err)
		}
		return len(capacities)
	}

	repoAdmin := authenticator.IssueToken("repo-admin")
	otherAdmin := authenticator.IssueToken("other-admin")
	stranger := authenticator.IssueToken("stranger")

	if names := grantRepositoryNames(repoAdmin); len(names) != 1 || names[0] != managed.Name {
		t.Fatalf("repository administrator sees grants for %v", names)
	}
	if names := grantRepositoryNames(otherAdmin); len(names) != 1 || names[0] != foreign.Name {
		t.Fatalf("other administrator sees grants for %v", names)
	}
	if names := grantRepositoryNames(stranger); len(names) != 0 {
		t.Fatalf("a caller with no administration sees grants for %v", names)
	}
	if names := grantRepositoryNames("admin-secret"); len(names) != 2 {
		t.Fatalf("platform administrator sees grants for %v", names)
	}

	if names := jobRepositoryNames(repoAdmin); len(names) != 1 || names[0] != managed.Name {
		t.Fatalf("repository administrator sees jobs for %v", names)
	}
	if names := jobRepositoryNames(otherAdmin); len(names) != 0 {
		t.Fatalf("other administrator sees jobs for %v", names)
	}
	if names := jobRepositoryNames("admin-secret"); len(names) != 1 {
		t.Fatalf("platform administrator sees jobs for %v", names)
	}

	if count := capacityRepositoryCount(repoAdmin); count != 1 {
		t.Fatalf("repository administrator sees %d capacities", count)
	}
	if count := capacityRepositoryCount(stranger); count != 0 {
		t.Fatalf("a caller with no administration sees %d capacities", count)
	}
	if count := capacityRepositoryCount("admin-secret"); count != 2 {
		t.Fatalf("platform administrator sees %d capacities", count)
	}
}

// A repository administrator's job listing is filtered by the store, so every page is full until the listing
// ends however many rows of other repositories sit between its own.
func TestLifecycleJobListingPagesFullForARepositoryAdministrator(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	managed, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "paged-managed", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "paged-foreign", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplaceRepositoryGrants(ctx, managed.ID, []repository.RepositoryGrant{{Principal: "repo-admin", Scopes: []string{"repositories:admin"}}}, "1"); err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		repositoryID := foreign.ID
		if i%10 == 0 {
			repositoryID = managed.ID
		}
		job := repository.LifecycleJob{ID: uuid.NewString(), RepositoryID: repositoryID, Kind: repository.LifecycleJobRetention, IdempotencyKey: fmt.Sprint("paged-", i)}
		if _, _, err = store.EnqueueLifecycleJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	authenticator := testAuthenticator()
	handler := newTestGateway(t, GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: authenticator})
	token := authenticator.IssueToken("repo-admin")
	var sizes []int
	path := "/api/v2/lifecycle-jobs?limit=2"
	for path != "" {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		authorize(req, token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("jobs=%d body=%s", response.Code, response.Body.String())
		}
		var page adminopenapi.RepositoryLifecycleJobPage
		if err = json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.RepositoryId.String() != managed.ID {
				t.Fatalf("a repository administrator received a job of %s", item.RepositoryName)
			}
		}
		sizes = append(sizes, len(page.Items))
		path = ""
		if page.NextPageToken != nil {
			path = "/api/v2/lifecycle-jobs?limit=2&pageToken=" + url.QueryEscape(*page.NextPageToken)
		}
	}
	if fmt.Sprint(sizes) != "[2 1]" {
		t.Fatalf("page sizes %v, want [2 1]: a full page, then the last job", sizes)
	}
}

// walkCountingStore counts the repository reads a request makes.
type walkCountingStore struct {
	*repository.MemoryStore
	listed, loaded int
}

func (s *walkCountingStore) ListHostedRepositories(ctx context.Context, size int, after string) ([]repository.HostedRepository, string, error) {
	s.listed++
	return s.MemoryStore.ListHostedRepositories(ctx, size, after)
}

func (s *walkCountingStore) GetHostedRepository(ctx context.Context, id string) (repository.HostedRepository, error) {
	s.loaded++
	return s.MemoryStore.GetHostedRepository(ctx, id)
}

// The repositories a caller administers come from the caller's own grants: the listing does not read every
// hosted repository, loads only the candidates the grants name, and still lets the authorizer decide on each.
func TestAdministeredRepositoriesComeFromTheGrantsNotAWalk(t *testing.T) {
	ctx := context.Background()
	memory := repository.NewMemoryStore()
	var managed, readOnly repository.HostedRepository
	for i := range 250 {
		repo, err := memory.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: fmt.Sprint("walk-", i), Format: repository.FormatRaw})
		if err != nil {
			t.Fatal(err)
		}
		managed, readOnly = repo, managed
	}
	if readOnly.ID == "" {
		t.Fatal("the fixture needs two repositories")
	}
	for repo, scope := range map[string]string{managed.ID: "repositories:admin", readOnly.ID: "repositories:read"} {
		if _, err := memory.ReplaceRepositoryGrants(ctx, repo, []repository.RepositoryGrant{{Principal: "repo-admin", Scopes: []string{scope}}}, "1"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := memory.EnqueueLifecycleJob(ctx, repository.LifecycleJob{ID: uuid.NewString(), RepositoryID: repo, Kind: repository.LifecycleJobRetention, IdempotencyKey: "walk-" + repo}); err != nil {
			t.Fatal(err)
		}
	}
	store := &walkCountingStore{MemoryStore: memory}
	authenticator := testAuthenticator()
	handler := newTestGateway(t, GatewayOptions{Store: store, Adapter: TestAdapter{}, Authenticator: authenticator})
	req := httptest.NewRequest(http.MethodGet, "/api/v2/lifecycle-jobs", nil)
	authorize(req, authenticator.IssueToken("repo-admin"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	var page adminopenapi.RepositoryLifecycleJobPage
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].RepositoryId.String() != managed.ID {
		t.Fatalf("jobs=%d body=%s: want only the job of the repository the caller administers", response.Code, response.Body.String())
	}
	if store.listed != 0 {
		t.Fatalf("the listing walked %d pages of the registry", store.listed)
	}
	if store.loaded > 2 {
		t.Fatalf("the listing loaded %d repositories for a caller with one admin grant", store.loaded)
	}
}
