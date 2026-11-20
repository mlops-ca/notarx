//go:build integration

package lifecycle

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/background"
	"github.com/mlops-ca/notarx/internal/repository"
)

// TestStoppingARuntimeWithAJobInFlightRecordsItsOutcome stops the runtime while a handler is running. The
// handler returns as the context ends, and the runtime's completion write runs after that: the job must be
// completed, not left leased until its lease expires, and the group the process waits on must see the loop
// return only after that write (ADR 0023 section 7).
func TestStoppingARuntimeWithAJobInFlightRecordsItsOutcome(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := repository.NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	repo, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "runtime-stop-" + uuid.NewString(), Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := store.EnqueueLifecycleJob(ctx, repository.LifecycleJob{ID: uuid.NewString(), RepositoryID: repo.ID, Kind: repository.LifecycleJobReclaim, IdempotencyKey: uuid.NewString(), Payload: []byte(`{"format":"raw"}`)})
	if err != nil {
		t.Fatal(err)
	}

	group := &background.Group{}
	runCtx, stop := context.WithCancel(background.WithGroup(ctx, group))
	defer stop()
	inFlight := make(chan struct{})
	runtime := Runtime{Store: store, Kind: repository.LifecycleJobReclaim, Formats: []repository.Format{repository.FormatRaw}, Name: "runtime stop test", Operation: "runtime_stop_test"}
	runtime.Start(runCtx, time.Hour, 100, func(handlerCtx context.Context, claimed repository.LifecycleJob) error {
		if claimed.ID != job.ID {
			return nil
		}
		close(inFlight)
		<-handlerCtx.Done()
		return nil
	})
	select {
	case <-inFlight:
	case <-time.After(30 * time.Second):
		t.Fatal("the job never started")
	}
	stop()
	if !group.Wait(30 * time.Second) {
		t.Fatal("the runtime loop did not return")
	}
	got, err := store.GetLifecycleJob(ctx, repo.ID, job.ID)
	if err != nil || got.State != repository.LifecycleJobCompleted {
		t.Fatalf("job after the stop = %+v err=%v, want completed rather than leased", got, err)
	}
}
