// Package reseal re-encrypts every sealed column with the current key of the
// secrets ring, so a retired key can be dropped from
// NOTARX_SETTINGS_ENCRYPTION_KEY_PREVIOUS.
package reseal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/mlops-ca/notarx/internal/secrets"
)

// Target describes one column that holds sealed values.
type Target struct {
	// Name is the human label reported for the column, table.column.
	Name string
	// Select returns rows of (row key, purpose key, sealed value). Empty values
	// are skipped. It must lock the rows (FOR UPDATE).
	Select string
	// Update takes (new value, row key) and rewrites one sealed value.
	Update string
	// Purpose derives the AAD purpose from the purpose key.
	Purpose func(purposeKey string) string
}

// Targets lists every sealed column. A new sealed column must be added here;
// TestTargetsCoverSealedColumns fails when a baseline column named like a
// ciphertext is missing.
func Targets() []Target {
	return []Target{
		{
			Name:    "email_targets.recipient_ciphertext",
			Select:  "SELECT id::text, id::text, recipient_ciphertext FROM email_targets WHERE recipient_ciphertext <> '' ORDER BY id FOR UPDATE",
			Update:  "UPDATE email_targets SET recipient_ciphertext = $1 WHERE id = $2::uuid",
			Purpose: func(id string) string { return "email-target:" + id },
		},
		{
			Name:    "email_test_deliveries.recipient_ciphertext",
			Select:  "SELECT id::text, target_id::text, recipient_ciphertext FROM email_test_deliveries WHERE recipient_ciphertext <> '' ORDER BY id FOR UPDATE",
			Update:  "UPDATE email_test_deliveries SET recipient_ciphertext = $1 WHERE id = $2::uuid",
			Purpose: func(targetID string) string { return "email-target:" + targetID },
		},
		{
			Name:    "nexus_migration_sources.credential_ciphertext",
			Select:  "SELECT id::text, id::text, credential_ciphertext FROM nexus_migration_sources WHERE credential_ciphertext <> '' ORDER BY id FOR UPDATE",
			Update:  "UPDATE nexus_migration_sources SET credential_ciphertext = $1 WHERE id = $2::uuid",
			Purpose: func(id string) string { return "nexus-source:" + id },
		},
		{
			Name:    "oidc_settings.client_secret",
			Select:  "SELECT singleton::text, singleton::text, client_secret FROM oidc_settings WHERE client_secret <> '' FOR UPDATE",
			Update:  "UPDATE oidc_settings SET client_secret = $1 WHERE singleton = $2::text::boolean",
			Purpose: func(string) string { return "oidc.client-secret" },
		},
		{
			Name:    "webhook_subscriptions.secret_ciphertext",
			Select:  "SELECT id::text, id::text, secret_ciphertext FROM webhook_subscriptions WHERE secret_ciphertext <> '' ORDER BY id FOR UPDATE",
			Update:  "UPDATE webhook_subscriptions SET secret_ciphertext = $1 WHERE id = $2::uuid",
			Purpose: func(id string) string { return "webhook-subscription:" + id },
		},
		{
			Name:    "hosted_repositories.upstream_auth.secret",
			Select:  "SELECT id::text, id::text, upstream_auth->>'secret' FROM hosted_repositories WHERE COALESCE(upstream_auth->>'secret', '') <> '' ORDER BY id FOR UPDATE",
			Update:  "UPDATE hosted_repositories SET upstream_auth = jsonb_set(upstream_auth, '{secret}', to_jsonb($1::text)) WHERE id = $2::uuid",
			Purpose: func(string) string { return "repository-upstream-auth" },
		},
	}
}

// Result counts one target's rows.
type Result struct {
	Target    string `json:"target"`
	Scanned   int    `json:"scanned"`
	Resealed  int    `json:"resealed"`
	Unchanged int    `json:"unchanged"`
}

// Run reseals every target. Each target is one transaction that holds its rows
// locked, so a concurrent writer waits instead of being overwritten. With
// dryRun nothing is committed; the counts show what a real run would change.
// A value that cannot be opened with the ring aborts that target and the run,
// naming the row but never its contents. The operation is idempotent: rerun it
// after fixing the ring.
func Run(ctx context.Context, db *sql.DB, dryRun bool) ([]Result, error) {
	results := []Result{}
	for _, target := range Targets() {
		result, err := runTarget(ctx, db, target, dryRun)
		if err != nil {
			return results, fmt.Errorf("%s: %w", target.Name, err)
		}
		results = append(results, result)
	}
	return results, nil
}

type row struct{ key, purposeKey, value string }

func runTarget(ctx context.Context, db *sql.DB, target Target, dryRun bool) (result Result, err error) {
	result.Target = target.Name
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() {
		if err != nil || dryRun {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) && err == nil {
				err = rollbackErr
			}
		}
	}()
	rows, err := tx.QueryContext(ctx, target.Select)
	if err != nil {
		return result, err
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.key, &r.purposeKey, &r.value); err != nil {
			_ = rows.Close()
			return result, err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return result, err
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	for _, r := range pending {
		result.Scanned++
		sealed, changed, err := secrets.Reseal(target.Purpose(r.purposeKey), r.value)
		if err != nil {
			return result, fmt.Errorf("row %s: %w", r.key, err)
		}
		if !changed {
			result.Unchanged++
			continue
		}
		result.Resealed++
		if dryRun {
			continue
		}
		if _, err := tx.ExecContext(ctx, target.Update, sealed, r.key); err != nil {
			return result, fmt.Errorf("row %s: %w", r.key, err)
		}
	}
	if dryRun {
		return result, nil
	}
	return result, tx.Commit()
}
