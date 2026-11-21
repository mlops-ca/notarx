package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ArtifactPromotionState is the lifecycle of one promotion attempt (ADR 0021, Provenance and approval). The states
// pending_approval, rejected and expired belong to the approval slice; a promotion written by the PromotionService
// starts queued and follows its job.
type ArtifactPromotionState string

const (
	ArtifactPromotionPendingApproval ArtifactPromotionState = "pending_approval"
	ArtifactPromotionQueued          ArtifactPromotionState = "queued"
	ArtifactPromotionRunning         ArtifactPromotionState = "running"
	ArtifactPromotionCompleted       ArtifactPromotionState = "completed"
	ArtifactPromotionFailed          ArtifactPromotionState = "failed"
	ArtifactPromotionRejected        ArtifactPromotionState = "rejected"
	ArtifactPromotionExpired         ArtifactPromotionState = "expired"
	ArtifactPromotionCancelled       ArtifactPromotionState = "cancelled"
)

// Valid reports whether s is one of the states the table's CHECK allows.
func (s ArtifactPromotionState) Valid() bool {
	switch s {
	case ArtifactPromotionPendingApproval, ArtifactPromotionQueued, ArtifactPromotionRunning, ArtifactPromotionCompleted,
		ArtifactPromotionFailed, ArtifactPromotionRejected, ArtifactPromotionExpired, ArtifactPromotionCancelled:
		return true
	}
	return false
}

// PromotionChainHop is one step of the path an artifact took to reach its repository: the instance (empty for the local
// one), the repository it was promoted from and the rule that promoted it (empty for a manual promotion).
type PromotionChainHop struct {
	Instance   string    `json:"instance"`
	Repository string    `json:"repository"`
	RuleID     string    `json:"ruleId"`
	At         time.Time `json:"at"`
}

// PromotionApproval is one recorded approval.
type PromotionApproval struct {
	Approver string    `json:"approver"`
	At       time.Time `json:"at"`
}

// ArtifactPromotion is the provenance row of one promotion attempt. ChainPath is append-only.
type ArtifactPromotion struct {
	ID, TargetRepositoryID string
	Format                 Format
	Coordinate, Digest     string
	// OriginInstanceID is empty for a promotion inside this instance; OriginRepository names the source repository.
	OriginInstanceID, OriginRepository string
	// RuleID and RuleVersion are empty and zero for a manual promotion.
	RuleID      string
	RuleVersion int64
	ChainPath   []PromotionChainHop
	RequestedBy string
	State       ArtifactPromotionState
	// Attempt counts the tries of one idempotency key, from 1.
	Attempt        int
	IdempotencyKey string
	// JobID is the promotion job of the attempt; empty until a job exists.
	JobID       string
	Approvals   []PromotionApproval
	CreatedAt   time.Time
	CompletedAt time.Time
}

// ArtifactPromotionQuery filters a listing. RepositoryIDs nil means every target repository; an empty, non-nil slice
// matches nothing.
type ArtifactPromotionQuery struct {
	RepositoryIDs      []string
	TargetRepositoryID string
	Format             Format
	State              ArtifactPromotionState
	Coordinate, Digest string
	Limit              int
	PageToken          string
}

// ArtifactPromotionPage is one keyset page, newest first.
type ArtifactPromotionPage struct {
	Items         []ArtifactPromotion
	NextPageToken string
}

// ArtifactPromotionStore reads provenance. A promotion row is written by EnqueueLifecycleJob, in the job's
// transaction, when the context carries WithArtifactPromotion; its state then follows the job.
type ArtifactPromotionStore interface {
	// GetArtifactPromotionByJob returns the newest attempt of the promotion that owns the job.
	GetArtifactPromotionByJob(ctx context.Context, jobID string) (ArtifactPromotion, error)
	PageArtifactPromotions(ctx context.Context, query ArtifactPromotionQuery) (ArtifactPromotionPage, error)
}

type artifactPromotionContextKey struct{}

