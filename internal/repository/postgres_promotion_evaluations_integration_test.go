//go:build integration

package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/events"
)

// staticFanout returns fixed rule ids, or an error.
type staticFanout struct {
	rules []string
	err   error
}

func (f staticFanout) MatchingRules(context.Context, events.DB, events.Event) ([]string, error) {
	return f.rules, f.err
}

type evaluationFixture struct {
	store  *PostgresStore
	source HostedRepository
	target HostedRepository
	rule   PromotionRule
}

// newEvaluationFixture creates two Raw repositories, a service account and a rule between them in the shared test database,
// and removes them (their evaluations cascade) when the test ends.
func newEvaluationFixture(t *testing.T) evaluationFixture {
	t.Helper()
	store := domainEventsStore(t)
	ctx := context.Background()
	source, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "eval-src-" + uuid.NewString()[:8], Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "eval-dst-" + uuid.NewString()[:8], Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.CreateServiceAccount(ctx, ServiceAccount{ID: uuid.NewString(), Name: "eval-" + uuid.NewString()[:8], State: ServiceAccountActive})
	if err != nil {
		t.Fatal(err)
	}
	rule, err := store.CreatePromotionRule(ctx, PromotionRule{ID: uuid.NewString(), Name: "eval-" + uuid.NewString()[:8], SourceRepositoryID: source.ID, TargetKind: PromotionTargetLocal,
		TargetRepositoryID: target.ID, Match: []byte(`{"coordinate":{"glob":"**"}}`), RunAsServiceAccountID: account.ID, Enabled: true, CreatedBy: "alice"}, 32)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		events.SetRuleFanout(nil)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM promotion_rules WHERE id=$1`, rule.ID)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM domain_events WHERE repository_id=$1`, source.ID)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM hosted_repositories WHERE id IN ($1,$2)`, source.ID, target.ID)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM service_accounts WHERE id=$1`, account.ID)
	})
	return evaluationFixture{store: store, source: source, target: target, rule: rule}
}

func (f evaluationFixture) publish(t *testing.T, commit bool) (events.Event, error) {
	t.Helper()
	ctx := context.Background()
	event := events.Published(f.source.ID, "raw", "releases/"+uuid.NewString()+".txt", "sha256:"+uuid.NewString(), "")
	tx, err := f.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = events.Emit(ctx, tx, event); err != nil {
		return event, err
	}
	if !commit {
		return event, tx.Rollback()
	}
	return event, tx.Commit()
}

