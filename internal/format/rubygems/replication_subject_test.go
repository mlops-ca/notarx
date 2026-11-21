package rubygems

import (
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/repository"
)

func TestReplicationSubjectDropsWorkloadBearerExpiry(t *testing.T) {
	until := time.Now().Add(5 * time.Minute)
	workload := replicationSubject(repository.RubyGemsGovernanceIdentity{Actor: "service-account:x", Kind: "workload_federation", ValidUntil: until})
	if !workload.ValidUntil.IsZero() {
		t.Fatalf("workload job carries the bearer expiry %v", workload.ValidUntil)
	}
	apiKey := replicationSubject(repository.RubyGemsGovernanceIdentity{Actor: "api-key:x", Kind: "api_key", ValidUntil: until})
	if !apiKey.ValidUntil.Equal(until) {
		t.Fatalf("api key job lost its credential expiry: %v", apiKey.ValidUntil)
	}
}
