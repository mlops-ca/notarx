package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/events"
	"github.com/mlops-ca/notarx/internal/objectstore"
	"github.com/mlops-ca/notarx/internal/promotionrules"
	mavenprotocol "github.com/mlops-ca/notarx/internal/protocol/maven"
	ociprotocol "github.com/mlops-ca/notarx/internal/protocol/oci"
	rawprotocol "github.com/mlops-ca/notarx/internal/protocol/raw"
	"github.com/mlops-ca/notarx/internal/repository"
)

// evaluatorFixture is a Raw dev -> staging pair with a rule between them, run as an active service account that may
// read dev and write staging.
type evaluatorFixture struct {
	t         *testing.T
	store     *repository.MemoryStore
	objects   *objectstore.MemoryStore
	evaluator *PromotionEvaluator
	account   repository.ServiceAccount
	source    repository.HostedRepository
	target    repository.HostedRepository
	rule      repository.PromotionRule
	now       time.Time
}

func ruleMatch(t *testing.T, match promotionrules.Match) json.RawMessage {
	t.Helper()
	raw, err := match.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustCreateRepository(t *testing.T, store *repository.MemoryStore, name string, format repository.Format) repository.HostedRepository {
	t.Helper()
	repo, err := store.CreateHostedRepository(context.Background(), repository.HostedRepository{ID: uuid.NewString(), Name: name, Format: format})
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func grantServiceAccount(t *testing.T, store *repository.MemoryStore, repo repository.HostedRepository, accountID string, scopes ...string) {
	t.Helper()
	current, err := store.GetRepositoryGrants(context.Background(), repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	grants := append(append([]repository.RepositoryGrant(nil), current.Grants...), repository.RepositoryGrant{Principal: "service-account:" + accountID, Scopes: scopes})
	if _, err = store.ReplaceRepositoryGrants(context.Background(), repo.ID, grants, current.Version); err != nil {
		t.Fatal(err)
	}
}

// newEvaluatorFixture builds the pair with the given formats and rule match and conditions (nil means none).
func newEvaluatorFixture(t *testing.T, format repository.Format, match promotionrules.Match, conditions json.RawMessage) *evaluatorFixture {
	t.Helper()
	ctx := context.Background()
	store := repository.NewMemoryStore()
	store.SetPromotionRuleMatcher(PromotionRuleMatcher())
	f := &evaluatorFixture{t: t, store: store, objects: objectstore.NewMemoryStore(), now: time.Now().UTC()}
	f.source, f.target = mustCreateRepository(t, store, "dev", format), mustCreateRepository(t, store, "staging", format)
	account, err := store.CreateServiceAccount(ctx, repository.ServiceAccount{ID: uuid.NewString(), Name: "promoter", State: repository.ServiceAccountActive})
	if err != nil {
		t.Fatal(err)
	}
	f.account = account
	grantServiceAccount(t, store, f.source, account.ID, "repositories:read")
	grantServiceAccount(t, store, f.target, account.ID, "repositories:write")
	f.rule, err = store.CreatePromotionRule(ctx, repository.PromotionRule{ID: uuid.NewString(), Name: "dev-to-staging", SourceRepositoryID: f.source.ID, TargetKind: repository.PromotionTargetLocal,
		TargetRepositoryID: f.target.ID, Match: ruleMatch(t, match), Conditions: conditions, RunAsServiceAccountID: account.ID, Enabled: true, CreatedBy: "alice"}, 32)
	if err != nil {
		t.Fatal(err)
	}
	f.evaluator = NewPromotionEvaluator(PromotionEvaluatorOptions{Store: store, Authorizer: RepositoryAuthorizer{Grants: store, Legacy: testAuthenticator()}, Owner: "test-worker", Now: func() time.Time { return f.now }})
	return f
}

func globMatch(glob string) promotionrules.Match {
	return promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: glob}}
}

// publishRaw stores one Raw asset in repo, announcing it with artifact.published like a publish does.
func (f *evaluatorFixture) publishRaw(repo repository.HostedRepository, path, content string) repository.RawAsset {
	f.t.Helper()
	sum := sha256.Sum256([]byte(content))
	key := fmt.Sprintf("native/raw/sha256/%x", sum[:])
	if err := f.objects.Put(context.Background(), key, []byte(content)); err != nil {
		f.t.Fatal(err)
	}
	asset, err := f.store.PutRawAsset(context.Background(), repository.RawAsset{RepositoryID: repo.ID, Path: path, Digest: fmt.Sprintf("sha256:%x", sum[:]), ObjectKey: key, Size: int64(len(content)), ContentType: "text/plain"})
	if err != nil {
		f.t.Fatal(err)
	}
	return asset
}

func (f *evaluatorFixture) evaluations(states ...repository.PromotionEvaluationState) []repository.PromotionEvaluation {
	f.t.Helper()
	items, err := f.store.ListPromotionEvaluations(context.Background(), repository.PromotionEvaluationQuery{RuleID: f.rule.ID, Limit: 500})
	if err != nil {
		f.t.Fatal(err)
	}
	if len(states) == 0 {
		return items
	}
	var out []repository.PromotionEvaluation
	for _, item := range items {
		for _, state := range states {
			if item.State == state {
				out = append(out, item)
			}
		}
	}
	return out
}

func (f *evaluatorFixture) runOnce() int {
	f.t.Helper()
	handled, err := f.evaluator.RunOnce(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return handled
}

func (f *evaluatorFixture) promotionRows(targetID string) []repository.ArtifactPromotion {
	f.t.Helper()
	page, err := f.store.PageArtifactPromotions(context.Background(), repository.ArtifactPromotionQuery{TargetRepositoryID: targetID, Limit: 100})
	if err != nil {
		f.t.Fatal(err)
	}
	return page.Items
}

// runRawPromotionJobs executes the queued Raw promotion jobs, so the artifact reaches the target.
func (f *evaluatorFixture) runRawPromotionJobs() error {
	f.t.Helper()
	return rawprotocol.NativePromotion{Store: f.store, Intelligence: f.store}.RunJobs(context.Background(), 10)
}

func TestPublishingFansOutToTheMatchingRuleAndTheEvaluatorQueuesOnePromotion(t *testing.T) {
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("releases/**"), nil)
	f.publishRaw(f.source, "docs/readme.txt", "not matched")
	if got := f.evaluations(); len(got) != 0 {
		t.Fatalf("a coordinate outside the match fanned out: %+v", got)
	}
	asset := f.publishRaw(f.source, "releases/widget.txt", "widget")
	pending := f.evaluations(repository.PromotionEvaluationPending)
	if len(pending) != 1 || pending[0].Coordinate != asset.Path || pending[0].Digest != asset.Digest || pending[0].RepositoryID != f.source.ID || pending[0].EventSeq == 0 {
		t.Fatalf("pending = %+v", pending)
	}
	if handled := f.runOnce(); handled != 1 {
		t.Fatalf("handled = %d", handled)
	}
	queued := f.evaluations(repository.PromotionEvaluationQueued)
	if len(queued) != 1 || queued[0].PromotionID == "" || queued[0].LeaseToken != "" {
		t.Fatalf("queued = %+v", queued)
	}
	rows := f.promotionRows(f.target.ID)
	if len(rows) != 1 || rows[0].ID != queued[0].PromotionID || rows[0].RuleID != f.rule.ID || rows[0].RuleVersion != 1 || rows[0].Attempt != 1 ||
		rows[0].RequestedBy != "service-account:"+f.account.ID || !strings.HasPrefix(rows[0].IdempotencyKey, "rule:"+f.rule.ID+":") || rows[0].ChainPath[0].RuleID != f.rule.ID {
		t.Fatalf("provenance = %+v", rows)
	}
	if handled := f.runOnce(); handled != 0 {
		t.Fatalf("a finished evaluation was handled again: %d", handled)
	}
}

func TestEvaluatorSkipsWithAReasonWhenTheRuleOrTheArtifactNoLongerMatches(t *testing.T) {
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), nil)
	f.publishRaw(f.source, "a.txt", "a")
	// The rule is disabled between the fan-out and the evaluation.
	disabled := f.rule
	disabled.Enabled = false
	if _, err := f.store.UpdatePromotionRule(context.Background(), disabled, f.rule.Version); err != nil {
		t.Fatal(err)
	}
	f.runOnce()
	if got := f.evaluations(repository.PromotionEvaluationSkipped); len(got) != 1 || got[0].LastReason != reasonRuleDisabled {
		t.Fatalf("disabled rule: %+v", f.evaluations())
	}
	if rows := f.promotionRows(f.target.ID); len(rows) != 0 {
		t.Fatalf("a disabled rule promoted: %+v", rows)
	}
	// A disabled rule fans out nothing.
	f.publishRaw(f.source, "b.txt", "b")
	if got := f.evaluations(); len(got) != 1 {
		t.Fatalf("a disabled rule fanned out: %+v", got)
	}
}

