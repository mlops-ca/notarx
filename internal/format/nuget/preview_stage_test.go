package nuget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/authorization"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/repository"
)

// stagingOrderStore records the order of payload staging and publication locking.
type stagingOrderStore struct {
	*repository.MemoryStore
	events []string
	staged [][]byte
}

type stagedMarker struct{}

func (s *stagingOrderStore) StageNuGetNuspec(ctx context.Context, repo, id, version string, nuspec []byte) (context.Context, error) {
	s.events = append(s.events, "stage")
	s.staged = append(s.staged, append([]byte(nil), nuspec...))
	return context.WithValue(ctx, stagedMarker{}, true), nil
}

func (s *stagingOrderStore) LockNuGetPublication(ctx context.Context, repo, id, version string) (context.Context, func(), error) {
	if ctx.Value(stagedMarker{}) == nil {
		s.events = append(s.events, "lock-unstaged")
	} else {
		s.events = append(s.events, "lock-staged")
	}
	return s.MemoryStore.LockNuGetPublication(ctx, repo, id, version)
}

// TestNuGetPreviewCommitStagesTheNuspecBeforeThePublicationLock pins the order of a session commit: the session's
// nuspec is staged on the request context first and the lock is taken with that context.
func TestNuGetPreviewCommitStagesTheNuspecBeforeThePublicationLock(t *testing.T) {
	memory := repository.NewMemoryStore()
	store := &stagingOrderStore{MemoryStore: memory}
	repo, err := memory.CreateHostedRepository(context.Background(), repository.HostedRepository{ID: uuid.NewString(), Name: "nuget-test", Format: repository.FormatNuGet, Type: repository.RepositoryTypeHosted})
	if err != nil {
		t.Fatal(err)
	}
	auth := authorization.Authenticator{AdminToken: "nuget-test-admin", AdminActor: "operator"}
	m, err := New(Options{Store: store, Repositories: memory, Objects: objectstore.NewMemoryStore(), Authenticator: auth, Authorizer: authorization.RepositoryAuthorizer{Grants: memory, Legacy: auth}, Audit: memory, Anonymous: memory})
	if err != nil {
		t.Fatal(err)
	}
	body := packageFixture(t, "Acme.Core", "1.0.0", "")
	sum := sha256.Sum256(body)
	base := "/api/v2/previews/nuget/repositories/" + repo.ID + "/publication-sessions"
	in, _ := json.Marshal(publicationRequest{PackageID: "Acme.Core", Version: "1", Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(body))})
	created := request(t, m.Preview(), "POST", base, in, map[string]string{"Authorization": "Bearer nuget-test-admin", "Idempotency-Key": "stage-order"})
	if created.Code != 201 {
		t.Fatalf("reserve=%d %s", created.Code, created.Body.String())
	}
	var session previewSession
	if err = json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if w := request(t, m.Preview(), "PUT", base+"/"+session.ID+"/package", body, bearer); w.Code != 200 {
		t.Fatalf("stage=%d %s", w.Code, w.Body.String())
	}
	store.events, store.staged = nil, nil
	if w := request(t, m.Preview(), "POST", base+"/"+session.ID+"/commit", nil, bearer); w.Code != 200 {
		t.Fatalf("commit=%d %s", w.Code, w.Body.String())
	}
	if len(store.events) != 2 || store.events[0] != "stage" || store.events[1] != "lock-staged" {
		t.Fatalf("order=%v", store.events)
	}
	got, err := memory.GetNuGetSession(context.Background(), session.ID)
	if err != nil || len(store.staged) != 1 || string(store.staged[0]) != string(got.Nuspec) {
		t.Fatalf("staged %q, session nuspec %q, err %v", store.staged, got.Nuspec, err)
	}
	// A read stages nothing.
	store.events = nil
	if w := request(t, m.Preview(), "GET", base+"/"+session.ID, nil, bearer); w.Code != 200 || len(store.events) != 0 {
		t.Fatalf("read=%d events=%v", w.Code, store.events)
	}
}
