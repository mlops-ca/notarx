package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MetaSweepOutcome is what the sweep did with one object of the meta namespace.
type MetaSweepOutcome string

const (
	// MetaSweepDeleted means no row referenced the object and its pin was older than the grace period.
	MetaSweepDeleted MetaSweepOutcome = "deleted"
	// MetaSweepReferenced means a row still references the object.
	MetaSweepReferenced MetaSweepOutcome = "referenced"
	// MetaSweepYoung means the object was written, or first seen, within the grace period.
	MetaSweepYoung MetaSweepOutcome = "young"
)

// MetaSweepName is the name of the sweep lease: one row, one sweep at a time (ADR 0023 section 9).
const MetaSweepName = "meta"

// MetaSweepStore is the database side of the sweep of unreferenced objects of the meta namespace
// (ADR 0023 section 9). It is deliberately independent of GatewayStore: the MySQL twin lands with the
// ADR 0022 store slices.
type MetaSweepStore interface {
	// AcquireMetaSweepLease takes the sweep lease for owner when no other node holds an unexpired one and the
	// last sweep completed at least interval ago. It reports whether owner now holds it.
	AcquireMetaSweepLease(ctx context.Context, owner string, ttl, interval time.Duration) (bool, error)
	// RenewMetaSweepLease extends the lease owner holds, and fails with ErrNotFound once another node took it.
	RenewMetaSweepLease(ctx context.Context, owner string, ttl time.Duration) error
	// FinishMetaSweepLease releases the lease; completed records the sweep as done, so the next one waits for
	// the interval, and otherwise another node may retry at once.
	FinishMetaSweepLease(ctx context.Context, owner string, completed bool) error
	// SweepMetaObject runs remove, and forgets the object's pin, when digest ("sha256:<hex>") is referenced by
	// no row, was last pinned at least grace ago and has been seen unreferenced by an earlier sweep at least grace
	// ago; the first sweep that finds an unreferenced object only marks it. The reference check, the pin check and
	// remove run while the pin row is locked, which is what keeps a concurrent writer of the same bytes from losing
	// them.
	SweepMetaObject(ctx context.Context, digest string, grace time.Duration, remove func(context.Context) error) (MetaSweepOutcome, error)
	// MetaObjectPinsOlderThan returns up to limit digests after the given one, in order, whose pin is at least
	// grace old. The sweep compares them with the objects that exist: a pin without an object is left by a put
	// that failed after pinning.
	MetaObjectPinsOlderThan(ctx context.Context, after string, grace time.Duration, limit int) ([]string, error)
	// ForgetMetaObjectPin removes the pin of digest when it is still at least grace old.
	ForgetMetaObjectPin(ctx context.Context, digest string, grace time.Duration) error
}

func metaDigestKey(digest string) (string, error) {
	hexPart, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hexPart) != sha256.Size*2 {
		return "", fmt.Errorf("meta object digest must be sha256:<64 hex characters>")
	}
	if _, err := hex.DecodeString(hexPart); err != nil || strings.ToLower(hexPart) != hexPart {
		return "", fmt.Errorf("meta object digest must be sha256:<64 hex characters>")
	}
	return "meta/sha256/" + hexPart, nil
}

// pinMetaObject records that the object of data is about to be written, before the put: the sweep reads the pin as
// the age of the object and waits on its row while it deletes. With q the autocommit database the pin is committed
// when the call returns; with the transaction that writes the referencing row it commits with the row, and the
// sweep waits for it on the pin's row lock, then sees a fresh pin.
func (s *PostgresStore) pinMetaObject(ctx context.Context, q sqlRunner, data []byte) error {
	if s.db == nil {
		// A store with no database, as the unit tests of the payload paths build, has nothing to pin in.
		return nil
	}
	sum := sha256.Sum256(data)
	_, err := q.ExecContext(ctx, `INSERT INTO meta_object_pins (digest, seen_at) VALUES ($1, now())
		ON CONFLICT (digest) DO UPDATE SET seen_at = now(), unreferenced_since = NULL`, "sha256:"+hex.EncodeToString(sum[:]))
	if err != nil {
		return fmt.Errorf("pin meta object: %w", err)
	}
	return nil
}

