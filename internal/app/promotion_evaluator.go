package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mlops-ca/notarx/internal/background"
	"github.com/mlops-ca/notarx/internal/events"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/promotionfanout"
	"github.com/mlops-ca/notarx/internal/promotionrules"
	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

// promotionEvaluationLease is how long a claimed evaluation belongs to its worker. An evaluation is a handful of reads
// and one enqueue, so the lease is generous; a worker that dies leaves the row to become due again when it ends.
const promotionEvaluationLease = 2 * time.Minute

// Reasons recorded on an evaluation (last_reason). They are stable codes, not worker error text.
const (
	reasonRuleDeleted       = "rule_deleted"
	reasonRuleDisabled      = "rule_disabled"
	reasonRuleParkedPrefix  = "rule_"
	reasonSourceUnavailable = "source_unavailable"
	reasonTargetUnavailable = "target_unavailable"
	reasonRemoteUnsupported = "remote_target_not_supported"
	reasonRuleInvalid       = "rule_invalid"
	reasonNoMatch           = "no_match"
	reasonAlreadyAtTarget   = "already_at_target"
	reasonConflict          = "conflict"
	reasonAuthorizationLost = "authorization_lost"
	reasonConditionPrefix   = "condition_failed:"
	reasonEvaluationError   = "evaluation_error"
	reasonVersionNotSealed  = "version_not_sealed"
)

// PromotionEvaluatorStore is the gateway store plus the evaluation queue. The queue is not part of GatewayStore: only the
// evaluator and the fan-out reach it.
type PromotionEvaluatorStore interface {
	GatewayStore
	repository.PromotionEvaluationStore
}

// PromotionEvaluatorOptions configure the evaluator.
type PromotionEvaluatorOptions struct {
	Store PromotionEvaluatorStore
	// Authorizer re-authorizes the run-as service account on the source (read) and the target (write) at every evaluation.
	Authorizer  RepositoryAuthorizer
	Owner       string
	Interval    time.Duration
	Concurrency int
	// Now is the clock of the age condition; nil means time.Now.
	Now func() time.Time
}

// PromotionEvaluator runs the rule evaluations that events.Emit fanned out (ADR 0021, Evaluator): it claims due rows,
// reloads the artifact, re-checks the rule's match, re-authorizes the run-as service account, evaluates the conditions
// and either records why it skipped, waits, or asks the PromotionService for the promotion. Delivery is at least once;
// the idempotency key of a promotion makes the promotion effectively once.
type PromotionEvaluator struct {
	store       PromotionEvaluatorStore
	service     PromotionService
	authorizer  RepositoryAuthorizer
	registry    *formatmodule.Registry
	ports       promotionDryRunPorts
	owner       string
	interval    time.Duration
	concurrency int
	now         func() time.Time
}

// NewPromotionEvaluator builds the evaluator on the gateway store. The promotion service and the condition ports are the
// ones the management API uses, so a rule promotion takes exactly the path of a manual one.
func NewPromotionEvaluator(options PromotionEvaluatorOptions) *PromotionEvaluator {
	store := options.Store
	adapter := generatedRepositoryAPIAdapter{store: store, sessions: nativeMavenHandler{store: store}, securityPolicies: store, intelligence: store, audit: store, quarantine: store, cargo: store}
	evaluator := &PromotionEvaluator{
		store: store, service: newPromotionService(adapter), authorizer: options.Authorizer, registry: promotionFormatRegistry(promotionModuleDeps{Store: store}),
		ports: promotionDryRunPorts{securityPolicies: store, intelligence: store, quarantine: store, distributionDigests: adapter.artifactDistributionDigests},
		owner: options.Owner, interval: options.Interval, concurrency: options.Concurrency, now: options.Now,
	}
	if evaluator.owner == "" {
		evaluator.owner = "promotion-evaluator"
	}
	if evaluator.interval <= 0 {
		evaluator.interval = 5 * time.Second
	}
	if evaluator.concurrency <= 0 {
		evaluator.concurrency = 4
	}
	if evaluator.now == nil {
		evaluator.now = time.Now
	}
	evaluator.ports.now = evaluator.now
	return evaluator
}

