package app

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	adminopenapi "github.com/mlops-ca/notarx/internal/admin/openapi"
	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/promotionrules"
	"github.com/mlops-ca/notarx/internal/repository"
)

// Bounds of a dry run: the examination cap keeps one request from walking an arbitrarily large repository, and the
// page size is the projection read size.
const (
	promotionDryRunMaxExamined = 5000
	promotionDryRunPageSize    = 200
	promotionDryRunDefaultList = 100
	promotionDryRunMaxList     = 500
)

// promotionDryRunPorts are the read-only ports a dry run uses. Nothing here can enqueue a promotion: the evaluation
// never reaches PromotionService.
type promotionDryRunPorts struct {
	securityPolicies repository.RepositorySecurityPolicyStore
	intelligence     repository.ArtifactIntelligenceStore
	search           repository.ArtifactSearchStore
	rawVersions      repository.NativeRawVersionStore
	// quarantine is the store handed to repository.ArtifactDistributionAllowedForDigests.
	quarantine any
	// distributionDigests lists the digests whose quarantine decides the distribution of an artifact.
	distributionDigests func(ctx context.Context, source repository.HostedRepository, coordinate, digest string) ([]string, error)
	now                 func() time.Time
}

// dryRunCandidate is one artifact of the source repository offered to the match.
type dryRunCandidate struct {
	promotionrules.Candidate
	Digest    string
	CreatedAt time.Time
}

func (h promotionRuleHandler) DryRunPromotionRule(w http.ResponseWriter, r *http.Request, ruleID adminopenapi.PromotionRuleId, params adminopenapi.DryRunPromotionRuleParams) {
	principal, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	limit := promotionDryRunDefaultList
	if params.Limit != nil {
		limit = *params.Limit
	}
	if limit < 1 || limit > promotionDryRunMaxList {
		writeHostedProblem(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and "+strconv.Itoa(promotionDryRunMaxList))
		return
	}
	rule, source, ok := h.loadRule(w, r, principal, ruleID)
	if !ok {
		return
	}
	run := dryRun{handler: h, source: source, remote: rule.TargetKind == repository.PromotionTargetRemote}
	if run.remote {
		if !principal.Admin {
			writeHostedProblem(w, http.StatusForbidden, "access_denied", "administrator permission is required for a remote target")
			return
		}
	} else {
		target, found := h.loadRepository(w, r, rule.TargetRepositoryID)
		if !found || !h.authorizeAdmin(w, r, principal, target) {
			return
		}
		run.target = target
	}
	match, err := promotionrules.ParseMatch(rule.Match)
	if err == nil {
		run.conditions, err = promotionrules.ParseConditions(rule.Conditions)
	}
	if err == nil {
		run.minAge, err = run.conditions.MinAgeDuration()
	}
	if err == nil {
		run.match, err = match.Compile()
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "stored promotion rule is invalid")
		return
	}
	if governance, has := formatmodule.Capability[formatmodule.Governance](h.registry, source.Format); has {
		run.validCoordinate = governance.ValidCoordinate
	}
	if tagged, has := formatmodule.Capability[formatmodule.Tagged](h.registry, source.Format); has && run.match.HasRefs() {
		run.tagged = tagged
	}
	ports := h.dryRunPorts
	run.ports = ports
	if ports.now != nil {
		run.now = ports.now()
	} else {
		run.now = time.Now()
	}
	if !run.remote && run.conditions.SecurityPolicy != "" {
		if run.policy, err = ports.securityPolicies.GetRepositorySecurityPolicy(r.Context(), run.target.ID); err != nil {
			writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "get security policy failed")
			return
		}
	}
	result, err := run.execute(r.Context(), limit)
	if errors.Is(err, errDryRunUnavailable) {
		writeHostedProblem(w, http.StatusNotImplemented, "not_supported", "artifact listing is unavailable for this repository")
		return
	}
	if err != nil {
		writeHostedProblem(w, http.StatusInternalServerError, "internal_error", "evaluate promotion rule failed")
		return
	}
	result.RuleId, result.RuleVersion = uuid.MustParse(rule.ID), rule.Version
	writeNativeMavenJSON(w, http.StatusOK, result)
}

