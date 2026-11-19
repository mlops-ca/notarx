//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"
)

// TestBaselineSchemaInvariants asserts the constraints and defaults that earlier
// migrations introduced and the re-squashed baseline (ADR 0015) now carries, so a
// regeneration or an edit that drops one is noticed.
func TestBaselineSchemaInvariants(t *testing.T) {
	store := openProviderStore(t)
	ctx := context.Background()
	definition := func(table, constraint string) string {
		t.Helper()
		var def string
		if err := store.db.QueryRowContext(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = to_regclass($1) AND conname = $2`, "public."+table, constraint).Scan(&def); err != nil {
			t.Fatalf("%s.%s: %v", table, constraint, err)
		}
		return def
	}
	columnDefault := func(table, column string) string {
		t.Helper()
		var def string
		if err := store.db.QueryRowContext(ctx, `SELECT COALESCE(column_default, '') FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, table, column).Scan(&def); err != nil {
			t.Fatalf("%s.%s: %v", table, column, err)
		}
		return def
	}
	for _, table := range []string{"email_targets", "email_test_deliveries"} {
		def := definition(table, table+"_locale_check")
		if !strings.Contains(def, "en-CA") || !strings.Contains(def, "fr-CA") || !strings.Contains(def, "zh-CN") || strings.Contains(def, "'en'::text") {
			t.Errorf("%s locale check must allow exactly the BCP 47 tags: %s", table, def)
		}
		if got := columnDefault(table, "locale"); !strings.Contains(got, "en-CA") {
			t.Errorf("%s locale default = %q, want en-CA", table, got)
		}
	}
	for _, table := range []string{"hosted_repositories", "hosted_groups"} {
		def := definition(table, table+"_visibility_check")
		for _, value := range []string{"private", "authenticated", "public"} {
			if !strings.Contains(def, "'"+value+"'") {
				t.Errorf("%s visibility check misses %s: %s", table, value, def)
			}
		}
		if got := columnDefault(table, "visibility"); !strings.Contains(got, "private") {
			t.Errorf("%s visibility default = %q, want private", table, got)
		}
		var retired int
		if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name LIKE 'anonymous%'`, table).Scan(&retired); err != nil || retired != 0 {
			t.Errorf("%s keeps a retired anonymous column: %d %v", table, retired, err)
		}
	}
	if def := definition("user_sessions", "user_sessions_kind_check"); !strings.Contains(def, "saml") {
		t.Errorf("user_sessions kind must allow saml: %s", def)
	}
	var registry, legacy bool
	if err := store.db.QueryRowContext(ctx, `SELECT to_regclass('public.identity_providers') IS NOT NULL, to_regclass('public.oidc_settings') IS NOT NULL`).Scan(&registry, &legacy); err != nil || !registry || legacy {
		t.Fatalf("identity_providers must exist and oidc_settings must not: %v %v %v", registry, legacy, err)
	}
}
