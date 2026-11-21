package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/ids"
)

// promotionProvenance returns a context under which the next promotion job enqueue writes a provenance row.
func promotionProvenance(ctx context.Context, format Format, coordinate, digest, key string) context.Context {
	return WithArtifactPromotion(ctx, ArtifactPromotion{ID: ids.New(), Format: format, Coordinate: coordinate, Digest: digest,
		OriginRepository: "source", RequestedBy: "alice", IdempotencyKey: "manual:" + key})
}

func memoryPromotionFixture(t *testing.T) (*MemoryStore, HostedRepository, string) {
	t.Helper()
	store := NewMemoryStore()
	target := HostedRepository{ID: uuid.NewString(), Name: "target", Format: FormatRaw}
	store.hostedRepositories[target.ID] = target
	return store, target, uuid.NewString()
}

func promotionJob(target HostedRepository, source, key string) LifecycleJob {
	return LifecycleJob{ID: ids.New(), RepositoryID: target.ID, Kind: LifecycleJobPromotion, IdempotencyKey: key, MaxAttempts: 2,
		Payload: []byte(`{"format":"raw","sourceRepositoryId":"` + source + `","path":"a.bin","digest":"sha256:` + strings.Repeat("a", 64) + `"}`)}
}

func TestMemoryPromotionWritesOneProvenanceRowAndReplayReturnsIt(t *testing.T) {
	ctx := context.Background()
	store, target, source := memoryPromotionFixture(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	job, replayed, err := store.EnqueueLifecycleJob(promotionProvenance(ctx, FormatRaw, "a.bin", digest, "k1"), promotionJob(target, source, "k1"))
	if err != nil || replayed {
		t.Fatalf("enqueue replayed=%t err=%v", replayed, err)
	}
	row, err := store.GetArtifactPromotionByJob(ctx, job.ID)
	if err != nil || row.State != ArtifactPromotionQueued || row.Attempt != 1 || row.TargetRepositoryID != target.ID || row.JobID != job.ID ||
		row.Coordinate != "a.bin" || row.Digest != digest || row.RequestedBy != "alice" || row.IdempotencyKey != "manual:k1" || row.ChainPath == nil || row.Approvals == nil {
		t.Fatalf("row=%+v err=%v", row, err)
	}
	again, replayed, err := store.EnqueueLifecycleJob(promotionProvenance(ctx, FormatRaw, "a.bin", digest, "k1"), promotionJob(target, source, "k1"))
	if err != nil || !replayed || again.ID != job.ID {
		t.Fatalf("replay job=%+v replayed=%t err=%v", again, replayed, err)
	}
	replayRow, err := store.GetArtifactPromotionByJob(ctx, job.ID)
	if err != nil || replayRow.ID != row.ID {
		t.Fatalf("replay row=%+v err=%v", replayRow, err)
	}
	page, err := store.PageArtifactPromotions(ctx, ArtifactPromotionQuery{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	// The same provenance key under another job is a conflict, not a second row.
	other := promotionJob(target, source, "k2")
	if _, _, err = store.EnqueueLifecycleJob(promotionProvenance(ctx, FormatRaw, "a.bin", digest, "k1"), other); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting key err=%v", err)
	}
}

func TestMemoryPromotionRowFollowsTheJobAndEmitsTerminalEvents(t *testing.T) {
	ctx := context.Background()
	store, target, source := memoryPromotionFixture(t)
	if _, err := store.CreateWebhookSubscription(ctx, WebhookSubscription{ID: uuid.NewString(), Name: "all", EndpointURL: "https://events.example.test/p", SecretCiphertext: "x", Enabled: true,
		EventTypes: []WebhookEventType{WebhookEventArtifactPromoted, WebhookEventPromotionFailed}}); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	job, _, err := store.EnqueueLifecycleJob(promotionProvenance(ctx, FormatRaw, "a.bin", digest, "k"), promotionJob(target, source, "k"))
	if err != nil {
		t.Fatal(err)
	}
	state := func() ArtifactPromotionState {
		row, getErr := store.GetArtifactPromotionByJob(ctx, job.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		return row.State
	}
	claimed, err := store.ClaimLifecycleJobsByKindAndFormat(ctx, LifecycleJobPromotion, FormatRaw, 1)
	if err != nil || len(claimed) != 1 || state() != ArtifactPromotionRunning {
		t.Fatalf("claim=%+v err=%v state=%s", claimed, err, state())
	}
	if err = store.FailLifecycleJob(ctx, job.ID, claimed[0].LeaseToken, "boom"); err != nil || state() != ArtifactPromotionQueued || len(store.webhookEvents) != 0 {
		t.Fatalf("retrying err=%v state=%s events=%d", err, state(), len(store.webhookEvents))
	}
	if _, err = store.RunLifecycleJobNow(ctx, target.ID, job.ID); err != nil {
		t.Fatal(err)
	}
	claimed, _ = store.ClaimLifecycleJobsByKindAndFormat(ctx, LifecycleJobPromotion, FormatRaw, 1)
	if len(claimed) != 1 {
		t.Fatalf("second claim=%+v", claimed)
	}
	if err = store.FailLifecycleJob(ctx, job.ID, claimed[0].LeaseToken, "boom"); err != nil || state() != ArtifactPromotionFailed {
		t.Fatalf("failed err=%v state=%s", err, state())
	}
	row, _ := store.GetArtifactPromotionByJob(ctx, job.ID)
	if row.CompletedAt.IsZero() {
		t.Fatal("a failed promotion has no completion time")
	}
	if _, err = store.RetryLifecycleJob(ctx, target.ID, job.ID); err != nil || state() != ArtifactPromotionQueued {
		t.Fatalf("retry err=%v state=%s", err, state())
	}
	claimed, _ = store.ClaimLifecycleJobsByKindAndFormat(ctx, LifecycleJobPromotion, FormatRaw, 1)
	if err = store.CompleteLifecycleJob(ctx, job.ID, claimed[0].LeaseToken); err != nil || state() != ArtifactPromotionCompleted {
		t.Fatalf("complete err=%v state=%s", err, state())
	}
	types := map[WebhookEventType]string{}
	for _, event := range store.webhookEvents {
		types[event.Type] = event.Version
		if event.RepositoryID != target.ID || event.Coordinate != "a.bin" || event.Digest != digest || event.Format != "raw" || event.Version != row.ID {
			t.Fatalf("event %+v", event)
		}
	}
	if len(store.webhookEvents) != 2 || types[WebhookEventPromotionFailed] == "" || types[WebhookEventArtifactPromoted] == "" {
		t.Fatalf("events=%+v", store.webhookEvents)
	}
}

func TestMemoryPromotionClaimRefusesAJobThatTheTightenedTargetPolicyDenies(t *testing.T) {
	ctx := context.Background()
	store, target, source := memoryPromotionFixture(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	job, _, err := store.EnqueueLifecycleJob(promotionProvenance(ctx, FormatRaw, "a.bin", digest, "k"), promotionJob(target, source, "k"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := store.GetRepositorySecurityPolicy(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	version := policy.Version
	policy.Enabled, policy.RequireSBOM = true, true
	if _, err = store.ReplaceRepositorySecurityPolicy(ctx, target.ID, policy, version); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimLifecycleJobsByKindAndFormat(ctx, LifecycleJobPromotion, FormatRaw, 1)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("a refused job was handed to its worker: %+v err=%v", claimed, err)
	}
	stored, err := store.GetLifecycleJob(ctx, target.ID, job.ID)
	if err != nil || stored.State != LifecycleJobRetrying || !strings.HasPrefix(stored.LastError, PromotionPolicyRefusalPrefix) || !strings.Contains(stored.LastError, "sbom_required") {
		t.Fatalf("job=%+v err=%v", stored, err)
	}
	// A job without a provenance row is not re-evaluated here.
	plain := promotionJob(target, source, "plain")
	if _, _, err = store.EnqueueLifecycleJob(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if _, err = store.RunLifecycleJobNow(ctx, target.ID, job.ID); err != nil {
		t.Fatal(err)
	}
	// The refused job holds the repository's one promotion slot for this claim; the next claim serves the other job.
	if claimed, err = store.ClaimLifecycleJobsByKindAndFormat(ctx, LifecycleJobPromotion, FormatRaw, 5); err != nil || len(claimed) != 0 {
		t.Fatalf("refused claim=%+v err=%v", claimed, err)
	}
	claimed, err = store.ClaimLifecycleJobsByKindAndFormat(ctx, LifecycleJobPromotion, FormatRaw, 5)
	if err != nil || len(claimed) != 1 || claimed[0].ID != plain.ID {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
}

func TestMemoryPromotionPageFiltersAndKeysets(t *testing.T) {
	ctx := context.Background()
	store, target, source := memoryPromotionFixture(t)
	other := HostedRepository{ID: uuid.NewString(), Name: "other", Format: FormatRaw}
	store.hostedRepositories[other.ID] = other
	digest := "sha256:" + strings.Repeat("a", 64)
	for i, repo := range []HostedRepository{target, target, other} {
		key := string(rune('a' + i))
		if _, _, err := store.EnqueueLifecycleJob(promotionProvenance(ctx, FormatRaw, "a.bin", digest, key), promotionJob(repo, source, key)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.PageArtifactPromotions(ctx, ArtifactPromotionQuery{Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextPageToken == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := store.PageArtifactPromotions(ctx, ArtifactPromotionQuery{Limit: 2, PageToken: first.NextPageToken})
	if err != nil || len(second.Items) != 1 || second.NextPageToken != "" || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if _, err = store.PageArtifactPromotions(ctx, ArtifactPromotionQuery{Limit: 2, TargetRepositoryID: target.ID, PageToken: first.NextPageToken}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("a token of another filter err=%v", err)
	}
	for query, want := range map[string]int{"target": 2, "other": 1, "none": 0} {
		got := ArtifactPromotionQuery{}
		switch query {
		case "target":
			got.RepositoryIDs = []string{target.ID}
		case "other":
			got.TargetRepositoryID = other.ID
		case "none":
			got.RepositoryIDs = []string{}
		}
		page, pageErr := store.PageArtifactPromotions(ctx, got)
		if pageErr != nil || len(page.Items) != want {
			t.Fatalf("%s: page=%+v err=%v", query, page, pageErr)
		}
	}
	if page, _ := store.PageArtifactPromotions(ctx, ArtifactPromotionQuery{State: ArtifactPromotionCompleted}); len(page.Items) != 0 {
		t.Fatalf("state filter returned %+v", page.Items)
	}
}

// fakeClaimedPromotions is a claimedPromotionPort that records how each claimed job leaves the claim.
type fakeClaimedPromotions struct {
	policyErr, intelligenceErr, failErr, releaseErr error
	allowed                                         bool
	failed, released                                []string
}

func (f *fakeClaimedPromotions) GetRepositorySecurityPolicy(context.Context, string) (RepositorySecurityPolicy, error) {
	policy := DefaultRepositorySecurityPolicy()
	if !f.allowed {
		policy.Enabled, policy.RequireSBOM = true, true
	}
	return policy, f.policyErr
}

func (f *fakeClaimedPromotions) GetArtifactIntelligence(context.Context, string, Format, string, string) (ArtifactIntelligence, error) {
	return ArtifactIntelligence{}, f.intelligenceErr
}

func (f *fakeClaimedPromotions) promotionOfJob(_ context.Context, jobID string) (ArtifactPromotion, error) {
	return ArtifactPromotion{JobID: jobID, Format: FormatRaw}, nil
}

func (f *fakeClaimedPromotions) failClaimed(_ context.Context, job LifecycleJob, _ string) error {
	f.failed = append(f.failed, job.ID)
	return f.failErr
}

func (f *fakeClaimedPromotions) releaseClaimed(_ context.Context, job LifecycleJob) error {
	f.released = append(f.released, job.ID)
	return f.releaseErr
}

func claimedPromotionJobs(ids ...string) []LifecycleJob {
	jobs := make([]LifecycleJob, len(ids))
	for i, id := range ids {
		jobs[i] = LifecycleJob{ID: id, Kind: LifecycleJobPromotion, Payload: []byte(`{"sourceRepositoryId":"s"}`)}
	}
	return jobs
}

func TestClaimedPromotionsTransientEvaluationErrorReleasesWithoutSpendingAnAttempt(t *testing.T) {
	for name, fake := range map[string]*fakeClaimedPromotions{
		"policy read":       {policyErr: errors.New("connection reset")},
		"intelligence read": {allowed: true, intelligenceErr: errors.New("connection reset")},
	} {
		kept, err := refuseClaimedPromotions(context.Background(), fake, claimedPromotionJobs("a", "b"))
		if err != nil || len(kept) != 0 || len(fake.failed) != 0 || len(fake.released) != 2 {
			t.Fatalf("%s: kept=%v failed=%v released=%v err=%v", name, kept, fake.failed, fake.released, err)
		}
	}
}

func TestClaimedPromotionsVanishedTargetAndRefusalFailTheJob(t *testing.T) {
	for name, fake := range map[string]*fakeClaimedPromotions{
		"target gone": {policyErr: ErrNotFound},
		"refused":     {},
	} {
		kept, err := refuseClaimedPromotions(context.Background(), fake, claimedPromotionJobs("a"))
		if err != nil || len(kept) != 0 || len(fake.failed) != 1 || len(fake.released) != 0 {
			t.Fatalf("%s: kept=%v failed=%v released=%v err=%v", name, kept, fake.failed, fake.released, err)
		}
	}
	allowed := &fakeClaimedPromotions{allowed: true}
	if kept, err := refuseClaimedPromotions(context.Background(), allowed, claimedPromotionJobs("a")); err != nil || len(kept) != 1 {
		t.Fatalf("allowed kept=%v err=%v", kept, err)
	}
}

func TestClaimedPromotionsFailureToApplyAnOutcomeIsReturnedAndReleasesTheRest(t *testing.T) {
	failErr := errors.New("write failed")
	fake := &fakeClaimedPromotions{failErr: failErr}
	kept, err := refuseClaimedPromotions(context.Background(), fake, claimedPromotionJobs("a", "b", "c"))
	if !errors.Is(err, failErr) || kept != nil || len(fake.failed) != 1 || len(fake.released) != 2 {
		t.Fatalf("kept=%v failed=%v released=%v err=%v", kept, fake.failed, fake.released, err)
	}
	releaseErr := errors.New("release failed")
	fake = &fakeClaimedPromotions{policyErr: errors.New("blip"), releaseErr: releaseErr}
	if _, err = refuseClaimedPromotions(context.Background(), fake, claimedPromotionJobs("a")); !errors.Is(err, releaseErr) {
		t.Fatalf("a release failure must surface: %v", err)
	}
}

func TestMemoryReleaseLifecycleJobLeaseReturnsTheAttemptAndTheQueuedRow(t *testing.T) {
	ctx := context.Background()
	store, target, source := memoryPromotionFixture(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	job, _, err := store.EnqueueLifecycleJob(promotionProvenance(ctx, FormatRaw, "a.bin", digest, "k"), promotionJob(target, source, "k"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimLifecycleJobsByKindAndFormat(ctx, LifecycleJobPromotion, FormatRaw, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	store.mu.Lock()
	err = store.releaseLifecycleJobLeaseLocked(job.ID, claimed[0].LeaseToken)
	store.mu.Unlock()
	stored, getErr := store.GetLifecycleJob(ctx, target.ID, job.ID)
	row, rowErr := store.GetArtifactPromotionByJob(ctx, job.ID)
	if err != nil || getErr != nil || rowErr != nil || stored.State != LifecycleJobPending || stored.Attempts != 0 || stored.LeaseToken != "" || row.State != ArtifactPromotionQueued {
		t.Fatalf("job=%+v row=%+v err=%v %v %v", stored, row, err, getErr, rowErr)
	}
	store.mu.Lock()
	err = store.releaseLifecycleJobLeaseLocked(job.ID, "stale")
	store.mu.Unlock()
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale lease err=%v", err)
	}
}