func (f evaluationFixture) evaluations(t *testing.T) []PromotionEvaluation {
	t.Helper()
	items, err := f.store.ListPromotionEvaluations(context.Background(), PromotionEvaluationQuery{RuleID: f.rule.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// A publication fans out to the rules the fan-out names in its own transaction: the evaluation commits with the publication
// and rolls back with it, a rule deleted or disabled since the rule set was cached gets none, and a failure of the fan-out
// fails the publication.
func TestPostgresPublicationFansOutInItsOwnTransaction(t *testing.T) {
	f := newEvaluationFixture(t)
	events.SetRuleFanout(staticFanout{rules: []string{f.rule.ID}})
	event, err := f.publish(t, true)
	if err != nil {
		t.Fatal(err)
	}
	items := f.evaluations(t)
	if len(items) != 1 || items[0].State != PromotionEvaluationPending || items[0].Coordinate != event.Coordinate || items[0].Digest != event.Digest || items[0].RepositoryID != f.source.ID || items[0].EventSeq == 0 || items[0].EventOccurredAt.IsZero() {
		t.Fatalf("evaluations = %+v", items)
	}
	if _, err = f.publish(t, false); err != nil {
		t.Fatal(err)
	}
	if got := f.evaluations(t); len(got) != 1 {
		t.Fatalf("a rolled-back publication left an evaluation: %+v", got)
	}

	// The rule set is a notification behind: a rule named by the cache that was disabled since gets no evaluation.
	disabled := f.rule
	disabled.Enabled = false
	if _, err = f.store.UpdatePromotionRule(context.Background(), disabled, f.rule.Version); err != nil {
		t.Fatal(err)
	}
	if _, err = f.publish(t, true); err != nil {
		t.Fatal(err)
	}
	if got := f.evaluations(t); len(got) != 1 {
		t.Fatalf("a disabled rule got an evaluation: %+v", got)
	}

	// An unknown rule id is dropped by the same statement, not a foreign key violation that would fail the publication.
	events.SetRuleFanout(staticFanout{rules: []string{uuid.NewString()}})
	if _, err = f.publish(t, true); err != nil {
		t.Fatalf("a stale rule id failed the publication: %v", err)
	}

	// A failing fan-out fails the publication, and nothing of it is written.
	events.SetRuleFanout(staticFanout{err: errors.New("rule set unavailable")})
	failing := events.Published(f.source.ID, "raw", "releases/failing.txt", "sha256:"+uuid.NewString(), "")
	tx, err := f.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = events.Emit(context.Background(), tx, failing); err == nil {
		t.Fatal("a fan-out failure must fail Emit")
	}
	_ = tx.Rollback()
	if n := countDomainEvents(t, f.store, failing.DedupeKey()); n != 0 {
		t.Fatalf("a failed publication left %d events", n)
	}
}

// Two workers claim disjoint rows (FOR UPDATE SKIP LOCKED), the claim leases them, the outcome is recorded only under the
// live lease, and a row whose worker died is due again when its lease ends.
func TestPostgresEvaluationClaimLeaseAndOutcome(t *testing.T) {
	f := newEvaluationFixture(t)
	ctx := context.Background()
	events.SetRuleFanout(staticFanout{rules: []string{f.rule.ID}})
	for range 6 {
		if _, err := f.publish(t, true); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	claims := make([][]PromotionEvaluation, 2)
	errs := make([]error, 2)
	for i := range claims {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claims[i], errs[i] = f.store.ClaimPromotionEvaluations(ctx, "worker-"+string(rune('a'+i)), time.Minute, 3)
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, claimed := range claims {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		for _, evaluation := range claimed {
			if seen[evaluation.ID] || evaluation.LeaseToken == "" || evaluation.Attempts != 1 || !evaluation.NextAttemptAt.Equal(evaluation.LeaseExpiresAt) {
				t.Fatalf("claim %d took %+v (seen before: %v)", i, evaluation, seen[evaluation.ID])
			}
			seen[evaluation.ID] = true
		}
	}
	if len(seen) != 6 {
		t.Fatalf("two workers claimed %d of 6 rows", len(seen))
	}
	if again, err := f.store.ClaimPromotionEvaluations(ctx, "worker-c", time.Minute, 10); err != nil || len(again) != 0 {
		t.Fatalf("leased rows were claimed again: %+v, %v", again, err)
	}

	first := claims[0][0]
	if err := f.store.FinishPromotionEvaluation(ctx, first.ID, uuid.NewString(), PromotionEvaluationOutcome{State: PromotionEvaluationSkipped}); !errors.Is(err, ErrPromotionEvaluationLeaseLost) {
		t.Fatalf("a foreign lease token recorded an outcome: %v", err)
	}
	if err := f.store.FinishPromotionEvaluation(ctx, first.ID, first.LeaseToken, PromotionEvaluationOutcome{State: PromotionEvaluationSkipped, Reason: "no_match"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishPromotionEvaluation(ctx, first.ID, first.LeaseToken, PromotionEvaluationOutcome{State: PromotionEvaluationFailed}); !errors.Is(err, ErrPromotionEvaluationLeaseLost) {
		t.Fatalf("a released lease recorded a second outcome: %v", err)
	}
	second := claims[0][1]
	due := time.Now().Add(time.Hour)
	if err := f.store.FinishPromotionEvaluation(ctx, second.ID, second.LeaseToken, PromotionEvaluationOutcome{State: PromotionEvaluationWaiting, Reason: "condition_failed:minAge", NextAttemptAt: due}); err != nil {
		t.Fatal(err)
	}
	for _, evaluation := range f.evaluations(t) {
		switch evaluation.ID {
		case first.ID:
			if evaluation.State != PromotionEvaluationSkipped || evaluation.LastReason != "no_match" || evaluation.LeaseToken != "" {
				t.Fatalf("skipped = %+v", evaluation)
			}
		case second.ID:
			if evaluation.State != PromotionEvaluationWaiting || evaluation.Attempts != 0 || evaluation.NextAttemptAt.Before(due.Add(-time.Second)) {
				t.Fatalf("waiting = %+v", evaluation)
			}
		}
	}
}

func TestPostgresAbandonedEvaluationBecomesDueWhenItsLeaseEnds(t *testing.T) {
	f := newEvaluationFixture(t)
	ctx := context.Background()
	events.SetRuleFanout(staticFanout{rules: []string{f.rule.ID}})
	if _, err := f.publish(t, true); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.store.ClaimPromotionEvaluations(ctx, "dying-worker", 50*time.Millisecond, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed = %+v, %v", claimed, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		again, claimErr := f.store.ClaimPromotionEvaluations(ctx, "survivor", time.Minute, 1)
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		if len(again) == 1 {
			if again[0].ID != claimed[0].ID || again[0].Attempts != 2 || again[0].LeaseOwner != "survivor" {
				t.Fatalf("recovered = %+v", again[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the abandoned evaluation never became due")
		}
		<-time.After(20 * time.Millisecond)
	}
	// The dead worker's late outcome is refused.
	if err = f.store.FinishPromotionEvaluation(ctx, claimed[0].ID, claimed[0].LeaseToken, PromotionEvaluationOutcome{State: PromotionEvaluationQueued}); !errors.Is(err, ErrPromotionEvaluationLeaseLost) {
		t.Fatalf("the dead worker's outcome was recorded: %v", err)
	}
}

// Parking a rule changes its state, announces promotion.failed in the same transaction, flushes the rule cache scope and is
// refused for a rule that is not active.
func TestPostgresParkPromotionRuleAnnouncesAndInvalidates(t *testing.T) {
	f := newEvaluationFixture(t)
	ctx := context.Background()
	subscription, err := f.store.CreateWebhookSubscription(ctx, WebhookSubscription{ID: uuid.NewString(), Name: "park-" + uuid.NewString()[:8], EndpointURL: "https://events.example.test/p",
		SecretCiphertext: "sealed", EventTypes: []WebhookEventType{WebhookEventPromotionFailed}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.store.db.ExecContext(ctx, `DELETE FROM webhook_subscriptions WHERE id=$1`, subscription.ID)
	})
	var scopes []string
	f.store.SetCacheInvalidator(func(scope, _ string) { scopes = append(scopes, scope) })
	t.Cleanup(func() { f.store.SetCacheInvalidator(nil) })
	event := events.Event{Type: events.PromotionFailed, RepositoryID: f.target.ID, Format: "raw", Coordinate: "releases/a.txt", Digest: "sha256:" + uuid.NewString(), Version: "rule:" + f.rule.ID + ":1"}
	// A decision made from an older version of the rule is refused: the rule was edited since.
	if stale, staleErr := f.store.ParkPromotionRule(ctx, f.rule.ID, "99", PromotionRuleAuthorizationLost, "stale", event); staleErr != nil || stale {
		t.Fatalf("parked on a stale version: %v, %v", stale, staleErr)
	}
	parked, err := f.store.ParkPromotionRule(ctx, f.rule.ID, f.rule.Version, PromotionRuleAuthorizationLost, "service_account_disabled", event)
	if err != nil || !parked {
		t.Fatalf("parked = %v, %v", parked, err)
	}
	rule, err := f.store.GetPromotionRule(ctx, f.rule.ID)
	if err != nil || rule.State != PromotionRuleAuthorizationLost || rule.StateReason != "service_account_disabled" || rule.Version != f.rule.Version {
		t.Fatalf("rule = %+v, %v", rule, err)
	}
	if n := countDomainEvents(t, f.store, event.DedupeKey()); n != 1 {
		t.Fatalf("promotion.failed events = %d", n)
	}
	if len(scopes) != 1 || scopes[0] != CacheScopePromotionRule {
		t.Fatalf("invalidated scopes = %v", scopes)
	}
	// A parked rule is still listed for the fan-out: the evaluations of what is published meanwhile wait for it.
	if enabled, err := ListEnabledPromotionRulesFrom(ctx, f.store.db, f.source.ID); err != nil || len(enabled) != 1 || enabled[0].State != PromotionRuleAuthorizationLost {
		t.Fatalf("listed rules = %+v, %v", enabled, err)
	}
	if again, err := f.store.ParkPromotionRule(ctx, f.rule.ID, rule.Version, PromotionRuleAuthorizationLost, "again", event); err != nil || again {
		t.Fatalf("a parked rule was parked again: %v, %v", again, err)
	}
	// An edit returns it to active.
	if updated, err := f.store.UpdatePromotionRule(ctx, rule, rule.Version); err != nil || updated.State != PromotionRuleActive || updated.StateReason != "" {
		t.Fatalf("edited rule = %+v, %v", updated, err)
	}
	if enabled, err := ListEnabledPromotionRulesFrom(ctx, f.store.db, f.source.ID); err != nil || len(enabled) != 1 || enabled[0].State != PromotionRuleActive {
		t.Fatalf("listed rules = %+v, %v", enabled, err)
	}
}

// A row that used all its attempts is failed by the claim and never claimed again; a row below the limit is still claimed.
func TestPostgresClaimFailsRowsThatUsedTheirAttempts(t *testing.T) {
	f := newEvaluationFixture(t)
	ctx := context.Background()
	events.SetRuleFanout(staticFanout{rules: []string{f.rule.ID}})
	if _, err := f.publish(t, true); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= PromotionEvaluationMaxAttempts; attempt++ {
		deadline := time.Now().Add(10 * time.Second)
		for {
			claimed, err := f.store.ClaimPromotionEvaluations(ctx, "dying-worker", 5*time.Millisecond, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(claimed) == 1 {
				if claimed[0].Attempts != attempt {
					t.Fatalf("claim %d counted %d attempts", attempt, claimed[0].Attempts)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("claim %d never became due", attempt)
			}
			<-time.After(5 * time.Millisecond)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		claimed, err := f.store.ClaimPromotionEvaluations(ctx, "survivor", time.Minute, 1)
		if err != nil || len(claimed) != 0 {
			t.Fatalf("a row with no attempts left was claimed: %+v, %v", claimed, err)
		}
		if failed, listErr := f.store.ListPromotionEvaluations(ctx, PromotionEvaluationQuery{RuleID: f.rule.ID, State: PromotionEvaluationFailed, Limit: 10}); listErr != nil {
			t.Fatal(listErr)
		} else if len(failed) == 1 {
			if failed[0].LastReason != PromotionEvaluationExhausted || failed[0].LeaseToken != "" {
				t.Fatalf("failed row = %+v", failed[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the exhausted row was never failed")
		}
		<-time.After(5 * time.Millisecond)
	}
}

// The rule fan-out lists the rules through the connection of the publishing transaction.
func TestPostgresRulesAreListedThroughTheTransaction(t *testing.T) {
	f := newEvaluationFixture(t)
	ctx := context.Background()
	tx, err := f.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	rules, err := ListEnabledPromotionRulesFrom(ctx, tx, f.source.ID)
	if err != nil || len(rules) != 1 || rules[0].ID != f.rule.ID || rules[0].State != PromotionRuleActive {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
}

// Recording a hold for a parked rule re-checks the rule in the same statement: a rule that is active again makes the row due at once.
func TestPostgresHoldForARuleIsDueAtOnceWhenTheRuleIsActive(t *testing.T) {
	f := newEvaluationFixture(t)
	ctx := context.Background()
	events.SetRuleFanout(staticFanout{rules: []string{f.rule.ID}})
	if _, err := f.publish(t, true); err != nil {
		t.Fatal(err)
	}
	claimed, err := f.store.ClaimPromotionEvaluations(ctx, "worker", time.Minute, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed = %+v, %v", claimed, err)
	}
	next := time.Now().Add(7 * 24 * time.Hour)
	if err = f.store.FinishPromotionEvaluation(ctx, claimed[0].ID, claimed[0].LeaseToken, PromotionEvaluationOutcome{State: PromotionEvaluationWaiting, Reason: "rule_authorization_lost", NextAttemptAt: next, HoldForRule: true}); err != nil {
		t.Fatal(err)
	}
	items := f.evaluations(t)
	if len(items) != 1 || items[0].State != PromotionEvaluationWaiting || items[0].NextAttemptAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("a hold for an active rule must be due now: %+v", items)
	}
	// While the rule is parked the hold stands.
	event := events.Event{Type: events.PromotionFailed, RepositoryID: f.target.ID, Format: "raw", Coordinate: "a", Digest: "sha256:" + uuid.NewString(), Version: "hold"}
	if parked, parkErr := f.store.ParkPromotionRule(ctx, f.rule.ID, f.rule.Version, PromotionRuleAuthorizationLost, "x", event); parkErr != nil || !parked {
		t.Fatalf("parked = %v, %v", parked, parkErr)
	}
	again, err := f.store.ClaimPromotionEvaluations(ctx, "worker", time.Minute, 1)
	if err != nil || len(again) != 1 {
		t.Fatalf("claimed again = %+v, %v", again, err)
	}
	if err = f.store.FinishPromotionEvaluation(ctx, again[0].ID, again[0].LeaseToken, PromotionEvaluationOutcome{State: PromotionEvaluationWaiting, Reason: "rule_authorization_lost", NextAttemptAt: next, HoldForRule: true}); err != nil {
		t.Fatal(err)
	}
	if held := f.evaluations(t); len(held) != 1 || held[0].NextAttemptAt.Before(next.Add(-time.Minute)) {
		t.Fatalf("the hold for a parked rule must stand: %+v", held)
	}
}
