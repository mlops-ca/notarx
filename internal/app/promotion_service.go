package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/repository"
)

// PromotionService is the single path every promotion takes (manual API, and later rules and
// batches): same-format active Hosted target check, quarantine recheck, admission policy
// evaluation, and the lifecycle_jobs promotion enqueue (ADR 0021 "Evaluator"). Callers have
// already authenticated the principal and authorised it on both repositories.
type PromotionService struct {
	store            GatewayStore
	securityPolicies repository.RepositorySecurityPolicyStore
	intelligence     repository.ArtifactIntelligenceStore
	audit            repository.Store
	promotions       repository.ArtifactPromotionStore
	registry         *formatmodule.Registry
	// instance names this instance in the chain paths of imported artifacts; maxHops is NOTARX_PROMOTION_MAX_HOPS.
	instance localInstanceIDReader
	maxHops  int
	// quarantine rechecks that the source artifact exists and is distributable.
	quarantine func(ctx context.Context, principal Principal, source, target repository.HostedRepository, coordinate, digest string) *PromotionError
}

// promotionStores are the provenance and batch ports of the gateway store. NewGatewayHandler refuses a store that lacks
// them, so a promotion never meets a store without provenance after its job is queued.
type promotionStores interface {
	repository.ArtifactPromotionStore
	repository.PromotionBatchStore
}

// requirePromotionStores asserts the promotion ports of the gateway store once, at construction, so a store without them
// fails the start of the gateway and never a request.
func requirePromotionStores(store GatewayStore) (promotionStores, error) {
	promotions, ok := store.(promotionStores)
	if !ok {
		return nil, errors.New("gateway store must implement the artifact promotion and promotion batch stores")
	}
	return promotions, nil
}

// newPromotionService wires the service from the management adapter's stores.
func newPromotionService(h generatedRepositoryAPIAdapter) PromotionService {
	return PromotionService{
		store: h.sessions.store, securityPolicies: h.securityPolicies, intelligence: h.intelligence, audit: h.audit, promotions: h.promotions,
		registry: promotionFormatRegistry(promotionModuleDeps{Store: h.sessions.store, Objects: h.objects[repository.FormatOCI]}), instance: h.sessions.store, maxHops: promotionMaxHops(h.promotionMaxHops),
		quarantine: func(ctx context.Context, principal Principal, source, target repository.HostedRepository, coordinate, digest string) *PromotionError {
			return h.checkQuarantinedDistribution(ctx, principal, source, target, coordinate, digest, "promote")
		},
	}
}

// PromotionRequest is one validated request to promote an immutable artifact.
type PromotionRequest struct {
	Principal      Principal
	Source         repository.HostedRepository
	TargetID       string
	Coordinate     string
	Digest         string
	AptTargetSuite *string
	IdempotencyKey string
	// Rule is set when a promotion rule requests the promotion (ADR 0021, Evaluator); a manual request leaves it nil.
	Rule *PromotionRuleOrigin
	// admission is what the caller's admission checks of this request resolved against the same target (a batch or a
	// release admits every member before it queues any); request uses it instead of running the checks a second time.
	admission *promotionAdmission
}

// PromotionRuleOrigin ties a promotion to the rule that requested it. Key is the provenance idempotency key of the rule
// (RulePromotionKey); the service reads the attempts already recorded under it, so a request for a key whose last
// attempt failed is the next attempt of that key and a request for a key whose last attempt is queued, running or
// completed is a replay.
type PromotionRuleOrigin struct {
	RuleID      string
	RuleVersion int64
	Key         string
}

// RulePromotionKey is the provenance idempotency key of a rule promotion: rule:<ruleId>:<sha256 of
// target|coordinate|digest|generation>. The generation is the tombstone time of the target coordinate (empty while it
// was never tombstoned), so promoting again after the target artifact was tombstoned mints a new key, and nothing else
// does.
func RulePromotionKey(ruleID, targetID, coordinate, digest, generation string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{targetID, coordinate, digest, generation}, "|")))
	return "rule:" + ruleID + ":" + hex.EncodeToString(sum[:])
}

