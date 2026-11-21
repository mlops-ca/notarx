package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// promotionEvaluationTimeout bounds one evaluation below its lease, so a stuck read or a large release cannot outlive the claim
// and be evaluated twice; promotionFinishTimeout bounds the write of its outcome, which must outlive a shutdown.
const (
	promotionEvaluationTimeout = promotionEvaluationLease - 30*time.Second
	promotionFinishTimeout     = 15 * time.Second
)

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
	reasonEvaluationPanic   = "evaluation_panic"
	reasonRuleChanged       = "rule_changed"
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
func NewPromotionRuleFanout(caches *readcache.Registry) *promotionfanout.Index {
	return promotionfanout.New(repository.ListEnabledPromotionRulesFrom, caches, promotionCoordinateValidator(promotionFormatRegistry(promotionModuleDeps{})))
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
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.Error("promotion evaluation panicked", "component", "promotion_evaluator", "evaluationId", evaluation.ID, "ruleId", evaluation.RuleID, "attempt", evaluation.Attempts, "panic", fmt.Sprint(recovered))
					e.finish(ctx, evaluation, e.retryOutcome(evaluation, reasonEvaluationPanic))
				}
			}()
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
	evaluateCtx, cancel := context.WithTimeout(ctx, promotionEvaluationTimeout)
	defer cancel()
	outcome, err := e.evaluate(evaluateCtx, evaluation)
	if err != nil {
		if ctx.Err() != nil {
			return // shutdown: the lease expires and the row is taken again
		}
		slog.Warn("promotion evaluation failed", "component", "promotion_evaluator", "evaluationId", evaluation.ID, "ruleId", evaluation.RuleID, "attempt", evaluation.Attempts, "error", err)
		outcome = e.retryOutcome(evaluation, reasonEvaluationError)
	}
	e.finish(ctx, evaluation, outcome)
}

// retryOutcome is what a claimed evaluation becomes when it could not be decided: pending again after a backoff, or
// failed once it used its attempts. The claim also fails rows that used their attempts without an outcome (a worker that
// died on them), so a row that keeps killing its worker does not loop forever.
func (e *PromotionEvaluator) retryOutcome(evaluation repository.PromotionEvaluation, reason string) repository.PromotionEvaluationOutcome {
	if evaluation.Attempts >= repository.PromotionEvaluationMaxAttempts {
		return repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationFailed, Reason: reason}
	}
	return repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationPending, Reason: reason, NextAttemptAt: e.now().Add(repository.PromotionEvaluationBackoff(evaluation.Attempts))}
}

// finish records the outcome under the live lease; a lost lease drops it, because another worker owns the row now.
func (e *PromotionEvaluator) finish(ctx context.Context, evaluation repository.PromotionEvaluation, outcome repository.PromotionEvaluationOutcome) {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), promotionFinishTimeout)
	defer cancel()
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
		// A parked rule comes back with an edit, and the artifacts published meanwhile must not be lost: wait.
		return e.later(evaluation.EventOccurredAt, reasonRuleParkedPrefix+rule.State), nil
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
	run, err := e.prepare(ctx, rule, source, target)
	if errors.Is(err, errInvalidRule) {
		return failed(reasonRuleInvalid)
	}
	if err != nil {
		return repository.PromotionEvaluationOutcome{}, err
	}
	published := evaluation.EventOccurredAt
	evaluation, version, outcome, err := e.reload(ctx, run, evaluation)
	if err != nil || outcome.State != "" {
		return outcome, err
	}
	candidate := run.candidate(evaluation, version)
	if version != nil && !version.SealedAt.IsZero() {
		published = version.SealedAt // a version is published when it is sealed
	}
	matched, outcome, err := e.matches(ctx, run, evaluation, candidate)
	if err != nil || !matched {
		return outcome, err
	}
	members, err := e.members(ctx, run, evaluation)
	if errors.Is(err, repository.ErrNotFound) {
		return skipped(reasonSourceUnavailable)
	}
	if err != nil {
		return repository.PromotionEvaluationOutcome{}, err
	}
	// The conditions hold for every artifact the rule promotes: for scope release, every member of the release.
	var verdicts []promotionrules.ConditionVerdict
	for _, member := range members {
		memberVerdicts, found, verdictErr := run.verdicts(ctx, dryRunCandidate{Candidate: promotionrules.Candidate{Coordinate: member.coordinate}, Digest: member.digest, CreatedAt: published})
		if verdictErr != nil {
			return repository.PromotionEvaluationOutcome{}, verdictErr
		}
		if !found {
			return skipped(reasonSourceUnavailable)
		}
		verdicts = append(verdicts, memberVerdicts...)
	}
	if blocked, outcome := e.conditionOutcome(run, published, verdicts); blocked {
		return outcome, nil
	}
	return e.promote(ctx, run, members, published)
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
	for _, check := range []struct {
		repo      repository.HostedRepository
		operation RepositoryOperation
		reason    string
	}{{source, RepositoryRead, "source_read_denied"}, {target, RepositoryWrite, "target_write_denied"}} {
		decision := e.authorizer.Authorize(ctx, principal, check.repo, check.operation)
		if decision.Reason == errGrantLookupFailed {
			// The grants could not be read; that says nothing about the account's authority, so the evaluation retries.
			return false, "", errors.New("promotion evaluator: repository grants lookup failed")
		}
		if !decision.Allowed {
			return true, check.reason, nil
		}
	}
	return false, "", nil
}

