package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/events"
	"github.com/mlops-ca/notarx/internal/ids"
)

func memoryWebhookSubscription(t *testing.T, store *MemoryStore, enabled bool, types ...WebhookEventType) WebhookSubscription {
	t.Helper()
	subscription, err := store.CreateWebhookSubscription(context.Background(), WebhookSubscription{ID: ids.New(), Name: "hook-" + ids.New(), EndpointURL: "https://hooks.example.test/x",
		SecretCiphertext: "sealed", EventTypes: types, Enabled: enabled})
	if err != nil {
		t.Fatal(err)
	}
	return subscription
}

func TestEnqueueWebhookEventWritesNothingWithoutAConsumer(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	if _, err := store.EnqueueWebhookEvent(ctx, WebhookEvent{Type: WebhookEventArtifactReleased}); err != nil {
		t.Fatal(err)
	}
	if len(store.webhookEvents) != 0 {
		t.Fatalf("an event without a consumer was written: %v", store.webhookEvents)
	}
	// A disabled subscription and a subscription of another type are not consumers.
	memoryWebhookSubscription(t, store, false, WebhookEventArtifactReleased)
	memoryWebhookSubscription(t, store, true, WebhookEventArtifactQuarantined)
	if _, err := store.EnqueueWebhookEvent(ctx, WebhookEvent{Type: WebhookEventArtifactReleased}); err != nil {
		t.Fatal(err)
	}
	if len(store.webhookEvents) != 0 {
		t.Fatalf("an event nobody asked for was written: %v", store.webhookEvents)
	}
	// An internal consumer (artifact.published) is a consumer on its own.
	if _, err := store.EnqueueWebhookEvent(ctx, WebhookEvent{Type: WebhookEventArtifactPublished}); err != nil {
		t.Fatal(err)
	}
	if len(store.webhookEvents) != 1 || len(store.webhookDeliveries) != 0 {
		t.Fatalf("events=%d deliveries=%d", len(store.webhookEvents), len(store.webhookDeliveries))
	}
	// A matching subscription gets the event and one delivery.
	memoryWebhookSubscription(t, store, true, WebhookEventArtifactReleased)
	if _, err := store.EnqueueWebhookEvent(ctx, WebhookEvent{Type: WebhookEventArtifactReleased}); err != nil {
		t.Fatal(err)
	}
	if len(store.webhookEvents) != 2 || len(store.webhookDeliveries) != 1 {
		t.Fatalf("events=%d deliveries=%d", len(store.webhookEvents), len(store.webhookDeliveries))
	}
	if _, err := store.EnqueueWebhookEvent(ctx, WebhookEvent{Type: "cargo.promotion.completed"}); err == nil {
		t.Fatal("a removed Cargo event name was accepted")
	}
}

