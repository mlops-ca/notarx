package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/partitions"
	"github.com/mlops-ca/notarx/internal/repository"
)

type retentionPolicies struct {
	policy repository.AuditRetentionPolicy
	err    error
}

func (p retentionPolicies) GetAuditRetentionPolicy(context.Context) (repository.AuditRetentionPolicy, error) {
	return p.policy, p.err
}

func TestAuditRetentionPolicyOverridesResolverPartitionRetention(t *testing.T) {
	registry := map[string]partitions.Spec{}
	for _, spec := range partitions.DefaultRegistry() {
		registry[spec.Table] = spec
	}
	resolver, security := registry["resolver_audit_log"], registry["security_audit_log"]
	cases := []struct {
		name   string
		policy repository.AuditRetentionPolicy
		spec   partitions.Spec
		want   time.Duration
	}{
		{"enabled and longer", repository.AuditRetentionPolicy{Enabled: true, KeepDays: 365}, resolver, 365 * 24 * time.Hour},
		{"enabled and shorter", repository.AuditRetentionPolicy{Enabled: true, KeepDays: 7}, resolver, 7 * 24 * time.Hour},
		{"disabled keeps the default", repository.AuditRetentionPolicy{Enabled: false, KeepDays: 365}, resolver, 90 * 24 * time.Hour},
		{"enabled without days keeps the default", repository.AuditRetentionPolicy{Enabled: true}, resolver, 90 * 24 * time.Hour},
		{"security table ignores the policy", repository.AuditRetentionPolicy{Enabled: true, KeepDays: 7}, security, 730 * 24 * time.Hour},
	}
	for _, c := range cases {
		got, err := auditRetentionFor(retentionPolicies{policy: c.policy})(context.Background(), c.spec)
		if err != nil || got != c.want {
			t.Errorf("%s: retention = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	failure := errors.New("database unavailable")
	if _, err := auditRetentionFor(retentionPolicies{err: failure})(context.Background(), resolver); !errors.Is(err, failure) {
		t.Fatalf("a policy lookup failure must be returned, got %v", err)
	}
	if _, err := auditRetentionFor(retentionPolicies{err: failure})(context.Background(), security); err != nil {
		t.Fatalf("the security table never reads the policy: %v", err)
	}
}
