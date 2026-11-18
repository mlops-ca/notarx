package main

import (
	"context"
	"time"

	"github.com/mlops-ca/notarx/internal/partitions"
	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/retention"
)

// baselinePolicyDays and baselinePolicyVersion identify an audit_retention_policy row
// nobody has edited: the baseline default is enabled with 90 days at version 1.
const (
	baselinePolicyDays    = 90
	baselinePolicyVersion = "1"
)

// auditPolicyReader is the part of the store the override needs.
type auditPolicyReader interface {
	GetAuditRetentionPolicy(context.Context) (repository.AuditRetentionPolicy, error)
}

// auditRetentionFor resolves the retention of a partitioned audit table (ADR 0023
// section 2). resolver_audit_log: a console-edited audit retention policy wins; an
// untouched baseline policy defers to the janitor configuration, so the free-tier
// profile (7 days) and NOTARX_RETENTION_RESOLVER_AUDIT_LOG apply. security_audit_log is
// not affected by the policy: its retention is the janitor configuration alone
// (730 days standard, 90 free tier, NOTARX_RETENTION_SECURITY_AUDIT_LOG). A table
// switched off with the value "off" is kept forever (retention 0).
func auditRetentionFor(policies auditPolicyReader, cfg retention.Config) func(context.Context, partitions.Spec) (time.Duration, error) {
	return func(ctx context.Context, spec partitions.Spec) (time.Duration, error) {
		if spec.Table == "resolver_audit_log" {
			policy, err := policies.GetAuditRetentionPolicy(ctx)
			if err != nil {
				return 0, err
			}
			edited := policy.Enabled && policy.KeepDays > 0 && !(policy.KeepDays == baselinePolicyDays && policy.Version == baselinePolicyVersion)
			if edited {
				return time.Duration(policy.KeepDays) * 24 * time.Hour, nil
			}
		}
		keep, on, err := cfg.KeepFor(spec.Table)
		if err != nil {
			return spec.Retention, nil
		}
		if !on {
			return 0, nil
		}
		return keep, nil
	}
}