func TestMemoryDomainEventsReadCursor(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	types := []events.Type{events.ArtifactPublished}
	// A consumer without a cursor starts at the present: the history is not replayed.
	if _, err := store.EnqueueWebhookEvent(ctx, WebhookEvent{ID: ids.New(), Type: WebhookEventArtifactPublished, OccurredAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ReadEvents(ctx, "c", types, 0, 10); err != nil || len(got) != 0 {
		t.Fatalf("history replayed: %v %v", got, err)
	}
	base := time.Now().Add(time.Minute).UTC()
	for i, id := range []string{"a", "b", "c"} {
		if _, err := store.EnqueueWebhookEvent(ctx, WebhookEvent{ID: id, Type: WebhookEventArtifactPublished, RepositoryID: "r", Coordinate: id, OccurredAt: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.EnqueueWebhookEvent(ctx, WebhookEvent{ID: "other", Type: WebhookEventArtifactReleased, OccurredAt: base}); err != nil {
		t.Fatal(err)
	}
	// Young events wait out the settle time.
	if got, _ := store.ReadEvents(ctx, "c", types, time.Hour, 10); len(got) != 0 {
		t.Fatalf("unsettled events read: %v", got)
	}
	got, err := store.ReadEvents(ctx, "c", types, -2*time.Minute, 2)
	if err != nil || len(got) != 2 || got[0].Event.ID != "a" || got[1].Event.ID != "b" || got[0].Event.Coordinate != "a" {
		t.Fatalf("batch = %+v %v", got, err)
	}
	if err = store.AdvanceCursor(ctx, "c", got[1].Position); err != nil {
		t.Fatal(err)
	}
	// The cursor never moves back.
	if err = store.AdvanceCursor(ctx, "c", got[0].Position); err != nil {
		t.Fatal(err)
	}
	got, err = store.ReadEvents(ctx, "c", types, -2*time.Minute, 10)
	if err != nil || len(got) != 1 || got[0].Event.ID != "c" {
		t.Fatalf("after cursor = %+v %v", got, err)
	}
}

func TestMemorySwapScanResultEmitsScanCompleted(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	subscription := memoryWebhookSubscription(t, store, true, WebhookEventArtifactScanCompleted)
	repo, digest := ids.New(), "sha256:"+strings.Repeat("a", 64)
	swap := func(counts [2]int, vulns ...string) ScanResult {
		t.Helper()
		result := ScanResult{ID: ids.New(), RepositoryID: repo, ArtifactDigest: digest, Scanner: "trivy", Status: ScanStatusVulnerable, Critical: counts[0], High: counts[1], ScannedAt: time.Now().UTC()}
		var findings []ScanFinding
		for i, v := range vulns {
			findings = append(findings, ScanFinding{VulnerabilityID: result.ID, Ordinal: i, VulnID: v, Source: "trivy", Severity: "high", Component: "c", Version: "1", Status: "affected"})
		}
		if len(findings) > 0 {
			if err := store.InsertScanFindings(ctx, findings); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.SwapScanResult(ctx, result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := swap([2]int{0, 1}, "CVE-1")
	second := swap([2]int{0, 1}, "CVE-1", "CVE-2")
	if len(store.webhookDeliveries) != 2 {
		t.Fatalf("deliveries = %d, want one per scan", len(store.webhookDeliveries))
	}
	claims, err := store.ClaimWebhookDeliveries(ctx, "w", time.Now().Add(time.Hour), time.Minute, 10)
	if err != nil || len(claims) != 2 {
		t.Fatalf("claims = %d %v", len(claims), err)
	}
	byVersion := map[string]WebhookEvent{}
	for _, claim := range claims {
		if claim.Subscription.ID != subscription.ID || claim.Event.Type != WebhookEventArtifactScanCompleted {
			t.Fatalf("claim = %+v", claim)
		}
		byVersion[claim.Event.Version] = claim.Event
	}
	if byVersion[first.ID].Digest != digest || byVersion[second.ID].RepositoryID != repo {
		t.Fatalf("events = %+v", byVersion)
	}
	var payload struct {
		Previous    *scanSummaryPayload `json:"previous"`
		Current     scanSummaryPayload  `json:"current"`
		NewFindings int                 `json:"newFindings"`
	}
	if err = json.Unmarshal(byVersion[first.ID].Data, &payload); err != nil || payload.Previous != nil || payload.Current.ID != first.ID || payload.NewFindings != 1 {
		t.Fatalf("first payload = %s (%v)", byVersion[first.ID].Data, err)
	}
	payload.Previous = nil
	if err = json.Unmarshal(byVersion[second.ID].Data, &payload); err != nil || payload.Previous == nil || payload.Previous.ID != first.ID || payload.Current.ID != second.ID || payload.NewFindings != 1 {
		t.Fatalf("second payload = %s (%v)", byVersion[second.ID].Data, err)
	}
	if byVersion[first.ID].domainEvent().DedupeKey() == byVersion[second.ID].domainEvent().DedupeKey() {
		t.Error("a re-scan shares the dedupe key of the first scan")
	}
}

func TestQuarantineAndCargoEventsCarryTheArtifactIdentity(t *testing.T) {
	quarantine := ArtifactQuarantine{RepositoryID: "r", Format: FormatRaw, Coordinate: "a", Digest: "sha256:aa", State: ArtifactQuarantineStateReleased, Version: "4", UpdatedAt: time.Now()}
	event := artifactQuarantineWebhookEvent(quarantine)
	if event.Type != WebhookEventArtifactReleased || event.RepositoryID != "r" || event.Format != "raw" || event.Version != "4" || event.Coordinate != "a" {
		t.Fatalf("quarantine event = %+v", event)
	}
	plan, ok := cargoReplicationWebhookEvent(ReplicationPlan{ID: "p", Format: FormatCargo, State: "completed", TargetRepositoryID: "t", Coordinate: "demo@1.0.0", Digest: "sha256:bb", Attempts: 2})
	if !ok || plan.Type != WebhookEventReplicationCompleted || plan.Version != "p:2" || plan.RepositoryID != "t" {
		t.Fatalf("replication event = %+v", plan)
	}
}

func TestMemorySwapScanResultTwiceWithTheSameResultEmitsOnce(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	memoryWebhookSubscription(t, store, true, WebhookEventArtifactScanCompleted)
	result := ScanResult{ID: ids.New(), RepositoryID: ids.New(), ArtifactDigest: "sha256:" + strings.Repeat("b", 64), Scanner: "trivy", Status: ScanStatusClean, ScannedAt: time.Now().UTC()}
	for range 2 {
		if _, err := store.SwapScanResult(ctx, result); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.webhookEvents) != 1 {
		t.Fatalf("a repeated swap of the stored result emitted %d events", len(store.webhookEvents))
	}
}
