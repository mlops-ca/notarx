package promotionrules

import (
	"encoding/json"
	"time"
)

// Severities a scan condition can cap, lowest first.
var severities = []string{"none", "low", "medium", "high", "critical"}

// MaxMinAge bounds the minimum age of a condition.
const MaxMinAge = 365 * 24 * time.Hour

// SecurityPolicyPass is the only value of the securityPolicy condition.
const SecurityPolicyPass = "pass"

// Scan is the scan condition: the artifact has a completed scan, optionally with no finding above MaxSeverity.
type Scan struct {
	Completed   bool   `json:"completed"`
	MaxSeverity string `json:"maxSeverity,omitempty"`
}

// Conditions are the gates of a rule; all of them must hold (ADR 0021, "Rules"). The artifact must also not be
// quarantined, which is always required and so has no member.
type Conditions struct {
	// SecurityPolicy "pass" evaluates the target repository's admission policy against the artifact.
	SecurityPolicy string `json:"securityPolicy,omitempty"`
	Scan           *Scan  `json:"scan,omitempty"`
	// MinAge is the minimum time since publication, a Go duration such as "72h".
	MinAge            string `json:"minAge,omitempty"`
	RequireSignature  bool   `json:"requireSignature,omitempty"`
	RequireSBOM       bool   `json:"requireSbom,omitempty"`
	RequireProvenance bool   `json:"requireProvenance,omitempty"`
}

// ParseConditions decodes a stored or submitted conditions document, refusing unknown members. An empty document is no
// conditions.
func ParseConditions(raw []byte) (Conditions, error) {
	if len(raw) == 0 {
		return Conditions{}, nil
	}
	var conditions Conditions
	if err := decodeStrict(raw, &conditions); err != nil {
		return Conditions{}, invalid("conditions are invalid: %v", err)
	}
	return conditions, nil
}

// Marshal returns the canonical stored form of the conditions.
func (c Conditions) Marshal() ([]byte, error) { return json.Marshal(c) }

// Validate checks every member.
func (c Conditions) Validate() error {
	if c.SecurityPolicy != "" && c.SecurityPolicy != SecurityPolicyPass {
		return invalid("securityPolicy must be %q", SecurityPolicyPass)
	}
	if c.Scan != nil && c.Scan.MaxSeverity != "" && severityRank(c.Scan.MaxSeverity) < 0 {
		return invalid("scan.maxSeverity must be one of %v", severities)
	}
	if c.Scan != nil && !c.Scan.Completed && c.Scan.MaxSeverity != "" {
		return invalid("scan.maxSeverity requires scan.completed")
	}
	if c.MinAge != "" {
		if _, err := c.MinAgeDuration(); err != nil {
			return err
		}
	}
	return nil
}

// MinAgeDuration parses MinAge; zero when unset.
func (c Conditions) MinAgeDuration() (time.Duration, error) {
	if c.MinAge == "" {
		return 0, nil
	}
	age, err := time.ParseDuration(c.MinAge)
	if err != nil || age <= 0 || age > MaxMinAge {
		return 0, invalid("minAge must be a duration between 1s and %s, such as \"72h\"", MaxMinAge)
	}
	return age, nil
}

func severityRank(value string) int {
	for rank, name := range severities {
		if name == value {
			return rank
		}
	}
	return -1
}

// SeverityExceeds reports whether the worst severity counted by a scan is above limit. counts maps a severity to its
// number of findings.
func SeverityExceeds(limit string, counts map[string]int) bool {
	limitRank := severityRank(limit)
	if limitRank < 0 {
		return false
	}
	for severity, count := range counts {
		if count > 0 && severityRank(severity) > limitRank {
			return true
		}
	}
	return false
}

// Verdict of one condition for one artifact.
const (
	VerdictPass         = "pass"
	VerdictFail         = "fail"
	VerdictNotEvaluated = "not_evaluated"
)

// ConditionVerdict is the outcome of one condition for one artifact; Reason is a stable code or a short explanation.
type ConditionVerdict struct {
	Condition string
	Verdict   string
	Reason    string
}

// Outcome of all conditions of one artifact.
const (
	OutcomeEligible     = "eligible"
	OutcomeBlocked      = "blocked"
	OutcomeUndetermined = "undetermined"
)

// Outcome combines condition verdicts: blocked when any fails, undetermined when none fails but one was not evaluated,
// eligible otherwise.
func Outcome(verdicts []ConditionVerdict) string {
	outcome := OutcomeEligible
	for _, verdict := range verdicts {
		switch verdict.Verdict {
		case VerdictFail:
			return OutcomeBlocked
		case VerdictNotEvaluated:
			outcome = OutcomeUndetermined
		}
	}
	return outcome
}
