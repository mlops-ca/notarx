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
