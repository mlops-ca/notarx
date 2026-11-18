package repository

import (
	"context"
	"testing"
)

func TestVisibilityOrderingAndFailClosed(t *testing.T) {
	if VisibilityPrivate.Rank() >= VisibilityAuthenticated.Rank() || VisibilityAuthenticated.Rank() >= VisibilityPublic.Rank() {
		t.Fatal("visibility tiers must be ordered private < authenticated < public")
	}
	for _, unknown := range []Visibility{"", "internal", "PUBLIC"} {
		if unknown.Valid() || unknown.Rank() >= VisibilityPrivate.Rank() {
			t.Fatalf("%q must be invalid and rank below private", unknown)
		}
	}
	if NormalizeVisibility("") != VisibilityPrivate || NormalizeVisibility(VisibilityPublic) != VisibilityPublic || NormalizeVisibility("bogus").Valid() {
		t.Fatal("normalisation must default to private and keep invalid values invalid")
	}
	if MinVisibility(VisibilityPublic, VisibilityAuthenticated) != VisibilityAuthenticated || MinVisibility("bogus", VisibilityPublic).Valid() {
		t.Fatal("minimum must pick the lower tier and stay fail-closed for unknown values")
	}
}

func TestMemoryStoreDefaultsVisibilityToPrivate(t *testing.T) {
	store := NewMemoryStore()
	repo, err := store.CreateHostedRepository(context.Background(), HostedRepository{ID: "0198a5c0-0000-7000-8000-000000000001", Name: "plain", Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	if repo.Visibility != VisibilityPrivate {
		t.Fatalf("default visibility = %q", repo.Visibility)
	}
}
