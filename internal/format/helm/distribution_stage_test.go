package helm

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/repository"
)

// stagingOrderStore records the order of payload staging and publication locking.
type stagingOrderStore struct {
	*repository.MemoryStore
	events []string
	staged [][]byte
}

type stagedMarker struct{}

func (s *stagingOrderStore) StageHelmChart(ctx context.Context, repo, name, version string, chartYAML []byte) (context.Context, error) {
	s.events = append(s.events, "stage")
	s.staged = append(s.staged, append([]byte(nil), chartYAML...))
	return context.WithValue(ctx, stagedMarker{}, true), nil
}

func (s *stagingOrderStore) LockHelmPublication(ctx context.Context, repo, name, version string) (context.Context, func(), error) {
	if ctx.Value(stagedMarker{}) == nil {
		s.events = append(s.events, "lock-unstaged")
	} else {
		s.events = append(s.events, "lock-staged")
	}
	return s.MemoryStore.LockHelmPublication(ctx, repo, name, version)
}

// TestHelmReplicationStagesTheChartYAMLBeforeThePublicationLock pins the order of a replicated item: the source
// chart.yaml is staged on the context first and the target publication lock is taken with that context.
func TestHelmReplicationStagesTheChartYAMLBeforeThePublicationLock(t *testing.T) {
	f := newReplicationFixture(t)
	if w := replicationUpload(t, f.fixture, chartFixture(t, "app", "1.0.0", "copy")); w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	source, err := f.store.GetHelmPublication(t.Context(), f.repo.ID, "app", "1.0.0")
	if err != nil || len(source.ChartYAML) == 0 {
		t.Fatalf("source chart.yaml %q: %v", source.ChartYAML, err)
	}
	job := createReplication(t, f, replicationPlan(t, f, "app"), uuid.NewString(), "helm-owned-admin", 202)
	order := &stagingOrderStore{MemoryStore: f.store}
	f.module.store = order
	if got := runReplication(t, f, job); got.State != "succeeded" {
		t.Fatalf("replication=%+v", got)
	}
	if len(order.events) != 2 || order.events[0] != "stage" || order.events[1] != "lock-staged" {
		t.Fatalf("order=%v", order.events)
	}
	if len(order.staged) != 1 || string(order.staged[0]) != string(source.ChartYAML) {
		t.Fatalf("staged %q, source chart.yaml %q", order.staged, source.ChartYAML)
	}
}