// errGrantLookupFailed is the reason RepositoryAuthorizer reports when the grant set could not be read.
const errGrantLookupFailed = "grant_lookup_failed"

// park moves the rule to authorization_lost and announces it with promotion.failed, in one transaction with the audit
// record. The evaluation fails with the reason; the rule evaluates nothing more until an edit restores it.
func (e *PromotionEvaluator) park(ctx context.Context, rule repository.PromotionRule, source, target repository.HostedRepository, evaluation repository.PromotionEvaluation, reason string) (repository.PromotionEvaluationOutcome, error) {
	payload, _ := json.Marshal(map[string]string{"ruleId": rule.ID, "ruleName": rule.Name, "ruleVersion": rule.Version, "state": repository.PromotionRuleAuthorizationLost, "reason": reason,
		"sourceRepositoryId": source.ID, "targetRepositoryId": target.ID, "format": string(source.Format)})
	event := events.Event{Type: events.PromotionFailed, RepositoryID: target.ID, Format: string(source.Format), Coordinate: evaluation.Coordinate, Digest: evaluation.Digest, Version: "rule:" + rule.ID + ":" + rule.Version, Payload: payload}
	audit := repository.ManagementAudit(serviceAccountPrincipal(rule.RunAsServiceAccountID).Actor, "promotion.rule.authorization_lost", "promotion-rules/"+rule.ID, http.StatusForbidden)
	audit.Repository, audit.GroupName = source.Name, source.Name
	audit.Evidence = map[string]string{"ruleId": rule.ID, "version": rule.Version, "reason": reason}
	parked, err := e.store.ParkPromotionRule(ctx, rule.ID, rule.Version, repository.PromotionRuleAuthorizationLost, reason, event, audit)
	if err != nil {
		return repository.PromotionEvaluationOutcome{}, err
	}
	if !parked {
		// The rule was edited, parked or deleted since this evaluation read it: what it found is stale, so decide again.
		return repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationPending, Reason: reasonRuleChanged}, nil
	}
	return failed(reasonAuthorizationLost + ":" + reason)
}

// ruleRun is a rule loaded for evaluation.
type ruleRun struct {
	dryRun
	rule repository.PromotionRule
}

// candidate is the artifact the rule matches. The coordinate-derived candidate carries what publication knows; a version of
// a versioned Raw source adds its version string and the properties frozen at seal (ADR 0028).
func (r ruleRun) candidate(evaluation repository.PromotionEvaluation, version *repository.RawVersion) promotionrules.Candidate {
	candidate := promotionfanout.CandidateOf(r.source.Format, evaluation.Coordinate)
	if version != nil {
		candidate.Version, candidate.Properties = version.Version, version.Properties
	}
	return candidate
}

// errInvalidRule marks a stored rule that cannot be parsed; the evaluation fails with a reason.
var errInvalidRule = errors.New("stored promotion rule is invalid")

