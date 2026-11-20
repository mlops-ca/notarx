package notarxctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/secrets"
	"github.com/mlops-ca/notarx/internal/secrets/reseal"
)

const (
	secretsDatabaseEnv  = "NOTARX_DATABASE_URL"
	secretsResealHint   = "re-run with --yes to confirm, or with --dry-run to count what would change"
	secretsResealDBHint = "export " + secretsDatabaseEnv + " (the same value the Gateway uses) before running this command"
)

// secretsResealCommand re-encrypts every sealed column with the current key of
// the ring. It talks to PostgreSQL directly, not through the management API,
// because it needs the encryption keys, which the API never exposes.
func secretsResealCommand() command {
	return command{
		name:     "secrets reseal",
		synopsis: "re-encrypt all sealed settings with the current encryption key",
		usage:    "notarxctl secrets reseal --yes [--dry-run] [--json]",
		meta: commandMeta{
			Resource:     "local",
			Scenarios:    []string{"admin"},
			Stage:        "m2",
			Endpoints:    []string{},
			Mutating:     true,
			MinimumRole:  "none",
			OutputSchema: "ResealResult",
			DryRun:       "local-count",
		},
		run: func(ctx context.Context, env *commandContext) error {
			return runReseal(ctx, env, reseal.Everything, "secrets reseal", "sealed settings")
		},
	}
}

// runReseal is the body of the reseal commands: it opens the database directly, because the encryption keys never
// reach the management API, and reseals the columns of scope. label names what the run rewrites.
func runReseal(ctx context.Context, env *commandContext, scope reseal.Scope, commandName, label string) error {
	if !env.globals.dryRun && !env.globals.yes {
		return &UsageError{Message: "notarxctl " + commandName + " needs --yes to rewrite " + label, Hint: secretsResealHint}
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("NOTARX_DATABASE_DRIVER")), database.DriverMySQL) {
		return &UsageError{Message: commandName + " supports PostgreSQL only: NOTARX_DATABASE_DRIVER=mysql has no store for it yet", Hint: "unset NOTARX_DATABASE_DRIVER or use a PostgreSQL database"}
	}
	databaseURL := os.Getenv(secretsDatabaseEnv)
	if databaseURL == "" {
		return &UsageError{Message: secretsDatabaseEnv + " is not set", Hint: secretsResealDBHint}
	}
	if err := secrets.ValidateRing(); err != nil {
		return &UsageError{Message: err.Error(), Hint: "export " + secrets.KeyEnv + " (and " + secrets.KeyPreviousEnv + " while retired keys remain)"}
	}
	db, err := database.OpenPostgres(databaseURL, database.DefaultPoolConfig())
	if err != nil {
		// The error can quote the URL, which carries the password.
		return fmt.Errorf("open database: invalid %s", secretsDatabaseEnv)
	}
	defer func() { _ = db.Close() }()
	results, runErr := reseal.RunAuditedScoped(ctx, db, env.globals.dryRun, scope)
	if runErr != nil && len(results) == 0 {
		return runErr
	}
	payload := newListPayload(results)
	meta := &envelopeMeta{Pagination: &paginationMeta{Mode: "none"}}
	if err := env.render(payload, meta, func(w io.Writer) error {
		rows := [][]string{{"COLUMN", "SCANNED", "RESEALED", "UNCHANGED", "FAILED"}}
		for _, result := range results {
			rows = append(rows, []string{result.Target, strconv.Itoa(result.Scanned), strconv.Itoa(result.Resealed), strconv.Itoa(result.Unchanged), strconv.Itoa(result.Failed)})
		}
		if err := writeColumns(w, rows); err != nil {
			return err
		}
		for _, result := range results {
			for _, failure := range result.Failures {
				if _, err := fmt.Fprintf(w, "%s %s\n", result.Target, failure); err != nil {
					return err
				}
			}
		}
		if env.globals.dryRun {
			_, err := fmt.Fprintln(w, "dry run: nothing was written")
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	if errors.Is(runErr, reseal.ErrAuditMissing) {
		_, _ = fmt.Fprintln(env.stderr, "warning: sealed values were rewritten but the security audit record is missing; record this run manually")
	}
	return runErr
}