// NewPromotionRuleFanout is the rule fan-out events.Emit consults in the publishing transaction: the cached enabled rules
// of the source repository, matched on the coordinate and version. Install it with events.SetRuleFanout.
func NewPromotionRuleFanout(store repository.PromotionEvaluationStore, caches *readcache.Registry) *promotionfanout.Index {
	return promotionfanout.New(store, caches, promotionCoordinateValidator(promotionFormatRegistry(promotionModuleDeps{})))
}

// PromotionRuleMatcher is the in-memory store's rule fan-out; it has no cache.
func PromotionRuleMatcher() repository.PromotionRuleMatcher {
	return promotionfanout.Matcher{Validate: promotionCoordinateValidator(promotionFormatRegistry(promotionModuleDeps{}))}
}

func promotionCoordinateValidator(registry *formatmodule.Registry) promotionfanout.CoordinateValidator {
	return func(format repository.Format) func(string) bool {
		if governance, ok := formatmodule.Capability[formatmodule.Governance](registry, format); ok {
			return governance.ValidCoordinate
		}
		return nil
	}
}

// RunOnce claims up to the configured concurrency of due evaluations, evaluates them concurrently and records each
// outcome. It returns how many it handled.
func (e *PromotionEvaluator) RunOnce(ctx context.Context) (int, error) {
	claimed, err := e.store.ClaimPromotionEvaluations(readcache.Bypass(ctx), e.owner, promotionEvaluationLease, e.concurrency)
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for _, evaluation := range claimed {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.handle(ctx, evaluation)
		}()
	}
	wg.Wait()
	return len(claimed), nil
}

// handle evaluates one claimed row and records the outcome. A transient error leaves the row pending with a backoff
// until its attempts are exhausted; a lost lease drops the outcome, because another worker owns the row now.
func (e *PromotionEvaluator) handle(ctx context.Context, evaluation repository.PromotionEvaluation) {
	ctx = readcache.Bypass(ctx)
	outcome, err := e.evaluate(ctx, evaluation)
	if err != nil {
		if ctx.Err() != nil {
			return // shutdown: the lease expires and the row is taken again
		}
		slog.Warn("promotion evaluation failed", "component", "promotion_evaluator", "evaluationId", evaluation.ID, "ruleId", evaluation.RuleID, "attempt", evaluation.Attempts, "error", err)
		outcome = repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationPending, Reason: reasonEvaluationError, NextAttemptAt: e.now().Add(repository.PromotionEvaluationBackoff(evaluation.Attempts))}
		if evaluation.Attempts >= repository.PromotionEvaluationMaxAttempts {
			outcome = repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationFailed, Reason: reasonEvaluationError}
		}
	}
	finishCtx := context.WithoutCancel(ctx)
	if finishErr := e.store.FinishPromotionEvaluation(finishCtx, evaluation.ID, evaluation.LeaseToken, outcome); finishErr != nil && !errors.Is(finishErr, repository.ErrPromotionEvaluationLeaseLost) {
		slog.Warn("record promotion evaluation outcome failed", "component", "promotion_evaluator", "evaluationId", evaluation.ID, "error", finishErr)
	}
}

func skipped(reason string) (repository.PromotionEvaluationOutcome, error) {
	return repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationSkipped, Reason: reason}, nil
}

func failed(reason string) (repository.PromotionEvaluationOutcome, error) {
	return repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationFailed, Reason: reason}, nil
}

