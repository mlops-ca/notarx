package workloadpolicy

import "strings"

// ReferencedClaims returns the claims a policy's conditions refer to, rendered in the scalar
// form the conditions compare against, for the audit trail of a successful match (ADR 0019,
// "Audit"). Only claims named by a condition are returned, never the whole token, the result
// holds at most MaxConditions entries and a value is cut to MaxValueBytes.
func ReferencedClaims(policy Policy, claims map[string]any) map[string]string {
	out := make(map[string]string, len(policy.Conditions))
	for _, condition := range policy.Conditions {
		raw, found := condition.Claim.lookup(claims)
		if !found {
			continue
		}
		value, ok := scalar(raw)
		if !ok {
			continue
		}
		if len(value) > MaxValueBytes {
			value = strings.ToValidUTF8(value[:MaxValueBytes], "")
		}
		out[condition.Claim.String()] = value
	}
	return out
}
