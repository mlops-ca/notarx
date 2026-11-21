// Package reseal re-encrypts every sealed column with the current key of the
// secrets ring, so a retired key can be dropped from
// NOTARX_SETTINGS_ENCRYPTION_KEY_PREVIOUS.
package reseal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/mlops-ca/notarx/internal/identity"
	"github.com/mlops-ca/notarx/internal/remoteinstance"
	"github.com/mlops-ca/notarx/internal/secrets"
	"github.com/mlops-ca/notarx/internal/tokenissuer"
)

// Target describes one column that holds sealed values.
type Target struct {
	// Name is the human label reported for the column, table.column.
	Name string
	// Select returns rows of (row key, purpose key, sealed value) for every
	// non-empty value. It takes no locks.
	Select string
	// Update takes (new value, row key, old value) and is a compare-and-set:
	// it matches only while the stored value is still the one that was read, so
	// a concurrent management write is never overwritten. Where the table has
	// an optimistic-concurrency version it bumps it, so a writer that read the
	// old ciphertext fails its If-Match instead of writing it back.
	Update string
	// Purpose derives the AAD purpose from the purpose key.
	Purpose func(purposeKey string) string
}

// Targets lists every sealed column. A new sealed column must be added here;
// TestTargetsCoverSealedColumns fails when a migration declares a column named
// like a ciphertext that is missing.
func Targets() []Target {
	return []Target{
		{
			Name:    "email_targets.recipient_ciphertext",
			Select:  "SELECT id::text, id::text, recipient_ciphertext FROM email_targets WHERE recipient_ciphertext <> '' ORDER BY email_targets.id",
			Update:  "UPDATE email_targets SET recipient_ciphertext = $1, version = version + 1 WHERE id = $2::uuid AND recipient_ciphertext = $3",
			Purpose: func(id string) string { return "email-target:" + id },
		},
		{
			// Delivery rows snapshot the target ciphertext; their version is the
			// worker's lease state and must not change.
			Name:    "email_test_deliveries.recipient_ciphertext",
			Select:  "SELECT id::text, target_id::text, recipient_ciphertext FROM email_test_deliveries WHERE recipient_ciphertext <> '' ORDER BY email_test_deliveries.id",
			Update:  "UPDATE email_test_deliveries SET recipient_ciphertext = $1 WHERE id = $2::uuid AND recipient_ciphertext = $3",
			Purpose: func(targetID string) string { return "email-target:" + targetID },
		},
		{
			Name:    "nexus_migration_sources.credential_ciphertext",
			Select:  "SELECT id::text, id::text, credential_ciphertext FROM nexus_migration_sources WHERE credential_ciphertext <> '' ORDER BY nexus_migration_sources.id",
			Update:  "UPDATE nexus_migration_sources SET credential_ciphertext = $1, version = version + 1 WHERE id = $2::uuid AND credential_ciphertext = $3",
			Purpose: func(id string) string { return "nexus-source:" + id },
		},
		{
			Name:    "identity_providers.secrets_sealed",
			Select:  "SELECT id::text, id::text, secrets_sealed FROM identity_providers WHERE COALESCE(secrets_sealed, '') <> '' ORDER BY identity_providers.id",
			Update:  "UPDATE identity_providers SET secrets_sealed = $1, version = version + 1 WHERE id = $2::uuid AND secrets_sealed = $3",
			Purpose: func(string) string { return identity.SecretPurpose },
		},
		{
			Name:    "webhook_subscriptions.secret_ciphertext",
			Select:  "SELECT id::text, id::text, secret_ciphertext FROM webhook_subscriptions WHERE secret_ciphertext <> '' ORDER BY webhook_subscriptions.id",
			Update:  "UPDATE webhook_subscriptions SET secret_ciphertext = $1, version = version + 1 WHERE id = $2::uuid AND secret_ciphertext = $3",
			Purpose: func(id string) string { return "webhook-subscription:" + id },
		},
		{
			Name:    "remote_instances.token_sealed",
			Select:  "SELECT id::text, id::text, token_sealed FROM remote_instances WHERE COALESCE(token_sealed, '') <> '' ORDER BY remote_instances.id",
			Update:  "UPDATE remote_instances SET token_sealed = $1, version = version + 1 WHERE id = $2::uuid AND token_sealed = $3",
			Purpose: remoteinstance.TokenPurpose,
		},
		{
			Name:    "hosted_repositories.upstream_auth.secret",
			Select:  "SELECT id::text, id::text, upstream_auth->>'secret' FROM hosted_repositories WHERE COALESCE(upstream_auth->>'secret', '') <> '' ORDER BY hosted_repositories.id",
			Update:  "UPDATE hosted_repositories SET upstream_auth = jsonb_set(upstream_auth, '{secret}', to_jsonb($1::text)), version = version + 1 WHERE id = $2::uuid AND upstream_auth->>'secret' = $3",
			Purpose: func(string) string { return "repository-upstream-auth" },
		},
		{
			// ADR 0020: signing keys are sealed per kid. The integrity MAC of the same rows is
			// refreshed by resealIntegrityMACs, so dropping a retired key keeps them verifiable.
			Name:    "token_signing_keys.private_sealed",
			Select:  "SELECT kid, kid, private_sealed FROM token_signing_keys WHERE private_sealed <> '' ORDER BY kid",
			Update:  "UPDATE token_signing_keys SET private_sealed = $1 WHERE kid = $2 AND private_sealed = $3",
			Purpose: func(kid string) string { return tokenissuer.SealPurpose(kid) },
		},
	}
}