// evaluate decides one evaluation. A non-nil error is transient (a store read failed); every decision about the rule or
// the artifact is an outcome.
func (e *PromotionEvaluator) evaluate(ctx context.Context, evaluation repository.PromotionEvaluation) (repository.PromotionEvaluationOutcome, error) {
	rule, err := e.store.GetPromotionRule(ctx, evaluation.RuleID)
	if errors.Is(err, repository.ErrNotFound) {
		return skipped(reasonRuleDeleted)
	}
	if err != nil {
		return repository.PromotionEvaluationOutcome{}, err
	}
	if !rule.Enabled {
		return skipped(reasonRuleDisabled)
	}
	if rule.State != repository.PromotionRuleActive {
		return skipped(reasonRuleParkedPrefix + rule.State)
	}
	if rule.TargetKind != repository.PromotionTargetLocal {
		return skipped(reasonRemoteUnsupported)
	}
	source, found, err := e.repository(ctx, rule.SourceRepositoryID)
	if err != nil || !found {
		return skippedOrError(err, reasonSourceUnavailable)
	}
	target, found, err := e.repository(ctx, rule.TargetRepositoryID)
	if err != nil || !found {
		return skippedOrError(err, reasonTargetUnavailable)
	}
	if lost, reason, authErr := e.authorizationLost(ctx, rule, source, target); authErr != nil {
		return repository.PromotionEvaluationOutcome{}, authErr
	} else if lost {
		return e.park(ctx, rule, source, target, evaluation, reason)
	}
	run, err := e.prepare(rule, source, target)
	if err != nil {
		return failed(reasonRuleInvalid)
	}
	evaluation, properties, outcome, err := e.reload(ctx, run, evaluation)
	if err != nil || outcome.State != "" {
		return outcome, err
	}
	matched, outcome, err := e.matches(ctx, run, evaluation, properties)
	if err != nil || !matched {
		return outcome, err
	}
	verdicts, found, err := run.verdicts(ctx, dryRunCandidate{Candidate: run.candidate(evaluation), Digest: evaluation.Digest, CreatedAt: evaluation.EventOccurredAt})
	if err != nil {
		return repository.PromotionEvaluationOutcome{}, err
	}
	if !found {
		return skipped(reasonSourceUnavailable)
	}
	if blocked, outcome := conditionOutcome(run, evaluation, verdicts); blocked {
		return outcome, nil
	}
	return e.promote(ctx, run, evaluation)
}

func skippedOrError(err error, reason string) (repository.PromotionEvaluationOutcome, error) {
	if err != nil {
		return repository.PromotionEvaluationOutcome{}, err
	}
	return skipped(reason)
}

// repository loads an active hosted repository; found is false for one that does not exist or is not active.
func (e *PromotionEvaluator) repository(ctx context.Context, id string) (repository.HostedRepository, bool, error) {
	repo, err := e.store.GetHostedRepository(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return repository.HostedRepository{}, false, nil
	}
	if err != nil {
		return repository.HostedRepository{}, false, err
	}
	return repo, repo.State == repository.RepositoryActive, nil
}

// authorizationLost re-authorizes the rule's run-as service account at this evaluation: it must still exist and be
// active, may read the source and may write the target. A refusal parks the rule.
func (e *PromotionEvaluator) authorizationLost(ctx context.Context, rule repository.PromotionRule, source, target repository.HostedRepository) (bool, string, error) {
	account, err := e.store.GetServiceAccount(ctx, rule.RunAsServiceAccountID)
	if errors.Is(err, repository.ErrNotFound) {
		return true, "service_account_deleted", nil
	}
	if err != nil {
		return false, "", err
	}
	if account.State != repository.ServiceAccountActive {
		return true, "service_account_disabled", nil
	}
	principal := serviceAccountPrincipal(rule.RunAsServiceAccountID)
	if !e.authorizer.Authorize(ctx, principal, source, RepositoryRead).Allowed {
		return true, "source_read_denied", nil
	}
	if !e.authorizer.Authorize(ctx, principal, target, RepositoryWrite).Allowed {
		return true, "target_write_denied", nil
	}
	return false, "", nil
}

// park moves the rule to authorization_lost and announces it with promotion.failed, in one transaction with the audit
// record. The evaluation fails with the reason; the rule evaluates nothing more until an edit restores it.
func (e *PromotionEvaluator) park(ctx context.Context, rule repository.PromotionRule, source, target repository.HostedRepository, evaluation repository.PromotionEvaluation, reason string) (repository.PromotionEvaluationOutcome, error) {
	payload, _ := json.Marshal(map[string]string{"ruleId": rule.ID, "ruleName": rule.Name, "ruleVersion": rule.Version, "state": repository.PromotionRuleAuthorizationLost, "reason": reason,
		"sourceRepositoryId": source.ID, "targetRepositoryId": target.ID, "format": string(source.Format)})
	event := events.Event{Type: events.PromotionFailed, RepositoryID: target.ID, Format: string(source.Format), Coordinate: evaluation.Coordinate, Digest: evaluation.Digest, Version: "rule:" + rule.ID + ":" + rule.Version, Payload: payload}
	audit := repository.ManagementAudit(serviceAccountPrincipal(rule.RunAsServiceAccountID).Actor, "promotion.rule.authorization_lost", "promotion-rules/"+rule.ID, http.StatusForbidden)
	audit.Repository, audit.GroupName = source.Name, source.Name
	audit.Evidence = map[string]string{"ruleId": rule.ID, "version": rule.Version, "reason": reason}
	if _, err := e.store.ParkPromotionRule(ctx, rule.ID, repository.PromotionRuleAuthorizationLost, reason, event, audit); err != nil {
		return repository.PromotionEvaluationOutcome{}, err
	}
	return failed(reasonAuthorizationLost + ":" + reason)
}

