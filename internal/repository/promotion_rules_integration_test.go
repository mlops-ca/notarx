//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func promotionRulePostgres(t *testing.T) *PostgresStore {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	store, err := NewPostgresStore(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestPostgresPromotionRuleStore(t *testing.T) {
	exercisePromotionRuleStore(t, promotionRulePostgres(t))
}

// The checks of the table tie the target columns to target_kind, keep a rule off its own source and require JSON objects.
func TestPostgresPromotionRuleTableChecks(t *testing.T) {
	store := promotionRulePostgres(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	source, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "chk-source-" + suffix, Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "chk-target-" + suffix, Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.CreateServiceAccount(ctx, ServiceAccount{ID: uuid.NewString(), Name: "chk-runner-" + suffix})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := store.CreateRemoteInstance(ctx, RemoteInstance{ID: uuid.NewString(), Name: "chk-peer-" + suffix, BaseURL: "https://peer.example.test", InstanceID: uuid.NewString(),
		AuthKind: RemoteInstanceAuthWorkloadIdentity, Audience: "aud", State: RemoteInstanceStateActive})
	if err != nil {
		t.Fatal(err)
	}
	base := func(name string) PromotionRule {
		return PromotionRule{ID: uuid.NewString(), Name: name + "-" + suffix, SourceRepositoryID: source.ID, TargetKind: PromotionTargetLocal, TargetRepositoryID: target.ID,
			Match: json.RawMessage(`{}`), Conditions: json.RawMessage(`{}`), RunAsServiceAccountID: account.ID, Enabled: true, CreatedBy: "alice"}
	}
	for name, mutate := range map[string]func(*PromotionRule){
		"local with a remote instance": func(r *PromotionRule) { r.RemoteInstanceID, r.RemoteRepository = remote.ID, "x" },
		"local without a target":       func(r *PromotionRule) { r.TargetRepositoryID = "" },
		"local onto its own source":    func(r *PromotionRule) { r.TargetRepositoryID = source.ID },
		"remote with a local target": func(r *PromotionRule) {
			r.TargetKind, r.RemoteInstanceID, r.RemoteRepository = PromotionTargetRemote, remote.ID, "x"
		},
		"remote without a repository": func(r *PromotionRule) {
			r.TargetKind, r.TargetRepositoryID, r.RemoteInstanceID = PromotionTargetRemote, "", remote.ID
		},
		"remote without an instance": func(r *PromotionRule) {
			r.TargetKind, r.TargetRepositoryID, r.RemoteRepository = PromotionTargetRemote, "", "x"
		},
		"unknown kind":                 func(r *PromotionRule) { r.TargetKind = "hybrid" },
		"match is not an object":       func(r *PromotionRule) { r.Match = json.RawMessage(`[]`) },
		"conditions are not an object": func(r *PromotionRule) { r.Conditions = json.RawMessage(`"x"`) },
		"approval is not an object":    func(r *PromotionRule) { r.Approval = json.RawMessage(`[]`) },
		"empty name":                   func(r *PromotionRule) { r.Name = "" },
	} {
		rule := base("bad")
		mutate(&rule)
		if _, err = store.CreatePromotionRule(ctx, rule, 10); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	good := base("good")
	good.Approval, good.Schedule = json.RawMessage(`{"approvers":[]}`), json.RawMessage(`{"cron":"0 * * * *"}`)
	created, err := store.CreatePromotionRule(ctx, good, 10)
	if err != nil || !jsonEqual(created.Approval, good.Approval) || !jsonEqual(created.Schedule, good.Schedule) {
		t.Fatalf("created = %#v, %v", created, err)
	}
	// Deleting a repository removes its rules; a remote instance a rule names cannot be deleted.
	remoteRule := base("remote")
	remoteRule.TargetKind, remoteRule.TargetRepositoryID, remoteRule.RemoteInstanceID, remoteRule.RemoteRepository = PromotionTargetRemote, "", remote.ID, "x"
	if _, err = store.CreatePromotionRule(ctx, remoteRule, 10); err != nil {
		t.Fatal(err)
	}
	if err = store.DeleteRemoteInstance(ctx, remote.ID); err == nil {
		t.Fatal("a remote instance that a rule names was deleted")
	}
	if _, err = store.db.ExecContext(ctx, `DELETE FROM hosted_repositories WHERE id=$1::uuid`, target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetPromotionRule(ctx, created.ID); err != ErrNotFound {
		t.Fatalf("a rule outlived its target repository: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM promotion_rules WHERE name LIKE '%-' || $1`, suffix)
	})
}

// Concurrent creates cannot both pass the cap: the count and the insert run under an advisory lock of the source.
func TestPostgresPromotionRuleCapHoldsUnderConcurrency(t *testing.T) {
	store := promotionRulePostgres(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	source, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "cap-source-" + suffix, Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	target, err := store.CreateHostedRepository(ctx, HostedRepository{ID: uuid.NewString(), Name: "cap-target-" + suffix, Format: FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.CreateServiceAccount(ctx, ServiceAccount{ID: uuid.NewString(), Name: "cap-runner-" + suffix})
	if err != nil {
		t.Fatal(err)
	}
	const maxRules, attempts = 3, 12
	var created, refused atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, createErr := store.CreatePromotionRule(ctx, PromotionRule{ID: uuid.NewString(), Name: "cap-" + uuid.NewString(), SourceRepositoryID: source.ID, TargetKind: PromotionTargetLocal,
				TargetRepositoryID: target.ID, Match: json.RawMessage(`{}`), RunAsServiceAccountID: account.ID, CreatedBy: "alice"}, maxRules)
			switch createErr {
			case nil:
				created.Add(1)
			case ErrPromotionRuleLimit:
				refused.Add(1)
			default:
				t.Error(createErr)
			}
		}()
	}
	wg.Wait()
	if created.Load() != maxRules || refused.Load() != attempts-maxRules {
		t.Fatalf("created %d refused %d, want %d and %d", created.Load(), refused.Load(), maxRules, attempts-maxRules)
	}
}
