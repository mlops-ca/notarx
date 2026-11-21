package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	_ ArtifactPromotionStore = (*PostgresStore)(nil)
	_ ArtifactPromotionStore = (*MemoryStore)(nil)
)

const artifactPromotionColumns = `id::text,target_repository_id::text,format,coordinate,digest,COALESCE(origin_instance_id::text,''),origin_repository,COALESCE(rule_id::text,''),COALESCE(rule_version,0),chain_path,requested_by,state,attempt,idempotency_key,COALESCE(job_id::text,''),approvals,created_at,completed_at`

func scanArtifactPromotion(scanner interface{ Scan(...any) error }, promotion *ArtifactPromotion) error {
	var chain, approvals []byte
	var completedAt sql.NullTime
	if err := scanner.Scan(&promotion.ID, &promotion.TargetRepositoryID, &promotion.Format, &promotion.Coordinate, &promotion.Digest,
		&promotion.OriginInstanceID, &promotion.OriginRepository, &promotion.RuleID, &promotion.RuleVersion, &chain, &promotion.RequestedBy,
		&promotion.State, &promotion.Attempt, &promotion.IdempotencyKey, &promotion.JobID, &approvals, &promotion.CreatedAt, &completedAt); err != nil {
		return err
	}
	promotion.ChainPath, promotion.Approvals = nil, nil
	if err := json.Unmarshal(chain, &promotion.ChainPath); err != nil {
		return err
	}
	if err := json.Unmarshal(approvals, &promotion.Approvals); err != nil {
		return err
	}
	promotion.CompletedAt = time.Time{}
	if completedAt.Valid {
		promotion.CompletedAt = completedAt.Time
	}
	return nil
}

// insertArtifactPromotion writes the provenance row of a promotion job in the job's transaction. A key already used by
// another job is an idempotency conflict.
func insertArtifactPromotion(ctx context.Context, q sqlRunner, promotion ArtifactPromotion) error {
	chain, err := json.Marshal(promotion.ChainPath)
	if err != nil {
		return err
	}
	approvals, err := json.Marshal(promotion.Approvals)
	if err != nil {
		return err
	}
	if promotion.ID == "" {
		return errors.New("artifact promotion has no id")
	}
	var ruleID, originInstance, jobID any
	if promotion.RuleID != "" {
		ruleID = promotion.RuleID
	}
	if promotion.OriginInstanceID != "" {
		originInstance = promotion.OriginInstanceID
	}
	if promotion.JobID != "" {
		jobID = promotion.JobID
	}
	var ruleVersion any
	if promotion.RuleVersion != 0 {
		ruleVersion = promotion.RuleVersion
	}
	_, err = q.ExecContext(ctx, `INSERT INTO artifact_promotions (id,target_repository_id,format,coordinate,digest,origin_instance_id,origin_repository,rule_id,rule_version,chain_path,requested_by,state,attempt,idempotency_key,job_id,approvals)
VALUES ($1::uuid,$2::uuid,$3,$4,$5,$6::uuid,$7,$8::uuid,$9::bigint,$10::jsonb,$11,$12,$13,$14,$15::uuid,$16::jsonb)`,
		promotion.ID, promotion.TargetRepositoryID, promotion.Format, promotion.Coordinate, promotion.Digest, originInstance, promotion.OriginRepository, ruleID,
		ruleVersion, chain, promotion.RequestedBy, promotion.State, promotion.Attempt, promotion.IdempotencyKey, jobID, approvals)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrIdempotencyConflict
	}
	return err
}

// GetArtifactPromotionByJob implements ArtifactPromotionStore.
func (s *PostgresStore) GetArtifactPromotionByJob(ctx context.Context, jobID string) (ArtifactPromotion, error) {
	var promotion ArtifactPromotion
	err := scanArtifactPromotion(s.db.QueryRowContext(ctx, `SELECT `+artifactPromotionColumns+` FROM artifact_promotions WHERE job_id=$1::uuid ORDER BY attempt DESC LIMIT 1`, jobID), &promotion)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactPromotion{}, ErrNotFound
	}
	return promotion, err
}