// ruleRun is a rule loaded for evaluation.
type ruleRun struct {
	dryRun
	rule repository.PromotionRule
}

func (r ruleRun) candidate(evaluation repository.PromotionEvaluation) promotionrules.Candidate {
	return promotionfanout.CandidateOf(r.source.Format, evaluation.Coordinate)
}

// prepare parses the stored rule once; the dry run's machinery evaluates the conditions.
func (e *PromotionEvaluator) prepare(rule repository.PromotionRule, source, target repository.HostedRepository) (ruleRun, error) {
	match, err := promotionrules.ParseMatch(rule.Match)
	if err != nil {
		return ruleRun{}, err
	}
	conditions, err := promotionrules.ParseConditions(rule.Conditions)
	if err != nil {
		return ruleRun{}, err
	}
	minAge, err := conditions.MinAgeDuration()
	if err != nil {
		return ruleRun{}, err
	}
	compiled, err := match.Compile()
	if err != nil {
		return ruleRun{}, err
	}
	run := ruleRun{rule: rule, dryRun: dryRun{ports: e.ports, source: source, target: target, match: compiled, conditions: conditions, minAge: minAge, now: e.now()}}
	if governance, has := formatmodule.Capability[formatmodule.Governance](e.registry, source.Format); has {
		run.validCoordinate = governance.ValidCoordinate
	}
	if tagged, has := formatmodule.Capability[formatmodule.Tagged](e.registry, source.Format); has && compiled.HasRefs() {
		run.tagged = tagged
	}
	return run, nil
}

// matches re-checks the match against the artifact as it is now: the refs that point at it and, for a versioned Raw
// source, the properties frozen at seal. Neither is known when the event is written, so the fan-out leaves them to this
// check.
func (e *PromotionEvaluator) matches(ctx context.Context, run ruleRun, evaluation repository.PromotionEvaluation, properties map[string]string) (bool, repository.PromotionEvaluationOutcome, error) {
	candidate := run.candidate(evaluation)
	if run.tagged != nil {
		tags, err := run.tagsOf(ctx, dryRunCandidate{Candidate: candidate, Digest: evaluation.Digest})
		if err != nil {
			return false, repository.PromotionEvaluationOutcome{}, err
		}
		candidate.Refs = tags
	}
	candidate.Properties = properties
	if !run.match.Matches(candidate, run.validCoordinate) {
		outcome, _ := skipped(reasonNoMatch)
		return false, outcome, nil
	}
	return true, repository.PromotionEvaluationOutcome{}, nil
}

// versionedRawWait is how long an evaluation of a file of an open version waits before it looks at the version again, and
// versionedRawGiveUp how long after the file was published a version may stay open before the evaluation is dropped.
const (
	versionedRawWait   = 5 * time.Minute
	versionedRawGiveUp = 7 * 24 * time.Hour
)

// reload reads the artifact the evaluation names as it is now. A file of a versioned Raw repository is announced by its
// path, but the artifact a rule matches and promotes is its version, name@version with the manifest digest and the
// properties frozen at seal (ADR 0028): the evaluation is rewritten to that, and waits while the version is still open.
// Every other format returns the evaluation as it is.
func (e *PromotionEvaluator) reload(ctx context.Context, run ruleRun, evaluation repository.PromotionEvaluation) (repository.PromotionEvaluation, map[string]string, repository.PromotionEvaluationOutcome, error) {
	if run.source.Format != repository.FormatRaw || run.source.RawLayout != repository.RawLayoutVersioned {
		return evaluation, nil, repository.PromotionEvaluationOutcome{}, nil
	}
	versions, ok := any(e.store).(repository.NativeRawVersionStore)
	parsed, parseErr := repository.ParseRawVersionPath(evaluation.Coordinate)
	if !ok || parseErr != nil {
		outcome, _ := skipped(reasonNoMatch)
		return evaluation, nil, outcome, nil
	}
	version, err := versions.GetRawVersion(ctx, run.source.ID, parsed.Name, parsed.Version)
	if errors.Is(err, repository.ErrNotFound) {
		outcome, _ := skipped(reasonSourceUnavailable)
		return evaluation, nil, outcome, nil
	}
	if err != nil {
		return evaluation, nil, repository.PromotionEvaluationOutcome{}, err
	}
	if version.State != repository.RawVersionSealed {
		if e.now().Sub(evaluation.EventOccurredAt) > versionedRawGiveUp {
			outcome, _ := skipped(reasonVersionNotSealed)
			return evaluation, nil, outcome, nil
		}
		return evaluation, nil, repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationWaiting, Reason: reasonVersionNotSealed, NextAttemptAt: e.now().Add(versionedRawWait)}, nil
	}
	evaluation.Coordinate, evaluation.Digest = version.Coordinate(), version.ManifestDigest
	return evaluation, version.Properties, repository.PromotionEvaluationOutcome{}, nil
}

