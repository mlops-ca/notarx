//go:build integration

package pgstore

import (
	"database/sql"
	"os"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/mlops-ca/notarx/internal/testsupport"
)

// The id-keyset listing selects id::text, so a bare ORDER BY id would sort that text output column and
// add a Sort node on every page. The statement names the table column and reads the primary key in order
// (ADR 0023 sections 4 and 8).
func TestListingReadsThePrimaryKeyInOrder(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	id := uuid.NewString()
	testsupport.RequireOrderedByIndex(t, db, "nexus_migration_entries", listEntriesQuery, id, id, 50)
}
