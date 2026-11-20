//go:build integration

package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/mlops-ca/notarx/internal/repository"
)

func TestPostgresInternalTaskLeaseRowIsHiddenFromTheScheduleAPI(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL contract tests")
	}
	store, err := repository.NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	task := repository.InternalTaskLease{ID: "00000000-0000-7000-8000-0000000000a3", Name: "internal:raw-idle-seal", Kind: "raw-idle-seal", Description: "hidden"}
	if held, err := store.AcquireInternalTaskLease(t.Context(), task, "node", time.Minute, 0); err != nil || !held {
		t.Fatalf("acquire: %v %v", held, err)
	}
	t.Cleanup(func() { _ = store.ReleaseInternalTaskLease(context.Background(), task, "node", false) })
	tasks, err := store.ListScheduledTasks(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, listed := range tasks {
		if listed.ID == task.ID || string(listed.Kind) == task.Kind {
			t.Fatalf("an internal lease row is listed as a schedule: %#v", listed)
		}
	}
	if _, err := store.GetScheduledTask(t.Context(), task.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("an internal lease row is readable as a schedule: %v", err)
	}
}

func TestPostgresInternalTaskLeaseContract(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL contract tests")
	}
	runInternalTaskLeaseContract(t, func(t *testing.T) repository.InternalTaskLeaseStore {
		t.Helper()
		store, err := repository.NewPostgresStore(databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		// The lease row is shared by every run of the suite; remove it so each case starts without a holder and
		// without a completed run.
		db, err := sql.Open("pgx", databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if _, err := db.ExecContext(t.Context(), "DELETE FROM scheduled_tasks WHERE id='00000000-0000-7000-8000-0000000000a3'"); err != nil {
			t.Fatal(err)
		}
		return store
	})
}