// conditionOutcome turns failed condition verdicts into an outcome. A rule whose only failing condition is the age waits
// until the artifact is old enough; any other failing condition skips the artifact with the first failing condition and
// its reason. A not-evaluated verdict never blocks.
func conditionOutcome(run ruleRun, evaluation repository.PromotionEvaluation, verdicts []promotionrules.ConditionVerdict) (bool, repository.PromotionEvaluationOutcome) {
	var other *promotionrules.ConditionVerdict
	waitsForAge := false
	for i, verdict := range verdicts {
		if verdict.Verdict != promotionrules.VerdictFail {
			continue
		}
		if verdict.Condition == "minAge" {
			waitsForAge = true
			continue
		}
		if other == nil {
			other = &verdicts[i]
		}
	}
	switch {
	case other != nil:
		return true, repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationSkipped, Reason: reasonConditionPrefix + other.Condition + ":" + other.Reason}
	case waitsForAge:
		return true, repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationWaiting, Reason: reasonConditionPrefix + "minAge:younger_than_min_age", NextAttemptAt: evaluation.EventOccurredAt.Add(run.minAge)}
	}
	return false, repository.PromotionEvaluationOutcome{}
}

// promotionMember is one artifact a rule promotes for an evaluation: the matched artifact, or every member of its release.
type promotionMember struct{ coordinate, digest string }

// members expands the evaluation to the artifacts to promote: the matched artifact itself, or for scope release the
// members the format's ReleaseSet returns for it.
func (e *PromotionEvaluator) members(ctx context.Context, run ruleRun, evaluation repository.PromotionEvaluation) ([]promotionMember, error) {
	if run.match.Scope() != promotionrules.ScopeRelease {
		return []promotionMember{{evaluation.Coordinate, evaluation.Digest}}, nil
	}
	releaseSet, ok := formatmodule.Capability[formatmodule.ReleaseSet](e.registry, run.source.Format)
	if !ok {
		return nil, errUnsupportedPromotionFormat
	}
	released, err := releaseSet.ReleaseMembers(ctx, run.source.ID, evaluation.Coordinate)
	if err != nil {
		return nil, err
	}
	members := make([]promotionMember, 0, len(released))
	for _, member := range released {
		members = append(members, promotionMember{member.Coordinate, member.Digest})
	}
	return members, nil
}