func TestEvaluatorDeletedRuleLeavesNoEvaluations(t *testing.T) {
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), nil)
	f.publishRaw(f.source, "a.txt", "a")
	if err := f.store.DeletePromotionRule(context.Background(), f.rule.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.evaluations(); len(got) != 0 {
		t.Fatalf("deleting a rule must delete its evaluations: %+v", got)
	}
	if handled := f.runOnce(); handled != 0 {
		t.Fatalf("handled = %d", handled)
	}
}

func TestWorkerCrashAndDuplicateDeliveryYieldOnePromotionRowAndOneJob(t *testing.T) {
	ctx := context.Background()
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), nil)
	asset := f.publishRaw(f.source, "releases/widget.txt", "widget")
	// A worker claims the evaluation, requests the promotion and dies before it records the outcome: its lease expires.
	claimed, err := f.store.ClaimPromotionEvaluations(ctx, "crashed-worker", 0, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed = %+v, %v", claimed, err)
	}
	if outcome, evalErr := f.evaluator.evaluate(ctx, claimed[0]); evalErr != nil || outcome.State != repository.PromotionEvaluationQueued {
		t.Fatalf("first delivery: %+v, %v", outcome, evalErr)
	}
	// Another worker takes the row up again and evaluates it a second time, and the event is delivered twice.
	if handled := f.runOnce(); handled != 1 {
		t.Fatalf("the abandoned evaluation was not taken again: %d", handled)
	}
	if outcome, evalErr := f.evaluator.evaluate(ctx, claimed[0]); evalErr != nil || outcome.State != repository.PromotionEvaluationQueued {
		t.Fatalf("duplicate delivery: %+v, %v", outcome, evalErr)
	}
	rows := f.promotionRows(f.target.ID)
	if len(rows) != 1 || rows[0].Coordinate != asset.Path {
		t.Fatalf("provenance rows = %+v", rows)
	}
	jobs, err := f.store.ListLifecycleJobs(ctx, f.target.ID, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}
	// The crashed worker's late outcome is refused: the lease is no longer its.
	if err = f.store.FinishPromotionEvaluation(ctx, claimed[0].ID, claimed[0].LeaseToken, repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationFailed}); err == nil {
		t.Fatal("an expired lease recorded an outcome")
	}
}

