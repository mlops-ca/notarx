package repository

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryPublicAccessPolicyDefaultsDisabledAndUsesOptimisticVersioning(t *testing.T) {
	store := NewMemoryStore()
	policy, err := store.GetPublicAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if policy.Enabled || policy.Version != "1" {
		t.Fatalf("default policy = %#v, want disabled version 1", policy)
	}
	updated, err := store.ReplacePublicAccessPolicy(context.Background(), PublicAccessPolicy{Enabled: true}, policy.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Enabled || updated.Version != "2" {
		t.Fatalf("updated policy = %#v, want enabled version 2", updated)
	}
	if _, err := store.ReplacePublicAccessPolicy(context.Background(), PublicAccessPolicy{}, policy.Version); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale replace error = %v, want ErrVersionConflict", err)
	}
}
