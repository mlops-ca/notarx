//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The PostgreSQL store satisfies the same contract as the in-memory one: limits refused with typed errors, idempotent
// creation, the blob and commit state machine, and the reaper.
func TestPostgresImportSessionStore(t *testing.T) {
	store := promotionIntegrationStore(t)
	principal := "pg-" + uuid.NewString()[:8]
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM import_sessions WHERE principal LIKE $1`, principal+"%")
	})
	exerciseImportSessionStore(t, store, principal, func() int64 {
		var staged int64
		if err := store.db.QueryRowContext(context.Background(), `SELECT COALESCE(sum(bytes),0)::bigint FROM import_sessions WHERE state IN ('open','committing') AND expires_at > $1`, time.Now().UTC()).Scan(&staged); err != nil {
			t.Fatal(err)
		}
		return staged
	})
}

// Two creations racing for the last open-session slot of a principal cannot both succeed, and two racing for one
// idempotency key converge on one session.
func TestPostgresImportSessionCreationRacesHonourTheLimits(t *testing.T) {
	store := promotionIntegrationStore(t)
	ctx := context.Background()
	principal := "pg-race-" + uuid.NewString()[:8]
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM import_sessions WHERE principal=$1`, principal)
	})
	f := importFixture{t: t, store: store, principal: principal, now: time.Now().UTC().Truncate(time.Microsecond), limits: ImportLimits{MaxOpenSessionsPerPrincipal: 2}}
	const attempts = 8
	results := make(chan error, attempts)
	for i := range attempts {
		go func() {
			_, _, err := f.create("slot-"+uuid.NewString(), "race@1.0", blob(dg("r"), int64(i)))
			results <- err
		}()
	}
	created := 0
	for range attempts {
		if err := <-results; err == nil {
			created++
		}
	}
	if created != 2 {
		t.Fatalf("%d concurrent creations passed a limit of 2", created)
	}
	same := make(chan ImportSession, attempts)
	f.limits = ImportLimits{}
	target := uuid.NewString()
	for range attempts {
		go func() {
			request := f.session(principal+"-same", "one-key", "same@1.0")
			request.TargetRepositoryID = target
			stored, _, err := store.CreateImportSession(ctx, request, nil, f.limits)
			if err != nil {
				t.Error(err)
			}
			same <- stored
		}()
	}
	first := <-same
	for range attempts - 1 {
		if got := <-same; got.ID != first.ID {
			t.Fatalf("concurrent replays created two sessions: %s and %s", first.ID, got.ID)
		}
	}
}