func TestRetryOfAFailedPromotionWritesALinkedAttemptRow(t *testing.T) {
	ctx := context.Background()
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), nil)
	asset := f.publishRaw(f.source, "releases/widget.txt", "widget")
	f.runOnce()
	first := f.promotionRows(f.target.ID)
	if len(first) != 1 {
		t.Fatalf("rows = %+v", first)
	}
	// The job fails for good: every claim fails, and the job is made due again until it runs out of attempts.
	var failedRow repository.ArtifactPromotion
	for attempt := 0; attempt < 20 && failedRow.State != repository.ArtifactPromotionFailed; attempt++ {
		claimed, err := f.store.ClaimLifecycleJobsByKind(ctx, repository.LifecycleJobPromotion, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, job := range claimed {
			if err = f.store.FailLifecycleJob(ctx, job.ID, job.LeaseToken, "target unavailable"); err != nil {
				t.Fatal(err)
			}
		}
		if failedRow, err = f.store.GetArtifactPromotionByJob(ctx, first[0].JobID); err != nil {
			t.Fatal(err)
		}
		if failedRow.State != repository.ArtifactPromotionFailed {
			if _, err = f.store.RunLifecycleJobNow(ctx, f.target.ID, first[0].JobID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if failedRow.State != repository.ArtifactPromotionFailed {
		t.Fatalf("the job never failed for good: %+v", failedRow)
	}
	// The same rule asks again: attempt 2 under the same key.
	service := f.evaluator.service
	key := first[0].IdempotencyKey
	result, perr := service.Request(ctx, PromotionRequest{Principal: serviceAccountPrincipal(f.account.ID), Source: f.source, TargetID: f.target.ID, Coordinate: asset.Path, Digest: asset.Digest,
		IdempotencyKey: key, Rule: &PromotionRuleOrigin{RuleID: f.rule.ID, RuleVersion: 1, Key: key}})
	if perr != nil {
		t.Fatal(perr)
	}
	if result.Promotion.Attempt != 2 || result.Promotion.IdempotencyKey != key || result.Promotion.State != repository.ArtifactPromotionQueued || result.Promotion.ID == first[0].ID || result.Job.ID == first[0].JobID {
		t.Fatalf("retry = %+v job=%+v", result.Promotion, result.Job)
	}
	// The same request again is a replay of attempt 2, not attempt 3.
	again, perr := service.Request(ctx, PromotionRequest{Principal: serviceAccountPrincipal(f.account.ID), Source: f.source, TargetID: f.target.ID, Coordinate: asset.Path, Digest: asset.Digest,
		IdempotencyKey: key, Rule: &PromotionRuleOrigin{RuleID: f.rule.ID, RuleVersion: 1, Key: key}})
	if perr != nil || again.Promotion.ID != result.Promotion.ID || again.Promotion.Attempt != 2 {
		t.Fatalf("replay = %+v, %v", again.Promotion, perr)
	}
	if got := len(f.promotionRows(f.target.ID)); got != 2 {
		t.Fatalf("rows = %d, want the failed attempt and its retry", got)
	}
}

func TestRepromotionAfterTheTargetWasTombstonedMintsANewKey(t *testing.T) {
	ctx := context.Background()
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), nil)
	asset := f.publishRaw(f.source, "releases/widget.txt", "widget")
	f.runOnce()
	if err := f.runRawPromotionJobs(); err != nil {
		t.Fatal(err)
	}
	first := f.promotionRows(f.target.ID)
	if len(first) != 1 || first[0].State != repository.ArtifactPromotionCompleted {
		t.Fatalf("rows = %+v", first)
	}
	// While the target holds the artifact a second evaluation of the same artifact is a no-op.
	again := repository.PromotionEvaluation{ID: uuid.NewString(), RuleID: f.rule.ID, RepositoryID: f.source.ID, Coordinate: asset.Path, Digest: asset.Digest, EventOccurredAt: f.now}
	if outcome, err := f.evaluator.evaluate(ctx, again); err != nil || outcome.State != repository.PromotionEvaluationSkipped || outcome.Reason != reasonAlreadyAtTarget {
		t.Fatalf("a target that holds the artifact: %+v, %v", outcome, err)
	}
	// The target artifact is tombstoned; the artifact is promoted again under a new key.
	if err := f.store.DeleteRawAsset(ctx, f.target.ID, asset.Path); err != nil {
		t.Fatal(err)
	}
	generation, holds, conflict, err := f.evaluator.targetState(ctx, f.target, repository.FormatRaw, asset.Path, asset.Digest)
	if err != nil || holds || conflict != "" || generation == "" {
		t.Fatalf("after tombstone: generation=%q holds=%v conflict=%q err=%v", generation, holds, conflict, err)
	}
	old := RulePromotionKey(f.rule.ID, f.target.ID, asset.Path, asset.Digest, "")
	renewed := RulePromotionKey(f.rule.ID, f.target.ID, asset.Path, asset.Digest, generation)
	if old != first[0].IdempotencyKey || renewed == old {
		t.Fatalf("keys: first=%q old=%q renewed=%q", first[0].IdempotencyKey, old, renewed)
	}
}

func TestRevokedServiceAccountParksTheRuleAndAnnouncesPromotionFailed(t *testing.T) {
	ctx := context.Background()
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), nil)
	// promotion.failed is written only when a subscription asks for it, and a consumer cursor starts at the present.
	if _, err := f.store.CreateWebhookSubscription(ctx, repository.WebhookSubscription{ID: uuid.NewString(), Name: "failures", EndpointURL: "https://events.example.test/p", SecretCiphertext: "x", Enabled: true,
		EventTypes: []repository.WebhookEventType{events.PromotionFailed}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReadEvents(ctx, "test", []events.Type{events.PromotionFailed}, 0, 10); err != nil {
		t.Fatal(err)
	}
	f.publishRaw(f.source, "releases/widget.txt", "widget")
	disabled := repository.ServiceAccountDisabled
	if _, err := f.store.UpdateServiceAccount(ctx, repository.ServiceAccountUpdate{ID: f.account.ID, State: &disabled}, f.account.Version); err != nil {
		t.Fatal(err)
	}
	f.runOnce()
	waitingRows := f.evaluations(repository.PromotionEvaluationWaiting)
	if len(waitingRows) != 1 || waitingRows[0].LastReason != reasonAuthorizationLost+":service_account_disabled" {
		t.Fatalf("the evaluation that found the authority lost must wait for the rule: %+v", f.evaluations())
	}
	rule, err := f.store.GetPromotionRule(ctx, f.rule.ID)
	if err != nil || rule.State != repository.PromotionRuleAuthorizationLost || rule.StateReason != "service_account_disabled" || rule.Version != f.rule.Version {
		t.Fatalf("rule = %+v, %v", rule, err)
	}
	if rows := f.promotionRows(f.target.ID); len(rows) != 0 {
		t.Fatalf("a rule without authority promoted: %+v", rows)
	}
	stored, err := f.store.ReadEvents(ctx, "test", []events.Type{events.PromotionFailed}, 0, 10)
	if err != nil || len(stored) != 1 || stored[0].Event.RepositoryID != f.target.ID {
		t.Fatalf("promotion.failed events = %+v, %v", stored, err)
	}
	var payload map[string]string
	if err = json.Unmarshal(stored[0].Event.Payload, &payload); err != nil || payload["ruleId"] != f.rule.ID || payload["state"] != repository.PromotionRuleAuthorizationLost || payload["reason"] != "service_account_disabled" {
		t.Fatalf("payload = %s, %v", stored[0].Event.Payload, err)
	}
	// A parked rule fans out nothing.
	f.publishRaw(f.source, "releases/next.txt", "next")
	if got := f.evaluations(repository.PromotionEvaluationPending); len(got) != 0 {
		t.Fatalf("a parked rule fanned out: %+v", got)
	}
	// Editing the rule once the account is active again restores it.
	active := repository.ServiceAccountActive
	account, err := f.store.GetServiceAccount(ctx, f.account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.UpdateServiceAccount(ctx, repository.ServiceAccountUpdate{ID: f.account.ID, State: &active}, account.Version); err != nil {
		t.Fatal(err)
	}
	if rule, err = f.store.UpdatePromotionRule(ctx, rule, rule.Version); err != nil || rule.State != repository.PromotionRuleActive || rule.StateReason != "" {
		t.Fatalf("restored rule = %+v, %v", rule, err)
	}
}

func TestEvaluationReauthorizesSourceReadAndTargetWriteEveryTime(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		revoke func(f *evaluatorFixture)
		reason string
	}{
		{"source read", func(f *evaluatorFixture) {
			current, _ := f.store.GetRepositoryGrants(ctx, f.source.ID)
			if _, err := f.store.ReplaceRepositoryGrants(ctx, f.source.ID, nil, current.Version); err != nil {
				f.t.Fatal(err)
			}
		}, "source_read_denied"},
		{"target write", func(f *evaluatorFixture) {
			current, _ := f.store.GetRepositoryGrants(ctx, f.target.ID)
			if _, err := f.store.ReplaceRepositoryGrants(ctx, f.target.ID, nil, current.Version); err != nil {
				f.t.Fatal(err)
			}
		}, "target_write_denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), nil)
			f.publishRaw(f.source, "releases/widget.txt", "widget")
			tc.revoke(f)
			f.runOnce()
			if got := f.evaluations(repository.PromotionEvaluationWaiting); len(got) != 1 || got[0].LastReason != reasonAuthorizationLost+":"+tc.reason {
				t.Fatalf("evaluations = %+v", f.evaluations())
			}
			if rule, err := f.store.GetPromotionRule(ctx, f.rule.ID); err != nil || rule.State != repository.PromotionRuleAuthorizationLost {
				t.Fatalf("rule = %+v, %v", rule, err)
			}
		})
	}
}

