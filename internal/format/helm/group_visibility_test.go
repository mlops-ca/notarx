package helm

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/access"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

// ADR 0029: preview groups obey the group visibility mode like the management API.
func TestHelmPreviewGroupsObeyTheGroupVisibilityMode(t *testing.T) {
	for _, tc := range []struct {
		mode   access.GroupMode
		status int
	}{{access.GroupStrict, 409}, {"", 409}, {access.GroupLenient, 201}} {
		t.Run(fmt.Sprintf("mode=%q", tc.mode), func(t *testing.T) {
			feed := newHelmFeed(t, chartFixture(t, "owned", "1.0.0", "owned"), nil)
			s := repository.NewMemoryStore()
			o := moduleOptions(s, objectstore.NewMemoryStore())
			o.Sources, o.Configuration, o.Groups, o.GroupMode = s, s, s, tc.mode
			o.Upstream = helmSourceClient{feed.server.Client()}
			m, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			hosted, err := s.CreateHostedRepository(t.Context(), repository.HostedRepository{ID: uuid.NewString(), Name: "helm-hosted", Format: repository.FormatHelm, Type: repository.RepositoryTypeHosted})
			if err != nil {
				t.Fatal(err)
			}
			headers := map[string]string{"Authorization": "Bearer helm-owned-admin", "Idempotency-Key": uuid.NewString()}
			body, _ := json.Marshal(map[string]any{"name": "feed", "visibility": "public", "members": []string{hosted.ID}})
			create := request(t, m.Preview(), "POST", "/api/v2/previews/helm/groups", body, headers)
			if create.Code != tc.status {
				t.Fatalf("create = %d %s, want %d", create.Code, create.Body.String(), tc.status)
			}
			if tc.status != 409 {
				return
			}
			if !strings.Contains(create.Body.String(), "visibility_conflict") {
				t.Fatalf("conflict body = %s", create.Body.String())
			}
			body, _ = json.Marshal(map[string]any{"name": "feed", "visibility": "private", "members": []string{hosted.ID}})
			headers["Idempotency-Key"] = uuid.NewString()
			private := request(t, m.Preview(), "POST", "/api/v2/previews/helm/groups", body, headers)
			group, _ := s.GetHostedGroupByName(t.Context(), "feed")
			if private.Code != 201 || group.ID == "" {
				t.Fatalf("private create = %d %s", private.Code, private.Body.String())
			}
			body, _ = json.Marshal(map[string]any{"name": "feed", "visibility": "public", "version": group.Version, "members": []string{hosted.ID}})
			replace := request(t, m.Preview(), "PUT", "/api/v2/previews/helm/groups/"+group.ID, body, headers)
			if replace.Code != 409 || !strings.Contains(replace.Body.String(), "visibility_conflict") {
				t.Fatalf("replace = %d %s", replace.Code, replace.Body.String())
			}
		})
	}
}

// Lowering a preview repository below a group that contains it is refused in strict mode.
func TestHelmPreviewRepositoryLoweringObeysTheGroupVisibilityMode(t *testing.T) {
	for _, tc := range []struct {
		mode   access.GroupMode
		status int
	}{{access.GroupStrict, 409}, {access.GroupLenient, 200}} {
		t.Run(fmt.Sprintf("mode=%q", tc.mode), func(t *testing.T) {
			feed := newHelmFeed(t, chartFixture(t, "owned", "1.0.0", "owned"), nil)
			s := repository.NewMemoryStore()
			o := moduleOptions(s, objectstore.NewMemoryStore())
			o.Sources, o.Configuration, o.Groups, o.GroupMode = s, s, s, tc.mode
			o.Upstream = helmSourceClient{feed.server.Client()}
			m, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			hosted, err := s.CreateHostedRepository(t.Context(), repository.HostedRepository{ID: uuid.NewString(), Name: "public-hosted", Format: repository.FormatHelm, Type: repository.RepositoryTypeHosted, Visibility: repository.VisibilityPublic})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.CreateHostedGroupIdempotently(t.Context(), repository.HostedGroup{ID: uuid.NewString(), Name: "feed", Format: repository.FormatHelm, Visibility: repository.VisibilityPublic, Members: []repository.GroupMember{{RepositoryID: hosted.ID}}}, "test", uuid.NewString(), "p"); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]any{"name": "public-hosted", "visibility": "private", "version": hosted.Version})
			w := request(t, m.Preview(), "PUT", "/api/v2/previews/helm/repositories/"+hosted.ID, body, map[string]string{"Authorization": "Bearer helm-owned-admin"})
			if w.Code != tc.status {
				t.Fatalf("lowering = %d %s, want %d", w.Code, w.Body.String(), tc.status)
			}
		})
	}
}
