package baselinefmt

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// dumpFixtures are real pg_dump outputs of the same schema from each supported
// major version. The generator pins the newest (see scripts/baseline-lib.sh); the
// older one stays because the support matrix still lists it (docs/postgresql-capabilities.md).
var dumpFixtures = []string{"testdata/pg_dump_16.sql", "testdata/pg_dump_18.sql"}

func normalizeFixture(t *testing.T, fixture string) string {
	t.Helper()
	dump, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Normalize(dump)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func forEachFixture(t *testing.T, check func(t *testing.T, got string)) {
	t.Helper()
	for _, fixture := range dumpFixtures {
		t.Run(fixture, func(t *testing.T) { check(t, normalizeFixture(t, fixture)) })
	}
}

func TestNormalizeMatchesGolden(t *testing.T) {
	forEachFixture(t, func(t *testing.T, got string) {
		want, err := os.ReadFile("testdata/baseline.golden.sql")
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Fatalf("output differs from testdata/baseline.golden.sql:\n%s", got)
		}
	})
}

func TestNormalizeStripsPreambleOwnershipAndLedger(t *testing.T) {
	forEachFixture(t, func(t *testing.T, got string) {
		for _, banned := range []string{
			"\\restrict", "\\unrestrict", "set_config", "SET statement_timeout", "SET check_function_bodies",
			"OWNER TO", "COMMENT ON EXTENSION", "notarx_schema_migrations", "schema_version",
			"WITH SCHEMA public", "Dumped from", "PostgreSQL database dump",
		} {
			if strings.Contains(got, banned) {
				t.Errorf("normalised output still contains %q", banned)
			}
		}
		for _, kept := range []string{
			"-- +goose Up\n", "CREATE EXTENSION IF NOT EXISTS pg_trgm;\n", "CREATE FUNCTION public.touch()",
			"CREATE TABLE public.items (", "gin_trgm_ops", "ADD CONSTRAINT items_pkey",
		} {
			if !strings.Contains(got, kept) {
				t.Errorf("normalised output lost %q", kept)
			}
		}
		if strings.Contains(got, "-- +goose Down") {
			t.Error("baseline must not carry a Down section")
		}
		if strings.Count(got, "CREATE EXTENSION") != 2 {
			t.Errorf("extensions must come only from the fixed header (pg_trgm and the guarded pg_stat_statements)")
		}
	})
}

func TestNormalizeGuardsOptionalExtension(t *testing.T) {
	forEachFixture(t, func(t *testing.T, got string) {
		if !strings.Contains(got, "DO $$") || !strings.Contains(got, "EXCEPTION WHEN OTHERS THEN") ||
			!strings.Contains(got, "RAISE NOTICE") {
			t.Fatal("pg_stat_statements must be created in a guarded block")
		}
		if strings.Contains(got, "CREATE EXTENSION IF NOT EXISTS pg_stat_statements;\n\n") {
			t.Fatal("pg_stat_statements must not be created outside the guarded block")
		}
	})
}

func TestNormalizeSeedHasExactlyTheFourSingletons(t *testing.T) {
	forEachFixture(t, func(t *testing.T, got string) {
		seed := got[strings.Index(got, "-- seed:"):]
		if n := strings.Count(seed, "INSERT INTO"); n != 4 {
			t.Fatalf("seed has %d INSERT statements, want 4", n)
		}
		for _, table := range []string{"audit_retention_policy", "anonymous_access_policy", "site_settings", "installation_repository_presets"} {
			if !strings.Contains(seed, "INSERT INTO public."+table+" ") {
				t.Errorf("seed missing %s", table)
			}
		}
		for _, migration := range []string{"000049_", "000055_", "000107_", "000174_"} {
			if !strings.Contains(seed, "Originating migration: "+migration) {
				t.Errorf("seed does not name migration %s", migration)
			}
		}
		if !strings.Contains(seed, "'standard-v1', 'pending'") || strings.Contains(seed, "skipped_existing'") {
			t.Error("presets seed must be standard-v1 pending")
		}
		if strings.Contains(got, "GRANT ") || strings.Contains(got, "PUBLIC") {
			t.Error("baseline must not grant to PUBLIC")
		}
	})
}

func TestNormalizeAddsIfNotExistsToOtherExtensions(t *testing.T) {
	got, err := Normalize([]byte("CREATE EXTENSION citext WITH SCHEMA public;\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "CREATE EXTENSION IF NOT EXISTS citext WITH SCHEMA public;") {
		t.Fatalf("missing IF NOT EXISTS:\n%s", got)
	}
	got, err = Normalize([]byte("CREATE EXTENSION IF NOT EXISTS citext WITH SCHEMA public;\n"))
	if err != nil || strings.Contains(string(got), "IF NOT EXISTS IF NOT EXISTS") {
		t.Fatalf("IF NOT EXISTS duplicated: %v\n%s", err, got)
	}
}

func TestNormalizeRejectsDataPrivilegesAndUnknownInput(t *testing.T) {
	cases := map[string]string{
		"copy":          "CREATE TABLE public.t (id int);\n\nCOPY public.t (id) FROM stdin;\n1\n\\.\n",
		"insert":        "INSERT INTO public.t VALUES (1);\n",
		"grant":         "GRANT SELECT ON TABLE public.t TO PUBLIC;\n",
		"revoke":        "REVOKE ALL ON TABLE public.t FROM PUBLIC;\n",
		"select":        "SELECT 1;\n",
		"meta":          "\\connect other\n",
		"unterminated":  "CREATE TABLE public.t (id int)\n",
		"dollar-opened": "CREATE FUNCTION public.f() RETURNS int AS $$ SELECT 1;\n",
	}
	for name, dump := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Normalize([]byte(dump)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if _, err := Normalize(nil); !errors.Is(err, ErrNoDump) {
		t.Fatalf("empty dump error = %v", err)
	}
}

func TestSplitKeepsSemicolonsInsideDollarQuotes(t *testing.T) {
	dump := "CREATE FUNCTION public.f() RETURNS int\n    LANGUAGE sql\n    AS $fn$\nSELECT 1;\n$fn$;\n"
	statements, err := split([]byte(dump))
	if err != nil || len(statements) != 1 {
		t.Fatalf("statements = %d, err = %v", len(statements), err)
	}
}