func TestFailedConditionWaitsAndPromotesOnceItHolds(t *testing.T) {
	ctx := context.Background()
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), json.RawMessage(`{"scan":{"completed":true}}`))
	asset := f.publishRaw(f.source, "releases/widget.txt", "widget")
	f.runOnce()
	waiting := f.evaluations(repository.PromotionEvaluationWaiting)
	if len(waiting) != 1 || waiting[0].LastReason != reasonConditionPrefix+"scan:scan_not_completed" || waiting[0].Attempts != 0 {
		t.Fatalf("a rule that needs a scan must wait for it: %+v", f.evaluations())
	}
	if waiting[0].NextAttemptAt.Before(f.now.Add(conditionPollFloor - time.Second)) {
		t.Fatalf("next attempt = %v, want at least a minute after %v", waiting[0].NextAttemptAt, f.now)
	}
	if rows := f.promotionRows(f.target.ID); len(rows) != 0 {
		t.Fatalf("rows = %+v", rows)
	}
	// The scan completes clean; the next look promotes.
	if _, err := f.store.ReplaceArtifactIntelligence(ctx, repository.ArtifactIntelligence{RepositoryID: f.source.ID, Format: repository.FormatRaw, Coordinate: asset.Path, Digest: asset.Digest,
		Vulnerability: &repository.ArtifactVulnerabilitySummary{Scanner: "test", Status: "clean", ScannedAt: f.now}}, ""); err != nil {
		t.Fatal(err)
	}
	if outcome, err := f.evaluator.evaluate(ctx, waiting[0]); err != nil || outcome.State != repository.PromotionEvaluationQueued {
		t.Fatalf("after the scan: %+v, %v", outcome, err)
	}
}

func TestFindingsAboveTheLimitWaitForARescanAndAWeekOldWaitGivesUp(t *testing.T) {
	ctx := context.Background()
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), json.RawMessage(`{"scan":{"completed":true,"maxSeverity":"medium"}}`))
	asset := f.publishRaw(f.source, "releases/widget.txt", "widget")
	if _, err := f.store.ReplaceArtifactIntelligence(ctx, repository.ArtifactIntelligence{RepositoryID: f.source.ID, Format: repository.FormatRaw, Coordinate: asset.Path, Digest: asset.Digest,
		Vulnerability: &repository.ArtifactVulnerabilitySummary{Scanner: "test", Status: "vulnerable", High: 1, ScannedAt: f.now}}, ""); err != nil {
		t.Fatal(err)
	}
	f.runOnce()
	waiting := f.evaluations(repository.PromotionEvaluationWaiting)
	if len(waiting) != 1 || waiting[0].LastReason != reasonConditionPrefix+"scan:severity_above_medium" {
		t.Fatalf("findings above the limit: %+v", f.evaluations())
	}
	// A rescan after the finding was fixed lets the same evaluation through.
	if _, err := f.store.ReplaceArtifactIntelligence(ctx, repository.ArtifactIntelligence{RepositoryID: f.source.ID, Format: repository.FormatRaw, Coordinate: asset.Path, Digest: asset.Digest,
		Vulnerability: &repository.ArtifactVulnerabilitySummary{Scanner: "test", Status: "clean", ScannedAt: f.now}}, "1"); err != nil {
		t.Fatal(err)
	}
	if outcome, err := f.evaluator.evaluate(ctx, waiting[0]); err != nil || outcome.State != repository.PromotionEvaluationQueued {
		t.Fatalf("after the rescan: %+v, %v", outcome, err)
	}
	// A condition that never holds stops being rechecked a week after publication.
	unscanned := repository.PromotionEvaluation{ID: uuid.NewString(), RuleID: f.rule.ID, RepositoryID: f.source.ID, Coordinate: "releases/old.txt", Digest: "sha256:" + strings.Repeat("0", 64), EventOccurredAt: f.now.Add(-8 * 24 * time.Hour)}
	old := f.publishRaw(f.source, unscanned.Coordinate, "old")
	unscanned.Digest = old.Digest
	if outcome, err := f.evaluator.evaluate(ctx, unscanned); err != nil || outcome.State != repository.PromotionEvaluationSkipped || outcome.Reason != reasonConditionPrefix+"scan:scan_not_completed" {
		t.Fatalf("a week-old wait: %+v, %v", outcome, err)
	}
}

func TestSecurityPolicyConditionReadsTheTargetPolicy(t *testing.T) {
	ctx := context.Background()
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), json.RawMessage(`{"securityPolicy":"pass"}`))
	current, err := f.store.GetRepositorySecurityPolicy(ctx, f.target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ReplaceRepositorySecurityPolicy(ctx, f.target.ID, repository.RepositorySecurityPolicy{Enabled: true, RequireSignature: true}, current.Version); err != nil {
		t.Fatal(err)
	}
	f.publishRaw(f.source, "releases/widget.txt", "widget")
	f.runOnce()
	waiting := f.evaluations(repository.PromotionEvaluationWaiting)
	if len(waiting) != 1 || !strings.HasPrefix(waiting[0].LastReason, reasonConditionPrefix+"securityPolicy:") {
		t.Fatalf("the target's policy was not evaluated: %+v", f.evaluations())
	}
}

