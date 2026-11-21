package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const promotionRuleColumns = `id::text,name,source_repository_id::text,target_kind,target_repository_id::text,remote_instance_id::text,remote_repository,match,conditions,approval,schedule,run_as_service_account_id::text,enabled,version::text,created_by,updated_by,created_at,updated_at`

func scanPromotionRule(scanner interface{ Scan(...any) error }) (PromotionRule, error) {
	var rule PromotionRule
	var target, remote, remoteRepository sql.NullString
	var match, conditions, approval, schedule []byte
	if err := scanner.Scan(&rule.ID, &rule.Name, &rule.SourceRepositoryID, &rule.TargetKind, &target, &remote, &remoteRepository, &match, &conditions,
		&approval, &schedule, &rule.RunAsServiceAccountID, &rule.Enabled, &rule.Version, &rule.CreatedBy, &rule.UpdatedBy, &rule.CreatedAt, &rule.UpdatedAt); err != nil {
		return PromotionRule{}, err
	}
	rule.TargetRepositoryID, rule.RemoteInstanceID, rule.RemoteRepository = target.String, remote.String, remoteRepository.String
	rule.Match, rule.Conditions = json.RawMessage(match), json.RawMessage(conditions)
	if approval != nil {
		rule.Approval = json.RawMessage(approval)
	}
	if schedule != nil {
		rule.Schedule = json.RawMessage(schedule)
	}
	return rule, nil
}

// nullableText stores an empty string as NULL: the table's check ties the target columns to target_kind.
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// nullableJSON stores an absent document as NULL.
func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return []byte(value)
}

// emptyObjectIfAbsent stores no conditions as the empty object: the column is not null.
func emptyObjectIfAbsent(value json.RawMessage) []byte {
	if len(value) == 0 {
		return []byte("{}")
	}
	return []byte(value)
}

func promotionRuleWriteError(err error) error {
	switch {
	case isUnique(err):
		return ErrPromotionRuleNameExists
	case isForeignKeyViolation(err):
		return ErrPromotionRuleReference
	}
	return err
}

// CreatePromotionRule counts the rules of the source repository and inserts the new one under an advisory lock of
// that repository, so two concurrent creates cannot both pass the cap.
func (s *PostgresStore) CreatePromotionRule(ctx context.Context, rule PromotionRule, maxPerSource int, audit ...AuditRecord) (PromotionRule, error) {
	return runTransactional(ctx, s, audit, func(q sqlRunner) (PromotionRule, error) {
		if _, err := q.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('promotion_rules:'||$1::text,0))`, rule.SourceRepositoryID); err != nil {
			return PromotionRule{}, err
		}
		var count int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM promotion_rules WHERE source_repository_id=$1::uuid`, rule.SourceRepositoryID).Scan(&count); err != nil {
			return PromotionRule{}, err
		}
		if maxPerSource > 0 && count >= maxPerSource {
			return PromotionRule{}, ErrPromotionRuleLimit
		}
		created, err := scanPromotionRule(q.QueryRowContext(ctx, `INSERT INTO promotion_rules (id,name,source_repository_id,target_kind,target_repository_id,remote_instance_id,remote_repository,match,conditions,approval,schedule,run_as_service_account_id,enabled,created_by,updated_by)
VALUES ($1::uuid,$2,$3::uuid,$4,$5::uuid,$6::uuid,$7,$8::jsonb,$9::jsonb,$10::jsonb,$11::jsonb,$12::uuid,$13,$14,$14) RETURNING `+promotionRuleColumns,
			rule.ID, rule.Name, rule.SourceRepositoryID, rule.TargetKind, nullableText(rule.TargetRepositoryID), nullableText(rule.RemoteInstanceID), nullableText(rule.RemoteRepository),
			[]byte(rule.Match), emptyObjectIfAbsent(rule.Conditions), nullableJSON(rule.Approval), nullableJSON(rule.Schedule), rule.RunAsServiceAccountID, rule.Enabled, rule.CreatedBy))
		return created, promotionRuleWriteError(err)
	})
}

