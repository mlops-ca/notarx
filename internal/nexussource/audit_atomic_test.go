package nexussource

import (
	"context"
	"errors"
	"testing"

	"github.com/mlops-ca/notarx/internal/repository"
	"github.com/mlops-ca/notarx/internal/secrets"
)

// TestSourceChangesRollBackWithTheirAudit proves ADR 0023 section 1 for the source service on the
// in-memory store: the audit of create, update and delete is written with the change, a failing
// audit fails the change itself, and a record without a resource names the source.
func TestSourceChangesRollBackWithTheirAudit(t *testing.T) {
	t.Setenv(secrets.KeyEnv, "0123456789abcdef0123456789abcdef")
	ctx := context.Background()
	store := newFixtureStore()
	service := New(store, emptyTargets{}, Options{})
	record := repository.AuditRecord{Actor: "alice", Operation: "migration.source.create", Format: "management"}

	created, err := service.Create(ctx, sourceInput(), record)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.audits) != 1 || store.audits[0].Resource != created.ID {
		t.Fatalf("create audits=%+v, want one naming source %s", store.audits, created.ID)
	}

	store.fault = func(repository.AuditRecord) error { return errors.New("audit store unavailable") }
	input := sourceInput()
	input.Name = "renamed"
	if _, err = service.Update(ctx, created.ID, input, created.Version, record); err == nil {
		t.Fatal("update took effect although its audit failed")
	}
	if got, _ := store.Get(ctx, created.ID); got.Name != created.Name || got.Version != created.Version {
		t.Fatalf("source changed: %+v", got)
	}
	if err = service.Delete(ctx, created.ID, created.Version, record); err == nil {
		t.Fatal("delete took effect although its audit failed")
	}
	if _, err = store.Get(ctx, created.ID); err != nil {
		t.Fatalf("source deleted: %v", err)
	}
	second := sourceInput()
	second.Name = "second"
	if _, err = service.Create(ctx, second, record); err == nil {
		t.Fatal("create took effect although its audit failed")
	}
	if len(store.values) != 1 || len(store.audits) != 1 {
		t.Fatalf("values=%d audits=%d, want the one source and one audit from before", len(store.values), len(store.audits))
	}

	store.fault = nil
	if err = service.Delete(ctx, created.ID, created.Version, record); err != nil {
		t.Fatal(err)
	}
	if len(store.audits) != 2 || store.audits[1].Resource != created.ID {
		t.Fatalf("delete audits=%+v", store.audits)
	}
}
