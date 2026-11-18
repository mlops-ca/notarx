//go:build integration

package preflight

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/mlops-ca/notarx/internal/testsupport"
)

// The migration role of a deployment may lack the privilege to install pg_trgm.
// This test builds that situation: a role without CREATE on a pristine
// database, next to the superuser of the integration PostgreSQL.
func TestTrigramPreflightAgainstPostgres(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	if err := testsupport.ValidateIsolatedDatabaseURL(raw); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	control, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	database, role, password := "trgm_"+suffix, "trgm_role_"+suffix, "pw"+suffix
	for _, statement := range []string{
		"CREATE ROLE " + role + " LOGIN PASSWORD '" + password + "'",
		"CREATE DATABASE " + database + " TEMPLATE template0",
		"REVOKE ALL ON DATABASE " + database + " FROM PUBLIC",
		"GRANT CONNECT ON DATABASE " + database + " TO " + role,
	} {
		if _, err := control.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = control.Exec("DROP DATABASE IF EXISTS " + database + " WITH (FORCE)")
		_, _ = control.Exec("DROP ROLE IF EXISTS " + role)
	})
	urlFor := func(user *url.Userinfo) string {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		parsed.Path = "/" + database
		if user != nil {
			parsed.User = user
		}
		return parsed.String()
	}
	restricted := urlFor(url.UserPassword(role, password))
	privileged := urlFor(nil)

	state := func(connection string) string {
		t.Helper()
		got, err := probeTrigram(ctx, connection)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := state(restricted); got != "denied" {
		t.Fatalf("role without CREATE: state = %q, want denied", got)
	}
	if result := trigramResult(state(restricted), nil); result.Status != StatusFail || !strings.Contains(result.Summary, "may not create pg_trgm") {
		t.Fatalf("restricted result = %#v", result)
	}
	if got := state(privileged); got != "creatable" {
		t.Fatalf("superuser: state = %q, want creatable", got)
	}
	admin, err := sql.Open("pgx", privileged)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err := admin.ExecContext(ctx, "CREATE EXTENSION pg_trgm"); err != nil {
		t.Fatal(err)
	}
	for name, connection := range map[string]string{"role": restricted, "superuser": privileged} {
		if got := state(connection); got != "installed" {
			t.Fatalf("%s after install: state = %q, want installed", name, got)
		}
	}
}