// PromotionResult is the accepted promotion: its job and its provenance row. A replay of the same Idempotency-Key
// returns the job and the row of the first request.
type PromotionResult struct {
	Job       repository.LifecycleJob
	Promotion repository.ArtifactPromotion
}

// PromotionError is a refusal the HTTP layer renders as an RFC 9457 problem.
type PromotionError struct {
	Status  int
	Code    string
	Message string
}

func (e *PromotionError) Error() string { return e.Code + ": " + e.Message }

// errUnsupportedPromotionFormat is the refusal for a format whose module does not declare the promotion capability.
var errUnsupportedPromotionFormat = promotionError(http.StatusUnprocessableEntity, "unsupported_format", "promotion is not supported for this repository format")

// SupportsFormat reports whether the format's module declares the promotion capability.
func (s PromotionService) SupportsFormat(format repository.Format) bool {
	_, ok := formatmodule.Capability[formatmodule.PromotionPublisher](s.registry, format)
	return ok
}

func promotionError(status int, code, message string) *PromotionError {
	return &PromotionError{Status: status, Code: code, Message: message}
}

// promotionTarget is the target repository of a promotion with its security policy, resolved once so a batch does not
// re-read them per item. A refusal of the target or its policy is carried in err and applies to every item.
type promotionTarget struct {
	repo   repository.HostedRepository
	policy repository.RepositorySecurityPolicy
	err    *PromotionError
}

// resolveTarget loads the target repository and its security policy and checks that the target is an active Hosted
// repository of the source's format other than the source.
func (s PromotionService) resolveTarget(ctx context.Context, source repository.HostedRepository, targetID string) *promotionTarget {
	target, err := s.store.GetHostedRepository(ctx, targetID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return &promotionTarget{err: promotionError(http.StatusInternalServerError, "internal_error", "get target repository failed")}
	}
	if err != nil || target.ID == source.ID || target.Format != source.Format || target.Type != repository.RepositoryTypeHosted || target.State != repository.RepositoryActive {
		return &promotionTarget{err: promotionError(http.StatusConflict, "invalid_target", "target must be an active Hosted repository with the same format")}
	}
	policy, err := s.securityPolicies.GetRepositorySecurityPolicy(ctx, target.ID)
	if err != nil {
		return &promotionTarget{repo: target, err: promotionError(http.StatusInternalServerError, "internal_error", "get security policy failed")}
	}
	return &promotionTarget{repo: target, policy: policy}
}

// promotionAdmission is what a passed admission check resolved: the target repository and the format publisher.
type promotionAdmission struct {
	target    repository.HostedRepository
	publisher formatmodule.PromotionPublisher
	// chain is the chain path the artifact arrives with at the source repository: the stored path of an import, ending
	// with this instance's hop there; nil for an artifact that was not imported.
	chain []repository.PromotionChainHop
}

// sourceChain is the chain path the artifact carries at the source repository (ADR 0021, Chains): the path of the newest
// completed promotion or import into the source, ending with the source's own hop (appended when the row is a local
// promotion, whose path ends before its target), or just the source's hop for an artifact that was published there. A
// promotion refuses a route that would exceed NOTARX_PROMOTION_MAX_HOPS (chain_too_long) or return to a repository of
// this instance the artifact already passed (chain_loop).
func (s PromotionService) sourceChain(ctx context.Context, in PromotionRequest, target repository.HostedRepository) ([]repository.PromotionChainHop, *PromotionError) {
	var path []repository.PromotionChainHop
	row, err := s.promotions.GetArtifactPromotionRoute(ctx, in.Source.ID, in.Source.Format, in.Coordinate, in.Digest)
	switch {
	case errors.Is(err, repository.ErrNotFound):
	case err != nil:
		return nil, promotionError(http.StatusInternalServerError, "internal_error", "get promotion provenance failed")
	default:
		path = slices.Clone(row.ChainPath)
	}
	local, err := s.instance.LocalInstanceID(ctx)
	if err != nil {
		return nil, promotionError(http.StatusInternalServerError, "internal_error", "read the local instance id failed")
	}
	onThisInstance := func(hop repository.PromotionChainHop, name string) bool {
		return (hop.Instance == "" || strings.EqualFold(hop.Instance, local)) && hop.Repository == name
	}
	if len(path) == 0 || !onThisInstance(path[len(path)-1], in.Source.Name) {
		path = append(path, repository.PromotionChainHop{Repository: in.Source.Name, At: time.Now().UTC()})
	}
	if len(path) > s.maxHops {
		return nil, promotionError(http.StatusUnprocessableEntity, "chain_too_long", "the chain path of the artifact already has the maximum number of hops")
	}
	for _, hop := range path {
		if onThisInstance(hop, target.Name) {
			return nil, promotionError(http.StatusUnprocessableEntity, "chain_loop", "the artifact already passed the target repository")
		}
	}
	return path, nil
}