// AcquireMetaSweepLease implements MetaSweepStore.
func (s *PostgresStore) AcquireMetaSweepLease(ctx context.Context, owner string, ttl, interval time.Duration) (bool, error) {
	var acquired int
	err := s.db.QueryRowContext(ctx, `INSERT INTO meta_sweep_lease (name, owner, expires_at) VALUES ($1, $2, now() + make_interval(secs => $3))
		ON CONFLICT (name) DO UPDATE SET owner = EXCLUDED.owner, expires_at = EXCLUDED.expires_at
		WHERE meta_sweep_lease.expires_at <= now()
		  AND (meta_sweep_lease.last_completed_at IS NULL OR meta_sweep_lease.last_completed_at <= now() - make_interval(secs => $4))
		RETURNING 1`, MetaSweepName, owner, ttl.Seconds(), interval.Seconds()).Scan(&acquired)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("acquire meta sweep lease: %w", err)
	}
	return true, nil
}

// RenewMetaSweepLease implements MetaSweepStore.
func (s *PostgresStore) RenewMetaSweepLease(ctx context.Context, owner string, ttl time.Duration) error {
	result, err := s.db.ExecContext(ctx, `UPDATE meta_sweep_lease SET expires_at = now() + make_interval(secs => $3)
		WHERE name = $1 AND owner = $2 AND expires_at > now()`, MetaSweepName, owner, ttl.Seconds())
	if err != nil {
		return fmt.Errorf("renew meta sweep lease: %w", err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// FinishMetaSweepLease implements MetaSweepStore.
func (s *PostgresStore) FinishMetaSweepLease(ctx context.Context, owner string, completed bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE meta_sweep_lease SET expires_at = now(),
		last_completed_at = CASE WHEN $3 THEN now() ELSE last_completed_at END
		WHERE name = $1 AND owner = $2`, MetaSweepName, owner, completed)
	if err != nil {
		return fmt.Errorf("release meta sweep lease: %w", err)
	}
	return nil
}

// metaReferenceTables are the tables whose rows can reference an object of the meta namespace by body_key, and
// whether the column is nullable (a row that kept its payload inline has no key). Each has the partial
// <table>_body_key_idx of migration 000012; add a table to both when a new payload kind is externalised.
var metaReferenceTables = []struct {
	table    string
	nullable bool
}{
	{"native_npm_versions", true},
	{"native_cargo_proxy_indexes", true},
	{"native_helm_publications", true},
	{"native_helm_source_documents", true},
	{"native_nuget_publications", true},
	{"native_nuget_source_documents", true},
	{"oci_discovery_snapshots", true},
	{"artifact_intelligence", true},
	{"artifact_sbom", false},
	{"repository_vex_document", false},
}

// metaObjectReferencedExpression is the boolean expression that says whether any row of any of those tables
// references the object key $1. Each branch is served by its partial index, so the check reads a handful of index
// pages however large the tables are.
var metaObjectReferencedExpression = func() string {
	branches := make([]string, len(metaReferenceTables))
	for i, t := range metaReferenceTables {
		branches[i] = "EXISTS (SELECT 1 FROM " + t.table + " WHERE body_key=$1"
		if !t.nullable {
			branches[i] += " AND body_key<>''"
		}
		branches[i] += ")"
	}
	return "(" + strings.Join(branches, " OR ") + ")"
}()

// SweepMetaObject implements MetaSweepStore. An object goes through three states: referenced (the common case,
// answered by one read and left alone), unreferenced and not yet marked (the sweep records when it first saw it
// so), and unreferenced since a mark older than the grace period (deleted). A write of the object, which pins
// it, resets all of that.
func (s *PostgresStore) SweepMetaObject(ctx context.Context, digest string, grace time.Duration, remove func(context.Context) error) (MetaSweepOutcome, error) {
	key, err := metaDigestKey(digest)
	if err != nil {
		return "", err
	}
	// The first look takes no lock and writes nothing unless the object is referenced and carries an old pin, which
	// it forgets: nearly every object of a namespace is referenced.
	var referenced bool
	if err = s.db.QueryRowContext(ctx, `WITH ref AS (SELECT `+metaObjectReferencedExpression+` AS referenced),
		forgotten AS (DELETE FROM meta_object_pins WHERE digest = $2 AND seen_at <= now() - make_interval(secs => $3) AND (SELECT referenced FROM ref) RETURNING 1)
		SELECT referenced FROM ref`, key, digest, grace.Seconds()).Scan(&referenced); err != nil {
		return "", fmt.Errorf("check meta object references: %w", err)
	}
	if referenced {
		return MetaSweepReferenced, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	// An object nobody pinned is pinned now. The insert and the lock that follows make the pin row ours until the
	// transaction ends; a writer that pins the same digest meanwhile waits for it.
	if _, err = tx.ExecContext(ctx, `INSERT INTO meta_object_pins (digest) VALUES ($1) ON CONFLICT (digest) DO NOTHING`, digest); err != nil {
		return "", fmt.Errorf("pin meta object: %w", err)
	}
	var young bool
	var withinGrace sql.NullBool
	if err = tx.QueryRowContext(ctx, `SELECT seen_at > now() - make_interval(secs => $2), unreferenced_since > now() - make_interval(secs => $2)
		FROM meta_object_pins WHERE digest = $1 FOR UPDATE`, digest, grace.Seconds()).Scan(&young, &withinGrace); err != nil {
		return "", fmt.Errorf("lock meta object pin: %w", err)
	}
	if young {
		return MetaSweepYoung, tx.Commit()
	}
	// Decided again under the lock: a row may have been committed since the first look.
	if err = tx.QueryRowContext(ctx, `SELECT `+metaObjectReferencedExpression, key).Scan(&referenced); err != nil {
		return "", fmt.Errorf("check meta object references: %w", err)
	}
	if referenced {
		if _, err = tx.ExecContext(ctx, `DELETE FROM meta_object_pins WHERE digest = $1`, digest); err != nil {
			return "", fmt.Errorf("forget meta object pin: %w", err)
		}
		return MetaSweepReferenced, tx.Commit()
	}
	if !withinGrace.Valid {
		if _, err = tx.ExecContext(ctx, `UPDATE meta_object_pins SET unreferenced_since = now() WHERE digest = $1`, digest); err != nil {
			return "", fmt.Errorf("mark meta object: %w", err)
		}
		return MetaSweepYoung, tx.Commit()
	}
	if withinGrace.Bool {
		return MetaSweepYoung, tx.Commit()
	}
	if err = remove(ctx); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM meta_object_pins WHERE digest = $1`, digest); err != nil {
		return "", fmt.Errorf("forget meta object pin: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return MetaSweepDeleted, nil
}

// MetaObjectPinsOlderThan implements MetaSweepStore.
func (s *PostgresStore) MetaObjectPinsOlderThan(ctx context.Context, after string, grace time.Duration, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT digest FROM meta_object_pins
		WHERE digest > $1 AND seen_at <= now() - make_interval(secs => $2) ORDER BY digest LIMIT $3`, after, grace.Seconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("list meta object pins: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var digests []string
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			return nil, err
		}
		digests = append(digests, digest)
	}
	return digests, rows.Err()
}

// ForgetMetaObjectPin implements MetaSweepStore.
func (s *PostgresStore) ForgetMetaObjectPin(ctx context.Context, digest string, grace time.Duration) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM meta_object_pins WHERE digest = $1 AND seen_at <= now() - make_interval(secs => $2)`, digest, grace.Seconds()); err != nil {
		return fmt.Errorf("forget meta object pin: %w", err)
	}
	return nil
}

var _ MetaSweepStore = (*PostgresStore)(nil)
