package rubygemsfixture

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mlops-ca/notarx/internal/ids"
	"github.com/mlops-ca/notarx/internal/repository"
	dist "github.com/mlops-ca/notarx/internal/rubygemsdistribution"
)

// WorkloadStore is the identity store that creates the service account, issuer and trust policy of a
// workload_federation principal and ends them.
type WorkloadStore interface {
	CreateServiceAccount(context.Context, repository.ServiceAccount, ...repository.AuditRecord) (repository.ServiceAccount, error)
	UpdateServiceAccount(context.Context, repository.ServiceAccountUpdate, string, ...repository.AuditRecord) (repository.ServiceAccount, error)
	CreateWorkloadIssuer(context.Context, repository.WorkloadIssuer, ...repository.AuditRecord) (repository.WorkloadIssuer, error)
	UpdateWorkloadIssuer(context.Context, repository.WorkloadIssuer, string, ...repository.AuditRecord) (repository.WorkloadIssuer, error)
	CreateWorkloadTrustPolicy(context.Context, repository.WorkloadTrustPolicy, ...repository.AuditRecord) (repository.WorkloadTrustPolicy, error)
	UpdateWorkloadTrustPolicy(context.Context, repository.WorkloadTrustPolicy, string, ...repository.AuditRecord) (repository.WorkloadTrustPolicy, error)
}

// WorkloadReplicationContract proves that a workload_federation subject (the service account, the trust policy id as
// credential id) creates a replication job and that disabling its policy, issuer or service account ends it.
func WorkloadReplicationContract(t *testing.T, s ReplicationStore, ws WorkloadStore, source, target repository.HostedRepository) {
	t.Helper()
	ctx := t.Context()
	for _, mode := range []string{"policy", "issuer", "service-account"} {
		t.Run("workload-"+mode, func(t *testing.T) {
			suffix := strings.ReplaceAll(ids.New(), "-", "")[20:]
			account, err := ws.CreateServiceAccount(ctx, repository.ServiceAccount{ID: ids.New(), Name: "wl-" + suffix})
			if err != nil {
				t.Fatal(err)
			}
			issuer, err := ws.CreateWorkloadIssuer(ctx, repository.WorkloadIssuer{Name: "wl-" + suffix, Kind: repository.WorkloadKindGitHubActions, Issuer: "https://token.actions.example.test/" + suffix, Audiences: []string{"https://notarx.example.test"}, JWKSMode: repository.WorkloadJWKSDiscovery})
			if err != nil {
				t.Fatal(err)
			}
			policy, err := ws.CreateWorkloadTrustPolicy(ctx, repository.WorkloadTrustPolicy{Name: "wl-" + suffix, IssuerID: issuer.ID, ServiceAccountID: account.ID, Conditions: json.RawMessage(`[{"claim":["repository_id"],"op":"eq","values":["1296269"]},{"claim":["ref"],"op":"eq","values":["refs/heads/main"]}]`)})
			if err != nil {
				t.Fatal(err)
			}
			subject := dist.Subject{Actor: "service-account:" + account.ID, Kind: "workload_federation", CredentialID: policy.ID}
			p := replicationPublication(t, s, source.ID, "workload-"+mode, "ruby")
			newJob := func() dist.Job {
				j, err := dist.NewJob(dist.Plan{SourceRepositoryID: source.ID, TargetRepositoryID: target.ID, Items: []dist.Identity{{Name: p.Name, Version: p.Version, Platform: p.Platform, Digest: p.Digest, Size: p.Size, SourceGovernanceVersion: p.Governance.Version}}}, subject, ids.New())
				if err != nil {
					t.Fatal(err)
				}
				return j
			}
			check := func(ctx context.Context, a dist.Authority) error {
				if a.Subject != subject || !a.Active {
					return dist.ErrAccessDenied
				}
				return ctx.Err()
			}
			if _, _, err = s.CreateRubyGemsReplication(ctx, newJob(), check); err != nil {
				t.Fatalf("workload job refused: %v", err)
			}
			switch mode {
			case "policy":
				policy.State = repository.WorkloadDisabled
				_, err = ws.UpdateWorkloadTrustPolicy(ctx, policy, policy.Version)
			case "issuer":
				issuer.State = repository.WorkloadDisabled
				_, err = ws.UpdateWorkloadIssuer(ctx, issuer, issuer.Version)
			default:
				state := repository.ServiceAccountDisabled
				_, err = ws.UpdateServiceAccount(ctx, repository.ServiceAccountUpdate{ID: account.ID, State: &state}, account.Version)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = s.CreateRubyGemsReplication(ctx, newJob(), check); !errors.Is(err, dist.ErrAccessDenied) {
				t.Fatalf("job after disabling the %s: %v", mode, err)
			}
		})
	}
}