// Admit runs the admission checks of a promotion (format capability, target, quarantine recheck, admission policy)
// without queueing anything; a batch admits every item before it queues any.
func (s PromotionService) Admit(ctx context.Context, in PromotionRequest) *PromotionError {
	_, perr := s.admit(ctx, in, s.resolveTarget(ctx, in.Source, in.TargetID))
	return perr
}

// admit runs the per-item checks against a target resolved by resolveTarget. The worker evaluates the target policy
// again when it claims the job, so a policy read earlier in the same batch does not weaken execution.
func (s PromotionService) admit(ctx context.Context, in PromotionRequest, resolved *promotionTarget) (promotionAdmission, *PromotionError) {
	source := in.Source
	publisher, ok := formatmodule.Capability[formatmodule.PromotionPublisher](s.registry, source.Format)
	if !ok {
		return promotionAdmission{}, errUnsupportedPromotionFormat
	}
	if resolved.err != nil && resolved.repo.ID == "" {
		return promotionAdmission{}, resolved.err
	}
	target := resolved.repo
	if source.Format == repository.FormatAPT && in.AptTargetSuite == nil {
		return promotionAdmission{}, promotionError(http.StatusBadRequest, "invalid_request", "aptTargetSuite is required for APT promotion")
	}
	if rejection := s.quarantine(ctx, in.Principal, source, target, in.Coordinate, in.Digest); rejection != nil {
		return promotionAdmission{}, rejection
	}
	if resolved.err != nil {
		return promotionAdmission{}, resolved.err
	}
	var intelligence *repository.ArtifactIntelligence
	if value, intelligenceErr := s.intelligence.GetArtifactIntelligence(ctx, source.ID, source.Format, in.Coordinate, in.Digest); intelligenceErr == nil {
		intelligence = &value
	} else if !errors.Is(intelligenceErr, repository.ErrNotFound) {
		return promotionAdmission{}, promotionError(http.StatusInternalServerError, "internal_error", "get artifact intelligence failed")
	}
	evaluation := repository.EvaluateRepositorySecurityPolicy(resolved.policy, intelligence)
	if !evaluation.Allowed {
		if s.audit != nil {
			_ = s.audit.RecordAudit(ctx, repository.AuditRecord{GroupName: target.Name, Repository: target.Name, Actor: in.Principal.Actor, Outcome: repository.AuditAccessDenied, OccurredAt: time.Now().UTC(), Format: "management", Resource: in.Coordinate, Representation: in.Digest, Operation: "promote.security_policy", Status: http.StatusForbidden, AuthorizationReason: strings.Join(evaluation.Reasons, ",")})
		}
		return promotionAdmission{}, promotionError(http.StatusForbidden, "security_policy_denied", "promotion denied by security policy: "+strings.Join(evaluation.Reasons, ","))
	}
	chain, perr := s.sourceChain(ctx, in, target)
	if perr != nil {
		return promotionAdmission{}, perr
	}
	return promotionAdmission{target: target, publisher: publisher, chain: chain}, nil
}

