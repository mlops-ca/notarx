package repository

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/events"
)

// CacheScopePromotionRule is the enabled promotion rules of a source repository, which events.Emit reads through an
// in-process cache to fan a publication out to rule evaluations (ADR 0021, ADR 0023 section 6). Every change to a rule
// announces the whole scope (the statement triggers of migration 000026).
const CacheScopePromotionRule = "promotion_rule"

// PromotionEvaluationChannel wakes the promotion evaluator when an evaluation is inserted. The quota alert evaluator
// owns notarx_evaluations (ADR 0023 section 7 names one channel per kind), so this one carries the kind in its name.
const PromotionEvaluationChannel = "notarx_promotion_evaluations"

// Promotion rule states (ADR 0021, Rules): only an active, enabled rule evaluates publications.
const (
	PromotionRuleActive            = "active"
	PromotionRuleAuthorizationLost = "authorization_lost"
	PromotionRulePaused            = "paused"
)

// PromotionEvaluationState is the state of one evaluation of a rule against one published artifact.
type PromotionEvaluationState string

const (
	// PromotionEvaluationPending rows are due at NextAttemptAt and are claimed by the evaluator.
	PromotionEvaluationPending PromotionEvaluationState = "pending"
	// PromotionEvaluationWaiting rows wait for a condition (an age, a scan) and are due again at NextAttemptAt.
	PromotionEvaluationWaiting PromotionEvaluationState = "waiting"
	// PromotionEvaluationQueued means the promotion was requested; its progress is the provenance row's.
	PromotionEvaluationQueued PromotionEvaluationState = "queued"
	// PromotionEvaluationSkipped means the artifact does not match, a condition failed or the target already has it.
	PromotionEvaluationSkipped PromotionEvaluationState = "skipped"
	// PromotionEvaluationFailed means the evaluation could not complete or the promotion was refused for good.
	PromotionEvaluationFailed PromotionEvaluationState = "failed"
)

// Terminal reports whether the state is final: the RetentionJanitor prunes such rows.
func (s PromotionEvaluationState) Terminal() bool {
	return s == PromotionEvaluationQueued || s == PromotionEvaluationSkipped || s == PromotionEvaluationFailed
}

// promotionReasonMax is the bound of a recorded reason, in characters: the length of last_reason and state_reason.
const promotionReasonMax = 1024

// PromotionEvaluationMaxAttempts is how many claims an evaluation gets before a transient failure becomes final. A claim
// counts every time the row is taken, so an evaluator that keeps dying on one row also exhausts it: the claim fails such a
// row with PromotionEvaluationExhausted.
const PromotionEvaluationMaxAttempts = 8