// Result counts one target's rows. Failures names rows that could not be
// resealed (never their contents).
type Result struct {
	Target    string   `json:"target"`
	Scanned   int      `json:"scanned"`
	Resealed  int      `json:"resealed"`
	Unchanged int      `json:"unchanged"`
	Failed    int      `json:"failed"`
	Failures  []string `json:"failures,omitempty"`
}

// ErrRowsFailed reports that some values could not be resealed.
var ErrRowsFailed = errors.New("some sealed values could not be resealed")

// maxPasses bounds how often a target is re-read after concurrent writers made
// compare-and-set updates miss.
const maxPasses = 5

// Run reseals every target and returns one Result per target. Each row is its
// own compare-and-set statement, so no lock outlives a statement and a
// concurrent management write simply wins and is picked up on the next pass.
// With dryRun nothing is written; Resealed counts what a real run would
// change. A value that cannot be opened with the ring is recorded under
// Failures and skipped; every other row is still processed, and Run returns
// ErrRowsFailed at the end. Reseal is idempotent: rerun it after fixing the ring.
func Run(ctx context.Context, db *sql.DB, dryRun bool) ([]Result, error) {
	return RunScoped(ctx, db, dryRun, Everything)
}

// Scope narrows a run to some of the sealed columns and names the audit record of the run.
type Scope struct {
	// Includes reports whether the column named table.column is part of the run.
	Includes func(name string) bool
	// Actor, Operation and Resource identify the run in security_audit_log.
	Actor, Operation, Resource string
}

// Everything is the scope of notarxctl secrets reseal: every sealed column.
var Everything = Scope{Includes: func(string) bool { return true }, Actor: Actor, Operation: Operation, Resource: "secrets"}

// SigningKeys is the scope of notarxctl issuer reseal: the sealed private keys of the OIDC issuer and the integrity
// MACs of the same rows (ADR 0020).
var SigningKeys = Scope{
	Includes: func(name string) bool { return strings.HasPrefix(name, "token_signing_keys.") },
	Actor:    "cli:issuer-reseal", Operation: "issuer.reseal", Resource: "issuer",
}

// RunScoped is Run limited to the columns of scope.
func RunScoped(ctx context.Context, db *sql.DB, dryRun bool, scope Scope) ([]Result, error) {
	results := []Result{}
	var runErr error
	for _, target := range Targets() {
		if !scope.Includes(target.Name) {
			continue
		}
		result, err := runTarget(ctx, db, target, dryRun)
		results = append(results, result)
		if err != nil {
			return results, fmt.Errorf("%s: %w", target.Name, err)
		}
		if result.Failed > 0 {
			runErr = ErrRowsFailed
		}
	}
	if !scope.Includes(macTargetName) {
		return results, runErr
	}
	result, err := resealIntegrityMACs(ctx, db, dryRun)
	results = append(results, result)
	if err != nil {
		return results, fmt.Errorf("%s: %w", result.Target, err)
	}
	if result.Failed > 0 {
		runErr = ErrRowsFailed
	}
	return results, runErr
}