func TestAgeConditionWaitsUntilPublishedPlusMinAgeAndNeverSpins(t *testing.T) {
	ctx := context.Background()
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), json.RawMessage(`{"minAge":"72h"}`))
	f.publishRaw(f.source, "releases/widget.txt", "widget")
	f.runOnce()
	waiting := f.evaluations(repository.PromotionEvaluationWaiting)
	if len(waiting) != 1 || waiting[0].Attempts != 0 {
		t.Fatalf("evaluations = %+v", f.evaluations())
	}
	if want := waiting[0].EventOccurredAt.Add(72 * time.Hour); !waiting[0].NextAttemptAt.Equal(want) {
		t.Fatalf("next attempt = %v, want published + 72h = %v", waiting[0].NextAttemptAt, want)
	}
	if handled := f.runOnce(); handled != 0 {
		t.Fatalf("a waiting evaluation was handled before it is due: %d", handled)
	}
	// Three days later the same evaluation finds the artifact old enough and the promotion is requested.
	f.now = f.now.Add(73 * time.Hour)
	if outcome, err := f.evaluator.evaluate(ctx, waiting[0]); err != nil || outcome.State != repository.PromotionEvaluationQueued || outcome.PromotionID == "" {
		t.Fatalf("a due evaluation: %+v, %v", outcome, err)
	}
	// A clock that runs behind the database's can find an evaluation a moment too early: it waits at least waitFloor, it
	// does not come straight back.
	other := f.publishRaw(f.source, "releases/other.txt", "other")
	early := repository.PromotionEvaluation{ID: uuid.NewString(), RuleID: f.rule.ID, RepositoryID: f.source.ID, Coordinate: other.Path, Digest: other.Digest, EventOccurredAt: f.now.Add(-72*time.Hour + time.Millisecond)}
	if outcome, err := f.evaluator.evaluate(ctx, early); err != nil || outcome.State != repository.PromotionEvaluationWaiting || outcome.NextAttemptAt.Before(f.now.Add(waitFloor)) {
		t.Fatalf("an almost old enough artifact: %+v, %v", outcome, err)
	}
}

// panickingStore panics when the evaluator reads a rule.
type panickingStore struct{ *repository.MemoryStore }

func (panickingStore) GetPromotionRule(context.Context, string) (repository.PromotionRule, error) {
	panic("corrupt rule")
}

func TestPanickingEvaluationIsRecordedAndFailsOnceItsAttemptsAreUsed(t *testing.T) {
	ctx := context.Background()
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), nil)
	f.publishRaw(f.source, "releases/widget.txt", "widget")
	evaluator := NewPromotionEvaluator(PromotionEvaluatorOptions{Store: panickingStore{f.store}, Authorizer: RepositoryAuthorizer{Grants: f.store, Legacy: testAuthenticator()}, Owner: "panicker"})
	if handled, err := evaluator.RunOnce(ctx); err != nil || handled != 1 {
		t.Fatalf("a panic escaped or was not counted: %d, %v", handled, err)
	}
	pending := f.evaluations(repository.PromotionEvaluationPending)
	if len(pending) != 1 || pending[0].LastReason != reasonEvaluationPanic || pending[0].Attempts != 1 || pending[0].LeaseToken != "" || !pending[0].NextAttemptAt.After(f.now) {
		t.Fatalf("evaluations = %+v", f.evaluations())
	}
}

// A worker that dies on the same row every time stops after the attempts the row has: the claim fails the row instead of
// taking it again.
func TestRowThatKeepsKillingItsWorkerIsFailedByTheClaim(t *testing.T) {
	ctx := context.Background()
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), nil)
	f.publishRaw(f.source, "releases/widget.txt", "widget")
	for attempt := 1; attempt <= repository.PromotionEvaluationMaxAttempts; attempt++ {
		claimed, err := f.store.ClaimPromotionEvaluations(ctx, "dying", 0, 1)
		if err != nil || len(claimed) != 1 || claimed[0].Attempts != attempt {
			t.Fatalf("claim %d = %+v, %v", attempt, claimed, err)
		}
	}
	if claimed, err := f.store.ClaimPromotionEvaluations(ctx, "survivor", time.Minute, 1); err != nil || len(claimed) != 0 {
		t.Fatalf("a row with no attempts left was claimed: %+v, %v", claimed, err)
	}
	if got := f.evaluations(repository.PromotionEvaluationFailed); len(got) != 1 || got[0].LastReason != repository.PromotionEvaluationExhausted || got[0].LeaseToken != "" {
		t.Fatalf("evaluations = %+v", f.evaluations())
	}
}

// grantsDownStore cannot read repository grants.
type grantsDownStore struct{ *repository.MemoryStore }

func (grantsDownStore) GetRepositoryGrants(context.Context, string) (repository.RepositoryGrantSet, error) {
	return repository.RepositoryGrantSet{}, errors.New("database unavailable")
}

func TestUnreadableGrantsRetryTheEvaluationAndDoNotParkTheRule(t *testing.T) {
	ctx := context.Background()
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), nil)
	f.publishRaw(f.source, "releases/widget.txt", "widget")
	evaluator := NewPromotionEvaluator(PromotionEvaluatorOptions{Store: f.store, Authorizer: RepositoryAuthorizer{Grants: grantsDownStore{f.store}, Legacy: testAuthenticator()}, Owner: "blip"})
	if handled, err := evaluator.RunOnce(ctx); err != nil || handled != 1 {
		t.Fatalf("handled = %d, %v", handled, err)
	}
	if pending := f.evaluations(repository.PromotionEvaluationPending); len(pending) != 1 || pending[0].LastReason != reasonEvaluationError {
		t.Fatalf("a database blip must retry the evaluation: %+v", f.evaluations())
	}
	if rule, err := f.store.GetPromotionRule(ctx, f.rule.ID); err != nil || rule.State != repository.PromotionRuleActive {
		t.Fatalf("a database blip parked the rule: %+v, %v", rule, err)
	}
}

func TestParkingIsConditionalOnTheRuleVersionTheEvaluationRead(t *testing.T) {
	ctx := context.Background()
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), nil)
	stale := f.rule
	edited, err := f.store.UpdatePromotionRule(ctx, f.rule, f.rule.Version)
	if err != nil {
		t.Fatal(err)
	}
	evaluation := repository.PromotionEvaluation{RuleID: f.rule.ID, RepositoryID: f.source.ID, Coordinate: "a.txt", Digest: "sha256:aa"}
	outcome, err := f.evaluator.park(ctx, stale, f.source, f.target, evaluation, "service_account_disabled")
	if err != nil || outcome.State != repository.PromotionEvaluationPending || outcome.Reason != reasonRuleChanged {
		t.Fatalf("park on a stale rule: %+v, %v", outcome, err)
	}
	if rule, getErr := f.store.GetPromotionRule(ctx, f.rule.ID); getErr != nil || rule.State != repository.PromotionRuleActive || rule.Version != edited.Version {
		t.Fatalf("a rule edited since was parked: %+v, %v", rule, getErr)
	}
}

