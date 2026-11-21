// Package promotionfanout decides, at publication time, which promotion rules evaluate a published artifact (ADR 0021,
// Evaluator). events.Emit calls it inside the publishing transaction through events.RuleFanout; the evaluator and the
// in-memory store use the same matching so every path derives the same candidate from a (format, coordinate) pair.
package promotionfanout

import (
	"context"
	"strings"

	"github.com/mlops-ca/notarx/internal/events"
	"github.com/mlops-ca/notarx/internal/promotionrules"
	"github.com/mlops-ca/notarx/internal/readcache"
	"github.com/mlops-ca/notarx/internal/repository"
)

// RuleSource lists the enabled, active rules of a source repository.
type RuleSource interface {
	ListEnabledPromotionRules(ctx context.Context, sourceRepositoryID string) ([]repository.PromotionRule, error)
}

// CoordinateValidator returns the format's canonical-coordinate check, or nil when the format declares none.
type CoordinateValidator func(format repository.Format) func(coordinate string) bool

// CandidateOf is the match candidate of a published artifact, derived from its format and canonical coordinate the way a
// promotion addresses it: name@version for npm, PyPI, Go (the version keeps its leading "v") and Cargo,
// reference#revision for Conan (the version is the second segment of the reference), group:artifact:version for Maven. A
// Raw path has no version here: a flat repository has no version concept, and the version of a versioned repository is
// read from the repository (CandidatesOf, and the evaluator). The tags that point at the artifact and the properties frozen
// at seal are not known at publication; the evaluator reads them when it reloads the artifact.
func CandidateOf(format repository.Format, coordinate string) promotionrules.Candidate {
	candidate := promotionrules.Candidate{Coordinate: coordinate}
	switch format {
	case repository.FormatNPM, repository.FormatPyPI, repository.FormatCargo:
		if at := strings.LastIndex(coordinate, "@"); at > 0 {
			candidate.Version = coordinate[at+1:]
		}
	case repository.FormatGo:
		if at := strings.LastIndex(coordinate, "@"); at > 0 {
			candidate.Version, candidate.ModuleVersion = coordinate[at+1:], true
		}
	case repository.FormatConan:
		reference, _, _ := strings.Cut(coordinate, "#")
		if parts := strings.Split(reference, "/"); len(parts) == 4 {
			candidate.Version = parts[1]
		}
	case repository.FormatMaven:
		if colon := strings.LastIndex(coordinate, ":"); colon > 0 {
			candidate.Version = coordinate[colon+1:]
		}
	}
	return candidate
}

// CandidatesOf is every candidate a published artifact can be matched as. A Raw path of the form name/version/file may be a
// file of a versioned repository, whose coordinate is name@version (ADR 0028); the fan-out does not know the layout of the
// repository, so it offers both readings and the evaluator, which does, decides.
func CandidatesOf(format repository.Format, coordinate string) []promotionrules.Candidate {
	candidates := []promotionrules.Candidate{CandidateOf(format, coordinate)}
	if format == repository.FormatRaw {
		if parsed, err := repository.ParseRawVersionPath(coordinate); err == nil {
			candidates = append(candidates, promotionrules.Candidate{Coordinate: parsed.Name + "@" + parsed.Version, Version: parsed.Version})
		}
	}
	return candidates
}

// compiledRule is a rule ready to match: its compiled match, or nil when the stored match cannot be compiled, in which
// case the rule is offered to the evaluator, which fails the evaluation with a reason instead of the publication.
type compiledRule struct {
	id    string
	match *promotionrules.Compiled
}

func compile(rules []repository.PromotionRule) []compiledRule {
	out := make([]compiledRule, 0, len(rules))
	for _, rule := range rules {
		entry := compiledRule{id: rule.ID}
		if match, err := promotionrules.ParseMatch(rule.Match); err == nil {
			if compiled, compileErr := match.Compile(); compileErr == nil {
				entry.match = compiled
			}
		}
		out = append(out, entry)
	}
	return out
}

// accepts reports whether the rule can match the event. Only what is known at publication is tested (coordinate and
// version); refs and properties are the evaluator's, so a rule that has them is never left out here.
func (c compiledRule) accepts(candidate promotionrules.Candidate, valid func(string) bool) bool {
	if c.match == nil {
		return true
	}
	if valid != nil && !valid(candidate.Coordinate) {
		return false
	}
	versionMatches := c.match.MatchesVersion(candidate.Version)
	if candidate.ModuleVersion {
		versionMatches = c.match.MatchesModuleVersion(candidate.Version)
	}
	return c.match.MatchesCoordinate(candidate.Coordinate) && versionMatches
}

func matching(rules []compiledRule, event events.Event, validate CoordinateValidator) []string {
	format := repository.Format(event.Format)
	var valid func(string) bool
	if validate != nil {
		valid = validate(format)
	}
	candidates := CandidatesOf(format, event.Coordinate)
	var ids []string
	for _, rule := range rules {
		for _, candidate := range candidates {
			if rule.accepts(candidate, valid) {
				ids = append(ids, rule.id)
				break
			}
		}
	}
	return ids
}

// Matcher matches the event against the rules it is given, without a cache. The in-memory store uses it while it holds
// its lock.
type Matcher struct{ Validate CoordinateValidator }

// Match implements repository.PromotionRuleMatcher.
func (m Matcher) Match(_ context.Context, event events.Event, rules []repository.PromotionRule) ([]string, error) {
	return matching(compile(rules), event, m.Validate), nil
}

var _ repository.PromotionRuleMatcher = Matcher{}

// Index is the cached rule set of ADR 0023 section 6: the enabled rules of each source repository, compiled once, held
// for NOTARX_AUTHZ_CACHE_TTL and flushed by the promotion_rule invalidation scope, which every rule change announces
// (migration 000026 triggers and the store's local eviction). A rule created, enabled, disabled, edited or parked
// therefore starts or stops fanning out within the TTL, on every node.
type Index struct {
	source   RuleSource
	validate CoordinateValidator
	cache    *readcache.Cache[string, []compiledRule]
}

// New builds the index on the registry that owns the invalidation scope.
func New(source RuleSource, registry *readcache.Registry, validate CoordinateValidator) *Index {
	return &Index{source: source, validate: validate, cache: readcache.New[string, []compiledRule](registry, readcache.Options[[]compiledRule]{
		Name: "promotion_rules", Scope: repository.CacheScopePromotionRule,
	})}
}

// MatchingRules implements events.RuleFanout.
func (i *Index) MatchingRules(ctx context.Context, event events.Event) ([]string, error) {
	rules, err := i.cache.Get(ctx, event.RepositoryID, func(ctx context.Context) ([]compiledRule, error) {
		stored, err := i.source.ListEnabledPromotionRules(ctx, event.RepositoryID)
		if err != nil {
			return nil, err
		}
		return compile(stored), nil
	})
	if err != nil {
		return nil, err
	}
	return matching(rules, event, i.validate), nil
}

var _ events.RuleFanout = (*Index)(nil)
