//go:build integration

package repository

import (
	"context"
	"os"
	"testing"
)

// TestMemberTablesStoreNoDerivedVisibility checks the migrated schema: no group
// member table carries a visibility or anonymous column (ADR 0029).
func TestMemberTablesStoreNoDerivedVisibility(t *testing.T) {
	connection := os.Getenv("TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	store, err := NewPostgresStore(connection)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	var derived int
	err = store.db.QueryRowContext(context.Background(), `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name LIKE '%\_members' ESCAPE '\'
		AND (column_name = 'anonymous' OR column_name LIKE 'visibility%')`).Scan(&derived)
	if err != nil || derived != 0 {
		t.Fatalf("member tables store derived visibility: %d columns, %v", derived, err)
	}
}

// TestMigration000006TurnsMixedLegacyGroupsPrivate replays migration 000006 over
// the pre-migration shape in a rolled-back transaction: a legacy group that had a
// non-anonymous member must not become wider when the per-member flag is dropped.
func TestMigration000006TurnsMixedLegacyGroupsPrivate(t *testing.T) {
	connection := os.Getenv("TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	migration, err := os.ReadFile("../../migrations/000007_group_member_visibility_computed.sql")
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresStore(connection)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range []string{"oci", "maven", "raw", "conan"} {
		if _, err = tx.ExecContext(ctx, "ALTER TABLE "+table+"_group_members ADD COLUMN anonymous boolean NOT NULL DEFAULT false"); err != nil {
			t.Fatal(err)
		}
	}
	for _, group := range []struct {
		name    string
		members []bool
	}{{"mig-mixed", []bool{true, false}}, {"mig-open", []bool{true, true}}} {
		if _, err = tx.ExecContext(ctx, "INSERT INTO maven_groups (name, enabled, anonymous) VALUES ($1, true, true)", group.name); err != nil {
			t.Fatal(err)
		}
		for position, anonymous := range group.members {
			if _, err = tx.ExecContext(ctx, "INSERT INTO maven_group_members (group_name, name, member_type, endpoint, position, anonymous) VALUES ($1, $2, 'hosted', '', $3, $4)", group.name, group.name+string(rune('a'+position)), position, anonymous); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = tx.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"mig-mixed": false, "mig-open": true} {
		var anonymous bool
		if err = tx.QueryRowContext(ctx, "SELECT anonymous FROM maven_groups WHERE name=$1", name).Scan(&anonymous); err != nil || anonymous != want {
			t.Fatalf("%s anonymous = %v (%v), want %v", name, anonymous, err, want)
		}
	}
}
