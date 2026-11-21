package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/ids"
)

func TestMemoryRemoteInstanceStore(t *testing.T) {
	exerciseRemoteInstanceStore(t, NewMemoryStore())
}

func TestMemoryRemoteInstanceDeleteRefusedWhileARuleTargetsIt(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	instance, err := store.CreateRemoteInstance(ctx, RemoteInstance{Name: "peer", BaseURL: "https://peer.example.com", InstanceID: ids.New(), AuthKind: "workload_identity", Audience: "peer", State: RemoteInstanceStateActive})
	if err != nil {
		t.Fatal(err)
	}
	store.promotionRules = map[string]PromotionRule{"rule": {ID: "rule", RemoteInstanceID: instance.ID}}
	if err = store.DeleteRemoteInstance(ctx, instance.ID); !errors.Is(err, ErrRemoteInstanceInUse) {
		t.Fatalf("delete while a rule targets it err = %v", err)
	}
	delete(store.promotionRules, "rule")
	if err = store.DeleteRemoteInstance(ctx, instance.ID); err != nil {
		t.Fatalf("delete after the rule is gone: %v", err)
	}
}

// exerciseRemoteInstanceStore is the store contract shared by the memory and PostgreSQL stores.
func exerciseRemoteInstanceStore(t *testing.T, store RemoteInstanceStore) {
	t.Helper()
	ctx := context.Background()
	suffix := ids.New()

	local, err := store.LocalInstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = uuid.Parse(local); err != nil {
		t.Fatalf("local instance id %q is not a UUID: %v", local, err)
	}
	if again, againErr := store.LocalInstanceID(ctx); againErr != nil || again != local {
		t.Fatalf("local instance id changed: %q then %q err=%v", local, again, againErr)
	}

	checked := time.Now().UTC().Truncate(time.Microsecond)
	token := RemoteInstance{ID: ids.New(), Name: "peer-token-" + suffix, BaseURL: "https://peer-a.example.test", InstanceID: ids.New(),
		AuthKind: RemoteInstanceAuthStaticToken, TokenSealed: "v1:kid:sealed-token", TrustEvidence: true, State: RemoteInstanceStateActive, LastCheckedAt: checked}
	workload := RemoteInstance{ID: ids.New(), Name: "Peer-workload-" + suffix, BaseURL: "https://peer-b.example.test/notarx", InstanceID: ids.New(),
		AuthKind: RemoteInstanceAuthWorkloadIdentity, Audience: "https://peer-b.example.test", State: RemoteInstanceStateActive}
	createdToken, err := store.CreateRemoteInstance(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if createdToken.Version != "1" || createdToken.TokenSealed != token.TokenSealed || !createdToken.TrustEvidence || !createdToken.LastCheckedAt.Equal(checked) || createdToken.CreatedAt.IsZero() {
		t.Fatalf("created token instance = %#v", createdToken)
	}
	createdWorkload, err := store.CreateRemoteInstance(ctx, workload)
	if err != nil {
		t.Fatal(err)
	}
	if createdWorkload.TokenSealed != "" || !createdWorkload.LastCheckedAt.IsZero() || createdWorkload.Audience != workload.Audience {
		t.Fatalf("created workload instance = %#v", createdWorkload)
	}
	duplicate := token
	duplicate.ID = ids.New()
	if _, err = store.CreateRemoteInstance(ctx, duplicate); !errors.Is(err, ErrRemoteInstanceNameExists) {
		t.Fatalf("duplicate name err = %v", err)
	}

	got, err := store.GetRemoteInstance(ctx, token.ID)
	if err != nil || got.Name != token.Name || got.BaseURL != token.BaseURL || got.InstanceID != token.InstanceID {
		t.Fatalf("get = %#v err=%v", got, err)
	}
	if _, err = store.GetRemoteInstance(ctx, ids.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing err = %v", err)
	}

	// Byte order: the upper-case name sorts before the lower-case one; the page token continues after the last name.
	first, err := store.PageRemoteInstances(ctx, RemoteInstanceQuery{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.NextPageToken == "" {
		t.Fatalf("first page = %#v err=%v", first, err)
	}
	second, err := store.PageRemoteInstances(ctx, RemoteInstanceQuery{Limit: 500, PageToken: first.NextPageToken})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, item := range append(first.Items, second.Items...) {
		names = append(names, item.Name)
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Fatalf("pages are not in byte order: %v", names)
		}
	}
	if _, err = store.PageRemoteInstances(ctx, RemoteInstanceQuery{PageToken: "not-a-token"}); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("invalid page token err = %v", err)
	}

	update := createdToken
	update.Name, update.State = "peer-renamed-"+suffix, RemoteInstanceStateDisabled
	updated, err := store.UpdateRemoteInstance(ctx, update, "1")
	if err != nil || updated.Version != "2" || updated.Name != update.Name || updated.State != RemoteInstanceStateDisabled || updated.TokenSealed != token.TokenSealed {
		t.Fatalf("updated = %#v err=%v", updated, err)
	}
	if _, err = store.UpdateRemoteInstance(ctx, update, "1"); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale update err = %v", err)
	}
	clash := updated
	clash.Name = workload.Name
	if _, err = store.UpdateRemoteInstance(ctx, clash, "2"); !errors.Is(err, ErrRemoteInstanceNameExists) {
		t.Fatalf("name clash err = %v", err)
	}
	missing := updated
	missing.ID = ids.New()
	if _, err = store.UpdateRemoteInstance(ctx, missing, "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing err = %v", err)
	}

	later := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	recorded, err := store.RecordRemoteInstanceCheck(ctx, workload.ID, RemoteInstanceStateUnreachable, later, "1")
	if err != nil || recorded.State != RemoteInstanceStateUnreachable || !recorded.LastCheckedAt.Equal(later) || recorded.Version != "1" {
		t.Fatalf("recorded = %#v err=%v", recorded, err)
	}
	if _, err = store.RecordRemoteInstanceCheck(ctx, ids.New(), RemoteInstanceStateActive, later, "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("record missing err = %v", err)
	}
	// A check decided from a stale version must not overwrite a concurrent edit or disable.
	if _, err = store.RecordRemoteInstanceCheck(ctx, token.ID, RemoteInstanceStateActive, later, "1"); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale check err = %v", err)
	}
	if got, getErr := store.GetRemoteInstance(ctx, token.ID); getErr != nil || got.State != RemoteInstanceStateDisabled {
		t.Fatalf("stale check changed the instance: %#v err=%v", got, getErr)
	}
	// An update without a new token keeps the stored (possibly rotated) token.
	keep := updated
	keep.TokenSealed = ""
	kept, err := store.UpdateRemoteInstance(ctx, keep, "2")
	if err != nil || kept.TokenSealed != token.TokenSealed {
		t.Fatalf("token not carried forward: %#v err=%v", kept, err)
	}

	for _, id := range []string{token.ID, workload.ID} {
		if err = store.DeleteRemoteInstance(ctx, id); err != nil {
			t.Fatal(err)
		}
		if err = store.DeleteRemoteInstance(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second delete err = %v", err)
		}
	}
}