// versionedRawFixture is a versioned Raw dev repository with a sealed version of two files and a rule with scope release.
func TestReleaseScopeOnVersionedRawPromotesEveryFileOfTheVersion(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	store.SetPromotionRuleMatcher(PromotionRuleMatcher())
	f := &evaluatorFixture{t: t, store: store, objects: objectstore.NewMemoryStore(), now: time.Now().UTC()}
	source, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "tools-dev", Format: repository.FormatRaw, RawLayout: repository.RawLayoutVersioned, SealMode: repository.RawSealIdle})
	if err != nil {
		t.Fatal(err)
	}
	target := mustCreateRepository(t, store, "tools-release", repository.FormatRaw)
	f.source, f.target = source, target
	account, err := store.CreateServiceAccount(ctx, repository.ServiceAccount{ID: uuid.NewString(), Name: "promoter", State: repository.ServiceAccountActive})
	if err != nil {
		t.Fatal(err)
	}
	f.account = account
	grantServiceAccount(t, store, source, account.ID, "repositories:read")
	grantServiceAccount(t, store, target, account.ID, "repositories:write")
	match := promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "tool@*"}, Scope: promotionrules.ScopeRelease}
	f.rule, err = store.CreatePromotionRule(ctx, repository.PromotionRule{ID: uuid.NewString(), Name: "release", SourceRepositoryID: source.ID, TargetKind: repository.PromotionTargetLocal,
		TargetRepositoryID: target.ID, Match: ruleMatch(t, match), RunAsServiceAccountID: account.ID, Enabled: true, CreatedBy: "alice"}, 32)
	if err != nil {
		t.Fatal(err)
	}
	f.evaluator = NewPromotionEvaluator(PromotionEvaluatorOptions{Store: store, Authorizer: RepositoryAuthorizer{Grants: store, Legacy: testAuthenticator()}, Owner: "test-worker", Now: func() time.Time { return f.now }})
	first := f.publishRaw(source, "tool/1.0.0/bin/tool", "binary")
	// A file of an open version is announced, but the version is not released until it is sealed.
	f.runOnce()
	if waiting := f.evaluations(repository.PromotionEvaluationWaiting); len(waiting) != 1 || waiting[0].LastReason != reasonVersionNotSealed {
		t.Fatalf("evaluations = %+v", f.evaluations())
	}
	second := f.publishRaw(source, "tool/1.0.0/README.md", "readme")
	if _, err = store.SealRawVersion(ctx, repository.SealRawVersionInput{RepositoryID: source.ID, Name: "tool", Version: "1.0.0", Actor: "ci"}); err != nil {
		t.Fatal(err)
	}
	// Both file evaluations (the waiting one is made due by the clock) now see the sealed version.
	for _, evaluation := range f.evaluations() {
		if outcome, evalErr := f.evaluator.evaluate(ctx, evaluation); evalErr != nil || outcome.State != repository.PromotionEvaluationQueued {
			t.Fatalf("evaluation %s: %+v, %v", evaluation.Coordinate, outcome, evalErr)
		}
	}
	rows := f.promotionRows(target.ID)
	if len(rows) != 2 {
		t.Fatalf("a release of two files wrote %d provenance rows: %+v", len(rows), rows)
	}
	got := map[string]string{}
	for _, row := range rows {
		got[row.Coordinate] = row.Digest
		if row.RuleID != f.rule.ID {
			t.Fatalf("row = %+v", row)
		}
	}
	if got[first.Path] != first.Digest || got[second.Path] != second.Digest {
		t.Fatalf("promoted %+v, want %s and %s", got, first.Path, second.Path)
	}
	// A version outside the match is not released.
	other := f.publishRaw(source, "other/2.0.0/file", "other")
	for _, evaluation := range f.evaluations(repository.PromotionEvaluationPending) {
		if evaluation.Coordinate == other.Path {
			t.Fatalf("a version outside the match fanned out: %+v", evaluation)
		}
	}
}

// chainFixture is a dev -> staging -> release chain of two rules between three repositories of one format, run by a
// service account that reads the sources and writes the targets.
type chainFixture struct {
	t         *testing.T
	store     *repository.MemoryStore
	account   repository.ServiceAccount
	dev       repository.HostedRepository
	staging   repository.HostedRepository
	release   repository.HostedRepository
	evaluator *PromotionEvaluator
}

func newChainFixture(t *testing.T, format repository.Format) *chainFixture {
	t.Helper()
	ctx := context.Background()
	store := repository.NewMemoryStore()
	store.SetPromotionRuleMatcher(PromotionRuleMatcher())
	c := &chainFixture{t: t, store: store, dev: mustCreateRepository(t, store, "dev", format), staging: mustCreateRepository(t, store, "staging", format), release: mustCreateRepository(t, store, "release", format)}
	account, err := store.CreateServiceAccount(ctx, repository.ServiceAccount{ID: uuid.NewString(), Name: "promoter", State: repository.ServiceAccountActive})
	if err != nil {
		t.Fatal(err)
	}
	c.account = account
	grantServiceAccount(t, store, c.dev, account.ID, "repositories:read")
	grantServiceAccount(t, store, c.staging, account.ID, "repositories:read", "repositories:write")
	grantServiceAccount(t, store, c.release, account.ID, "repositories:write")
	for name, edge := range map[string][2]repository.HostedRepository{"dev-to-staging": {c.dev, c.staging}, "staging-to-release": {c.staging, c.release}} {
		if _, err = store.CreatePromotionRule(ctx, repository.PromotionRule{ID: uuid.NewString(), Name: name, SourceRepositoryID: edge[0].ID, TargetKind: repository.PromotionTargetLocal, TargetRepositoryID: edge[1].ID,
			Match: ruleMatch(t, globMatch("**")), RunAsServiceAccountID: account.ID, Enabled: true, CreatedBy: "alice"}, 32); err != nil {
			t.Fatal(err)
		}
	}
	c.evaluator = NewPromotionEvaluator(PromotionEvaluatorOptions{Store: store, Authorizer: RepositoryAuthorizer{Grants: store, Legacy: testAuthenticator()}, Owner: "chain-worker"})
	return c
}

// settle alternates the evaluator and the format's promotion job runner until neither has work: the artifact has
// travelled as far as the rules take it.
func (c *chainFixture) settle(runJobs func() error) {
	c.t.Helper()
	for round := 0; round < 6; round++ {
		handled, err := c.evaluator.RunOnce(context.Background())
		if err != nil {
			c.t.Fatal(err)
		}
		before := len(c.promotionRows(c.staging)) + len(c.promotionRows(c.release))
		if err = runJobs(); err != nil {
			c.t.Fatal(err)
		}
		if handled == 0 && before == len(c.promotionRows(c.staging))+len(c.promotionRows(c.release)) && round > 0 {
			return
		}
	}
}

