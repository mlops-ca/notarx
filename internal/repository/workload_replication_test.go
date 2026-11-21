package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	helm "github.com/mlops-ca/notarx/internal/helmdistribution"
	nuget "github.com/mlops-ca/notarx/internal/nugetdistribution"
)

type workloadReplicationStore interface {
	CreateServiceAccount(context.Context, ServiceAccount, ...AuditRecord) (ServiceAccount, error)
	UpdateServiceAccount(context.Context, ServiceAccountUpdate, string, ...AuditRecord) (ServiceAccount, error)
	CreateWorkloadIssuer(context.Context, WorkloadIssuer, ...AuditRecord) (WorkloadIssuer, error)
	UpdateWorkloadIssuer(context.Context, WorkloadIssuer, string, ...AuditRecord) (WorkloadIssuer, error)
	CreateWorkloadTrustPolicy(context.Context, WorkloadTrustPolicy, ...AuditRecord) (WorkloadTrustPolicy, error)
	UpdateWorkloadTrustPolicy(context.Context, WorkloadTrustPolicy, string, ...AuditRecord) (WorkloadTrustPolicy, error)
}

var workloadReplicationModes = []string{"workload-policy-disable", "workload-issuer-disable", "workload-service-disable"}

// workloadReplicationActor creates a service account reached through a trust policy and returns the durable
// actor, the policy id (the credential id of the job) and the mutation that ends the actor's authority.
func workloadReplicationActor(t *testing.T, create, mutator workloadReplicationStore, mode string) (actor, policyID string, mutate func() error) {
	t.Helper()
	ctx := t.Context()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[20:]
	account, err := create.CreateServiceAccount(ctx, ServiceAccount{ID: uuid.NewString(), Name: "wl-" + suffix})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := create.CreateWorkloadIssuer(ctx, newWorkloadIssuer(suffix))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := create.CreateWorkloadTrustPolicy(ctx, WorkloadTrustPolicy{Name: "wl-" + suffix, IssuerID: issuer.ID, ServiceAccountID: account.ID, Conditions: json.RawMessage(workloadConditions)})
	if err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "workload-policy-disable":
		policy.State = WorkloadDisabled
		mutate = func() error { _, err := mutator.UpdateWorkloadTrustPolicy(ctx, policy, policy.Version); return err }
	case "workload-issuer-disable":
		issuer.State = WorkloadDisabled
		mutate = func() error { _, err := mutator.UpdateWorkloadIssuer(ctx, issuer, issuer.Version); return err }
	case "workload-service-disable":
		state := ServiceAccountDisabled
		mutate = func() error {
			_, err := mutator.UpdateServiceAccount(ctx, ServiceAccountUpdate{ID: account.ID, State: &state}, account.Version)
			return err
		}
	default:
		t.Fatalf("unknown mode %s", mode)
	}
	return "service-account:" + account.ID, policy.ID, mutate
}

func TestMemoryWorkloadPrincipalReplicationJobs(t *testing.T) {
	active := func(_ context.Context, a helm.Authority) error {
		if !a.Active || a.Kind != "workload_federation" {
			return helm.ErrAccessDenied
		}
		return nil
	}
	activeNuGet := func(_ context.Context, a nuget.Authority) error {
		if !a.Active || a.Kind != "workload_federation" {
			return nuget.ErrAccessDenied
		}
		return nil
	}
	for _, mode := range workloadReplicationModes {
		t.Run("helm "+mode, func(t *testing.T) {
			s := NewMemoryStore()
			f := helmReplicationContractSetup(t, s)
			actor, policy, mutate := workloadReplicationActor(t, s, s, mode)
			plan := helm.Plan{SourceRepositoryID: f.source.ID, TargetRepositoryID: f.target.ID, Items: []helm.Identity{f.job.Items[0].Identity}}
			job, err := helm.NewJob(plan, actor, "workload_federation", policy, uuid.NewString())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = s.CreateHelmReplication(t.Context(), job, active); err != nil {
				t.Fatal(err)
			}
			claim, err := s.ClaimHelmReplication(t.Context(), uuid.NewString(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.AuthorizeHelmReplicationItem(t.Context(), claim.Lease, 0, active); err != nil {
				t.Fatal(err)
			}
			if err = mutate(); err != nil {
				t.Fatal(err)
			}
			if err = s.AuthorizeHelmReplicationItem(t.Context(), claim.Lease, 0, active); !errors.Is(err, helm.ErrAccessDenied) {
				t.Fatalf("execution after %s = %v", mode, err)
			}
			again, err := helm.NewJob(plan, actor, "workload_federation", policy, uuid.NewString())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = s.CreateHelmReplication(t.Context(), again, active); !errors.Is(err, helm.ErrAccessDenied) {
				t.Fatalf("create after %s = %v", mode, err)
			}
		})
		t.Run("nuget "+mode, func(t *testing.T) {
			s := NewMemoryStore()
			f := replicationContractSetup(t, s)
			actor, policy, mutate := workloadReplicationActor(t, s, s, mode)
			plan := nuget.Plan{SourceRepositoryID: f.source.ID, TargetRepositoryID: f.target.ID, Items: []nuget.Identity{f.job.Items[0].Identity}}
			job, err := nuget.NewJob(plan, actor, "workload_federation", policy, uuid.NewString())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = s.CreateNuGetReplication(t.Context(), job, activeNuGet); err != nil {
				t.Fatal(err)
			}
			if err = mutate(); err != nil {
				t.Fatal(err)
			}
			again, err := nuget.NewJob(plan, actor, "workload_federation", policy, uuid.NewString())
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = s.CreateNuGetReplication(t.Context(), again, activeNuGet); !errors.Is(err, nuget.ErrAccessDenied) {
				t.Fatalf("create after %s = %v", mode, err)
			}
		})
	}
}