// prepare parses the stored rule once and loads what its conditions read; the dry run's machinery evaluates them.
func (e *PromotionEvaluator) prepare(ctx context.Context, rule repository.PromotionRule, source, target repository.HostedRepository) (ruleRun, error) {
	match, err := promotionrules.ParseMatch(rule.Match)
	if err != nil {
		return ruleRun{}, errInvalidRule
	}
	conditions, err := promotionrules.ParseConditions(rule.Conditions)
	if err != nil {
		return ruleRun{}, errInvalidRule
	}
	minAge, err := conditions.MinAgeDuration()
	if err != nil {
		return ruleRun{}, errInvalidRule
	}
	compiled, err := match.Compile()
	if err != nil {
		return ruleRun{}, errInvalidRule
	}
	run := ruleRun{rule: rule, dryRun: dryRun{ports: e.ports, source: source, target: target, match: compiled, conditions: conditions, minAge: minAge, now: e.now()}}
	if governance, has := formatmodule.Capability[formatmodule.Governance](e.registry, source.Format); has {
		run.validCoordinate = governance.ValidCoordinate
	}
	if tagged, has := formatmodule.Capability[formatmodule.Tagged](e.registry, source.Format); has && compiled.HasRefs() {
		run.tagged = tagged
	}
	if conditions.SecurityPolicy != "" {
		// The target's admission policy as it is now, like the dry run: the authoritative evaluation runs again in the job.
		if run.policy, err = e.ports.securityPolicies.GetRepositorySecurityPolicy(ctx, target.ID); err != nil {
			return ruleRun{}, err
		}
	}
	return run, nil
}

// Timing of conditions that depend on something that has not happened yet.
const (
	// conditionGiveUp is how long after publication an artifact may keep failing a condition that can still change (a
	// scan, evidence, a release from quarantine, the open version of a Raw repository) before the evaluation is dropped.
	conditionGiveUp = 7 * 24 * time.Hour
	// conditionPollFloor and conditionPollCeiling bound the delay before a waiting evaluation looks again: a quarter of
	// the artifact's age, so a fresh artifact is rechecked quickly and an old one rarely.
	conditionPollFloor   = time.Minute
	conditionPollCeiling = time.Hour
	// waitFloor is the least delay of any wait, so a clock that runs behind the database's never turns a wait into a spin.
	waitFloor = 5 * time.Second
)

// later is the outcome of an evaluation that cannot decide yet: it waits and looks again, or, once the artifact is older
// than conditionGiveUp, gives up with the reason. Scan completion and evidence arrive after publication, so a failing
// condition is not final (the re-pend on artifact.scan.completed of a later slice only makes the recheck immediate).
func (e *PromotionEvaluator) later(published time.Time, reason string) repository.PromotionEvaluationOutcome {
	age := e.now().Sub(published)
	if age > conditionGiveUp {
		return repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationSkipped, Reason: reason}
	}
	delay := min(max(age/4, conditionPollFloor), conditionPollCeiling)
	return repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationWaiting, Reason: reason, NextAttemptAt: e.now().Add(delay)}
}

// reload reads the artifact the evaluation names as it is now. A file of a versioned Raw repository is announced by its
// path, but the artifact a rule matches and promotes is its version, name@version with the manifest digest and the
// properties frozen at seal (ADR 0028): the evaluation is rewritten to that and the version is returned, and the
// evaluation waits while the version is still open. Every other format returns the evaluation as it is.
func (e *PromotionEvaluator) reload(ctx context.Context, run ruleRun, evaluation repository.PromotionEvaluation) (repository.PromotionEvaluation, *repository.RawVersion, repository.PromotionEvaluationOutcome, error) {
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
		// An open version is waited for as long as it keeps being written to: the clock is its last write, not the first file.
		active := evaluation.EventOccurredAt
		if version.UpdatedAt.After(active) {
			active = version.UpdatedAt
		}
		return evaluation, nil, e.later(active, reasonVersionNotSealed), nil
	}
	evaluation.Coordinate, evaluation.Digest = version.Coordinate(), version.ManifestDigest
	return evaluation, &version, repository.PromotionEvaluationOutcome{}, nil
}

