package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// walkPages follows the token of every page and reports each page's item ids, so a test can assert that a
// listing continues past its first window without repeating or skipping rows.
func walkPages(t *testing.T, fetch func(token string) ([]string, string, error)) [][]string {
	t.Helper()
	var pages [][]string
	token := ""
	for range 20 {
		ids, next, err := fetch(token)
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, ids)
		if next == "" {
			return pages
		}
		token = next
	}
	t.Fatal("listing did not end")
	return nil
}

func assertDistinctNewestFirst(t *testing.T, pages [][]string, want int, sizes ...int) {
	t.Helper()
	seen := map[string]bool{}
	total := 0
	for i, page := range pages {
		if i < len(sizes) && len(page) != sizes[i] {
			t.Fatalf("page %d holds %d rows, want %d (pages %v)", i+1, len(page), sizes[i], pages)
		}
		for _, id := range page {
			if seen[id] {
				t.Fatalf("row %s served twice: %v", id, pages)
			}
			seen[id] = true
			total++
		}
	}
	if total != want {
		t.Fatalf("pages served %d rows, want %d: %v", total, want, pages)
	}
}

func TestMemoryLifecycleJobsPageByKeysetNewestFirst(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	repo, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "keyset-jobs", Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "keyset-other", Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		for _, id := range []string{repo.ID, other.ID} {
			if _, _, err = store.EnqueueLifecycleJob(ctx, LifecycleJob{ID: uuid.NewString(), RepositoryID: id, Kind: LifecycleJobReclaim, IdempotencyKey: fmt.Sprint("k", i), Payload: []byte(`{"format":"raw"}`)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	var createdAts []time.Time
	pages := walkPages(t, func(token string) ([]string, string, error) {
		page, err := store.PageLifecycleJobs(ctx, repo.ID, LifecycleJobListQuery{Limit: 2, PageToken: token})
		var ids []string
		for _, job := range page.Items {
			ids = append(ids, job.ID)
			createdAts = append(createdAts, job.CreatedAt)
		}
		return ids, page.NextPageToken, err
	})
	assertDistinctNewestFirst(t, pages, 5, 2, 2, 1)
	for i := 1; i < len(createdAts); i++ {
		if createdAts[i].After(createdAts[i-1]) {
			t.Fatalf("jobs are not newest first: %v", createdAts)
		}
	}
	allPages := walkPages(t, func(token string) ([]string, string, error) {
		page, err := store.PageAllLifecycleJobs(ctx, LifecycleJobListQuery{Limit: 4, PageToken: token})
		var ids []string
		for _, record := range page.Items {
			ids = append(ids, record.Job.ID)
		}
		return ids, page.NextPageToken, err
	})
	assertDistinctNewestFirst(t, allPages, 10, 4, 4, 2)

	first, err := store.PageLifecycleJobs(ctx, repo.ID, LifecycleJobListQuery{Limit: 2})
	if err != nil || first.NextPageToken == "" {
		t.Fatalf("first page %v %v", first, err)
	}
	if _, err = store.PageLifecycleJobs(ctx, other.ID, LifecycleJobListQuery{Limit: 2, PageToken: first.NextPageToken}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("a token of another repository must be refused, got %v", err)
	}
	if _, err = store.PageAllLifecycleJobs(ctx, LifecycleJobListQuery{Limit: 2, PageToken: first.NextPageToken}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("a repository token must not continue the aggregate listing, got %v", err)
	}
	if _, err = store.PageLifecycleJobs(ctx, repo.ID, LifecycleJobListQuery{PageToken: "not-a-token"}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("a malformed token must be refused, got %v", err)
	}
}

func TestMemoryWebhookListingsPageByKeysetAndBindTokensToFilters(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	var subscriptions []WebhookSubscription
	for i := range 3 {
		subscription, err := store.CreateWebhookSubscription(ctx, WebhookSubscription{
			ID: uuid.NewString(), Name: fmt.Sprint("hook-", i), EndpointURL: "https://events.example.test/hooks/" + fmt.Sprint(i),
			SecretCiphertext: "sealed", EventTypes: []WebhookEventType{WebhookEventArtifactQuarantined}, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		subscriptions = append(subscriptions, subscription)
	}
	subscriptionPages := walkPages(t, func(token string) ([]string, string, error) {
		page, err := store.PageWebhookSubscriptions(ctx, WebhookSubscriptionQuery{Limit: 2, PageToken: token})
		var ids []string
		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}
		return ids, page.NextPageToken, err
	})
	assertDistinctNewestFirst(t, subscriptionPages, 3, 2, 1)

	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	for i := range 4 {
		if _, err := store.EnqueueWebhookEvent(ctx, WebhookEvent{ID: uuid.NewString(), Type: WebhookEventArtifactQuarantined, OccurredAt: now.Add(time.Duration(i) * time.Second), Data: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	deliveryPages := walkPages(t, func(token string) ([]string, string, error) {
		page, err := store.PageWebhookDeliveries(ctx, WebhookDeliveryQuery{Limit: 5, PageToken: token})
		var ids []string
		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}
		return ids, page.NextPageToken, err
	})
	assertDistinctNewestFirst(t, deliveryPages, 12, 5, 5, 2)

	filtered := WebhookDeliveryQuery{SubscriptionID: subscriptions[0].ID, Limit: 3}
	page, err := store.PageWebhookDeliveries(ctx, filtered)
	if err != nil || len(page.Items) != 3 || page.NextPageToken == "" {
		t.Fatalf("filtered page %v %v", page, err)
	}
	for _, mismatched := range []WebhookDeliveryQuery{
		{SubscriptionID: subscriptions[1].ID, Limit: 3, PageToken: page.NextPageToken},
		{SubscriptionID: subscriptions[0].ID, State: WebhookDeliveryDead, Limit: 3, PageToken: page.NextPageToken},
		{Limit: 3, PageToken: page.NextPageToken},
	} {
		if _, err = store.PageWebhookDeliveries(ctx, mismatched); !errors.Is(err, ErrInvalidPageToken) {
			t.Fatalf("a token issued for other filters must be refused, got %v for %+v", err, mismatched)
		}
	}
	continued := filtered
	continued.PageToken = page.NextPageToken
	next, err := store.PageWebhookDeliveries(ctx, continued)
	if err != nil || len(next.Items) != 1 || next.NextPageToken != "" {
		t.Fatalf("continuation %v %v", next, err)
	}
}

// A token that carries an id that is not a uuid is refused as an invalid token in every store, instead of
// reaching the database as a cast failure.
func TestKeysetTokenWithAnIdThatIsNotAUUIDIsInvalid(t *testing.T) {
	token := encodeCreatedAtToken(allLifecycleJobsFilter, time.Now(), "not-a-uuid")
	if _, err := NewMemoryStore().PageAllLifecycleJobs(context.Background(), LifecycleJobListQuery{PageToken: token}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("a token with a non-uuid id answered %v, want ErrInvalidPageToken", err)
	}
	if _, _, _, err := decodeCreatedAtToken(encodeCreatedAtToken("f", time.Now(), uuid.NewString()), "f"); err != nil {
		t.Fatalf("a well-formed token must decode: %v", err)
	}
}