func artifactPromotionsFilter(query ArtifactPromotionQuery) string {
	return strings.Join([]string{"artifact-promotions", query.TargetRepositoryID, string(query.Format), string(query.State), query.Coordinate, query.Digest}, "\x00")
}

// pageArtifactPromotionsQuery filters on optional equalities (a NULL parameter matches every row) and the readable
// repository set, and pages newest first by keyset on (created_at, id).
const pageArtifactPromotionsQuery = `SELECT ` + artifactPromotionColumns + ` FROM artifact_promotions
WHERE ($1::uuid IS NULL OR target_repository_id=$1::uuid)
  AND ($2::text IS NULL OR format=$2) AND ($3::text IS NULL OR state=$3)
  AND ($4::text IS NULL OR coordinate=$4) AND ($5::text IS NULL OR digest=$5)
  AND ($6::uuid[] IS NULL OR target_repository_id=ANY($6::uuid[]))
  AND ($7::timestamptz IS NULL OR (created_at,artifact_promotions.id)<($7::timestamptz,$8::uuid))
ORDER BY created_at DESC,artifact_promotions.id DESC LIMIT $9`

// PageArtifactPromotions implements ArtifactPromotionStore.
func (s *PostgresStore) PageArtifactPromotions(ctx context.Context, query ArtifactPromotionQuery) (ArtifactPromotionPage, error) {
	limit := pageLimit(query.Limit, 100, 500)
	filter := artifactPromotionsFilter(query)
	cursorAt, cursorID, after, err := decodeCreatedAtToken(query.PageToken, filter)
	if err != nil {
		return ArtifactPromotionPage{}, err
	}
	var target, format, state, coordinate, digest, repositories, cursorTime, cursorKey any
	if query.TargetRepositoryID != "" {
		target = query.TargetRepositoryID
	}
	if query.Format != "" {
		format = string(query.Format)
	}
	if query.State != "" {
		state = string(query.State)
	}
	if query.Coordinate != "" {
		coordinate = query.Coordinate
	}
	if query.Digest != "" {
		digest = query.Digest
	}
	if query.RepositoryIDs != nil {
		repositories = query.RepositoryIDs
	}
	if after {
		cursorTime, cursorKey = cursorAt, cursorID
	}
	rows, err := s.db.QueryContext(ctx, pageArtifactPromotionsQuery, target, format, state, coordinate, digest, repositories, cursorTime, cursorKey, limit+1)
	if err != nil {
		return ArtifactPromotionPage{}, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]ArtifactPromotion, 0, limit+1)
	for rows.Next() {
		var promotion ArtifactPromotion
		if err := scanArtifactPromotion(rows, &promotion); err != nil {
			return ArtifactPromotionPage{}, err
		}
		items = append(items, promotion)
	}
	if err := rows.Err(); err != nil {
		return ArtifactPromotionPage{}, err
	}
	page := ArtifactPromotionPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		page.NextPageToken = encodeCreatedAtToken(filter, last.CreatedAt, last.ID)
	}
	return page, nil
}

// postgresClaimedPromotions adapts the store to the claim-time re-evaluation.
type postgresClaimedPromotions struct{ *PostgresStore }

func (p postgresClaimedPromotions) promotionOfJob(ctx context.Context, jobID string) (ArtifactPromotion, error) {
	return p.GetArtifactPromotionByJob(ctx, jobID)
}

func (p postgresClaimedPromotions) failClaimed(ctx context.Context, job LifecycleJob, reason string) error {
	return p.FailLifecycleJob(ctx, job.ID, job.LeaseToken, reason)
}

func (p postgresClaimedPromotions) releaseClaimed(ctx context.Context, job LifecycleJob) error {
	return p.ReleaseLifecycleJobLease(ctx, job.ID, job.LeaseToken)
}
