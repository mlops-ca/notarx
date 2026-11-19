package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func ceilingFixture(t *testing.T) (*MemoryStore, HostedRepository, HostedRepository) {
	t.Helper()
	store := NewMemoryStore()
	create := func(name string, visibility Visibility) HostedRepository {
		repo, err := store.CreateHostedRepository(context.Background(), HostedRepository{ID: uuid.NewString(), Name: name, Format: FormatRaw, Visibility: visibility})
		if err != nil {
			t.Fatal(err)
		}
		return repo
	}
	return store, create("public-repo", VisibilityPublic), create("private-repo", VisibilityPrivate)
}

func TestStoreEnforcesTheGroupCeilingOnlyWhenAsked(t *testing.T) {
	store, public, private := ceilingFixture(t)
	strict := WithGroupCeiling(context.Background())
	group := HostedGroup{ID: uuid.NewString(), Name: "feed", Format: FormatRaw, Visibility: VisibilityPublic, Members: []GroupMember{{RepositoryID: public.ID}, {RepositoryID: private.ID, Position: 1}}}

	_, _, err := store.CreateHostedGroupIdempotently(strict, group, "a", "k1", "p")
	var conflict *VisibilityConflictError
	if !errors.Is(err, ErrVisibilityConflict) || !errors.As(err, &conflict) || len(conflict.Members) != 1 || conflict.Members[0].Name != "private-repo" {
		t.Fatalf("strict create = %v", err)
	}
	if _, _, err := store.CreateHostedGroupIdempotently(context.Background(), group, "a", "k2", "p"); err != nil {
		t.Fatalf("an unmarked context must write unchecked: %v", err)
	}
	stored, _ := store.GetHostedGroupByName(context.Background(), "feed")
	less := LessVisibleMembers(stored, map[string]HostedRepository{public.ID: public, private.ID: private})
	if len(less) != 1 || less[0].Name != "private-repo" {
		t.Fatalf("LessVisibleMembers = %+v", less)
	}
	if _, err := store.ReplaceHostedGroup(strict, stored, stored.Version); !errors.Is(err, ErrVisibilityConflict) {
		t.Fatalf("strict replace = %v", err)
	}
	if _, err := store.ReplaceHostedGroupMembers(strict, stored.ID, stored.Members, stored.Version); !errors.Is(err, ErrVisibilityConflict) {
		t.Fatalf("strict member replace = %v", err)
	}
	stored.Visibility = VisibilityPrivate
	if _, err := store.ReplaceHostedGroup(strict, stored, stored.Version); err != nil {
		t.Fatalf("lowering the group must pass: %v", err)
	}
}

func TestStoreRefusesLoweringARepositoryBelowItsGroup(t *testing.T) {
	store, public, _ := ceilingFixture(t)
	strict := WithGroupCeiling(context.Background())
	group := HostedGroup{ID: uuid.NewString(), Name: "feed", Format: FormatRaw, Visibility: VisibilityPublic, Members: []GroupMember{{RepositoryID: public.ID}}}
	if _, _, err := store.CreateHostedGroupIdempotently(strict, group, "a", "k", "p"); err != nil {
		t.Fatal(err)
	}
	above, err := store.ListGroupsAboveVisibility(context.Background(), public.ID, VisibilityAuthenticated)
	if err != nil || len(above) != 1 || above[0].Name != "feed" {
		t.Fatalf("groups above = %+v %v", above, err)
	}
	lowered := public
	lowered.Visibility = VisibilityAuthenticated
	_, err = store.UpdateHostedRepository(strict, lowered, public.Version)
	var conflict *VisibilityConflictError
	if !errors.As(err, &conflict) || !conflict.Lowering {
		t.Fatalf("lowering = %v", err)
	}
	if _, err := store.UpdateHostedRepository(context.Background(), lowered, public.Version); err != nil {
		t.Fatalf("lenient lowering: %v", err)
	}
}
