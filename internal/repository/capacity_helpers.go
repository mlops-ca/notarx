package repository

import (
	"context"
	"database/sql"
	"sort"

	"github.com/mlops-ca/notarx/internal/capacity"
)

// scanPlus scans the destinations a row scanner expects followed by extra columns, so a
// store can add RETURNING (xmax = 0) AS inserted to a statement whose scan function is
// shared.
type scanPlus struct {
	row   interface{ Scan(...any) error }
	extra []any
}

func (s scanPlus) Scan(dest ...any) error { return s.row.Scan(append(dest, s.extra...)...) }

// usageDelta accumulates the bytes and objects one transaction adds to, or releases from,
// each repository's usage counter (ADR 0023 section 3). A transaction that touches
// several repositories reserves them in repository id order, so concurrent transactions
// lock the counter rows in the same order.
type usageDelta struct {
	byRepository map[string]*[2]int64
}

func (d *usageDelta) add(repositoryID string, bytes, objects int64) {
	if d.byRepository == nil {
		d.byRepository = map[string]*[2]int64{}
	}
	entry := d.byRepository[repositoryID]
	if entry == nil {
		entry = &[2]int64{}
		d.byRepository[repositoryID] = entry
	}
	entry[0] += bytes
	entry[1] += objects
}

// reserve applies every non-zero delta with capacity.Reserve; call it as the last
// statement before commit.
func (d *usageDelta) reserve(ctx context.Context, tx *sql.Tx) error {
	ids := make([]string, 0, len(d.byRepository))
	for id := range d.byRepository {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		entry := d.byRepository[id]
		if entry[0] == 0 && entry[1] == 0 {
			continue
		}
		if err := capacity.Reserve(ctx, tx, id, entry[0], entry[1]); err != nil {
			return err
		}
	}
	return nil
}

// lockUsagePath takes a transaction-scoped advisory lock on key, so writers of one asset path
// (including the first insert of an absent path) compute their usage delta from the row
// the previous writer committed.
func lockUsagePath(ctx context.Context, tx *sql.Tx, key string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,0))`, key)
	return err
}

// measuredUsage is bytes and objects a before and after measurement compares.
type measuredUsage struct{ bytes, objects int64 }

func (u measuredUsage) minus(other measuredUsage) measuredUsage {
	return measuredUsage{bytes: u.bytes - other.bytes, objects: u.objects - other.objects}
}

// usageBracket is the before and after measurement technique of ADR 0023 section 3 for
// formats whose usage is not a per-row sum (Maven and Conan count an asset only while a
// visible artifact or revision covers it, so a state change of one row moves the usage of
// many). The writer opens the bracket after taking its repository lock and before its first
// change, then reserves the difference as the last statement of the transaction. The
// measurement is scoped to the coordinate the writer touches, so its cost follows the
// coordinate, not the repository.
type usageBracket struct {
	repositoryID string
	measure      func(context.Context, *sql.Tx) (measuredUsage, error)
	before       measuredUsage
}

func openUsageBracket(ctx context.Context, tx *sql.Tx, repositoryID string, measure func(context.Context, *sql.Tx) (measuredUsage, error)) (*usageBracket, error) {
	before, err := measure(ctx, tx)
	if err != nil {
		return nil, err
	}
	return &usageBracket{repositoryID: repositoryID, measure: measure, before: before}, nil
}

// reserve measures again and applies the difference to the repository's counter.
func (b *usageBracket) reserve(ctx context.Context, tx *sql.Tx) error {
	after, err := b.measure(ctx, tx)
	if err != nil {
		return err
	}
	delta := after.minus(b.before)
	if delta == (measuredUsage{}) {
		return nil
	}
	return capacity.Reserve(ctx, tx, b.repositoryID, delta.bytes, delta.objects)
}