var errDryRunUnavailable = errors.New("artifact listing is unavailable")

// dryRun is one evaluation of a rule against the current artifacts of its source.
type dryRun struct {
	handler         promotionRuleHandler
	ports           promotionDryRunPorts
	source, target  repository.HostedRepository
	remote          bool
	match           *promotionrules.Compiled
	conditions      promotionrules.Conditions
	minAge          time.Duration
	policy          repository.RepositorySecurityPolicy
	validCoordinate func(string) bool
	tagged          formatmodule.Tagged
	now             time.Time
}

func (d *dryRun) execute(ctx context.Context, limit int) (adminopenapi.PromotionRuleDryRun, error) {
	result := adminopenapi.PromotionRuleDryRun{Items: []adminopenapi.PromotionRuleDryRunItem{}}
	examined, truncated := 0, false
	err := d.candidates(ctx, func(candidate dryRunCandidate) (bool, error) {
		examined++
		if examined > promotionDryRunMaxExamined {
			truncated = true
			return false, nil
		}
		if candidate.Digest == "" {
			return true, nil
		}
		if d.tagged != nil {
			tags, tagErr := d.tagged.Tags(ctx, d.source.ID, candidate.Coordinate, candidate.Digest)
			if tagErr != nil && !errors.Is(tagErr, repository.ErrNotFound) {
				return false, tagErr
			}
			candidate.Refs = tags
		}
		if !d.match.Matches(candidate.Candidate, d.validCoordinate) {
			return true, nil
		}
		result.Matched++
		if len(result.Items) >= limit {
			truncated = true
			return true, nil
		}
		verdicts, found, verdictErr := d.verdicts(ctx, candidate)
		if verdictErr != nil {
			return false, verdictErr
		}
		if !found {
			result.Matched--
			return true, nil
		}
		item := adminopenapi.PromotionRuleDryRunItem{Coordinate: candidate.Coordinate, Digest: candidate.Digest, Outcome: adminopenapi.PromotionRuleDryRunItemOutcome(promotionrules.Outcome(verdicts)),
			Conditions: make([]adminopenapi.PromotionRuleConditionVerdict, 0, len(verdicts))}
		if candidate.Version != "" {
			version := candidate.Version
			item.Version = &version
		}
		for _, verdict := range verdicts {
			entry := adminopenapi.PromotionRuleConditionVerdict{Condition: adminopenapi.PromotionRuleConditionName(verdict.Condition), Verdict: adminopenapi.PromotionRuleConditionVerdictVerdict(verdict.Verdict)}
			if verdict.Reason != "" {
				reason := verdict.Reason
				entry.Reason = &reason
			}
			item.Conditions = append(item.Conditions, entry)
		}
		result.Items = append(result.Items, item)
		return true, nil
	})
	if err != nil {
		return adminopenapi.PromotionRuleDryRun{}, err
	}
	if examined > promotionDryRunMaxExamined {
		examined = promotionDryRunMaxExamined
	}
	result.Examined, result.Truncated = int32(examined), truncated
	return result, nil
}

// candidates walks the visible artifacts of the source repository and calls yield for each until it answers false. A
// versioned generic repository lists its sealed versions (coordinate name@version, digest the manifest digest, the
// properties frozen at seal); every other format reads the artifact search projection.
func (d *dryRun) candidates(ctx context.Context, yield func(dryRunCandidate) (bool, error)) error {
	if d.source.Format == repository.FormatRaw && d.source.RawLayout == repository.RawLayoutVersioned {
		return d.versionedCandidates(ctx, yield)
	}
	if d.ports.search == nil {
		return errDryRunUnavailable
	}
	after := repository.ArtifactSearchPosition{}
	for {
		items, err := d.ports.search.SearchArtifactProjection(ctx, d.source.ID, d.source.Format, repository.ArtifactSearchQuery{Mode: repository.ArtifactSearchByCoordinate}, promotionDryRunPageSize, after)
		if err != nil {
			return err
		}
		for _, item := range items {
			candidate := dryRunCandidate{Candidate: promotionrules.Candidate{Coordinate: item.Coordinate, Version: item.Version}, Digest: item.Digest}
			if item.CreatedAt != nil {
				candidate.CreatedAt = *item.CreatedAt
			}
			if more, yieldErr := yield(candidate); yieldErr != nil || !more {
				return yieldErr
			}
			after = repository.ArtifactSearchPosition{Coordinate: item.Coordinate, BuildNumber: item.BuildNumber, Digest: item.Digest}
		}
		if len(items) < promotionDryRunPageSize {
			return nil
		}
	}
}