// WithArtifactPromotion makes the next promotion EnqueueLifecycleJob write promotion as its provenance row, in the
// same transaction as the job. The target repository and job id are those of the job; State, Attempt and CreatedAt
// default to queued, 1 and now. A replay of an idempotency key writes nothing: the row of the first request stands.
func WithArtifactPromotion(ctx context.Context, promotion ArtifactPromotion) context.Context {
	return context.WithValue(ctx, artifactPromotionContextKey{}, promotion)
}

// artifactPromotionDraft returns the provenance row to write with a promotion job, completed from the job.
func artifactPromotionDraft(ctx context.Context, job LifecycleJob) (ArtifactPromotion, bool) {
	promotion, ok := ctx.Value(artifactPromotionContextKey{}).(ArtifactPromotion)
	if !ok || job.Kind != LifecycleJobPromotion {
		return ArtifactPromotion{}, false
	}
	promotion.TargetRepositoryID, promotion.JobID = job.RepositoryID, job.ID
	if promotion.State == "" {
		promotion.State = ArtifactPromotionQueued
	}
	if promotion.Attempt < 1 {
		promotion.Attempt = 1
	}
	if promotion.ChainPath == nil {
		promotion.ChainPath = []PromotionChainHop{}
	}
	if promotion.Approvals == nil {
		promotion.Approvals = []PromotionApproval{}
	}
	return promotion, true
}

// ErrPromotionChainRewritten is returned for a chain_path that is not an append of the stored one.
var ErrPromotionChainRewritten = errors.New("promotion chain path is append-only")

// promotionStateForJob is the provenance state a job state maps to.
func promotionStateForJob(state LifecycleJobState) ArtifactPromotionState {
	switch state {
	case LifecycleJobRunning:
		return ArtifactPromotionRunning
	case LifecycleJobCompleted:
		return ArtifactPromotionCompleted
	case LifecycleJobFailed:
		return ArtifactPromotionFailed
	case LifecycleJobCancelled:
		return ArtifactPromotionCancelled
	}
	return ArtifactPromotionQueued
}

// PromotionPolicyRefusalPrefix starts the last error of a promotion job refused at claim time by the target's
// admission policy.
const PromotionPolicyRefusalPrefix = "target admission policy refused the promotion: "

// promotionPolicyStore is what the claim-time re-evaluation reads.
type promotionPolicyStore interface {
	GetRepositorySecurityPolicy(ctx context.Context, repositoryID string) (RepositorySecurityPolicy, error)
	GetArtifactIntelligence(ctx context.Context, repositoryID string, format Format, coordinate, digest string) (ArtifactIntelligence, error)
}

// promotionRefusal re-evaluates the target's admission policy for the artifact of a claimed promotion job and returns
// the refusal message, or "" when the promotion may go on. The policy is read now, not when the job was enqueued, so a
// policy tightened in between refuses the job (ADR 0021, Evaluator). The artifact's own quarantine is rechecked by
// every format's publish step under the distribution admission lock. A job with no provenance row is not checked here.
func promotionRefusal(ctx context.Context, store promotionPolicyStore, promotion ArtifactPromotion, job LifecycleJob) (string, error) {
	var payload struct {
		SourceRepositoryID string `json:"sourceRepositoryId"`
	}
	_ = json.Unmarshal(job.Payload, &payload)
	policy, err := store.GetRepositorySecurityPolicy(ctx, job.RepositoryID)
	if err != nil {
		return "", err
	}
	var intelligence *ArtifactIntelligence
	if payload.SourceRepositoryID != "" {
		value, intelligenceErr := store.GetArtifactIntelligence(ctx, payload.SourceRepositoryID, promotion.Format, promotion.Coordinate, promotion.Digest)
		switch {
		case intelligenceErr == nil:
			intelligence = &value
		case !errors.Is(intelligenceErr, ErrNotFound):
			return "", intelligenceErr
		}
	}
	evaluation := EvaluateRepositorySecurityPolicy(policy, intelligence)
	if evaluation.Allowed {
		return "", nil
	}
	return PromotionPolicyRefusalPrefix + strings.Join(evaluation.Reasons, ","), nil
}
