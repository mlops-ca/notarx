//go:build integration

package repository

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
)

// TestEmailLocaleMigrationRewritesLegacyTagsAndConstrains replays migration
// 000002 over a legacy row inside a rolled-back transaction (ADR 0014).
func TestEmailLocaleMigrationRewritesLegacyTagsAndConstrains(t *testing.T) {
	connection := os.Getenv("TEST_DATABASE_URL")
	if connection == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	migration, err := os.ReadFile("../../migrations/000002_email_locale_tags.sql")
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
	// Return to the pre-migration shape, then insert a legacy row.
	if _, err = tx.ExecContext(ctx, `ALTER TABLE email_targets DROP CONSTRAINT email_targets_locale_check`); err != nil {
		t.Fatal(err)
	}
	legacy := uuid.NewString()
	if _, err = tx.ExecContext(ctx, `INSERT INTO email_targets(id,name,locale,recipient_ciphertext) VALUES($1,'legacy','en','synthetic')`, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	var locale string
	if err = tx.QueryRowContext(ctx, `SELECT locale FROM email_targets WHERE id=$1`, legacy).Scan(&locale); err != nil || locale != "en-CA" {
		t.Fatalf("legacy locale = %q, %v", locale, err)
	}
	for _, tag := range []string{"en-CA", "fr-CA", "zh-CN"} {
		if _, err = tx.ExecContext(ctx, "SAVEPOINT ok"); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO email_targets(id,name,locale,recipient_ciphertext) VALUES($1,'ok',$2,'synthetic')`, uuid.NewString(), tag); err != nil {
			t.Fatalf("%s must be accepted: %v", tag, err)
		}
	}
	for _, tag := range []string{"en", "en-US", "fr", ""} {
		if _, err = tx.ExecContext(ctx, "SAVEPOINT bad"); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO email_targets(id,name,locale,recipient_ciphertext) VALUES($1,'bad',$2,'synthetic')`, uuid.NewString(), tag); err == nil {
			t.Fatalf("%q must be rejected", tag)
		}
		if _, err = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT bad"); err != nil {
			t.Fatal(err)
		}
	}
	var def string
	if err = tx.QueryRowContext(ctx, `SELECT column_default FROM information_schema.columns WHERE table_name='email_targets' AND column_name='locale'`).Scan(&def); err != nil || def != "'en-CA'::text" {
		t.Fatalf("default = %q, %v", def, err)
	}
}