// admission is the outcome of the admission checks of a request: the checks themselves, or, for a request the caller already
// admitted, what they resolved.
func (s PromotionService) admission(ctx context.Context, in PromotionRequest, resolved *promotionTarget) (promotionAdmission, *PromotionError) {
	if in.admission != nil {
		return *in.admission, nil
	}
	return s.admit(ctx, in, resolved)
}

// Request runs the admission checks and enqueues the promotion job.
func (s PromotionService) Request(ctx context.Context, in PromotionRequest) (PromotionResult, *PromotionError) {
	return s.request(ctx, in, s.resolveTarget(ctx, in.Source, in.TargetID))
}

func (s PromotionService) request(ctx context.Context, in PromotionRequest, resolved *promotionTarget) (PromotionResult, *PromotionError) {
	admission, perr := s.admission(ctx, in, resolved)
	if perr != nil {
		return PromotionResult{}, perr
	}
	source, target, publisher := in.Source, admission.target, admission.publisher
	audit := repository.AuditRecord{Repository: source.Name, GroupName: source.Name, Actor: in.Principal.Actor, Resource: in.Coordinate, Operation: "promote", Status: http.StatusAccepted}
	provenance := repository.ArtifactPromotion{
		ID: ids.New(), Format: source.Format, Coordinate: in.Coordinate, Digest: in.Digest, OriginRepository: source.Name,
		ChainPath:   admission.chain,
		RequestedBy: in.Principal.Actor, IdempotencyKey: promotionIdempotencyKey(target.ID, in.IdempotencyKey),
	}
	jobKey := in.IdempotencyKey
	if in.Rule != nil {
		attempt, replay, perr := s.ruleAttempt(ctx, in)
		if perr != nil {
			return PromotionResult{}, perr
		}
		if replay != nil {
			return *replay, nil
		}
		provenance.IdempotencyKey, provenance.RuleID, provenance.RuleVersion, provenance.Attempt = in.Rule.Key, in.Rule.RuleID, in.Rule.RuleVersion, attempt
		provenance.ChainPath[len(provenance.ChainPath)-1].RuleID = in.Rule.RuleID
		jobKey = in.Rule.Key
		if attempt > 1 {
			jobKey += "#" + strconv.Itoa(attempt)
		}
		audit.Evidence = map[string]string{"ruleId": in.Rule.RuleID, "ruleVersion": strconv.FormatInt(in.Rule.RuleVersion, 10), "attempt": strconv.Itoa(attempt)}
	}
	promoteAudit := s.auditFor(audit)
	enqueueCtx := formatmodule.WithPromotionOptions(repository.WithArtifactPromotion(ctx, provenance), formatmodule.PromotionOptions{Actor: in.Principal.Actor, APTTargetSuite: aptDistributionSuite(in.AptTargetSuite)})
	job, _, err := publisher.EnqueuePromotion(enqueueCtx, source.ID, target.ID, in.Coordinate, in.Digest, jobKey, promoteAudit...)
	if errors.Is(err, repository.ErrIdempotencyConflict) {
		if result, replayed := s.replayPruned(ctx, in, admission.target); replayed {
			return result, nil
		}
		return PromotionResult{}, promotionError(http.StatusConflict, "idempotency_conflict", "Idempotency-Key conflicts with an existing promotion job")
	}
	if err != nil {
		return PromotionResult{}, promotionError(http.StatusInternalServerError, "internal_error", "enqueue promotion job failed")
	}
	row, err := s.promotions.GetArtifactPromotionByJob(ctx, job.ID)
	if err != nil {
		return PromotionResult{}, promotionError(http.StatusInternalServerError, "internal_error", "get promotion provenance failed")
	}
	return PromotionResult{Job: job, Promotion: row}, nil
}