// PagePromotionRules pages rules by name in byte order with a keyset on the unique name, served by
// promotion_rules_name_bytes_idx whatever the database collation.
func (s *PostgresStore) PagePromotionRules(ctx context.Context, query PromotionRuleQuery) (PromotionRulePage, error) {
	limit := pageLimit(query.Limit, 100, 500)
	filter := promotionRulesFilter(query)
	after, present, err := decodeNameToken(query.PageToken, filter)
	if err != nil {
		return PromotionRulePage{}, err
	}
	var sources, cursor any
	if query.SourceRepositoryIDs != nil {
		sources = query.SourceRepositoryIDs
	}
	if present {
		cursor = after
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+promotionRuleColumns+` FROM promotion_rules
WHERE ($1::text[] IS NULL OR source_repository_id::text=ANY($1::text[])) AND ($2::text IS NULL OR name COLLATE "C">$2::text)
ORDER BY name COLLATE "C" LIMIT $3`, sources, cursor, limit+1)
	if err != nil {
		return PromotionRulePage{}, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]PromotionRule, 0, limit+1)
	for rows.Next() {
		rule, scanErr := scanPromotionRule(rows)
		if scanErr != nil {
			return PromotionRulePage{}, scanErr
		}
		items = append(items, rule)
	}
	if err = rows.Err(); err != nil {
		return PromotionRulePage{}, err
	}
	page := PromotionRulePage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextPageToken = EncodeKeysetToken(filter, page.Items[limit-1].Name)
	}
	return page, nil
}

func (s *PostgresStore) GetPromotionRule(ctx context.Context, id string) (PromotionRule, error) {
	rule, err := scanPromotionRule(s.db.QueryRowContext(ctx, `SELECT `+promotionRuleColumns+` FROM promotion_rules WHERE id=$1::uuid`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return PromotionRule{}, ErrNotFound
	}
	return rule, err
}

func (s *PostgresStore) UpdatePromotionRule(ctx context.Context, rule PromotionRule, expectedVersion string, audit ...AuditRecord) (PromotionRule, error) {
	return runAudited(ctx, s, audit, func(q sqlRunner) (PromotionRule, error) {
		updated, err := scanPromotionRule(q.QueryRowContext(ctx, `UPDATE promotion_rules
SET name=$2,target_kind=$3,target_repository_id=$4::uuid,remote_instance_id=$5::uuid,remote_repository=$6,match=$7::jsonb,conditions=$8::jsonb,approval=$9::jsonb,schedule=$10::jsonb,
    run_as_service_account_id=$11::uuid,enabled=$12,updated_by=$13,version=version+1,updated_at=now()
WHERE id=$1::uuid AND version=$14::text::bigint RETURNING `+promotionRuleColumns,
			rule.ID, rule.Name, rule.TargetKind, nullableText(rule.TargetRepositoryID), nullableText(rule.RemoteInstanceID), nullableText(rule.RemoteRepository),
			[]byte(rule.Match), emptyObjectIfAbsent(rule.Conditions), nullableJSON(rule.Approval), nullableJSON(rule.Schedule), rule.RunAsServiceAccountID, rule.Enabled, rule.UpdatedBy, expectedVersion))
		if !errors.Is(err, sql.ErrNoRows) {
			return updated, promotionRuleWriteError(err)
		}
		var exists bool
		if queryErr := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM promotion_rules WHERE id=$1::uuid)`, rule.ID).Scan(&exists); queryErr != nil {
			return PromotionRule{}, queryErr
		}
		if exists {
			return PromotionRule{}, ErrVersionConflict
		}
		return PromotionRule{}, ErrNotFound
	})
}

func (s *PostgresStore) DeletePromotionRule(ctx context.Context, id string, audit ...AuditRecord) error {
	_, err := runAudited(ctx, s, audit, func(q sqlRunner) (struct{}, error) {
		result, execErr := q.ExecContext(ctx, `DELETE FROM promotion_rules WHERE id=$1::uuid`, id)
		if execErr != nil {
			return struct{}{}, execErr
		}
		if n, rowsErr := result.RowsAffected(); rowsErr != nil {
			return struct{}{}, rowsErr
		} else if n == 0 {
			return struct{}{}, ErrNotFound
		}
		return struct{}{}, nil
	})
	return err
}

var _ PromotionRuleStore = (*PostgresStore)(nil)