func (d *dryRun) versionedCandidates(ctx context.Context, yield func(dryRunCandidate) (bool, error)) error {
	if d.ports.rawVersions == nil {
		return errDryRunUnavailable
	}
	afterPackage := ""
	for {
		packages, err := d.ports.rawVersions.ListRawPackages(ctx, d.source.ID, afterPackage, promotionDryRunPageSize)
		if err != nil {
			return err
		}
		for _, pkg := range packages {
			var cursor *repository.RawVersionCursor
			for {
				versions, listErr := d.ports.rawVersions.ListRawVersions(ctx, repository.RawVersionQuery{RepositoryID: d.source.ID, Name: pkg.Name, State: repository.RawVersionSealed, After: cursor, Limit: promotionDryRunPageSize})
				if listErr != nil {
					return listErr
				}
				for _, version := range versions {
					candidate := dryRunCandidate{Candidate: promotionrules.Candidate{Coordinate: version.Coordinate(), Version: version.Version, Properties: version.Properties}, Digest: version.ManifestDigest, CreatedAt: version.SealedAt}
					if more, yieldErr := yield(candidate); yieldErr != nil || !more {
						return yieldErr
					}
				}
				if len(versions) < promotionDryRunPageSize {
					break
				}
				last := versions[len(versions)-1]
				cursor = &repository.RawVersionCursor{SealedAt: last.SealedAt, ID: last.ID}
			}
			afterPackage = pkg.Name
		}
		if len(packages) < promotionDryRunPageSize {
			return nil
		}
	}
}

// verdicts evaluates every condition for one matched artifact. found is false when the artifact vanished since it was
// listed.
func (d *dryRun) verdicts(ctx context.Context, candidate dryRunCandidate) (verdicts []promotionrules.ConditionVerdict, found bool, err error) {
	digests, err := d.ports.distributionDigests(ctx, d.source, candidate.Coordinate, candidate.Digest)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	allowed, err := repository.ArtifactDistributionAllowedForDigests(ctx, d.ports.quarantine, d.source.ID, d.source.Format, candidate.Coordinate, digests)
	if err != nil {
		return nil, false, err
	}
	verdicts = append(verdicts, quarantineVerdict(allowed))
	conditions := d.conditions
	needsIntelligence := conditions.SecurityPolicy != "" || (conditions.Scan != nil && conditions.Scan.Completed) || conditions.RequireSignature || conditions.RequireSBOM || conditions.RequireProvenance
	var intelligence *repository.ArtifactIntelligence
	if needsIntelligence {
		value, lookupErr := d.ports.intelligence.GetArtifactIntelligence(ctx, d.source.ID, d.source.Format, candidate.Coordinate, candidate.Digest)
		switch {
		case lookupErr == nil:
			intelligence = &value
		case !errors.Is(lookupErr, repository.ErrNotFound):
			return nil, false, lookupErr
		}
	}
	if conditions.SecurityPolicy != "" {
		verdicts = append(verdicts, d.securityPolicyVerdict(intelligence))
	}
	if conditions.Scan != nil && conditions.Scan.Completed {
		verdicts = append(verdicts, scanVerdict(conditions.Scan, intelligence))
	}
	if d.minAge > 0 {
		verdicts = append(verdicts, d.minAgeVerdict(candidate.CreatedAt))
	}
	if conditions.RequireSignature {
		verdicts = append(verdicts, presenceVerdict("requireSignature", intelligence != nil && len(intelligence.Signatures) > 0, "signature_required"))
	}
	if conditions.RequireSBOM {
		verdicts = append(verdicts, presenceVerdict("requireSbom", intelligence != nil && len(intelligence.SBOMs) > 0, "sbom_required"))
	}
	if conditions.RequireProvenance {
		verdicts = append(verdicts, presenceVerdict("requireProvenance", intelligence != nil && intelligence.Provenance != nil, "provenance_required"))
	}
	return verdicts, true, nil
}

