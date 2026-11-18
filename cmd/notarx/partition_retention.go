package main

import (
	"context"
	"time"

	"github.com/mlops-ca/notarx/internal/partitions"
	"github.com/mlops-ca/notarx/internal/repository"
)

// auditRetentionFor lets the console-editable audit retention policy override the
// resolver_audit_log partition retention (ADR 0023 section 2): an enabled policy
// with keepDays sets how long partitions are kept, longer or shorter than the
// default. A disabled policy leaves the default of 90 days. The security audit table
// is not affected by the policy; its retention is the registry default.
// auditPolicyReader is the part of the store the override needs.
type auditPolicyReader interface {
	GetAuditRetentionPolicy(context.Context) (repository.AuditRetentionPolicy, error)
}

func auditRetentionFor(policies auditPolicyReader) func(context.Context, partitions.Spec) (time.Duration, error) {
	return func(ctx context.Context, spec partitions.Spec) (time.Duration, error) {
		if spec.Table != "resolver_audit_log" {
			return spec.Retention, nil
		}
		policy, err := policies.GetAuditRetentionPolicy(ctx)
		if err != nil {
			return 0, err
		}
		if policy.Enabled && policy.KeepDays > 0 {
			return time.Duration(policy.KeepDays) * 24 * time.Hour, nil
		}
		return spec.Retention, nil
	}
}
