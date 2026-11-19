package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// TestMemoryQuotaBelowUsageIsRejected keeps the memory store on the contract of the PostgreSQL
// store: a quota below the current usage is rejected and leaves the old quota in place.
func TestMemoryQuotaBelowUsageIsRejected(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	repo, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "quota-floor", Format: FormatRaw, Type: RepositoryTypeHosted, State: RepositoryActive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.PutRawAsset(ctx, RawAsset{RepositoryID: repo.ID, Path: "a.bin", Digest: uuid.NewString(), Size: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, 9); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("quota below usage: %v", err)
	}
	if got, _ := store.GetRepositoryCapacity(ctx, repo.ID); got.QuotaBytes != 0 {
		t.Fatalf("rejected quota was stored: %d", got.QuotaBytes)
	}
	if got, err := store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, 10); err != nil || got.QuotaBytes != 10 {
		t.Fatalf("quota equal to usage: %+v %v", got, err)
	}
	if got, err := store.ReplaceRepositoryCapacityQuota(ctx, repo.ID, 0); err != nil || got.QuotaBytes != 0 {
		t.Fatalf("unlimited quota: %+v %v", got, err)
	}
}