func (c *chainFixture) promotionRows(target repository.HostedRepository) []repository.ArtifactPromotion {
	c.t.Helper()
	page, err := c.store.PageArtifactPromotions(context.Background(), repository.ArtifactPromotionQuery{TargetRepositoryID: target.ID, Limit: 100})
	if err != nil {
		c.t.Fatal(err)
	}
	return page.Items
}

// requireChain checks that the artifact was promoted into staging by the first rule and into release by the second, each
// time with a completed provenance row whose chain path names the hop.
func (c *chainFixture) requireChain(coordinate, digest string) {
	c.t.Helper()
	for _, hop := range []struct {
		target repository.HostedRepository
		origin string
	}{{c.staging, "dev"}, {c.release, "staging"}} {
		rows := c.promotionRows(hop.target)
		if len(rows) != 1 || rows[0].Coordinate != coordinate || rows[0].Digest != digest || rows[0].State != repository.ArtifactPromotionCompleted || rows[0].RuleID == "" ||
			rows[0].OriginRepository != hop.origin || rows[0].RequestedBy != "service-account:"+c.account.ID {
			c.t.Fatalf("promotion into %s = %+v", hop.target.Name, rows)
		}
	}
}

func TestChainPromotesOCIFromDevThroughStagingToRelease(t *testing.T) {
	ctx := context.Background()
	c := newChainFixture(t, repository.FormatOCI)
	objects := objectstore.NewMemoryStore()
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	sum := sha256.Sum256(manifest)
	digest := fmt.Sprintf("sha256:%x", sum[:])
	if err := objects.Put(ctx, "oci/dev/manifest", manifest); err != nil {
		t.Fatal(err)
	}
	if err := c.store.StageOCIObjectIntent(ctx, repository.OCIObjectIntent{RepositoryID: c.dev.ID, ObjectKey: "oci/dev/manifest", Digest: digest, Size: int64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.store.PutOCIManifest(ctx, repository.OCIManifest{RepositoryID: c.dev.ID, Name: "team/widget", Digest: digest, ObjectKey: "oci/dev/manifest", Size: int64(len(manifest))}, digest); err != nil {
		t.Fatal(err)
	}
	worker := ociprotocol.NativePromotion{Store: c.store, Objects: objects, Intelligence: c.store}
	c.settle(func() error { return worker.RunJobs(ctx, 10) })
	c.requireChain("team/widget", digest)
	for _, repo := range []repository.HostedRepository{c.staging, c.release} {
		if got, err := c.store.GetOCIManifest(ctx, repo.ID, "team/widget", digest); err != nil || got.Digest != digest {
			t.Fatalf("%s holds %+v, %v", repo.Name, got, err)
		}
	}
}

func TestChainPromotesMavenFromDevThroughStagingToRelease(t *testing.T) {
	ctx := context.Background()
	c := newChainFixture(t, repository.FormatMaven)
	const coordinate = "org.example:widget:1.0.0"
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	session := repository.MavenPublishSession{ID: "dev-session", RepositoryID: c.dev.ID, Coordinate: coordinate, Publisher: "ci", State: "open", ExpiresAt: time.Now().Add(time.Hour),
		Objects: []repository.MavenDeclaredObject{{Name: "widget-1.0.0.jar", Digest: digest, Size: 3}}}
	if _, err := c.store.CreateMavenPublishSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := c.store.MarkMavenPublishObject(ctx, session.ID, session.Objects[0].Name, "native/maven/widget"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.store.CommitMavenPublishSession(ctx, session.ID, []repository.MavenAsset{{RepositoryID: c.dev.ID, Path: "org/example/widget/1.0.0/widget-1.0.0.jar", ObjectKey: "native/maven/widget", Digest: digest, Size: 3}}); err != nil {
		t.Fatal(err)
	}
	worker := mavenprotocol.NativePromotion{Store: c.store, Intelligence: c.store}
	c.settle(func() error { return worker.RunJobs(ctx, 10) })
	c.requireChain(coordinate, digest)
	for _, repo := range []repository.HostedRepository{c.staging, c.release} {
		if assets, err := c.store.ListMavenAssets(ctx, repo.ID, coordinate); err != nil || len(assets) != 1 {
			t.Fatalf("%s holds %+v, %v", repo.Name, assets, err)
		}
	}
}

func TestEvaluationsOfAParkedRuleWaitAndPromoteOnceTheRuleIsEdited(t *testing.T) {
	ctx := context.Background()
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), nil)
	f.publishRaw(f.source, "releases/first.txt", "first")
	f.publishRaw(f.source, "releases/second.txt", "second")
	disabled := repository.ServiceAccountDisabled
	if _, err := f.store.UpdateServiceAccount(ctx, repository.ServiceAccountUpdate{ID: f.account.ID, State: &disabled}, f.account.Version); err != nil {
		t.Fatal(err)
	}
	// The two evaluations run at once: one parks the rule and the other either finds it parked or, having read it active,
	// loses the park to the first and is decided again (pending, due at once). Run until nothing is pending.
	for round := 0; round < 3 && (round == 0 || len(f.evaluations(repository.PromotionEvaluationPending)) > 0); round++ {
		f.runOnce()
	}
	// Both wait for the rule to come back.
	waitingRows := f.evaluations(repository.PromotionEvaluationWaiting)
	if len(waitingRows) != 2 {
		t.Fatalf("evaluations = %+v", f.evaluations())
	}
	reasons := map[string]bool{waitingRows[0].LastReason: true, waitingRows[1].LastReason: true}
	if !reasons[reasonAuthorizationLost+":service_account_disabled"] || !reasons[reasonRuleParkedPrefix+repository.PromotionRuleAuthorizationLost] {
		t.Fatalf("reasons = %v", reasons)
	}
	waiting := waitingRows[0]
	active := repository.ServiceAccountActive
	account, err := f.store.GetServiceAccount(ctx, f.account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.UpdateServiceAccount(ctx, repository.ServiceAccountUpdate{ID: f.account.ID, State: &active}, account.Version); err != nil {
		t.Fatal(err)
	}
	rule, err := f.store.GetPromotionRule(ctx, f.rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.UpdatePromotionRule(ctx, rule, rule.Version); err != nil {
		t.Fatal(err)
	}
	if outcome, evalErr := f.evaluator.evaluate(ctx, waiting); evalErr != nil || outcome.State != repository.PromotionEvaluationQueued {
		t.Fatalf("after the edit: %+v, %v", outcome, evalErr)
	}
}

func TestReleaseConditionsHoldForEveryMember(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	store.SetPromotionRuleMatcher(PromotionRuleMatcher())
	f := &evaluatorFixture{t: t, store: store, objects: objectstore.NewMemoryStore(), now: time.Now().UTC()}
	f.source = func() repository.HostedRepository {
		repo, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "tools-dev", Format: repository.FormatRaw, RawLayout: repository.RawLayoutVersioned, SealMode: repository.RawSealIdle})
		if err != nil {
			t.Fatal(err)
		}
		return repo
	}()
	f.target = mustCreateRepository(t, store, "tools-release", repository.FormatRaw)
	account, err := store.CreateServiceAccount(ctx, repository.ServiceAccount{ID: uuid.NewString(), Name: "promoter", State: repository.ServiceAccountActive})
	if err != nil {
		t.Fatal(err)
	}
	f.account = account
	grantServiceAccount(t, store, f.source, account.ID, "repositories:read")
	grantServiceAccount(t, store, f.target, account.ID, "repositories:write")
	match := promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "tool@*"}, Scope: promotionrules.ScopeRelease}
	f.rule, err = store.CreatePromotionRule(ctx, repository.PromotionRule{ID: uuid.NewString(), Name: "release", SourceRepositoryID: f.source.ID, TargetKind: repository.PromotionTargetLocal, TargetRepositoryID: f.target.ID,
		Match: ruleMatch(t, match), Conditions: json.RawMessage(`{"scan":{"completed":true}}`), RunAsServiceAccountID: account.ID, Enabled: true, CreatedBy: "alice"}, 32)
	if err != nil {
		t.Fatal(err)
	}
	f.evaluator = NewPromotionEvaluator(PromotionEvaluatorOptions{Store: store, Authorizer: RepositoryAuthorizer{Grants: store, Legacy: testAuthenticator()}, Owner: "test-worker", Now: func() time.Time { return f.now }})
	first := f.publishRaw(f.source, "tool/1.0.0/bin/tool", "binary")
	second := f.publishRaw(f.source, "tool/1.0.0/README.md", "readme")
	if _, err = store.SealRawVersion(ctx, repository.SealRawVersionInput{RepositoryID: f.source.ID, Name: "tool", Version: "1.0.0", Actor: "ci"}); err != nil {
		t.Fatal(err)
	}
	// Only one file of the release was scanned: the release as a whole waits.
	if _, err = store.ReplaceArtifactIntelligence(ctx, repository.ArtifactIntelligence{RepositoryID: f.source.ID, Format: repository.FormatRaw, Coordinate: first.Path, Digest: first.Digest,
		Vulnerability: &repository.ArtifactVulnerabilitySummary{Scanner: "test", Status: "clean", ScannedAt: f.now}}, ""); err != nil {
		t.Fatal(err)
	}
	f.runOnce()
	if rows := f.promotionRows(f.target.ID); len(rows) != 0 {
		t.Fatalf("a release with an unscanned member was promoted: %+v", rows)
	}
	if waiting := f.evaluations(repository.PromotionEvaluationWaiting); len(waiting) != 2 || waiting[0].LastReason != reasonConditionPrefix+"scan:scan_not_completed" {
		t.Fatalf("evaluations = %+v", f.evaluations())
	}
	if _, err = store.ReplaceArtifactIntelligence(ctx, repository.ArtifactIntelligence{RepositoryID: f.source.ID, Format: repository.FormatRaw, Coordinate: second.Path, Digest: second.Digest,
		Vulnerability: &repository.ArtifactVulnerabilitySummary{Scanner: "test", Status: "clean", ScannedAt: f.now}}, ""); err != nil {
		t.Fatal(err)
	}
	for _, evaluation := range f.evaluations() {
		if outcome, evalErr := f.evaluator.evaluate(ctx, evaluation); evalErr != nil || outcome.State != repository.PromotionEvaluationQueued {
			t.Fatalf("with every member scanned: %+v, %v", outcome, evalErr)
		}
	}
	if rows := f.promotionRows(f.target.ID); len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestTargetThatHoldsOtherBytesIsAConflict(t *testing.T) {
	ctx := context.Background()
	f := newEvaluatorFixture(t, repository.FormatRaw, globMatch("**"), nil)
	asset := f.publishRaw(f.source, "releases/widget.txt", "widget")
	f.runOnce()
	if err := f.runRawPromotionJobs(); err != nil {
		t.Fatal(err)
	}
	// The source coordinate is offered again with other bytes: the target is immutable, so this is a conflict, not a promotion.
	other := repository.PromotionEvaluation{ID: uuid.NewString(), RuleID: f.rule.ID, RepositoryID: f.source.ID, Coordinate: asset.Path, Digest: "sha256:" + strings.Repeat("b", 64), EventOccurredAt: f.now}
	outcome, err := f.evaluator.evaluate(ctx, other)
	if err != nil || outcome.State != repository.PromotionEvaluationFailed || !strings.HasPrefix(outcome.Reason, reasonConflict+":"+asset.Path+" holds "+asset.Digest) {
		t.Fatalf("conflict: %+v, %v", outcome, err)
	}
}

