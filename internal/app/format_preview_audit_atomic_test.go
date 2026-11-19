package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

// TestFormatPreviewMutationsRollBackWithTheirAudit proves ADR 0023 section 1 for the Helm and
// NuGet preview handlers on the memory store: a failing audit fails the change itself.
func TestFormatPreviewMutationsRollBackWithTheirAudit(t *testing.T) {
	store := repository.NewMemoryStore()
	runFormatPreviewAuditRollback(t, store, func(...string) {
		store.SetAuditFault(func(record repository.AuditRecord) error {
			if strings.Contains(record.Operation, ".preview.") {
				return errors.New("audit store unavailable")
			}
			return nil
		})
	}, func() { store.SetAuditFault(nil) })
}

// formatPreviewRollbackStore is what the preview handlers under test and the assertions need.
type formatPreviewRollbackStore interface {
	GatewayStore
	repository.NativeHelmStore
	repository.HelmGovernanceStore
	repository.HelmSourceStore
	repository.NativeNuGetStore
	repository.NuGetSourceStore
}

// runFormatPreviewAuditRollback drives every audited preview handler (create, quota, configure,
// group create and group configure of each format) while audits fail, asserts the change did not
// take effect and the answer is never audit_failed, then lets audits through and proves the same
// requests succeed. fail receives the repository and group names whose audits must fail.
func runFormatPreviewAuditRollback(t *testing.T, store formatPreviewRollbackStore, fail func(names ...string), clear func()) {
	t.Helper()
	ctx := context.Background()
	handler := newTestGateway(t, GatewayOptions{
		Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator(),
		HelmPreviewEnabled: true, HelmStore: store, HelmGovernance: store, HelmSources: store, HelmObjects: objectstore.NewMemoryStore(),
		NuGetPreviewEnabled: true, NuGetStore: store, NuGetSources: store, NuGetObjects: objectstore.NewMemoryStore(),
	})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		response := nugetGatewayRequest(t, handler, method, path, body, true)
		return response
	}
	suffix := uuid.NewString()[:8]
	for _, format := range []string{"helm", "nuget"} {
		base := "/api/v2/previews/" + format
		repoName, groupName := format+"-audit-"+suffix, format+"-audit-group-"+suffix
		newRepo, newGroup := format+"-audit-new-"+suffix, format+"-audit-group-new-"+suffix

		// Fixtures: a repository and a group, created while audits work.
		var repo struct{ ID, Version string }
		if response := call(http.MethodPost, base+"/repositories", `{"name":"`+repoName+`","visibility":"private"}`); response.Code != http.StatusCreated {
			t.Fatalf("%s fixture repository: %d %s", format, response.Code, response.Body.String())
		} else {
			decodeJSONBody(t, response, &repo)
		}
		var group struct{ ID, Version string }
		if response := call(http.MethodPost, base+"/groups", `{"name":"`+groupName+`","visibility":"private","members":["`+repo.ID+`"]}`); response.Code != http.StatusCreated {
			t.Fatalf("%s fixture group: %d %s", format, response.Code, response.Body.String())
		} else {
			decodeJSONBody(t, response, &group)
		}

		fail(repoName, groupName, newRepo, newGroup)
		cases := []struct {
			name, method, path, body string
			unchanged                func(t *testing.T)
		}{
			{"repository create", http.MethodPost, base + "/repositories", `{"name":"` + newRepo + `","visibility":"private"}`, func(t *testing.T) {
				if _, err := store.GetHostedRepositoryByName(ctx, newRepo); !errors.Is(err, repository.ErrNotFound) {
					t.Fatalf("repository created: %v", err)
				}
			}},
			{"quota configure", http.MethodPut, base + "/repositories/" + repo.ID + "/capacity", `{"quotaBytes":777}`, func(t *testing.T) {
				if got, err := store.GetRepositoryCapacity(ctx, repo.ID); err != nil || got.QuotaBytes == 777 {
					t.Fatalf("quota replaced: %+v %v", got, err)
				}
			}},
			{"repository configure", http.MethodPut, base + "/repositories/" + repo.ID, `{"name":"` + repoName + `","visibility":"public","version":"` + repo.Version + `"}`, func(t *testing.T) {
				if got, err := store.GetHostedRepository(ctx, repo.ID); err != nil || got.Version != repo.Version || got.Visibility == repository.VisibilityPublic {
					t.Fatalf("repository configured: %+v %v", got, err)
				}
			}},
			{"group create", http.MethodPost, base + "/groups", `{"name":"` + newGroup + `","visibility":"private","members":["` + repo.ID + `"]}`, func(t *testing.T) {
				if _, err := store.GetHostedGroupByName(ctx, newGroup); !errors.Is(err, repository.ErrNotFound) {
					t.Fatalf("group created: %v", err)
				}
			}},
			{"group configure", http.MethodPut, base + "/groups/" + group.ID, `{"name":"` + groupName + `","visibility":"public","members":["` + repo.ID + `"],"version":"` + group.Version + `"}`, func(t *testing.T) {
				if got, err := store.GetHostedGroup(ctx, group.ID); err != nil || got.Version != group.Version || got.Visibility == repository.VisibilityPublic {
					t.Fatalf("group configured: %+v %v", got, err)
				}
			}},
		}
		for _, c := range cases {
			response := call(c.method, c.path, c.body)
			if response.Code < 400 {
				t.Fatalf("%s %s: status %d, want a failure: %s", format, c.name, response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "audit_failed") {
				t.Fatalf("%s %s: answered audit_failed for a change that must not take effect: %s", format, c.name, response.Body.String())
			}
			c.unchanged(t)
		}
		clear()
		for _, c := range cases {
			if response := call(c.method, c.path, c.body); response.Code >= 400 {
				t.Fatalf("%s %s after audits work again: %d %s", format, c.name, response.Code, response.Body.String())
			}
			// The configure cases bump the version; later cases of the same format do not reuse it.
		}
	}
}

func decodeJSONBody(t *testing.T, response *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), into); err != nil {
		t.Fatalf("decode %s: %v", response.Body.String(), err)
	}
}
