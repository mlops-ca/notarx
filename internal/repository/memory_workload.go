package repository

import (
	"context"
	"encoding/hex"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

type memoryWorkloadState struct {
	issuers  map[string]WorkloadIssuer
	policies map[string]WorkloadTrustPolicy
	jti      map[string]time.Time
}

func (s *MemoryStore) workloadLocked() *memoryWorkloadState {
	if s.workload == nil {
		s.workload = &memoryWorkloadState{issuers: map[string]WorkloadIssuer{}, policies: map[string]WorkloadTrustPolicy{}, jti: map[string]time.Time{}}
	}
	return s.workload
}

func cloneWorkloadIssuer(issuer WorkloadIssuer) WorkloadIssuer {
	issuer.Audiences = slices.Clone(issuer.Audiences)
	issuer.AllowedAlgs = slices.Clone(issuer.AllowedAlgs)
	issuer.StaticJWKS = slices.Clone(issuer.StaticJWKS)
	return issuer
}

func cloneWorkloadPolicy(policy WorkloadTrustPolicy) WorkloadTrustPolicy {
	policy.Conditions = slices.Clone(policy.Conditions)
	if policy.ExpiresAt != nil {
		expires := *policy.ExpiresAt
		policy.ExpiresAt = &expires
	}
	return policy
}

func (w *memoryWorkloadState) issuerConflict(issuer WorkloadIssuer) error {
	for _, existing := range w.issuers {
		if existing.ID == issuer.ID {
			continue
		}
		if existing.Name == issuer.Name {
			return ErrNameExists
		}
		if existing.Issuer == issuer.Issuer {
			return ErrWorkloadIssuerExists
		}
	}
	return nil
}

func (s *MemoryStore) CreateWorkloadIssuer(ctx context.Context, issuer WorkloadIssuer, audit ...AuditRecord) (WorkloadIssuer, error) {
	issuer, err := normalizeWorkloadIssuer(issuer)
	if err != nil {
		return WorkloadIssuer{}, err
	}
	if issuer.ID == "" {
		issuer.ID = ids.New()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.workloadLocked()
	if _, exists := state.issuers[issuer.ID]; exists {
		return WorkloadIssuer{}, ErrNameExists
	}
	if err := state.issuerConflict(issuer); err != nil {
		return WorkloadIssuer{}, err
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return WorkloadIssuer{}, err
	}
	now := time.Now().UTC()
	issuer.Version, issuer.CreatedAt, issuer.UpdatedAt = "1", now, now
	state.issuers[issuer.ID] = cloneWorkloadIssuer(issuer)
	return issuer, nil
}

func (s *MemoryStore) GetWorkloadIssuer(_ context.Context, id string) (WorkloadIssuer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	issuer, ok := s.workloadLocked().issuers[id]
	if !ok {
		return WorkloadIssuer{}, ErrNotFound
	}
	return cloneWorkloadIssuer(issuer), nil
}

func (s *MemoryStore) GetWorkloadIssuerByIssuer(_ context.Context, value string) (WorkloadIssuer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, issuer := range s.workloadLocked().issuers {
		if issuer.Issuer == value {
			return cloneWorkloadIssuer(issuer), nil
		}
	}
	return WorkloadIssuer{}, ErrNotFound
}

func (s *MemoryStore) ListWorkloadIssuers(_ context.Context, limit int, afterID string) ([]WorkloadIssuer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	issuers := make([]WorkloadIssuer, 0)
	for _, issuer := range s.workloadLocked().issuers {
		if afterID == "" || issuer.ID > afterID {
			issuers = append(issuers, cloneWorkloadIssuer(issuer))
		}
	}
	sort.Slice(issuers, func(i, j int) bool { return issuers[i].ID < issuers[j].ID })
	if limit >= 0 && len(issuers) > limit {
		issuers = issuers[:limit]
	}
	return issuers, nil
}

func (s *MemoryStore) UpdateWorkloadIssuer(ctx context.Context, issuer WorkloadIssuer, expectedVersion string, audit ...AuditRecord) (WorkloadIssuer, error) {
	issuer, err := normalizeWorkloadIssuer(issuer)
	if err != nil {
		return WorkloadIssuer{}, err
	}
	version, err := parseWorkloadVersion(expectedVersion)
	if err != nil {
		return WorkloadIssuer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.workloadLocked()
	current, ok := state.issuers[issuer.ID]
	if !ok {
		return WorkloadIssuer{}, ErrNotFound
	}
	if current.Version != formatWorkloadVersion(version) {
		return WorkloadIssuer{}, ErrVersionConflict
	}
	if err := state.issuerConflict(issuer); err != nil {
		return WorkloadIssuer{}, err
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return WorkloadIssuer{}, err
	}
	issuer.CreatedAt, issuer.UpdatedAt = current.CreatedAt, time.Now().UTC()
	issuer.Version = formatWorkloadVersion(version + 1)
	state.issuers[issuer.ID] = cloneWorkloadIssuer(issuer)
	return issuer, nil
}

func (s *MemoryStore) DeleteWorkloadIssuer(ctx context.Context, id, expectedVersion string, audit ...AuditRecord) error {
	version, err := parseWorkloadVersion(expectedVersion)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.workloadLocked()
	current, ok := state.issuers[id]
	if !ok {
		return ErrNotFound
	}
	if current.Version != formatWorkloadVersion(version) {
		return ErrVersionConflict
	}
	for _, policy := range state.policies {
		if policy.IssuerID == id {
			return ErrWorkloadIssuerInUse
		}
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	delete(state.issuers, id)
	return nil
}

func (s *MemoryStore) CreateWorkloadTrustPolicy(ctx context.Context, policy WorkloadTrustPolicy, audit ...AuditRecord) (WorkloadTrustPolicy, error) {
	policy, err := normalizeWorkloadTrustPolicy(policy)
	if err != nil {
		return WorkloadTrustPolicy{}, err
	}
	if policy.ID == "" {
		policy.ID = ids.New()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.workloadLocked()
	if err := s.workloadPolicyConflictLocked(policy); err != nil {
		return WorkloadTrustPolicy{}, err
	}
	if _, exists := state.policies[policy.ID]; exists {
		return WorkloadTrustPolicy{}, ErrNameExists
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return WorkloadTrustPolicy{}, err
	}
	now := time.Now().UTC()
	policy.Version, policy.CreatedAt, policy.UpdatedAt = "1", now, now
	state.policies[policy.ID] = cloneWorkloadPolicy(policy)
	return policy, nil
}

// workloadPolicyConflictLocked checks the unique name and the two foreign keys, in the order the
// database reports them.
func (s *MemoryStore) workloadPolicyConflictLocked(policy WorkloadTrustPolicy) error {
	state := s.workloadLocked()
	for _, existing := range state.policies {
		if existing.ID != policy.ID && existing.Name == policy.Name {
			return ErrNameExists
		}
	}
	if _, ok := state.issuers[policy.IssuerID]; !ok {
		return ErrWorkloadReference
	}
	if _, ok := s.serviceAccounts[policy.ServiceAccountID]; !ok {
		return ErrWorkloadReference
	}
	return nil
}

func (s *MemoryStore) GetWorkloadTrustPolicy(_ context.Context, id string) (WorkloadTrustPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	policy, ok := s.workloadLocked().policies[id]
	if !ok {
		return WorkloadTrustPolicy{}, ErrNotFound
	}
	return cloneWorkloadPolicy(policy), nil
}

func (s *MemoryStore) listWorkloadPoliciesLocked(keep func(WorkloadTrustPolicy) bool) []WorkloadTrustPolicy {
	policies := make([]WorkloadTrustPolicy, 0)
	for _, policy := range s.workloadLocked().policies {
		if keep(policy) {
			policies = append(policies, cloneWorkloadPolicy(policy))
		}
	}
	return policies
}

func (s *MemoryStore) ListWorkloadTrustPolicies(_ context.Context, limit int, afterID string) ([]WorkloadTrustPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	policies := s.listWorkloadPoliciesLocked(func(policy WorkloadTrustPolicy) bool { return afterID == "" || policy.ID > afterID })
	sort.Slice(policies, func(i, j int) bool { return policies[i].ID < policies[j].ID })
	if limit >= 0 && len(policies) > limit {
		policies = policies[:limit]
	}
	return policies, nil
}

func (s *MemoryStore) ListActiveWorkloadTrustPolicies(_ context.Context, issuerID string, now time.Time) ([]WorkloadTrustPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	policies := s.listWorkloadPoliciesLocked(func(policy WorkloadTrustPolicy) bool {
		return policy.IssuerID == issuerID && policy.State == WorkloadActive && (policy.ExpiresAt == nil || policy.ExpiresAt.After(now))
	})
	sort.Slice(policies, func(i, j int) bool { return policies[i].Name < policies[j].Name })
	return policies, nil
}

func (s *MemoryStore) UpdateWorkloadTrustPolicy(ctx context.Context, policy WorkloadTrustPolicy, expectedVersion string, audit ...AuditRecord) (WorkloadTrustPolicy, error) {
	policy, err := normalizeWorkloadTrustPolicy(policy)
	if err != nil {
		return WorkloadTrustPolicy{}, err
	}
	version, err := parseWorkloadVersion(expectedVersion)
	if err != nil {
		return WorkloadTrustPolicy{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.workloadLocked()
	current, ok := state.policies[policy.ID]
	if !ok {
		return WorkloadTrustPolicy{}, ErrNotFound
	}
	if current.Version != formatWorkloadVersion(version) {
		return WorkloadTrustPolicy{}, ErrVersionConflict
	}
	if err := s.workloadPolicyConflictLocked(policy); err != nil {
		return WorkloadTrustPolicy{}, err
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return WorkloadTrustPolicy{}, err
	}
	policy.CreatedAt, policy.UpdatedAt = current.CreatedAt, time.Now().UTC()
	policy.Version = formatWorkloadVersion(version + 1)
	state.policies[policy.ID] = cloneWorkloadPolicy(policy)
	return policy, nil
}

func (s *MemoryStore) DeleteWorkloadTrustPolicy(ctx context.Context, id, expectedVersion string, audit ...AuditRecord) error {
	version, err := parseWorkloadVersion(expectedVersion)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.workloadLocked()
	current, ok := state.policies[id]
	if !ok {
		return ErrNotFound
	}
	if current.Version != formatWorkloadVersion(version) {
		return ErrVersionConflict
	}
	if err := s.commitAuditLocked(ctx, audit); err != nil {
		return err
	}
	delete(state.policies, id)
	return nil
}

// RecordWorkloadJTI mirrors the PostgreSQL table: a recorded pair conflicts until it is deleted,
// even after expires_at, because deletion belongs to the retention janitor.
func (s *MemoryStore) RecordWorkloadJTI(_ context.Context, issuerID string, jtiHash []byte, expiresAt time.Time) (bool, error) {
	if err := checkJTIArguments(issuerID, jtiHash); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := issuerID + ":" + hex.EncodeToString(jtiHash)
	state := s.workloadLocked()
	if _, seen := state.jti[key]; seen {
		return false, nil
	}
	state.jti[key] = expiresAt
	return true, nil
}

func formatWorkloadVersion(version int64) string { return strconv.FormatInt(version, 10) }
