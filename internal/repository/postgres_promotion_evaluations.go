package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/mlops-ca/notarx/internal/events"
)

const promotionEvaluationColumns = `e.id::text,e.event_seq,e.event_occurred_at,e.rule_id::text,e.repository_id::text,e.coordinate,e.digest,e.state,e.next_attempt_at,e.attempts,e.lease_owner,e.lease_token::text,e.lease_expires_at,e.last_reason,e.promotion_id::text,e.created_at,e.updated_at`

func scanPromotionEvaluation(scanner interface{ Scan(...any) error }) (PromotionEvaluation, error) {
	var evaluation PromotionEvaluation
	var state string
	var owner, token, promotion sql.NullString
	var leaseExpires sql.NullTime
	if err := scanner.Scan(&evaluation.ID, &evaluation.EventSeq, &evaluation.EventOccurredAt, &evaluation.RuleID, &evaluation.RepositoryID, &evaluation.Coordinate, &evaluation.Digest,
		&state, &evaluation.NextAttemptAt, &evaluation.Attempts, &owner, &token, &leaseExpires, &evaluation.LastReason, &promotion, &evaluation.CreatedAt, &evaluation.UpdatedAt); err != nil {
		return PromotionEvaluation{}, err
	}
	evaluation.State = PromotionEvaluationState(state)
	evaluation.LeaseOwner, evaluation.LeaseToken, evaluation.PromotionID = owner.String, token.String, promotion.String
	evaluation.LeaseExpiresAt = leaseExpires.Time
	return evaluation, nil
}

// RuleQuerier is the read side of a transaction or a pool that can list rules.
type RuleQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ListEnabledPromotionRules implements PromotionEvaluationStore; promotion_rules_source_idx serves it.
func (s *PostgresStore) ListEnabledPromotionRules(ctx context.Context, sourceRepositoryID string) ([]PromotionRule, error) {
	return ListEnabledPromotionRulesFrom(ctx, s.db, sourceRepositoryID)
}