func TestReleaseEdgeCasesAreDecidedWithAReasonNotRetried(t *testing.T) {
	ctx := context.Background()
	// A release rule on a format without ReleaseSet cannot even be created through the API, so the stored rule is built here.
	f := newEvaluatorFixture(t, repository.FormatNPM, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "**"}, Scope: promotionrules.ScopeRelease}, nil)
	unsupported := repository.PromotionEvaluation{ID: uuid.NewString(), RuleID: f.rule.ID, RepositoryID: f.source.ID, Coordinate: "app@1.0.0", Digest: "sha256:" + strings.Repeat("c", 64), EventOccurredAt: f.now}
	if outcome, err := f.evaluator.evaluate(ctx, unsupported); err != nil || outcome.State != repository.PromotionEvaluationFailed || outcome.Reason != reasonUnsupportedFormat {
		t.Fatalf("unsupported format: %+v, %v", outcome, err)
	}
	// An unversioned Raw release is the one file; a release larger than the bound fails with its reason.
	raw := newEvaluatorFixture(t, repository.FormatRaw, promotionrules.Match{Coordinate: promotionrules.Coordinate{Glob: "**"}, Scope: promotionrules.ScopeRelease}, nil)
	asset := raw.publishRaw(raw.source, "releases/widget.txt", "widget")
	raw.evaluator.releaseMax = 0
	evaluation := repository.PromotionEvaluation{ID: uuid.NewString(), RuleID: raw.rule.ID, RepositoryID: raw.source.ID, Coordinate: asset.Path, Digest: asset.Digest, EventOccurredAt: raw.now}
	if outcome, err := raw.evaluator.evaluate(ctx, evaluation); err != nil || outcome.State != repository.PromotionEvaluationFailed || outcome.Reason != reasonReleaseTooLarge {
		t.Fatalf("too large: %+v, %v", outcome, err)
	}
}
