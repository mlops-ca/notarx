//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestPostgresAssertionAudiencesAndSubjects(t *testing.T) {
	store := userGroupTestStore(t)
	ctx := context.Background()
	name := "assert-" + uuid.NewString()[:8]
	subject := DefaultAssertionSubjectPrefix + name
	account, err := store.CreateServiceAccount(ctx, ServiceAccount{ID: uuid.NewString(), Name: name, AssertionSubject: subject})
	if err != nil || account.AssertionSubject != subject {
		t.Fatalf("create = %+v, %v", account, err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM service_account_assertion_subjects WHERE subject=$1`, subject)
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM service_accounts WHERE id=$1::uuid`, account.ID)
	})
	read, err := store.GetServiceAccount(ctx, account.ID)
	if err != nil || read.AssertionSubject != subject {
		t.Fatalf("read = %+v, %v", read, err)
	}
	if _, err := store.CreateServiceAccount(ctx, ServiceAccount{ID: uuid.NewString(), Name: "other-" + name, AssertionSubject: subject}); !errors.Is(err, ErrAssertionSubjectReserved) {
		t.Fatalf("a held subject err=%v", err)
	}
	// The failed create must not leave the account behind: the name is still free.
	if _, err := store.CreateServiceAccount(ctx, ServiceAccount{ID: uuid.NewString(), Name: "other-" + name}); err != nil {
		t.Fatalf("a rolled-back create left its name taken: %v", err)
	}
	if _, err := store.CreateServiceAccount(ctx, ServiceAccount{ID: uuid.NewString(), Name: name}); !errors.Is(err, ErrNameExists) {
		t.Fatalf("duplicate name err=%v", err)
	}
	withoutSubject, err := store.GetServiceAccount(ctx, mustAccountByName(t, store, "other-"+name))
	if err != nil || withoutSubject.AssertionSubject != "" {
		t.Fatalf("an account without a subject reads back %q, %v", withoutSubject.AssertionSubject, err)
	}

	otherID := mustAccountByName(t, store, "other-"+name)
	if _, _, err := store.PutAssertionAudience(ctx, AssertionAudience{ServiceAccountID: otherID, Audience: "a", MaxLifetimeSeconds: 60}); !errors.Is(err, ErrAssertionSubjectMissing) {
		t.Fatalf("put for an account without a subject err=%v", err)
	}
	if list, err := store.ListAssertionAudiences(ctx, otherID); err != nil || len(list) != 0 {
		t.Fatalf("an empty list of an existing account = %v, %v", list, err)
	}
	if _, err := store.ListAssertionAudiences(ctx, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("list of an unknown account err=%v", err)
	}
	entry, created, err := store.PutAssertionAudience(ctx, AssertionAudience{ServiceAccountID: account.ID, Audience: "api://AzureADTokenExchange", MaxLifetimeSeconds: 300},
		AuditRecord{Actor: "test", Operation: "service_account.assertion_audience.put", Resource: "service-accounts/" + account.ID, Status: 200})
	if err != nil || !created || entry.CreatedAt.IsZero() {
		t.Fatalf("put = %+v %v %v", entry, created, err)
	}
	replaced, created, err := store.PutAssertionAudience(ctx, AssertionAudience{ServiceAccountID: account.ID, Audience: "api://AzureADTokenExchange", MaxLifetimeSeconds: 90})
	if err != nil || created || replaced.MaxLifetimeSeconds != 90 || !replaced.CreatedAt.Equal(entry.CreatedAt) {
		t.Fatalf("replace = %+v %v %v", replaced, created, err)
	}
	if _, _, err := store.PutAssertionAudience(ctx, AssertionAudience{ServiceAccountID: account.ID, Audience: "x", MaxLifetimeSeconds: 59}); err == nil {
		t.Fatal("a lifetime below 60 s must violate the check constraint")
	}
	if _, _, err := store.PutAssertionAudience(ctx, AssertionAudience{ServiceAccountID: account.ID, Audience: "x", MaxLifetimeSeconds: 601}); err == nil {
		t.Fatal("a lifetime above 600 s must violate the check constraint")
	}
	if _, _, err := store.PutAssertionAudience(ctx, AssertionAudience{ServiceAccountID: account.ID, Audience: "  ", MaxLifetimeSeconds: 60}); err == nil {
		t.Fatal("a blank audience must violate the check constraint")
	}
	if _, _, err := store.PutAssertionAudience(ctx, AssertionAudience{ServiceAccountID: uuid.NewString(), Audience: "a", MaxLifetimeSeconds: 60}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("put on an unknown account err=%v", err)
	}
	got, err := store.GetAssertionAudience(ctx, account.ID, "api://AzureADTokenExchange")
	if err != nil || got.MaxLifetimeSeconds != 90 {
		t.Fatalf("get = %+v %v", got, err)
	}
	if _, err := store.GetAssertionAudience(ctx, account.ID, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get of an unlisted audience err=%v", err)
	}
	list, err := store.ListAssertionAudiences(ctx, account.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v %v", list, err)
	}
	if err := store.DeleteAssertionAudience(ctx, account.ID, "api://AzureADTokenExchange"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAssertionAudience(ctx, account.ID, "api://AzureADTokenExchange"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err=%v", err)
	}

	// Deleting the account cascades its allow-list but keeps the subject reserved, owned by nobody.
	if _, _, err := store.PutAssertionAudience(ctx, AssertionAudience{ServiceAccountID: account.ID, Audience: "kept", MaxLifetimeSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM service_accounts WHERE id=$1::uuid`, account.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM service_account_assertion_audiences WHERE service_account_id=$1::uuid`, account.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("the allow-list must cascade: %d %v", count, err)
	}
	var owner *string
	if err := store.db.QueryRowContext(ctx, `SELECT service_account_id::text FROM service_account_assertion_subjects WHERE subject=$1`, subject).Scan(&owner); err != nil || owner != nil {
		t.Fatalf("the subject must survive the deletion with no owner: %v %v", owner, err)
	}
	if _, err := store.CreateServiceAccount(ctx, ServiceAccount{ID: uuid.NewString(), Name: name, AssertionSubject: subject}); !errors.Is(err, ErrAssertionSubjectReserved) {
		t.Fatalf("a later account of the same name must not inherit the subject: %v", err)
	}
}

func mustAccountByName(t *testing.T, store *PostgresStore, name string) string {
	t.Helper()
	var id string
	if err := store.db.QueryRowContext(context.Background(), `SELECT id::text FROM service_accounts WHERE lower(name)=lower($1)`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(context.Background(), `DELETE FROM service_accounts WHERE id=$1::uuid`, id)
	})
	return id
}