const macTargetName = "token_signing_keys.integrity_mac"

// resealIntegrityMACs recomputes the integrity MAC of signing key rows that were MACed with a
// retired ring key. A row whose current MAC does not verify is reported and left alone, so
// reseal can never launder a tampered row into a valid one.
func resealIntegrityMACs(ctx context.Context, db *sql.DB, dryRun bool) (Result, error) {
	result := Result{Target: macTargetName}
	for pass := 1; ; pass++ {
		rows, err := db.QueryContext(ctx, "SELECT kid, alg, state, public_jwk::text, integrity_mac FROM token_signing_keys ORDER BY kid")
		if err != nil {
			return result, err
		}
		var records []tokenissuer.KeyRecord
		for rows.Next() {
			var record tokenissuer.KeyRecord
			var state, jwk string
			if err := rows.Scan(&record.KID, &record.Alg, &state, &jwk, &record.IntegrityMAC); err != nil {
				_ = rows.Close()
				return result, err
			}
			record.State, record.PublicJWK = tokenissuer.State(state), []byte(jwk)
			records = append(records, record)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return result, err
		}
		result = Result{Target: macTargetName}
		retry := false
		for _, record := range records {
			result.Scanned++
			need, err := secrets.MACNeedsRefresh(record.IntegrityMAC)
			if err == nil && need {
				err = record.VerifyIntegrity()
			}
			switch {
			case err != nil:
				result.Failed++
				result.Failures = append(result.Failures, fmt.Sprintf("row %s: %v", record.KID, err))
			case !need:
				result.Unchanged++
			case dryRun:
				result.Resealed++
			default:
				previous := record.IntegrityMAC
				if err := record.Seal(); err != nil {
					return result, err
				}
				done, err := db.ExecContext(ctx, "UPDATE token_signing_keys SET integrity_mac = $1 WHERE kid = $2 AND integrity_mac = $3 AND state = $4", record.IntegrityMAC, record.KID, previous, string(record.State))
				if err != nil {
					return result, fmt.Errorf("row %s: %w", record.KID, err)
				}
				if n, err := done.RowsAffected(); err != nil {
					return result, err
				} else if n == 0 {
					retry = true // the rotation worker changed the row after it was read
					continue
				}
				result.Resealed++
			}
		}
		if !retry {
			return result, nil
		}
		if pass >= maxPasses {
			return result, errors.New("rows keep changing concurrently; retry when writes quiesce")
		}
	}
}

type row struct{ key, purposeKey, value string }

func readRows(ctx context.Context, db *sql.DB, query string) ([]row, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.key, &r.purposeKey, &r.value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func runTarget(ctx context.Context, db *sql.DB, target Target, dryRun bool) (Result, error) {
	result := Result{Target: target.Name}
	settled := map[string]bool{} // rows already counted in this result
	for pass := 1; ; pass++ {
		rows, err := readRows(ctx, db, target.Select)
		if err != nil {
			return result, err
		}
		retry := false
		for _, r := range rows {
			if settled[r.key] {
				continue
			}
			sealed, changed, err := secrets.Reseal(target.Purpose(r.purposeKey), r.value)
			switch {
			case err != nil:
				result.Failed++
				result.Failures = append(result.Failures, fmt.Sprintf("row %s: %v", r.key, err))
			case !changed:
				result.Unchanged++
			case dryRun:
				result.Resealed++
			default:
				done, err := db.ExecContext(ctx, target.Update, sealed, r.key, r.value)
				if err != nil {
					return result, fmt.Errorf("row %s: %w", r.key, err)
				}
				n, err := done.RowsAffected()
				if err != nil {
					return result, err
				}
				if n == 0 {
					// The row changed after it was read; look at it again.
					retry = true
					continue
				}
				result.Resealed++
			}
			settled[r.key] = true
			result.Scanned++
		}
		if !retry {
			return result, nil
		}
		if pass >= maxPasses {
			return result, errors.New("rows keep changing concurrently; retry when writes quiesce")
		}
	}
}