func quarantineVerdict(allowed bool) promotionrules.ConditionVerdict {
	if allowed {
		return promotionrules.ConditionVerdict{Condition: "notQuarantined", Verdict: promotionrules.VerdictPass}
	}
	return promotionrules.ConditionVerdict{Condition: "notQuarantined", Verdict: promotionrules.VerdictFail, Reason: repository.ArtifactQuarantinedReason}
}

// securityPolicyVerdict evaluates the target's admission policy now. The authoritative evaluation runs again inside the
// promotion job; a remote target's policy belongs to the remote instance and cannot be evaluated here.
func (d *dryRun) securityPolicyVerdict(intelligence *repository.ArtifactIntelligence) promotionrules.ConditionVerdict {
	if d.remote {
		return promotionrules.ConditionVerdict{Condition: "securityPolicy", Verdict: promotionrules.VerdictNotEvaluated, Reason: "remote_target_policy"}
	}
	evaluation := repository.EvaluateRepositorySecurityPolicy(d.policy, intelligence)
	if evaluation.Allowed {
		return promotionrules.ConditionVerdict{Condition: "securityPolicy", Verdict: promotionrules.VerdictPass}
	}
	return promotionrules.ConditionVerdict{Condition: "securityPolicy", Verdict: promotionrules.VerdictFail, Reason: strings.Join(evaluation.Reasons, ",")}
}

// scanVerdict reads the vulnerability summary of the artifact: a scan against a stale database does not count as
// completed, and the worst severity with findings must not exceed maxSeverity.
func scanVerdict(scan *promotionrules.Scan, intelligence *repository.ArtifactIntelligence) promotionrules.ConditionVerdict {
	fail := func(reason string) promotionrules.ConditionVerdict {
		return promotionrules.ConditionVerdict{Condition: "scan", Verdict: promotionrules.VerdictFail, Reason: reason}
	}
	if intelligence == nil || intelligence.Vulnerability == nil {
		return fail("scan_not_completed")
	}
	summary := intelligence.Vulnerability
	switch summary.Status {
	case "clean", "vulnerable":
	case "stale_db":
		return fail("scan_database_stale")
	default:
		return fail("scan_not_completed")
	}
	counts := map[string]int{"critical": summary.Critical, "high": summary.High, "medium": summary.Medium, "low": summary.Low}
	if scan.MaxSeverity != "" && promotionrules.SeverityExceeds(scan.MaxSeverity, counts) {
		return fail("severity_above_" + scan.MaxSeverity)
	}
	return promotionrules.ConditionVerdict{Condition: "scan", Verdict: promotionrules.VerdictPass}
}

func (d *dryRun) minAgeVerdict(publishedAt time.Time) promotionrules.ConditionVerdict {
	if publishedAt.IsZero() {
		return promotionrules.ConditionVerdict{Condition: "minAge", Verdict: promotionrules.VerdictNotEvaluated, Reason: "publication_time_unknown"}
	}
	if d.now.Sub(publishedAt) < d.minAge {
		return promotionrules.ConditionVerdict{Condition: "minAge", Verdict: promotionrules.VerdictFail, Reason: "younger_than_min_age"}
	}
	return promotionrules.ConditionVerdict{Condition: "minAge", Verdict: promotionrules.VerdictPass}
}

func presenceVerdict(condition string, present bool, reason string) promotionrules.ConditionVerdict {
	if present {
		return promotionrules.ConditionVerdict{Condition: condition, Verdict: promotionrules.VerdictPass}
	}
	return promotionrules.ConditionVerdict{Condition: condition, Verdict: promotionrules.VerdictFail, Reason: reason}
}
