package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/repository"
)

// addRawAsset publishes one more Raw asset in the fixture's source repository and returns it.
func (f provenanceFixture) addRawAsset(t *testing.T, path string) repository.RawAsset {
	t.Helper()
	sum := sha256.Sum256([]byte("batch content " + path))
	hexSum := fmt.Sprintf("%x", sum[:])
	asset := repository.RawAsset{RepositoryID: f.source.ID, Path: path, Digest: "sha256:" + hexSum, ObjectKey: "native/raw/sha256/" + hexSum, Size: 1, ContentType: "text/plain"}
	if _, err := f.store.PutRawAsset(context.Background(), asset); err != nil {
		t.Fatal(err)
	}
	return asset
}

func (f provenanceFixture) batchBody(items ...string) string {
	return `{"sourceRepositoryId":"` + f.source.ID + `","targetRepositoryId":"` + f.target.ID + `","items":[` + strings.Join(items, ",") + `]}`
}

func batchItem(coordinate, digest string) string {
	return `{"coordinate":"` + coordinate + `","digest":"` + digest + `"}`
}

func (f provenanceFixture) postBatch(t *testing.T, body, key, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v2/promotion-batches", strings.NewReader(body))
	if token != "" {
		authorize(request, token)
	}
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func (f provenanceFixture) getBatch(t *testing.T, id, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v2/promotion-batches/"+id, nil)
	if token != "" {
		authorize(request, token)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func decodeBatch(t *testing.T, response *httptest.ResponseRecorder) adminopenapi.PromotionBatch {
	t.Helper()
	var batch adminopenapi.PromotionBatch
	if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil {
		t.Fatalf("%v: %s", err, response.Body.String())
	}
	return batch
}

func TestPromotionBatchQueuesEveryItemAndReplayReturnsTheSameBatch(t *testing.T) {
	f := newProvenanceFixture(t)
	second := f.addRawAsset(t, "releases/second.txt")
	body := f.batchBody(batchItem(f.asset.Path, f.asset.Digest), batchItem(second.Path, second.Digest))
	first := f.postBatch(t, body, "batch-key", f.token)
	if first.Code != http.StatusAccepted {
		t.Fatalf("batch=%d %s", first.Code, first.Body.String())
	}
	batch := decodeBatch(t, first)
	if batch.State != adminopenapi.PromotionBatchStatePending || len(batch.Items) != 2 || batch.RequestedBy != "promoter" {
		t.Fatalf("batch=%+v", batch)
	}
	for index, item := range batch.Items {
		if item.Index != index || item.State != adminopenapi.PromotionBatchItemStateQueued || item.PromotionId == nil || item.JobId == nil {
			t.Fatalf("item %d=%+v", index, item)
		}
	}
	replay := f.postBatch(t, body, "batch-key", f.token)
	if replay.Code != http.StatusAccepted || decodeBatch(t, replay).Id != batch.Id {
		t.Fatalf("replay=%d %s", replay.Code, replay.Body.String())
	}
	if rows := f.list(t, "", f.token); len(rows.Items) != 2 {
		t.Fatalf("a batch and its replay wrote %d provenance rows, want 2", len(rows.Items))
	}
	if conflict := f.postBatch(t, f.batchBody(batchItem(f.asset.Path, f.asset.Digest)), "batch-key", f.token); conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "idempotency_conflict") {
		t.Fatalf("same key, other request=%d %s", conflict.Code, conflict.Body.String())
	}
	if other := decodeBatch(t, f.postBatch(t, body, "other-key", f.token)); other.Id == batch.Id {
		t.Fatal("a new key must create a new batch")
	}
	shown := f.getBatch(t, batch.Id.String(), f.token)
	if shown.Code != http.StatusOK || decodeBatch(t, shown).Id != batch.Id {
		t.Fatalf("get=%d %s", shown.Code, shown.Body.String())
	}
	if missing := f.getBatch(t, "0198f8a0-0000-7000-8000-000000000000", f.token); missing.Code != http.StatusNotFound {
		t.Fatalf("unknown batch=%d", missing.Code)
	}
}

func TestPromotionBatchAdmissionIsAllOrNothing(t *testing.T) {
	f := newProvenanceFixture(t)
	quarantined := f.addRawAsset(t, "releases/quarantined.txt")
	if _, err := f.store.ReplaceArtifactQuarantine(context.Background(), repository.ArtifactQuarantine{RepositoryID: f.source.ID, Format: repository.FormatRaw, Coordinate: quarantined.Path, Digest: quarantined.Digest, State: repository.ArtifactQuarantineStateQuarantined, Reason: "blocked", UpdatedBy: "user:security-admin"}, "0"); err != nil {
		t.Fatal(err)
	}
	body := f.batchBody(batchItem(f.asset.Path, f.asset.Digest), batchItem(quarantined.Path, quarantined.Digest), batchItem(f.asset.Path, f.asset.Digest), batchItem("bad coordinate", "nope"))
	response := f.postBatch(t, body, "refused", f.token)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("refused batch=%d %s", response.Code, response.Body.String())
	}
	var problem adminopenapi.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.Code != adminopenapi.ProblemCodePromotionBatchRejected || problem.Items == nil {
		t.Fatalf("problem=%+v err=%v", problem, err)
	}
	refused := map[int]string{}
	for _, item := range *problem.Items {
		refused[item.Index] = item.Code
	}
	if len(refused) != 3 || refused[0] != "" || refused[1] != "artifact_quarantined" || refused[2] != "invalid_request" || refused[3] != "invalid_request" {
		t.Fatalf("per-item problems=%v", refused)
	}
	if rows := f.list(t, "", f.token); len(rows.Items) != 0 {
		t.Fatalf("a refused batch queued %d items", len(rows.Items))
	}
	// The valid item alone is accepted under the same key: the refused batch left no record behind.
	if ok := f.postBatch(t, f.batchBody(batchItem(f.asset.Path, f.asset.Digest)), "refused", f.token); ok.Code != http.StatusAccepted {
		t.Fatalf("key of a refused batch must stay usable: %d %s", ok.Code, ok.Body.String())
	}
}

// countingPromotionStore counts the target repository and security policy reads a batch makes.
type countingPromotionStore struct {
	*repository.MemoryStore
	targetReads, policyReads int
}

func (s *countingPromotionStore) GetHostedRepository(ctx context.Context, id string) (repository.HostedRepository, error) {
	s.targetReads++
	return s.MemoryStore.GetHostedRepository(ctx, id)
}

func (s *countingPromotionStore) GetRepositorySecurityPolicy(ctx context.Context, id string) (repository.RepositorySecurityPolicy, error) {
	s.policyReads++
	return s.MemoryStore.GetRepositorySecurityPolicy(ctx, id)
}

func TestPromotionBatchLoadsTargetAndPolicyOncePerStage(t *testing.T) {
	f := newProvenanceFixture(t)
	counting := &countingPromotionStore{MemoryStore: f.store}
	service := PromotionBatchService{
		PromotionService: PromotionService{
			store: counting, securityPolicies: counting, intelligence: f.store, promotions: f.store,
			registry: promotionFormatRegistry(promotionModuleDeps{Store: f.store}),
			quarantine: func(context.Context, Principal, repository.HostedRepository, repository.HostedRepository, string, string) *PromotionError {
				return nil
			},
		},
		batches: f.store,
	}
	items := make([]repository.PromotionBatchItem, 0, 20)
	for i := 0; i < 20; i++ {
		asset := f.addRawAsset(t, fmt.Sprintf("releases/bulk-%d.txt", i))
		items = append(items, repository.PromotionBatchItem{Coordinate: asset.Path, Digest: asset.Digest})
	}
	view, perr := service.Create(context.Background(), PromotionBatchRequest{Principal: Principal{Actor: "promoter", Admin: true}, Source: f.source, TargetID: f.target.ID, Items: items, IdempotencyKey: "bulk"})
	if perr != nil || len(view.Items) != len(items) {
		t.Fatalf("batch=%+v err=%+v", view, perr)
	}
	// One read for admission of the whole batch and one for queueing it, however many items it holds.
	if counting.targetReads != 2 || counting.policyReads != 2 {
		t.Fatalf("target reads=%d policy reads=%d for %d items, want 2 and 2", counting.targetReads, counting.policyReads, len(items))
	}
}

func TestPromotionBatchRejectsMoreThanOneHundredItems(t *testing.T) {
	f := newProvenanceFixture(t)
	items := make([]string, repository.MaxPromotionBatchItems+1)
	for index := range items {
		items[index] = batchItem(f.asset.Path, f.asset.Digest)
	}
	if response := f.postBatch(t, f.batchBody(items...), "too-many", f.token); response.Code != http.StatusBadRequest {
		t.Fatalf("101 items=%d %s", response.Code, response.Body.String())
	}
	if response := f.postBatch(t, f.batchBody(), "none", f.token); response.Code != http.StatusBadRequest {
		t.Fatalf("0 items=%d %s", response.Code, response.Body.String())
	}
	if rows := f.list(t, "", f.token); len(rows.Items) != 0 {
		t.Fatalf("a rejected batch queued %d items", len(rows.Items))
	}
}

func TestPromotionBatchAggregatesPartialFailure(t *testing.T) {
	f := newProvenanceFixture(t)
	second := f.addRawAsset(t, "releases/second.txt")
	batch := decodeBatch(t, f.postBatch(t, f.batchBody(batchItem(f.asset.Path, f.asset.Digest), batchItem(second.Path, second.Digest)), "partial", f.token))
	ctx := context.Background()
	claimed, err := f.store.ClaimLifecycleJobsByKind(ctx, repository.LifecycleJobPromotion, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	state := func() adminopenapi.PromotionBatchState {
		return decodeBatch(t, f.getBatch(t, batch.Id.String(), f.token)).State
	}
	if got := state(); got != adminopenapi.PromotionBatchStateRunning {
		t.Fatalf("one item running: %s", got)
	}
	if err = f.store.CompleteLifecycleJob(ctx, claimed[0].ID, claimed[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != adminopenapi.PromotionBatchStateRunning {
		t.Fatalf("one completed, one queued: %s", got)
	}
	var other repository.LifecycleJob
	for _, item := range decodeBatch(t, f.getBatch(t, batch.Id.String(), f.token)).Items {
		if item.State == adminopenapi.PromotionBatchItemStateQueued {
			other, err = f.store.GetLifecycleJob(ctx, f.target.ID, item.JobId.String())
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = f.store.CancelLifecycleJob(ctx, f.target.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != adminopenapi.PromotionBatchStatePartiallyFailed {
		t.Fatalf("one completed, one cancelled: %s", got)
	}
}

func TestAggregatePromotionBatchState(t *testing.T) {
	for _, test := range []struct {
		states []string
		want   string
	}{
		{[]string{"pending", "queued"}, "pending"},
		{[]string{"queued", "running"}, "running"},
		{[]string{"completed", "queued"}, "running"},
		{[]string{"completed", "completed"}, "completed"},
		{[]string{"failed", "failed"}, "failed"},
		{[]string{"completed", "failed"}, "partially_failed"},
		{[]string{"completed", "cancelled", "completed"}, "partially_failed"},
	} {
		if got := aggregatePromotionBatchState(test.states); got != test.want {
			t.Errorf("%v = %s, want %s", test.states, got, test.want)
		}
	}
}

func TestPromotionBatchIsAuthorisedOnBothRepositories(t *testing.T) {
	f := newProvenanceFixture(t)
	ctx := context.Background()
	authenticator := testAuthenticator()
	grants := map[string][]repository.RepositoryGrant{
		f.source.ID: {{Principal: "source-only", Scopes: []string{"repositories:admin"}}, {Principal: "writer", Scopes: []string{"repositories:write"}}, {Principal: "reader", Scopes: []string{"repositories:read"}}, {Principal: "promoter", Scopes: []string{"repositories:admin", "repositories:read"}}},
		f.target.ID: {{Principal: "target-reader", Scopes: []string{"repositories:read"}}, {Principal: "writer", Scopes: []string{"repositories:write"}}, {Principal: "promoter", Scopes: []string{"repositories:admin", "repositories:read"}}},
	}
	for id, list := range grants {
		if _, err := f.store.ReplaceRepositoryGrants(ctx, id, list, "2"); err != nil {
			t.Fatal(err)
		}
	}
	body := f.batchBody(batchItem(f.asset.Path, f.asset.Digest))
	if response := f.postBatch(t, body, "anonymous", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous=%d", response.Code)
	}
	for _, name := range []string{"source-only", "writer", "reader", "target-reader"} {
		if response := f.postBatch(t, body, "key-"+name, authenticator.IssueToken(name)); response.Code != http.StatusForbidden {
			t.Fatalf("%s=%d %s", name, response.Code, response.Body.String())
		}
	}
	if rows := f.list(t, "", f.token); len(rows.Items) != 0 {
		t.Fatalf("a refused caller queued %d items", len(rows.Items))
	}
	batch := decodeBatch(t, f.postBatch(t, body, "granted", f.token))
	if response := f.getBatch(t, batch.Id.String(), authenticator.IssueToken("target-reader")); response.Code != http.StatusOK {
		t.Fatalf("target reader=%d %s", response.Code, response.Body.String())
	}
	if response := f.getBatch(t, batch.Id.String(), authenticator.IssueToken("source-only")); response.Code != http.StatusForbidden {
		t.Fatalf("a caller without target read=%d", response.Code)
	}
	if response := f.getBatch(t, batch.Id.String(), ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous get=%d", response.Code)
	}
}