// promote asks the PromotionService for the promotion of every member: all of them are admitted before any is queued
// (all-or-nothing, like a batch), each under its own idempotency key. A member the target already holds is a success
// no-op; different bytes at its coordinate are a conflict.
func (e *PromotionEvaluator) promote(ctx context.Context, run ruleRun, evaluation repository.PromotionEvaluation) (repository.PromotionEvaluationOutcome, error) {
	members, err := e.members(ctx, run, evaluation)
	if errors.Is(err, repository.ErrNotFound) {
		return skipped(reasonSourceUnavailable)
	}
	if err != nil {
		return repository.PromotionEvaluationOutcome{}, err
	}
	version, _ := strconv.ParseInt(run.rule.Version, 10, 64)
	principal := serviceAccountPrincipal(run.rule.RunAsServiceAccountID)
	requests := make([]PromotionRequest, 0, len(members))
	for _, member := range members {
		generation, state, conflict, stateErr := e.targetState(ctx, run.target, run.source.Format, member.coordinate, member.digest)
		if stateErr != nil {
			return repository.PromotionEvaluationOutcome{}, stateErr
		}
		if conflict != "" {
			return failed(reasonConflict + ":" + member.coordinate + " holds " + conflict)
		}
		if state == targetHolds {
			continue
		}
		key := RulePromotionKey(run.rule.ID, run.target.ID, member.coordinate, member.digest, generation)
		requests = append(requests, PromotionRequest{Principal: principal, Source: run.source, TargetID: run.target.ID, Coordinate: member.coordinate, Digest: member.digest,
			IdempotencyKey: key, Rule: &PromotionRuleOrigin{RuleID: run.rule.ID, RuleVersion: version, Key: key}})
	}
	if len(requests) == 0 {
		return skipped(reasonAlreadyAtTarget)
	}
	for _, request := range requests {
		if perr := e.service.Admit(ctx, request); perr != nil {
			return promotionRefusal(perr)
		}
	}
	promotionID := ""
	for _, request := range requests {
		result, perr := e.service.Request(ctx, request)
		if perr != nil {
			return promotionRefusal(perr)
		}
		if promotionID == "" {
			promotionID = result.Promotion.ID
		}
	}
	return repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationQueued, PromotionID: promotionID}, nil
}

// promotionRefusal maps a refusal of the PromotionService to an outcome: a server error is transient, a vanished source is
// a skip, a refusal by quarantine or the target's admission policy is a skip with its code (a rule never bypasses
// either), and anything else is a failure.
func promotionRefusal(perr *PromotionError) (repository.PromotionEvaluationOutcome, error) {
	switch {
	case perr.Status >= http.StatusInternalServerError:
		return repository.PromotionEvaluationOutcome{}, perr
	case perr.Status == http.StatusNotFound:
		return skipped(reasonSourceUnavailable)
	case perr.Status == http.StatusForbidden:
		return skipped(perr.Code)
	}
	return failed(perr.Code)
}

type targetPresence int

const (
	targetEmpty targetPresence = iota
	targetHolds
)

// targetState reports what the target holds at a coordinate, from the provenance of completed promotions into it: the
// generation (the tombstone time of the coordinate, empty if never tombstoned), whether it already holds this digest,
// and, when it holds different bytes, which digest. A promotion completed before the tombstone no longer counts.
func (e *PromotionEvaluator) targetState(ctx context.Context, target repository.HostedRepository, format repository.Format, coordinate, digest string) (generation string, presence targetPresence, conflict string, err error) {
	var tombstoned time.Time
	tombstone, tombstoneErr := e.store.GetArtifactTombstone(ctx, target.ID, format, coordinate)
	switch {
	case tombstoneErr == nil:
		tombstoned = tombstone.TombstonedAt
		generation = tombstoned.UTC().Format(time.RFC3339Nano)
	case !errors.Is(tombstoneErr, repository.ErrNotFound):
		return "", targetEmpty, "", tombstoneErr
	}
	page, err := e.service.promotions.PageArtifactPromotions(ctx, repository.ArtifactPromotionQuery{RepositoryIDs: []string{target.ID}, TargetRepositoryID: target.ID, Format: format, State: repository.ArtifactPromotionCompleted, Coordinate: coordinate, Limit: 50})
	if err != nil {
		return "", targetEmpty, "", err
	}
	for _, row := range page.Items {
		if row.Coordinate != coordinate || row.CompletedAt.Before(tombstoned) {
			continue
		}
		if row.Digest == digest {
			return generation, targetHolds, "", nil
		}
		conflict = row.Digest
	}
	return generation, targetEmpty, conflict, nil
}

// Start runs the evaluator until ctx ends: a loop that drains the due evaluations, then sleeps until the next interval or
// a wake-up from the evaluation channel.
func (e *PromotionEvaluator) Start(ctx context.Context) {
	background.Go(ctx, func() {
		wake := notificationWake(ctx, e.store, repository.PromotionEvaluationChannel)
		ticker := time.NewTicker(e.interval)
		defer ticker.Stop()
		for {
			handled, err := e.RunOnce(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Warn("promotion evaluator iteration failed", "component", "promotion_evaluator", "error", err)
			}
			if err == nil && handled > 0 {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case _, ok := <-wake:
				if !ok {
					wake = nil
				}
			}
		}
	})
}