// matches re-checks the match against the artifact as it is now: the refs that point at it and, for a versioned Raw
// source, the properties frozen at seal. Neither is known when the event is written, so the fan-out leaves them to this
// check.
func (e *PromotionEvaluator) matches(ctx context.Context, run ruleRun, evaluation repository.PromotionEvaluation, candidate promotionrules.Candidate) (bool, repository.PromotionEvaluationOutcome, error) {
	if run.tagged != nil {
		tags, err := run.tagsOf(ctx, dryRunCandidate{Candidate: candidate, Digest: evaluation.Digest})
		if err != nil {
			return false, repository.PromotionEvaluationOutcome{}, err
		}
		candidate.Refs = tags
	}
	if !run.match.Matches(candidate, run.validCoordinate) {
		outcome, _ := skipped(reasonNoMatch)
		return false, outcome, nil
	}
	return true, repository.PromotionEvaluationOutcome{}, nil
}

// conditionOutcome turns failed condition verdicts into an outcome. A rule whose only failing condition is the age waits
// until the artifact is old enough; any other failing condition (a scan, findings above the limit, evidence, the target's
// policy, a quarantine) waits and looks again, because a rescan, a refreshed database or a release can still change it. A
// not-evaluated verdict never blocks.
func (e *PromotionEvaluator) conditionOutcome(run ruleRun, published time.Time, verdicts []promotionrules.ConditionVerdict) (bool, repository.PromotionEvaluationOutcome) {
	var first *promotionrules.ConditionVerdict
	waitsForAge := false
	for i, verdict := range verdicts {
		if verdict.Verdict != promotionrules.VerdictFail {
			continue
		}
		switch {
		case verdict.Condition == "minAge":
			waitsForAge = true
		case first == nil:
			first = &verdicts[i]
		}
	}
	switch {
	case first != nil:
		return true, e.later(published, reasonConditionPrefix+first.Condition+":"+first.Reason)
	case waitsForAge:
		deadline := published.Add(run.minAge)
		if floor := e.now().Add(waitFloor); deadline.Before(floor) {
			deadline = floor // a clock behind the database's must not turn the wait into a spin
		}
		return true, repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationWaiting, Reason: reasonConditionPrefix + "minAge:younger_than_min_age", NextAttemptAt: deadline}
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
// (all-or-nothing, like a batch), each under its own idempotency key, against a target resolved once. A member the target
// already holds is a success no-op; different bytes at its coordinate are a conflict; when every member already has a live
// attempt under its key the evaluation is a replay and asks for nothing, which is what the second file event of a release
// does.
func (e *PromotionEvaluator) promote(ctx context.Context, run ruleRun, members []promotionMember, published time.Time) (repository.PromotionEvaluationOutcome, error) {
	version, _ := strconv.ParseInt(run.rule.Version, 10, 64)
	principal := serviceAccountPrincipal(run.rule.RunAsServiceAccountID)
	requests := make([]PromotionRequest, 0, len(members))
	keys := make([]string, 0, len(members))
	for _, member := range members {
		generation, holds, conflict, stateErr := e.targetState(ctx, run.target, run.source.Format, member.coordinate, member.digest)
		if stateErr != nil {
			return repository.PromotionEvaluationOutcome{}, stateErr
		}
		if conflict != "" {
			slog.Warn("promotion refused: the target holds different bytes at the coordinate", "component", "promotion_evaluator", "ruleId", run.rule.ID, "target", run.target.Name, "coordinate", member.coordinate, "held", conflict, "offered", member.digest)
			return failed(reasonConflict + ":" + member.coordinate + " holds " + conflict)
		}
		if holds {
			continue
		}
		key := RulePromotionKey(run.rule.ID, run.target.ID, member.coordinate, member.digest, generation)
		keys = append(keys, key)
		requests = append(requests, PromotionRequest{Principal: principal, Source: run.source, TargetID: run.target.ID, Coordinate: member.coordinate, Digest: member.digest,
			IdempotencyKey: key, Rule: &PromotionRuleOrigin{RuleID: run.rule.ID, RuleVersion: version, Key: key}})
	}
	if len(requests) == 0 {
		return skipped(reasonAlreadyAtTarget)
	}
	if id, replayed, replayErr := e.replay(ctx, keys); replayErr != nil {
		return repository.PromotionEvaluationOutcome{}, replayErr
	} else if replayed {
		return repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationQueued, PromotionID: id}, nil
	}
	resolved := e.service.resolveTarget(ctx, run.source, run.target.ID)
	for _, request := range requests {
		if _, perr := e.service.admit(ctx, request, resolved); perr != nil {
			return e.promotionRefusal(perr, published)
		}
	}
	promotionID := ""
	for _, request := range requests {
		request.admitted = true
		result, perr := e.service.request(ctx, request, resolved)
		if perr != nil {
			return e.promotionRefusal(perr, published)
		}
		if promotionID == "" {
			promotionID = result.Promotion.ID
		}
	}
	return repository.PromotionEvaluationOutcome{State: repository.PromotionEvaluationQueued, PromotionID: promotionID}, nil
}

