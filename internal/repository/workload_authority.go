package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/mlops-ca/notarx/internal/durableactor"
)

// WorkloadAuthority is the current authority of a durable workload_federation actor (ADR 0019,
// "Authentication flow and integration"): the actor is the service account, the credential id the trust policy.
// Active is true only while the policy, its issuer and the service account are all active, the policy names that
// account and it has not expired. ValidUntil is the policy expiry, zero when it has none.
type WorkloadAuthority struct {
	Active     bool
	ValidUntil time.Time
}

func workloadAuthority(policy WorkloadTrustPolicy, issuerState WorkloadState, accountActive bool, now time.Time) WorkloadAuthority {
	a := WorkloadAuthority{}
	if policy.ExpiresAt != nil {
		a.ValidUntil = *policy.ExpiresAt
	}
	a.Active = policy.State == WorkloadActive && issuerState == WorkloadActive && accountActive &&
		(policy.ExpiresAt == nil || now.Before(*policy.ExpiresAt))
	return a
}

// WorkloadAuthorityTx reads the current authority of the workload actor (service account, trust policy id) in tx,
// locking the policy, issuer and account rows so a concurrent disable waits for the commit. An unknown or
// mismatched policy is inactive, not an error.
func WorkloadAuthorityTx(ctx context.Context, tx *sql.Tx, actor, policyID string) (WorkloadAuthority, error) {
	account, ok := durableactor.ServiceAccountID(actor)
	if !ok {
		return WorkloadAuthority{}, nil
	}
	var policyState, issuerState, accountState string
	var expires sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT p.state,p.expires_at,i.state,a.state FROM workload_trust_policies p
JOIN workload_issuers i ON i.id=p.issuer_id JOIN service_accounts a ON a.id=p.service_account_id
WHERE p.id=$1::uuid AND p.service_account_id=$2::uuid FOR SHARE OF p,i,a`, policyID, account).Scan(&policyState, &expires, &issuerState, &accountState)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkloadAuthority{}, nil
	}
	if err != nil {
		return WorkloadAuthority{}, mapMalformedUUID(err)
	}
	policy := WorkloadTrustPolicy{State: WorkloadState(policyState)}
	if expires.Valid {
		policy.ExpiresAt = &expires.Time
	}
	return workloadAuthority(policy, WorkloadState(issuerState), ServiceAccountState(accountState) == ServiceAccountActive, time.Now()), nil
}

// WorkloadAuthorityLocked is WorkloadAuthorityTx for the memory store; the caller holds s.mu.
func (s *MemoryStore) WorkloadAuthorityLocked(actor, policyID string) WorkloadAuthority {
	accountID, ok := durableactor.ServiceAccountID(actor)
	if !ok {
		return WorkloadAuthority{}
	}
	state := s.workloadLocked()
	policy, found := state.policies[workloadKey(policyID)]
	if !found || workloadKey(policy.ServiceAccountID) != workloadKey(accountID) {
		return WorkloadAuthority{}
	}
	issuer, found := state.issuers[workloadKey(policy.IssuerID)]
	if !found {
		return WorkloadAuthority{}
	}
	account, found := s.serviceAccounts[policy.ServiceAccountID]
	if !found {
		return WorkloadAuthority{}
	}
	return workloadAuthority(policy, issuer.State, account.State == ServiceAccountActive, time.Now())
}