// PromotionEvaluation is one row of promotion_evaluations. It names its event by (EventSeq, EventOccurredAt): the event
// log is partitioned and expires, so the row holds everything the evaluator needs (rule, repository, coordinate, digest).
type PromotionEvaluation struct {
	ID              string
	EventSeq        int64
	EventOccurredAt time.Time
	RuleID          string
	RepositoryID    string
	Coordinate      string
	Digest          string
	State           PromotionEvaluationState
	NextAttemptAt   time.Time
	Attempts        int
	LeaseOwner      string
	LeaseToken      string
	LeaseExpiresAt  time.Time
	LastReason      string
	PromotionID     string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// PromotionEvaluationOutcome is what an evaluator decided for a claimed row.
type PromotionEvaluationOutcome struct {
	State  PromotionEvaluationState
	Reason string
	// NextAttemptAt is the due time of a pending or waiting outcome; zero means now.
	NextAttemptAt time.Time
	// PromotionID is the provenance row of a queued outcome.
	PromotionID string
	// NotAnAttempt returns the attempt the claim counted: the evaluator decided nothing (the rule changed under it) and
	// asks to be looked at again, which must not use up the row's attempts.
	NotAnAttempt bool
}

// PromotionEvaluationQuery lists the evaluations of one rule, newest first.
type PromotionEvaluationQuery struct {
	RuleID string
	State  PromotionEvaluationState
	Limit  int
}

// ErrPromotionEvaluationLeaseLost reports that the claim of an evaluation expired or moved to another worker, so the
// outcome was not recorded.
var ErrPromotionEvaluationLeaseLost = errors.New("promotion evaluation lease lost")

// PromotionRuleMatcher decides, from the rules of a source repository, which of them evaluate a published artifact. The
// in-memory store calls it while it holds its lock; the PostgreSQL path goes through events.RuleFanout instead.
type PromotionRuleMatcher interface {
	Match(ctx context.Context, event events.Event, rules []PromotionRule) ([]string, error)
}

// PromotionEvaluationStore is the queue of rule evaluations (ADR 0021, Evaluator).
type PromotionEvaluationStore interface {
	// ListEnabledPromotionRules returns the enabled rules of a source repository in any state: the rule set of the fan-out. A
	// parked rule is included so the artifacts published while it is parked wait for it instead of being lost; the evaluator
	// decides what a parked rule does.
	ListEnabledPromotionRules(ctx context.Context, sourceRepositoryID string) ([]PromotionRule, error)
	// ClaimPromotionEvaluations takes up to limit due pending or waiting rows with FOR UPDATE SKIP LOCKED and leases them
	// to owner. A row whose lease expired is due again, so a crashed worker's rows come back without a scheduler; a due
	// row that already used PromotionEvaluationMaxAttempts claims is failed with attempts_exhausted instead of claimed, so
	// a row that keeps killing its worker stops.
	ClaimPromotionEvaluations(ctx context.Context, owner string, lease time.Duration, limit int) ([]PromotionEvaluation, error)
	// FinishPromotionEvaluation records the outcome of a claimed row and releases the lease. It returns
	// ErrPromotionEvaluationLeaseLost when the lease is no longer the caller's.
	FinishPromotionEvaluation(ctx context.Context, id, leaseToken string, outcome PromotionEvaluationOutcome) error
	// ListPromotionEvaluations lists the evaluations of a rule, newest first.
	ListPromotionEvaluations(ctx context.Context, query PromotionEvaluationQuery) ([]PromotionEvaluation, error)
	// ParkPromotionRule moves an active rule to state (authorization_lost or paused) with a reason, bumping no version:
	// parking is not an edit. It is conditional on the version the caller decided from, so a rule edited since is left
	// alone, and it reports whether the rule was parked. The change and the promotion.failed event commit together with the
	// audit records.
	ParkPromotionRule(ctx context.Context, ruleID, expectedVersion, state, reason string, event events.Event, audit ...AuditRecord) (bool, error)
}

// PromotionEvaluationExhausted is the reason of an evaluation the claim failed because it used all its attempts.
const PromotionEvaluationExhausted = "attempts_exhausted"

// PromotionEvaluationBackoff is the delay before a transient failure of a claimed evaluation is tried again: 5 seconds, doubling,
// up to 5 minutes.
func PromotionEvaluationBackoff(attempts int) time.Duration {
	delay := 5 * time.Second
	for i := 1; i < attempts && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	return min(delay, 5*time.Minute)
}

func sortEvaluationsNewestFirst(items []PromotionEvaluation) {
	slices.SortFunc(items, func(a, b PromotionEvaluation) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return -strings.Compare(a.ID, b.ID)
	})
}

func sortEvaluationsOldestDueFirst(items []PromotionEvaluation) {
	slices.SortFunc(items, func(a, b PromotionEvaluation) int {
		if c := a.NextAttemptAt.Compare(b.NextAttemptAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
}

func sortPromotionRulesByID(rules []PromotionRule) {
	slices.SortFunc(rules, func(a, b PromotionRule) int { return strings.Compare(a.ID, b.ID) })
}
