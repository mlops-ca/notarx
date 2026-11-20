package workloadpolicy

import (
	"strings"
	"testing"
)

func TestReferencedClaimsReturnsOnlyConditionClaims(t *testing.T) {
	claims := decodeClaims(t, kubernetesToken)
	policy, err := NewPolicy(KindKubernetes, "p1", "builder", "sa", []byte(`[
		{"claim":["sub"],"op":"eq","values":["system:serviceaccount:team-a:builder"]},
		{"claim":["kubernetes.io","namespace"],"op":"eq","values":["team-a"]},
		{"claim":["absent"],"op":"eq","values":["x"]},
		{"claim":["kubernetes.io","pod"],"op":"eq","values":["x"]}]`))
	if err != nil {
		t.Fatal(err)
	}
	got := ReferencedClaims(policy, claims)
	if len(got) != 2 || got[`["sub"]`] != "system:serviceaccount:team-a:builder" || got[`["kubernetes.io","namespace"]`] != "team-a" {
		t.Fatalf("referenced = %v", got)
	}
}

func TestReferencedClaimsCutsLongValues(t *testing.T) {
	long := strings.Repeat("é", MaxValueBytes)
	policy := Policy{Conditions: []Condition{{Claim: ClaimPath{"sub"}, Op: OpEq, Values: []string{"x"}}}}
	got := ReferencedClaims(policy, map[string]any{"sub": long})
	if value := got[`["sub"]`]; len(value) > MaxValueBytes || !strings.HasPrefix(long, value) {
		t.Fatalf("value of %d bytes", len(value))
	}
}