// replayPruned answers a request whose Idempotency-Key has a provenance row but whose lifecycle job was pruned: the
// enqueue then wrote a fresh job that collided with the row's unique key and was rolled back. The identical request
// (same format, source, coordinate and digest) is accepted with the stored outcome and nothing is enqueued; a key
// reused for anything else, or whose job still exists, stays a conflict. The provenance row outlives its job, so the
// job is rebuilt from the row.
func (s PromotionService) replayPruned(ctx context.Context, in PromotionRequest, target repository.HostedRepository) (PromotionResult, bool) {
	key := promotionIdempotencyKey(target.ID, in.IdempotencyKey)
	if in.Rule != nil {
		key = in.Rule.Key
	}
	rows, err := s.promotions.GetArtifactPromotionsByKeys(ctx, []string{key})
	row, ok := rows[key]
	if err != nil || !ok || row.JobID == "" || row.Format != in.Source.Format || row.Coordinate != in.Coordinate || row.Digest != in.Digest || row.OriginRepository != in.Source.Name {
		return PromotionResult{}, false
	}
	if _, err = s.store.GetLifecycleJob(ctx, row.TargetRepositoryID, row.JobID); !errors.Is(err, repository.ErrNotFound) {
		return PromotionResult{}, false
	}
	job := repository.LifecycleJob{ID: row.JobID, RepositoryID: row.TargetRepositoryID, Kind: repository.LifecycleJobPromotion,
		IdempotencyKey: in.IdempotencyKey, State: lifecycleStateForPromotion(row.State), CreatedAt: row.CreatedAt, CompletedAt: row.CompletedAt}
	return PromotionResult{Job: job, Promotion: row}, true
}

// lifecycleStateForPromotion is the job state a provenance state stands for when the job itself was pruned.
func lifecycleStateForPromotion(state repository.ArtifactPromotionState) repository.LifecycleJobState {
	switch state {
	case repository.ArtifactPromotionRunning:
		return repository.LifecycleJobRunning
	case repository.ArtifactPromotionCompleted:
		return repository.LifecycleJobCompleted
	case repository.ArtifactPromotionFailed, repository.ArtifactPromotionRejected, repository.ArtifactPromotionExpired:
		return repository.LifecycleJobFailed
	case repository.ArtifactPromotionCancelled:
		return repository.LifecycleJobCancelled
	}
	return repository.LifecycleJobPending
}

// promotionIdempotencyKey scopes a caller's Idempotency-Key to the target: provenance keys are unique across the
// registry, a caller's key only within its target repository.
func promotionIdempotencyKey(targetID, key string) string { return "manual:" + targetID + ":" + key }

// auditFor returns the audit records handed to the enqueue, or none when audit is not configured.
func (s PromotionService) auditFor(record repository.AuditRecord) []repository.AuditRecord {
	if s.audit == nil {
		return nil
	}
	return []repository.AuditRecord{managementAudit(record)}
}

// ruleAttempt decides which attempt of a rule promotion a request is. The newest attempt recorded under the rule's key
// decides: none means attempt 1; a failed, rejected, expired or cancelled one means the next attempt (a retry, linked to
// the earlier rows by the shared key and the attempt counter); a queued, running, pending-approval or completed one is a
// replay, answered with that row and its job so nothing is queued twice.
func (s PromotionService) ruleAttempt(ctx context.Context, in PromotionRequest) (int, *PromotionResult, *PromotionError) {
	rows, err := s.promotions.GetArtifactPromotionsByKeys(ctx, []string{in.Rule.Key})
	if err != nil {
		return 0, nil, promotionError(http.StatusInternalServerError, "internal_error", "get promotion provenance failed")
	}
	row, found := rows[in.Rule.Key]
	if !found {
		return 1, nil, nil
	}
	switch row.State {
	case repository.ArtifactPromotionFailed, repository.ArtifactPromotionRejected, repository.ArtifactPromotionExpired, repository.ArtifactPromotionCancelled:
		return row.Attempt + 1, nil, nil
	}
	return 0, &PromotionResult{Job: repository.LifecycleJob{ID: row.JobID, RepositoryID: row.TargetRepositoryID, Kind: repository.LifecycleJobPromotion,
		IdempotencyKey: in.Rule.Key, State: lifecycleStateForPromotion(row.State), CreatedAt: row.CreatedAt, CompletedAt: row.CompletedAt}, Promotion: row}, nil
}
