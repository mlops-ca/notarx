package nuget

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/access"
	"github.com/mlops-ca/notarx/internal/repository"
)

func previewGroupRequest(f fixture, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://gateway.test/api/v2/previews/nuget/groups"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer nuget-test-admin")
	r.Header.Set("Idempotency-Key", uuid.NewString())
	w := httptest.NewRecorder()
	f.module.Preview().ServeHTTP(w, r)
	return w
}

// ADR 0029: preview groups obey the group visibility mode like the management API.
func TestNuGetPreviewGroupsObeyTheGroupVisibilityMode(t *testing.T) {
	for _, tc := range []struct {
		mode   access.GroupMode
		status int
	}{{access.GroupStrict, 409}, {"", 409}, {access.GroupLenient, 201}} {
		t.Run(fmt.Sprintf("mode=%q", tc.mode), func(t *testing.T) {
			feed := newTestFeed(t, packageFixture(t, "Acme.Leaf", "1.0.0", ""))
			f, proxy := proxyFixture(t, feed)
			module, err := New(Options{Store: f.store, Repositories: f.store, Objects: f.objects, Authenticator: f.module.auth, Authorizer: f.module.access, Audit: f.store, Anonymous: f.store, Capacities: f.store, Sources: f.store, Configuration: f.store, Groups: f.store, Upstream: testSourceClient{feed.server.Client()}, GroupMode: tc.mode})
			if err != nil {
				t.Fatal(err)
			}
			f.module = module
			// Both members are private (the default), so a public group exceeds them.
			create := previewGroupRequest(f, "POST", "", fmt.Sprintf(`{"name":"feed","visibility":"public","members":[%q,%q]}`, f.repo.ID, proxy.ID))
			if create.Code != tc.status {
				t.Fatalf("create = %d %s, want %d", create.Code, create.Body.String(), tc.status)
			}
			if tc.status == 409 {
				if !strings.Contains(create.Body.String(), "visibility_conflict") {
					t.Fatalf("conflict body = %s", create.Body.String())
				}
				// A private group over private members is fine, and replacing it with a public one is not.
				ok := previewGroupRequest(f, "POST", "", fmt.Sprintf(`{"name":"feed","visibility":"private","members":[%q]}`, f.repo.ID))
				group, _ := f.store.GetHostedGroupByName(t.Context(), "feed")
				if ok.Code != 201 || group.ID == "" {
					t.Fatalf("private create = %d %s", ok.Code, ok.Body.String())
				}
				replace := previewGroupRequest(f, "PUT", "/"+group.ID, fmt.Sprintf(`{"name":"feed","visibility":"public","members":[%q],"version":%q}`, f.repo.ID, group.Version))
				if replace.Code != 409 || !strings.Contains(replace.Body.String(), "visibility_conflict") {
					t.Fatalf("replace = %d %s", replace.Code, replace.Body.String())
				}
			}
		})
	}
}

// Lowering a preview repository below a group that contains it is refused in strict mode.
func TestNuGetPreviewRepositoryLoweringObeysTheGroupVisibilityMode(t *testing.T) {
	for _, tc := range []struct {
		mode   access.GroupMode
		status int
	}{{access.GroupStrict, 409}, {access.GroupLenient, 200}} {
		t.Run(fmt.Sprintf("mode=%q", tc.mode), func(t *testing.T) {
			feed := newTestFeed(t, packageFixture(t, "Acme.Leaf", "1.0.0", ""))
			f, _ := proxyFixture(t, feed)
			module, err := New(Options{Store: f.store, Repositories: f.store, Objects: f.objects, Authenticator: f.module.auth, Authorizer: f.module.access, Audit: f.store, Anonymous: f.store, Capacities: f.store, Sources: f.store, Configuration: f.store, Groups: f.store, Upstream: testSourceClient{feed.server.Client()}, GroupMode: tc.mode})
			if err != nil {
				t.Fatal(err)
			}
			f.module = module
			hosted, err := f.store.CreateHostedRepository(t.Context(), repository.HostedRepository{ID: uuid.NewString(), Name: "public-hosted", Format: repository.FormatNuGet, Type: repository.RepositoryTypeHosted, Visibility: repository.VisibilityPublic})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.store.CreateHostedGroupIdempotently(t.Context(), repository.HostedGroup{ID: uuid.NewString(), Name: "feed", Format: repository.FormatNuGet, Visibility: repository.VisibilityPublic, Members: []repository.GroupMember{{RepositoryID: hosted.ID}}}, "test", uuid.NewString(), "p"); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("PUT", "https://gateway.test/api/v2/previews/nuget/repositories/"+hosted.ID, strings.NewReader(fmt.Sprintf(`{"name":"public-hosted","visibility":"private","version":%q}`, hosted.Version)))
			r.Header.Set("Authorization", "Bearer nuget-test-admin")
			w := httptest.NewRecorder()
			f.module.Preview().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("lowering = %d %s, want %d", w.Code, w.Body.String(), tc.status)
			}
		})
	}
}
