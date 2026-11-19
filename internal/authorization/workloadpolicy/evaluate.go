package workloadpolicy

import (
	"fmt"
	"slices"
	"strings"
)

// Policy is a trust policy ready to evaluate.
type Policy struct {
	ID               string
	Name             string
	ServiceAccountID string
	Conditions       []Condition
}

// NewPolicy parses the stored conditions of a policy of an issuer of the given kind.
func NewPolicy(kind Kind, id, name, serviceAccountID string, conditions []byte) (Policy, error) {
	parsed, err := ParseConditions(kind, conditions)
	if err != nil {
		return Policy{}, fmt.Errorf("policy %s: %w", name, err)
	}
	return Policy{ID: id, Name: name, ServiceAccountID: serviceAccountID, Conditions: parsed}, nil
}

// NoMatchError carries the operator-only detail of a failed evaluation: for every
// policy, its first failing condition. It matches ErrNoMatch.
type NoMatchError struct{ Failures []Failure }

func (e *NoMatchError) Error() string { return ErrNoMatch.Error() }

func (e *NoMatchError) Is(target error) bool { return target == ErrNoMatch }

// AmbiguousError names the policies of different service accounts that matched. It matches ErrAmbiguous.
type AmbiguousError struct{ Policies []string }

func (e *AmbiguousError) Error() string {
	return ErrAmbiguous.Error() + ": " + strings.Join(e.Policies, ", ")
}

func (e *AmbiguousError) Is(target error) bool { return target == ErrAmbiguous }

// Evaluate returns the policy that authorises the claims of a verified token. The
// subject is always required, in addition to the conditions (the audience was
// checked by the verifier). When several policies match and all bind the same
// service account, the first by name wins, deterministically; when they bind
// different accounts the result is an AmbiguousError and nothing is selected.
func Evaluate(policies []Policy, claims map[string]any) (Policy, error) {
	var matched []Policy
	var failures []Failure
	subject, _ := claims["sub"].(string)
	for _, policy := range policies {
		if subject == "" {
			failures = append(failures, Failure{Policy: policy.Name, Condition: -1, Claim: ClaimPath{"sub"}, Reason: "claim is missing"})
			continue
		}
		if failure, ok := firstFailure(policy, claims); ok {
			failures = append(failures, failure)
			continue
		}
		matched = append(matched, policy)
	}
	switch {
	case len(matched) == 0:
		return Policy{}, &NoMatchError{Failures: failures}
	case len(matched) == 1:
		return matched[0], nil
	}
	slices.SortFunc(matched, func(a, b Policy) int {
		if byName := strings.Compare(a.Name, b.Name); byName != 0 {
			return byName
		}
		return strings.Compare(a.ID, b.ID)
	})
	for _, policy := range matched[1:] {
		if policy.ServiceAccountID != matched[0].ServiceAccountID {
			names := make([]string, 0, len(matched))
			for _, candidate := range matched {
				names = append(names, candidate.Name)
			}
			return Policy{}, &AmbiguousError{Policies: names}
		}
	}
	return matched[0], nil
}

// firstFailure returns the first condition of the policy that does not hold.
func firstFailure(policy Policy, claims map[string]any) (Failure, bool) {
	for index, condition := range policy.Conditions {
		if ok, reason := condition.match(claims); !ok {
			return Failure{Policy: policy.Name, Condition: index, Claim: condition.Claim, Reason: reason}, true
		}
	}
	return Failure{}, false
}
