package repository

import (
	"context"
	"errors"
	"testing"
)

func TestDefaultAssertionSubject(t *testing.T) {
	for name, want := range map[string]string{
		"ci":                      "notarx:service-account:ci",
		"Deploy.bot_1":            "notarx:service-account:Deploy.bot_1",
		"team:prod-eu":            "notarx:service-account:team:prod-eu",
		"a-b.c_d:E":               "notarx:service-account:a-b.c_d:E",
		"128":                     "notarx:service-account:128",
		"has space":               "",
		"slash/name":              "",
		"caf\u00e9":               "",
		"":                        "",
		"new\nline":               "",
		"at@sign":                 "",
		string(make([]byte, 129)): "",
	} {
		got, ok := DefaultAssertionSubject(name)
		if got != want || ok != (want != "") {
			t.Errorf("DefaultAssertionSubject(%q) = %q, %v; want %q", name, got, ok, want)
		}
	}
	long := make([]byte, 128)
	for i := range long {
		long[i] = 'a'
	}
	if _, ok := DefaultAssertionSubject(string(long)); !ok {
		t.Error("a 128 character name is legal")
	}
}

func TestMemoryAssertionSubjectsAreReservedForGood(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	account, err := store.CreateServiceAccount(ctx, ServiceAccount{ID: "11111111-1111-4111-8111-111111111111", Name: "ci", AssertionSubject: "notarx:service-account:ci"})
	if err != nil || account.AssertionSubject != "notarx:service-account:ci" {
		t.Fatalf("create = %+v, %v", account, err)
	}
	if _, err := store.CreateServiceAccount(ctx, ServiceAccount{ID: "22222222-2222-4222-8222-222222222222", Name: "other", AssertionSubject: "notarx:service-account:ci"}); !errors.Is(err, ErrAssertionSubjectReserved) {
		t.Fatalf("a held subject err=%v", err)
	}
	// An account without a subject does not reserve anything.
	if _, err := store.CreateServiceAccount(ctx, ServiceAccount{ID: "33333333-3333-4333-8333-333333333333", Name: "bare"}); err != nil {
		t.Fatal(err)
	}
	// The memory store has no delete; model the foreign key (ON DELETE SET NULL) directly.
	store.mu.Lock()
	delete(store.serviceAccounts, account.ID)
	store.assertions.subjects[account.AssertionSubject] = ""
	store.mu.Unlock()
	if _, err := store.CreateServiceAccount(ctx, ServiceAccount{ID: "44444444-4444-4444-8444-444444444444", Name: "ci", AssertionSubject: "notarx:service-account:ci"}); !errors.Is(err, ErrAssertionSubjectReserved) {
		t.Fatalf("a subject of a deleted account must stay reserved, err=%v", err)
	}
}

func TestMemoryAssertionAudiences(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	account, _ := store.CreateServiceAccount(ctx, ServiceAccount{ID: "11111111-1111-4111-8111-111111111111", Name: "ci", AssertionSubject: "s"})
	if _, err := store.ListAssertionAudiences(ctx, "99999999-9999-4999-8999-999999999999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("list of an unknown account err=%v", err)
	}
	if _, _, err := store.PutAssertionAudience(ctx, AssertionAudience{ServiceAccountID: "99999999-9999-4999-8999-999999999999", Audience: "a", MaxLifetimeSeconds: 60}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("put on an unknown account err=%v", err)
	}
	entry, created, err := store.PutAssertionAudience(ctx, AssertionAudience{ServiceAccountID: account.ID, Audience: "b", MaxLifetimeSeconds: 300})
	if err != nil || !created || entry.CreatedAt.IsZero() {
		t.Fatalf("put = %+v %v %v", entry, created, err)
	}
	if _, created, err := store.PutAssertionAudience(ctx, AssertionAudience{ServiceAccountID: account.ID, Audience: "a", MaxLifetimeSeconds: 60}); err != nil || !created {
		t.Fatalf("second put = %v %v", created, err)
	}
	replaced, created, err := store.PutAssertionAudience(ctx, AssertionAudience{ServiceAccountID: account.ID, Audience: "b", MaxLifetimeSeconds: 120})
	if err != nil || created || replaced.MaxLifetimeSeconds != 120 || !replaced.CreatedAt.Equal(entry.CreatedAt) {
		t.Fatalf("replace = %+v %v %v", replaced, created, err)
	}
	list, err := store.ListAssertionAudiences(ctx, account.ID)
	if err != nil || len(list) != 2 || list[0].Audience != "a" || list[1].Audience != "b" {
		t.Fatalf("list = %+v %v", list, err)
	}
	if got, err := store.GetAssertionAudience(ctx, account.ID, "b"); err != nil || got.MaxLifetimeSeconds != 120 {
		t.Fatalf("get = %+v %v", got, err)
	}
	if _, err := store.GetAssertionAudience(ctx, account.ID, "zzz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get of an unlisted audience err=%v", err)
	}
	if err := store.DeleteAssertionAudience(ctx, account.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAssertionAudience(ctx, account.ID, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err=%v", err)
	}
	// An audit that cannot be written fails the change.
	store.SetAuditFault(func(AuditRecord) error { return errors.New("audit table is down") })
	if _, _, err := store.PutAssertionAudience(ctx, AssertionAudience{ServiceAccountID: account.ID, Audience: "c", MaxLifetimeSeconds: 60}, AuditRecord{Operation: "x"}); err == nil {
		t.Fatal("the put must fail with its audit")
	}
	if err := store.DeleteAssertionAudience(ctx, account.ID, "b", AuditRecord{Operation: "x"}); err == nil {
		t.Fatal("the delete must fail with its audit")
	}
	store.SetAuditFault(nil)
	if _, err := store.GetAssertionAudience(ctx, account.ID, "b"); err != nil {
		t.Fatalf("the failed delete must not have removed the entry: %v", err)
	}
	if _, err := store.GetAssertionAudience(ctx, account.ID, "c"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the failed put must not have added the entry: %v", err)
	}
}
