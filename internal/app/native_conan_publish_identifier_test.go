package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

// uuidBoundConanStore behaves like the PostgreSQL store, which binds the session id
// as a uuid: a malformed id is an error (SQLSTATE 22P02), not "not found".
type uuidBoundConanStore struct {
	*repository.MemoryStore
	malformed atomic.Int32
}

func (s *uuidBoundConanStore) GetConanPublishSession(ctx context.Context, id string) (repository.ConanPublishSession, error) {
	if _, err := uuid.Parse(id); err != nil {
		s.malformed.Add(1)
		return repository.ConanPublishSession{}, fmt.Errorf("pq: invalid input syntax for type uuid: %q (22P02)", id)
	}
	return s.MemoryStore.GetConanPublishSession(ctx, id)
}

// A Conan publish session id comes from the URL; a malformed one is a client error at
// the boundary and never reaches the uuid-typed store query (ADR 0023 section 4).
func TestNativeConanPublishSessionRejectsMalformedIdentifiersWithoutStoreQuery(t *testing.T) {
	store := &uuidBoundConanStore{MemoryStore: repository.NewMemoryStore()}
	repo, err := store.CreateHostedRepository(context.Background(), repository.HostedRepository{ID: uuid.NewString(), Name: "conan-hosted", Format: repository.FormatConan})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplaceRepositoryGrants(context.Background(), repo.ID, []repository.RepositoryGrant{{Principal: "build-agent", Scopes: []string{"repositories:read", "repositories:write"}}}, "1"); err != nil {
		t.Fatal(err)
	}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, ConanPublications: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator(), Storage: ObjectStores{repository.FormatConan: objectstore.NewMemoryStore()}})
	for _, tc := range []struct{ name, method, path string }{
		{"read", http.MethodGet, "/api/v2/conan-publish-sessions/not-a-uuid"},
		{"commit", http.MethodPost, "/api/v2/conan-publish-sessions/not-a-uuid:commit"},
		{"object upload", http.MethodPut, "/api/v2/conan-publish-sessions/12345/objects/conanfile.py"},
		{"truncated uuid", http.MethodGet, "/api/v2/conan-publish-sessions/11111111-1111-4111-8111"},
		{"sql text", http.MethodGet, "/api/v2/conan-publish-sessions/%27%20OR%201%3D1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, strings.NewReader("x"))
			request.Header.Set("Authorization", "Bearer resolver-secret")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			// The generated management router answers 400 for a path parameter that is
			// not a uuid; the hand-written handler answers 404. Either way it is a client
			// error that never reaches the store.
			if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if n := store.malformed.Load(); n != 0 {
		t.Fatalf("%d malformed identifiers reached the uuid-typed store query", n)
	}
}
