package repository

import (
	"context"
	"testing"
)

// The in-memory store mirrors the grants_version bump of the PostgreSQL grant mutations, so the grant-set cache
// behaves the same over both stores.
func TestMemoryGrantMutationsBumpGrantsVersion(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	repo, err := store.CreateHostedRepository(ctx, HostedRepository{ID: "11111111-1111-4111-8111-111111111111", Name: "grants", Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	if repo.GrantsVersion != 1 {
		t.Fatalf("a new repository starts at grants_version 1, got %d", repo.GrantsVersion)
	}
	version := func() int64 {
		current, err := store.GetHostedRepository(ctx, repo.ID)
		if err != nil {
			t.Fatal(err)
		}
		return current.GrantsVersion
	}
	grant := RepositoryGrant{Principal: "user:ada", Scopes: []string{"read"}}
	steps := []struct {
		name string
		run  func() error
	}{
		{"upsert", func() error { _, err := store.UpsertRepositoryGrant(ctx, repo.ID, grant); return err }},
		{"replace", func() error {
			set, err := store.GetRepositoryGrants(ctx, repo.ID)
			if err != nil {
				return err
			}
			_, err = store.ReplaceRepositoryGrants(ctx, repo.ID, []RepositoryGrant{grant}, set.Version)
			return err
		}},
		{"delete", func() error { _, err := store.DeleteRepositoryGrant(ctx, repo.ID, grant.Principal, ""); return err }},
	}
	want := repo.GrantsVersion
	for _, step := range steps {
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		want++
		if got := version(); got != want {
			t.Fatalf("%s: grants_version = %d, want %d", step.name, got, want)
		}
	}
}
