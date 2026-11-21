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
	// GetArtifactPromotionsByKeys returns the newest attempt of each given idempotency key that has a row, by key.
	GetArtifactPromotionsByKeys(ctx context.Context, keys []string) (map[string]ArtifactPromotion, error)
	// GetImportedArtifactPromotion returns the newest completed provenance row that an import commit wrote for the
	// artifact in the repository (ADR 0021, Chains), or ErrNotFound when it was not imported there.
	GetImportedArtifactPromotion(ctx context.Context, targetRepositoryID string, format Format, coordinate, digest string) (ArtifactPromotion, error)
}

// ImportPromotionKeyPrefix starts the idempotency key of the provenance row an import commit writes.
const ImportPromotionKeyPrefix = "import:"

// ImportPromotionKey is the idempotency key of the provenance row of a committed import session.
func ImportPromotionKey(sessionID string) string { return ImportPromotionKeyPrefix + sessionID }

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

// claimedPromotionPort is what the claim-time re-evaluation needs of a store: the provenance row of a job, the policy
// and intelligence reads, and the two ways a claimed job leaves the claim without running.
type claimedPromotionPort interface {
	promotionPolicyStore
	promotionOfJob(ctx context.Context, jobID string) (ArtifactPromotion, error)
	// failClaimed fails the claimed job with the reason, taking its retry like any failure.
	failClaimed(ctx context.Context, job LifecycleJob, reason string) error
	// releaseClaimed hands the claimed job back to the queue unrun, returning the attempt its claim counted.
	releaseClaimed(ctx context.Context, job LifecycleJob) error
}

// refuseClaimedPromotions re-evaluates the target's admission policy for each claimed promotion job just before it runs
// (ADR 0021, Evaluator). A refused job fails with the policy reasons, takes its retry like any failure, and is not
// handed to its worker. A job whose policy could not be read (a store error, not a verdict) is released to the queue
// without spending an attempt; only a target repository that no longer exists fails it. A failure to apply either
// outcome is returned, and every claimed job not yet resolved is released so none waits out its lease.
func refuseClaimedPromotions(ctx context.Context, port claimedPromotionPort, claimed []LifecycleJob) ([]LifecycleJob, error) {
	kept := claimed[:0:0]
	for index, job := range claimed {
		if job.Kind != LifecycleJobPromotion {
			kept = append(kept, job)
			continue
		}
		promotion, err := port.promotionOfJob(ctx, job.ID)
		if errors.Is(err, ErrNotFound) {
			kept = append(kept, job)
			continue
		}
		reason := ""
		if err == nil {
			reason, err = promotionRefusal(ctx, port, promotion, job)
		}
		switch {
		case errors.Is(err, ErrNotFound):
			reason = "target admission policy could not be evaluated: the target repository no longer exists"
		case err != nil:
			// Transient: the verdict is unknown, so the job goes back unrun and its attempt is not spent.
			if releaseErr := port.releaseClaimed(ctx, job); releaseErr != nil {
				return nil, releaseUnresolved(ctx, port, errors.Join(err, releaseErr), kept, claimed[index+1:])
			}
			continue
		}
		if reason == "" {
			kept = append(kept, job)
			continue
		}
		if failErr := port.failClaimed(ctx, job, reason); failErr != nil {
			return nil, releaseUnresolved(ctx, port, failErr, kept, claimed[index+1:])
		}
	}
	return kept, nil
}

// releaseUnresolved releases the claimed jobs that were neither failed nor released, best effort, and returns cause
// joined with any release failure.
func releaseUnresolved(ctx context.Context, port claimedPromotionPort, cause error, groups ...[]LifecycleJob) error {
	errs := []error{cause}
	for _, group := range groups {
		for _, job := range group {
			if err := port.releaseClaimed(ctx, job); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