// ListEnabledPromotionRulesFrom lists the enabled, active rules of a source repository through q: the rule fan-out passes
// the publishing transaction, so a cache miss does not take a second connection while the transaction holds one.
func ListEnabledPromotionRulesFrom(ctx context.Context, q RuleQuerier, sourceRepositoryID string) ([]PromotionRule, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+promotionRuleColumns+` FROM promotion_rules WHERE source_repository_id=$1::uuid AND enabled AND state='active' ORDER BY promotion_rules.id`, sourceRepositoryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var rules []PromotionRule
	for rows.Next() {
		rule, scanErr := scanPromotionRule(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

// claimPromotionEvaluationsQuery is the claim pattern of ADR 0023 section 7: the candidates come from the partial claim
// index in due-time order (no global sort), are locked with SKIP LOCKED so concurrent workers take disjoint rows, and
// are leased in the same statement. The lease moves next_attempt_at to its expiry, so a row whose worker died is due
// again by itself.
const claimPromotionEvaluationsQuery = `WITH clock AS MATERIALIZED (SELECT clock_timestamp() AS now), exhausted AS (
    UPDATE promotion_evaluations e SET state='failed',last_reason='attempts_exhausted',lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=clock.now
    FROM clock WHERE e.id IN (
        SELECT x.id FROM promotion_evaluations x WHERE x.state IN ('pending','waiting') AND x.next_attempt_at<=clock.now AND x.attempts>=$4
        ORDER BY x.next_attempt_at FOR UPDATE OF x SKIP LOCKED LIMIT 100)
), candidates AS (
    SELECT e.id FROM promotion_evaluations e CROSS JOIN clock
    WHERE e.state IN ('pending','waiting') AND e.next_attempt_at<=clock.now AND e.attempts<$4
    ORDER BY e.next_attempt_at FOR UPDATE OF e SKIP LOCKED LIMIT $3
), claimed AS (
    UPDATE promotion_evaluations e SET lease_owner=$1,lease_token=gen_random_uuid(),lease_expires_at=clock.now+$2::interval,next_attempt_at=clock.now+$2::interval,
        attempts=e.attempts+1,updated_at=clock.now
    FROM candidates,clock WHERE e.id=candidates.id RETURNING e.*
)
SELECT ` + promotionEvaluationColumns + ` FROM claimed e ORDER BY e.next_attempt_at,e.id`

func intervalOf(d time.Duration) string { return fmt.Sprintf("%d milliseconds", d.Milliseconds()) }

// ClaimPromotionEvaluations implements PromotionEvaluationStore.
func (s *PostgresStore) ClaimPromotionEvaluations(ctx context.Context, owner string, lease time.Duration, limit int) ([]PromotionEvaluation, error) {
	rows, err := s.db.QueryContext(ctx, claimPromotionEvaluationsQuery, owner, intervalOf(lease), limit, PromotionEvaluationMaxAttempts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var claimed []PromotionEvaluation
	for rows.Next() {
		evaluation, scanErr := scanPromotionEvaluation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		claimed = append(claimed, evaluation)
	}
	return claimed, rows.Err()
}

// FinishPromotionEvaluation implements PromotionEvaluationStore. A waiting outcome resets the attempt counter: it is a
// wait for a condition, not a failure. The update is conditional on the live lease, so a worker that outlived its lease
// cannot overwrite the row a second worker owns.
func (s *PostgresStore) FinishPromotionEvaluation(ctx context.Context, id, leaseToken string, outcome PromotionEvaluationOutcome) error {
	var next any
	if !outcome.NextAttemptAt.IsZero() {
		next = outcome.NextAttemptAt.UTC()
	}
	result, err := s.db.ExecContext(ctx, `WITH clock AS MATERIALIZED (SELECT clock_timestamp() AS now)
UPDATE promotion_evaluations e SET state=$3,last_reason=$4,
    next_attempt_at=CASE WHEN $3 IN ('pending','waiting') THEN COALESCE($5::timestamptz,clock.now) ELSE e.next_attempt_at END,
    attempts=CASE WHEN $3='waiting' THEN 0 ELSE e.attempts END,promotion_id=COALESCE($6::uuid,e.promotion_id),
    lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=clock.now
FROM clock WHERE e.id=$1::uuid AND e.lease_token=$2::uuid AND e.lease_expires_at>clock.now`,
		id, leaseToken, string(outcome.State), truncateReason(outcome.Reason), next, nullableText(outcome.PromotionID))
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return ErrPromotionEvaluationLeaseLost
	}
	return nil
}

// truncateReason keeps a recorded reason inside the column's bound of 1024 characters, cutting at a character boundary
// (a byte cut inside a multi-byte character is invalid UTF-8, which the database refuses).
func truncateReason(reason string) string {
	if len(reason) <= 1024 {
		return reason
	}
	cut := 1024
	for cut > 0 && !utf8.RuneStart(reason[cut]) {
		cut--
	}
	return reason[:cut]
}

// ListPromotionEvaluations implements PromotionEvaluationStore.
func (s *PostgresStore) ListPromotionEvaluations(ctx context.Context, query PromotionEvaluationQuery) ([]PromotionEvaluation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+promotionEvaluationColumns+` FROM promotion_evaluations e
WHERE e.rule_id=$1::uuid AND ($2='' OR e.state=$2) ORDER BY e.created_at DESC,e.id DESC LIMIT $3`, query.RuleID, string(query.State), pageLimit(query.Limit, 100, 500))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []PromotionEvaluation
	for rows.Next() {
		evaluation, scanErr := scanPromotionEvaluation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, evaluation)
	}
	return items, rows.Err()
}

// ParkPromotionRule implements PromotionEvaluationStore. The state change, the promotion.failed event and the audit
// records commit together, so a parked rule is always announced.
func (s *PostgresStore) ParkPromotionRule(ctx context.Context, ruleID, expectedVersion, state, reason string, event events.Event, audit ...AuditRecord) (bool, error) {
	parked, err := runTransactional(ctx, s, audit, func(q sqlRunner) (bool, error) {
		result, execErr := q.ExecContext(ctx, `UPDATE promotion_rules SET state=$2,state_reason=$3,updated_at=now() WHERE id=$1::uuid AND state='active' AND version=$4::text::bigint`, ruleID, state, truncateReason(reason), expectedVersion)
		if execErr != nil {
			return false, execErr
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return false, nil
		}
		_, emitErr := events.Emit(ctx, q, event)
		return true, emitErr
	})
	if err == nil && parked {
		s.invalidateLocalScope(CacheScopePromotionRule)
	}
	return parked, err
}

var _ PromotionEvaluationStore = (*PostgresStore)(nil)
