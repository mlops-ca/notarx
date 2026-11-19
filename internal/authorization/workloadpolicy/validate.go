package workloadpolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
)

// ValidateOptions carries acknowledgements that are part of a write request but not
// of the stored policy.
type ValidateOptions struct {
	// AllowAnyRef acknowledges that no ref, environment or workflow restriction is
	// wanted for a github_actions or gitlab_ci policy.
	AllowAnyRef bool
}

// ValidationError reports a policy that must not be stored.
type ValidationError struct{ Reason string }

func (e *ValidationError) Error() string { return "invalid workload trust policy: " + e.Reason }

func invalidf(format string, args ...any) error {
	return &ValidationError{Reason: fmt.Sprintf(format, args...)}
}

// kubernetesSubject is system:serviceaccount:<namespace>:<name> with DNS-shaped parts.
var kubernetesSubject = regexp.MustCompile(`^system:serviceaccount:[a-z0-9]([-a-z0-9]*[a-z0-9])?:[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// Validate checks a condition list for the issuer kind and returns it in canonical
// form (claims as arrays of keys) with the parsed conditions. It rejects policies
// that lack the immutable-identity conditions of the kind: a policy satisfied by an
// owner or namespace id alone would let any workflow of that owner, including
// pull_request_target workflows holding id-token, publish as the service account.
func Validate(kind Kind, raw []byte, options ValidateOptions) (json.RawMessage, []Condition, error) {
	switch kind {
	case KindGitHubActions, KindGitLabCI, KindKubernetes, KindGeneric:
	default:
		return nil, nil, invalidf("unknown issuer kind %q", kind)
	}
	conditions, err := ParseConditions(kind, raw)
	if err != nil {
		return nil, nil, invalidf("%v", err)
	}
	if err := checkMandatory(kind, conditions, options); err != nil {
		return nil, nil, err
	}
	canonical, err := Canonical(conditions)
	if err != nil {
		return nil, nil, err
	}
	return canonical, conditions, nil
}

// find returns the conditions on a top-level claim.
func find(conditions []Condition, claim string) []Condition {
	var found []Condition
	for _, condition := range conditions {
		if len(condition.Claim) == 1 && condition.Claim[0] == claim {
			found = append(found, condition)
		}
	}
	return found
}

// exactID reports an eq or in condition on claim: identity claims are compared exactly,
// never by glob or prefix, so a policy cannot widen to a whole owner or namespace.
func exactID(conditions []Condition, claim string) bool {
	return slices.ContainsFunc(find(conditions, claim), func(c Condition) bool { return c.Op == OpEq || c.Op == OpIn })
}

func has(conditions []Condition, claim string) bool { return len(find(conditions, claim)) > 0 }

func checkMandatory(kind Kind, conditions []Condition, options ValidateOptions) error {
	switch kind {
	case KindGitHubActions:
		if !exactID(conditions, "repository_id") && !(exactID(conditions, "repository_owner_id") && exactID(conditions, "repository")) {
			return invalidf("github_actions policies need an eq or in condition on repository_id, or on repository_owner_id together with an exact repository")
		}
		if !options.AllowAnyRef && !has(conditions, "ref") && !has(conditions, "environment") && !has(conditions, "job_workflow_ref") {
			return invalidf("github_actions policies need a ref, environment or job_workflow_ref condition, or allow_any_ref")
		}
	case KindGitLabCI:
		if !exactID(conditions, "project_id") && !(exactID(conditions, "namespace_id") && exactID(conditions, "project_path")) {
			return invalidf("gitlab_ci policies need an eq or in condition on project_id, or on namespace_id together with an exact project_path")
		}
		protected := slices.ContainsFunc(find(conditions, "ref_protected"), func(c Condition) bool {
			return c.Op == OpEq && c.Values[0] == "true"
		})
		if !options.AllowAnyRef && !protected && !has(conditions, "ref") {
			return invalidf("gitlab_ci policies need ref_protected eq \"true\" or a ref condition, or allow_any_ref")
		}
	case KindKubernetes:
		if !slices.ContainsFunc(find(conditions, "sub"), func(c Condition) bool {
			return c.Op == OpEq && kubernetesSubject.MatchString(c.Values[0])
		}) {
			return invalidf("kubernetes policies need sub eq system:serviceaccount:<namespace>:<name>")
		}
	case KindGeneric:
		if !has(conditions, "sub") {
			return invalidf("generic policies need a condition on sub")
		}
	}
	return nil
}

var (
	// ErrNoMatch means no policy of the issuer matched the claims.
	ErrNoMatch = errors.New("no workload trust policy matches")
	// ErrAmbiguous means policies bound to different service accounts matched; the
	// caller must reject the token rather than choose between accounts.
	ErrAmbiguous = errors.New("workload token matches trust policies of different service accounts")
)
