//go:build integration

package repository

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/repository/explaincheck"
)

func ceilingStore(t *testing.T) *PostgresStore {
	t.Helper()
	connection := os.Getenv("TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	store, err := NewPostgresStore(connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func ceilingRepo(t *testing.T, store *PostgresStore, visibility Visibility) HostedRepository {
	t.Helper()
	repo, err := store.CreateHostedRepository(context.Background(), HostedRepository{ID: uuid.NewString(), Name: "ceiling-" + uuid.NewString()[:8], Format: FormatRaw, Visibility: visibility})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM hosted_repositories WHERE id=$1`, repo.ID)
	})
	return repo
}

func ceilingGroup(t *testing.T, store *PostgresStore, visibility Visibility, members ...HostedRepository) (HostedGroup, error) {
	t.Helper()
	group := HostedGroup{ID: uuid.NewString(), Name: "ceiling-group-" + uuid.NewString()[:8], Format: FormatRaw, Visibility: visibility}
	for position, member := range members {
		group.Members = append(group.Members, GroupMember{RepositoryID: member.ID, Position: position})
	}
	created, _, err := store.CreateHostedGroupIdempotently(WithGroupCeiling(context.Background()), group, "test", uuid.NewString(), "payload")
	if err == nil {
		t.Cleanup(func() {
			_, _ = store.db.ExecContext(context.Background(), `DELETE FROM hosted_groups WHERE id=$1`, created.ID)
		})
	}
	return created, err
}

func TestPostgresStoreEnforcesTheGroupCeilingInTheWriteTransaction(t *testing.T) {
	store := ceilingStore(t)
	ctx := context.Background()
	strict := WithGroupCeiling(ctx)
	public := ceilingRepo(t, store, VisibilityPublic)
	private := ceilingRepo(t, store, VisibilityPrivate)

	var conflict *VisibilityConflictError
	if _, err := ceilingGroup(t, store, VisibilityPublic, public, private); !errors.As(err, &conflict) || len(conflict.Members) != 1 || conflict.Members[0].ID != private.ID {
		t.Fatalf("strict create over a private member = %v", err)
	}
	group, err := ceilingGroup(t, store, VisibilityPublic, public)
	if err != nil {
		t.Fatal(err)
	}
	// Replacement and member replacement are checked against the stored ceiling.
	withPrivate := group
	withPrivate.Members = []GroupMember{{RepositoryID: public.ID, Position: 0}, {RepositoryID: private.ID, Position: 1}}
	if _, err := store.ReplaceHostedGroup(strict, withPrivate, group.Version); !errors.Is(err, ErrVisibilityConflict) {
		t.Fatalf("strict replace = %v", err)
	}
	if _, err := store.ReplaceHostedGroupMembers(strict, group.ID, withPrivate.Members, group.Version); !errors.Is(err, ErrVisibilityConflict) {
		t.Fatalf("strict member replace = %v", err)
	}
	if stored, err := store.GetHostedGroup(ctx, group.ID); err != nil || len(stored.Members) != 1 || stored.Version != group.Version {
		t.Fatalf("a refused write changed the group: %+v %v", stored, err)
	}
	// Without the mark the same write goes through (lenient mode).
	if _, err := store.ReplaceHostedGroupMembers(ctx, group.ID, withPrivate.Members, group.Version); err != nil {
		t.Fatalf("lenient member replace = %v", err)
	}
	// Lowering a member below the group is refused and rolled back; a repository outside every group is not.
	lowered := public
	lowered.Visibility = VisibilityAuthenticated
	if _, err := store.UpdateHostedRepository(strict, lowered, public.Version); !errors.As(err, &conflict) || !conflict.Lowering {
		t.Fatalf("strict lowering = %v", err)
	}
	if stored, _ := store.GetHostedRepository(ctx, public.ID); stored.Visibility != VisibilityPublic || stored.Version != public.Version {
		t.Fatalf("a refused lowering was stored: %+v", stored)
	}
	free := ceilingRepo(t, store, VisibilityPublic)
	free.Visibility = VisibilityPrivate
	if _, err := store.UpdateHostedRepository(strict, free, free.Version); err != nil {
		t.Fatalf("lowering a repository outside every group = %v", err)
	}
	// A stale version keeps its own error ahead of the conflict.
	if _, err := store.UpdateHostedRepository(strict, lowered, "999"); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale lowering = %v", err)
	}
}

// A group raise and a member lowering run concurrently; whichever order they commit
// in, the ceiling must hold afterwards.
func TestPostgresGroupRaiseAndMemberLoweringCannotBothCommit(t *testing.T) {
	store := ceilingStore(t)
	ctx := WithGroupCeiling(context.Background())
	for round := 0; round < 40; round++ {
		repo := ceilingRepo(t, store, VisibilityPublic)
		group, err := ceilingGroup(t, store, VisibilityPrivate, repo)
		if err != nil {
			t.Fatal(err)
		}
		raise, lower := group, repo
		raise.Visibility = VisibilityPublic
		lower.Visibility = VisibilityPrivate
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, _ = store.ReplaceHostedGroup(ctx, raise, group.Version)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, _ = store.UpdateHostedRepository(ctx, lower, repo.Version)
		}()
		close(start)
		wg.Wait()
		storedGroup, err := store.GetHostedGroup(context.Background(), group.ID)
		if err != nil {
			t.Fatal(err)
		}
		storedRepo, err := store.GetHostedRepository(context.Background(), repo.ID)
		if err != nil {
			t.Fatal(err)
		}
		if storedGroup.Visibility.Rank() > storedRepo.Visibility.Rank() {
			t.Fatalf("round %d: group %q is more visible than its member %q", round, storedGroup.Visibility, storedRepo.Visibility)
		}
	}
}

func TestPostgresListGroupsAboveVisibilityUsesTheMemberIndex(t *testing.T) {
	store := ceilingStore(t)
	ctx := context.Background()
	repo := ceilingRepo(t, store, VisibilityPublic)
	public, err := ceilingGroup(t, store, VisibilityPublic, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ceilingGroup(t, store, VisibilityAuthenticated, repo); err != nil {
		t.Fatal(err)
	}
	above, err := store.ListGroupsAboveVisibility(ctx, repo.ID, VisibilityAuthenticated)
	if err != nil || len(above) != 1 || above[0].ID != public.ID {
		t.Fatalf("groups above authenticated = %+v %v", above, err)
	}
	if above, err = store.ListGroupsAboveVisibility(ctx, repo.ID, VisibilityPrivate); err != nil || len(above) != 2 {
		t.Fatalf("groups above private = %+v %v", above, err)
	}
	if above, err = store.ListGroupsAboveVisibility(ctx, repo.ID, VisibilityPublic); err != nil || len(above) != 0 {
		t.Fatalf("groups above public = %+v %v", above, err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	plan, err := explaincheck.Plan(ctx, tx, groupsAboveVisibilityQuery, repo.ID, VisibilityAuthenticated.Rank())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.UsesIndex("hosted_group_members_repository_id_fk_idx") {
		t.Fatalf("the lowering scan does not use the member index: %v", plan.Indexes())
	}
	if scans := plan.SequentialScans(); len(scans) != 0 {
		t.Fatalf("the lowering scan reads %v sequentially", scans)
	}
}
