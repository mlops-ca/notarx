package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	"github.com/mlops-ca/notarx/internal/repository"
)

type keysetPage struct {
	Items         []json.RawMessage `json:"items"`
	NextPageToken *string           `json:"nextPageToken"`
}

// The lifecycle job and webhook listings continue past their first window with the returned token, and a
// token issued for other filters answers 400 (ADR 0023 section 8).
func TestManagementListingsContinuePastTheFirstWindowWithPageTokens(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	repo, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "keyset-api", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if _, _, err = store.EnqueueLifecycleJob(ctx, repository.LifecycleJob{ID: uuid.NewString(), RepositoryID: repo.ID, Kind: repository.LifecycleJobReclaim, IdempotencyKey: fmt.Sprint("k", i), Payload: []byte(`{"format":"raw"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		if _, err = store.CreateWebhookSubscription(ctx, repository.WebhookSubscription{ID: uuid.NewString(), Name: fmt.Sprint("hook-", i), EndpointURL: fmt.Sprintf("https://events.example.test/hooks/%d", i), SecretCiphertext: "sealed", EventTypes: []repository.WebhookEventType{repository.WebhookEventArtifactQuarantined}, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		if _, err = store.EnqueueWebhookEvent(ctx, repository.WebhookEvent{ID: uuid.NewString(), Type: repository.WebhookEventArtifactQuarantined, OccurredAt: time.Date(2026, 11, 2, 9, 0, i, 0, time.UTC), Data: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: testAuthenticator()})
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		authorize(r, "admin-secret")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	follow := func(path string, wantItems int) {
		t.Helper()
		total, token := 0, ""
		for range 10 {
			target := path
			if token != "" {
				target += "&pageToken=" + url.QueryEscape(token)
			}
			response := get(target)
			var page keysetPage
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil {
				t.Fatalf("%s: %d %s", target, response.Code, response.Body.String())
			}
			total += len(page.Items)
			if page.NextPageToken == nil {
				break
			}
			token = *page.NextPageToken
		}
		if total != wantItems {
			t.Fatalf("%s served %d items, want %d", path, total, wantItems)
		}
	}
	follow("/api/v2/repositories/"+repo.ID+"/lifecycle-jobs?limit=2", 5)
	follow("/api/v2/lifecycle-jobs?limit=2", 5)
	follow("/api/v2/webhook-subscriptions?limit=2", 3)
	follow("/api/v2/webhook-deliveries?limit=4", 9)

	var first adminopenapi.WebhookDeliveryPage
	response := get("/api/v2/webhook-deliveries?limit=4")
	if json.Unmarshal(response.Body.Bytes(), &first) != nil || first.NextPageToken == nil {
		t.Fatalf("first page %d %s", response.Code, response.Body.String())
	}
	for _, mismatched := range []string{
		"/api/v2/webhook-deliveries?limit=4&state=dead&pageToken=",
		"/api/v2/lifecycle-jobs?limit=4&pageToken=",
		"/api/v2/webhook-subscriptions?limit=4&pageToken=",
		"/api/v2/repositories/" + repo.ID + "/lifecycle-jobs?limit=4&pageToken=",
	} {
		if got := get(mismatched + url.QueryEscape(*first.NextPageToken)); got.Code != http.StatusBadRequest {
			t.Fatalf("%s with a token of another listing answered %d %s, want 400", mismatched, got.Code, got.Body.String())
		}
	}
	for _, path := range []string{"/api/v2/webhook-deliveries?limit=0", "/api/v2/webhook-subscriptions?limit=501", "/api/v2/repositories/" + repo.ID + "/lifecycle-jobs?limit=101", "/api/v2/lifecycle-jobs?limit=0"} {
		if got := get(path); got.Code != http.StatusBadRequest {
			t.Fatalf("%s answered %d %s, want 400", path, got.Code, got.Body.String())
		}
	}
}
