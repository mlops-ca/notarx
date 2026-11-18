package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/partitions"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/retention"
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
		got, err := auditRetentionFor(retentionPolicies{policy: c.policy}, retention.DefaultConfig())(context.Background(), c.spec)
		if err != nil || got != c.want {
			t.Errorf("%s: retention = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
	failure := errors.New("database unavailable")
	if _, err := auditRetentionFor(retentionPolicies{err: failure}, retention.DefaultConfig())(context.Background(), resolver); !errors.Is(err, failure) {
		t.Fatalf("a policy lookup failure must be returned, got %v", err)
	}
	if _, err := auditRetentionFor(retentionPolicies{err: failure}, retention.DefaultConfig())(context.Background(), security); err != nil {
		t.Fatalf("the security table never reads the policy: %v", err)
	}
}

func TestRetentionConfigDrivesAuditPartitionRetention(t *testing.T) {
	registry := map[string]partitions.Spec{}
	for _, spec := range partitions.DefaultRegistry() {
		registry[spec.Table] = spec
	}
	resolver, security := registry["resolver_audit_log"], registry["security_audit_log"]
	pristine := repository.AuditRetentionPolicy{Enabled: true, KeepDays: 90, Version: "1"}
	free := retention.DefaultConfig()
	free.Profile = retention.FreeTier
	overridden := retention.DefaultConfig()
	overridden.Overrides["security_audit_log"] = retention.Override{Keep: 1000 * 24 * time.Hour}
	overridden.Overrides["resolver_audit_log"] = retention.Override{Off: true}
	cases := []struct {
		name   string
		policy repository.AuditRetentionPolicy
		cfg    retention.Config
		spec   partitions.Spec
		want   time.Duration
	}{
		{"free tier resolver with the baseline policy", pristine, free, resolver, 7 * 24 * time.Hour},
		{"free tier security", pristine, free, security, 90 * 24 * time.Hour},
		{"an edited policy beats the free tier profile", repository.AuditRetentionPolicy{Enabled: true, KeepDays: 30, Version: "2"}, free, resolver, 30 * 24 * time.Hour},
		{"security override", pristine, overridden, security, 1000 * 24 * time.Hour},
		{"off keeps the resolver table forever", pristine, overridden, resolver, 0},
		{"standard baseline policy", pristine, retention.DefaultConfig(), resolver, 90 * 24 * time.Hour},
	}
	for _, c := range cases {
		got, err := auditRetentionFor(retentionPolicies{policy: c.policy}, c.cfg)(context.Background(), c.spec)
		if err != nil || got != c.want {
			t.Errorf("%s: retention = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
}