// replay reports whether every key already has a live attempt (queued, running, pending approval or completed), with the
// id of the first one, from one read.
func (e *PromotionEvaluator) replay(ctx context.Context, keys []string) (string, bool, error) {
	rows, err := e.service.promotions.GetArtifactPromotionsByKeys(ctx, keys)
	if err != nil {
		return "", false, err
	}
	first := ""
	for _, key := range keys {
		row, found := rows[key]
		if !found {
			return "", false, nil
		}
		switch row.State {
		case repository.ArtifactPromotionFailed, repository.ArtifactPromotionRejected, repository.ArtifactPromotionExpired, repository.ArtifactPromotionCancelled:
			return "", false, nil
		}
		if first == "" {
			first = row.ID
		}
	}
	return first, true, nil
}

// promotionRefusal maps a refusal of the PromotionService to an outcome: a server error is transient, a vanished source is
// a skip, a refusal by quarantine or the target's admission policy waits like a failed condition (a release or a new
// scan can lift it; a rule never bypasses either), and anything else is a failure.
func (e *PromotionEvaluator) promotionRefusal(perr *PromotionError, published time.Time) (repository.PromotionEvaluationOutcome, error) {
	switch {
	case perr.Status >= http.StatusInternalServerError:
		return repository.PromotionEvaluationOutcome{}, perr
	case perr.Status == http.StatusNotFound:
		return skipped(reasonSourceUnavailable)
	case perr.Status == http.StatusForbidden:
		return e.later(published, perr.Code), nil
	}
	return failed(perr.Code)
}

// targetState reports what the target holds at a coordinate, from the newest completed promotion into it that is newer than
// the target's tombstone: the generation (the tombstone time of the coordinate, empty if never tombstoned), whether it
// already holds this digest, and, when it holds different bytes, which digest.
func (e *PromotionEvaluator) targetState(ctx context.Context, target repository.HostedRepository, format repository.Format, coordinate, digest string) (generation string, holds bool, conflict string, err error) {
	var tombstoned time.Time
	tombstone, tombstoneErr := e.store.GetArtifactTombstone(ctx, target.ID, format, coordinate)
	switch {
	case tombstoneErr == nil:
		tombstoned = tombstone.TombstonedAt
		generation = tombstoned.UTC().Format(time.RFC3339Nano)
	case !errors.Is(tombstoneErr, repository.ErrNotFound):
		return "", false, "", tombstoneErr
	}
	page, err := e.service.promotions.PageArtifactPromotions(ctx, repository.ArtifactPromotionQuery{RepositoryIDs: []string{target.ID}, TargetRepositoryID: target.ID, Format: format, State: repository.ArtifactPromotionCompleted, Coordinate: coordinate, Limit: 1})
	if err != nil {
		return "", false, "", err
	}
	for _, row := range page.Items {
		if row.Coordinate != coordinate || row.CompletedAt.Before(tombstoned) {
			continue
		}
		if row.Digest == digest {
			return generation, true, "", nil
		}
		return generation, false, row.Digest, nil
	}
	return generation, false, "", nil
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
