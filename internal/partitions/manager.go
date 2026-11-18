package partitions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// leaseTaskID and leaseTaskName identify the lease row in scheduled_tasks.
	leaseTaskID   = "00000000-0000-7000-8000-0000000000a1"
	leaseTaskName = "internal:partition-manager"
	// DefaultInterval is how often the manager runs after start-up.
	DefaultInterval = 6 * time.Hour

	sqlLockNotAvailable = "55P03"
	sqlCheckViolation   = "23514"
)

// Options tunes a Manager; zero values select the ADR 0023 defaults.
type Options struct {
	// Ahead is the number of future periods created beyond the current one.
	Ahead int
	// Owner identifies this node in the lease row.
	Owner string
	// LeaseTTL bounds how long a crashed runner blocks the others.
	LeaseTTL time.Duration
	// LockTimeout is the lock_timeout applied to DROP TABLE and ATTACH.
	LockTimeout time.Duration
	// Attempts and Backoff retry a DROP or ATTACH that hit the lock timeout.
	Attempts int
	Backoff  time.Duration
	// Now and Logger are injectable for tests.
	Now    func() time.Time
	Logger *slog.Logger
}

// Report is the outcome of one Maintain call.
type Report struct {
	// Leader is false when another node holds the lease and nothing was done.
	Leader  bool
	Created []string
	Dropped []string
	// Moved counts default-partition rows moved into new partitions, by partition.
	Moved map[string]int64
}

// TableStat is the observable state of one managed table.
type TableStat struct {
	Table       string
	DefaultRows int64
	Partitions  int
}

// Manager creates, drops and repairs partitions of the tables in its registry.
type Manager struct {
	db    *sql.DB
	specs []Spec
	opts  Options

	mu    sync.Mutex
	stats map[string]TableStat
}

// New validates the registry and fills option defaults.
func New(db *sql.DB, specs []Spec, opts Options) (*Manager, error) {
	if db == nil {
		return nil, errors.New("partitions: database is required")
	}
	if len(specs) == 0 {
		return nil, errors.New("partitions: registry is empty")
	}
	for _, spec := range specs {
		if err := spec.Validate(); err != nil {
			return nil, err
		}
	}
	if opts.Ahead < 0 {
		return nil, errors.New("partitions: ahead must not be negative")
	}
	if opts.Ahead == 0 {
		opts.Ahead = DefaultAhead
	}
	if opts.Owner == "" {
		host, _ := os.Hostname()
		opts.Owner = fmt.Sprintf("%s/%d", host, os.Getpid())
	}
	if opts.LeaseTTL <= 0 {
		opts.LeaseTTL = 2 * time.Minute
	}
	if opts.LockTimeout <= 0 {
		opts.LockTimeout = 2 * time.Second
	}
	if opts.Attempts <= 0 {
		opts.Attempts = 4
	}
	if opts.Backoff <= 0 {
		opts.Backoff = 250 * time.Millisecond
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Manager{db: db, specs: append([]Spec(nil), specs...), opts: opts, stats: map[string]TableStat{}}, nil
}

// Stats returns the table state observed by the last Maintain that held the lease.
func (m *Manager) Stats() []TableStat {
	m.mu.Lock()
	defer m.mu.Unlock()
	stats := make([]TableStat, 0, len(m.specs))
	for _, spec := range m.specs {
		stat, ok := m.stats[spec.Table]
		if !ok {
			stat = TableStat{Table: spec.Table}
		}
		stats = append(stats, stat)
	}
	return stats
}

// Run calls Maintain every interval until ctx ends. A failed run is logged and
// retried on the next tick; the DEFAULT partitions keep inserts working meanwhile.
func (m *Manager) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := m.Maintain(ctx); err != nil && ctx.Err() == nil {
				m.opts.Logger.Error("partition maintenance failed", "error", err)
			}
		}
	}
}

// Maintain runs one maintenance pass if this node wins the lease. It continues
// past a failing table so one bad table cannot starve the others, and returns
// the joined errors.
func (m *Manager) Maintain(ctx context.Context) (Report, error) {
	report := Report{Moved: map[string]int64{}}
	leader, err := m.acquire(ctx)
	if err != nil {
		return report, fmt.Errorf("partitions: acquire lease: %w", err)
	}
	if !leader {
		return report, nil
	}
	report.Leader = true
	defer func() {
		releaseContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = m.db.ExecContext(releaseContext, `UPDATE scheduled_tasks SET lease_owner='',lease_until=NULL WHERE id=$1::uuid AND lease_owner=$2`, leaseTaskID, m.opts.Owner)
	}()
	var errs []error
	for _, spec := range m.specs {
		if err := m.ensure(ctx, spec, &report); err != nil {
			errs = append(errs, fmt.Errorf("%s: ensure partitions: %w", spec.Table, err))
		}
		if err := m.drop(ctx, spec, &report); err != nil {
			errs = append(errs, fmt.Errorf("%s: drop expired partitions: %w", spec.Table, err))
		}
		if err := m.observe(ctx, spec); err != nil {
			errs = append(errs, fmt.Errorf("%s: observe: %w", spec.Table, err))
		}
	}
	return report, errors.Join(errs...)
}

// acquire takes or renews the lease row in one statement. The row is created on
// first use and is never enabled, so the task scheduler never claims it.
func (m *Manager) acquire(ctx context.Context) (bool, error) {
	var owner string
	err := m.db.QueryRowContext(ctx, `INSERT INTO scheduled_tasks (id,name,description,kind,interval_seconds,enabled,next_run_at,lease_owner,lease_until)
VALUES ($1::uuid,$2,'Internal lease row for the partition manager','partition-manager',21600,false,now(),$3,now()+$4::bigint*interval '1 millisecond')
ON CONFLICT (id) DO UPDATE SET lease_owner=EXCLUDED.lease_owner,lease_until=EXCLUDED.lease_until
WHERE scheduled_tasks.lease_until IS NULL OR scheduled_tasks.lease_until<=now() OR scheduled_tasks.lease_owner=EXCLUDED.lease_owner
RETURNING lease_owner`, leaseTaskID, leaseTaskName, m.opts.Owner, m.opts.LeaseTTL.Milliseconds()).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && owner == m.opts.Owner, err
}

func quote(identifier string) string { return `"` + identifier + `"` }

// ensure creates the current and the next Ahead partitions. A period whose range
// already holds rows in the DEFAULT partition cannot be created directly, so it
// is built standalone, filled from the default partition and attached in one
// transaction.
func (m *Manager) ensure(ctx context.Context, spec Spec, report *Report) error {
	existing, err := m.children(ctx, spec)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, child := range existing {
		have[child.name] = true
	}
	var errs []error
	for _, start := range spec.Periods(m.opts.Now(), m.opts.Ahead) {
		name := spec.Name(start)
		if have[name] {
			continue
		}
		end := spec.Granularity.Next(start)
		pending, err := m.defaultHasRows(ctx, spec, start, end)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !pending {
			err = m.inLockedTx(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)`, quote(name), quote(spec.Table), boundLiteral(start), boundLiteral(end)))
			if err != nil {
				errs = append(errs, fmt.Errorf("create %s: %w", name, err))
				continue
			}
			report.Created = append(report.Created, name)
			continue
		}
		moved, err := m.attachWithRows(ctx, spec, name, start, end)
		if err != nil {
			errs = append(errs, fmt.Errorf("move default rows into %s: %w", name, err))
			continue
		}
		report.Created = append(report.Created, name)
		report.Moved[name] = moved
	}
	return errors.Join(errs...)
}

func (m *Manager) defaultHasRows(ctx context.Context, spec Spec, start, end time.Time) (bool, error) {
	var found bool
	err := m.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE %s >= %s AND %s < %s)`,
		quote(spec.DefaultName()), quote(spec.Column), boundLiteral(start), quote(spec.Column), boundLiteral(end))).Scan(&found)
	return found, err
}

func (m *Manager) attachWithRows(ctx context.Context, spec Spec, name string, start, end time.Time) (int64, error) {
	var moved int64
	err := m.retry(ctx, func() error {
		tx, err := m.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err = m.setLockTimeout(ctx, tx); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE %s (LIKE %s INCLUDING ALL)`, quote(name), quote(spec.Table))); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, fmt.Sprintf(`WITH moved AS (DELETE FROM %s WHERE %s >= %s AND %s < %s RETURNING *) INSERT INTO %s SELECT * FROM moved`,
			quote(spec.DefaultName()), quote(spec.Column), boundLiteral(start), quote(spec.Column), boundLiteral(end), quote(name)))
		if err != nil {
			return err
		}
		if moved, err = result.RowsAffected(); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ATTACH PARTITION %s FOR VALUES FROM (%s) TO (%s)`, quote(spec.Table), quote(name), boundLiteral(start), boundLiteral(end))); err != nil {
			return err
		}
		return tx.Commit()
	}, sqlLockNotAvailable, sqlCheckViolation)
	return moved, err
}

type child struct {
	name     string
	from, to time.Time
}

// children lists the bounded partitions of the table (the DEFAULT one is skipped).
func (m *Manager) children(ctx context.Context, spec Spec) ([]child, error) {
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// pg_get_expr renders timestamptz bounds in the session time zone.
	if _, err = tx.ExecContext(ctx, `SET LOCAL TimeZone='UTC'`); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.relname,pg_get_expr(c.relpartbound,c.oid)
FROM pg_inherits i JOIN pg_class c ON c.oid=i.inhrelid JOIN pg_class p ON p.oid=i.inhparent
WHERE p.oid=$1::regclass ORDER BY c.relname`, quote(spec.Table))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var children []child
	for rows.Next() {
		var name, bound string
		if err = rows.Scan(&name, &bound); err != nil {
			return nil, err
		}
		if from, to, ok := parseBound(bound); ok {
			children = append(children, child{name: name, from: from, to: to})
		}
	}
	return children, rows.Err()
}

// drop removes partitions whose whole range is older than the retention.
func (m *Manager) drop(ctx context.Context, spec Spec, report *Report) error {
	if spec.Retention <= 0 {
		return nil
	}
	children, err := m.children(ctx, spec)
	if err != nil {
		return err
	}
	cutoff := m.opts.Now().UTC().Add(-spec.Retention)
	sort.Slice(children, func(i, j int) bool { return children[i].from.Before(children[j].from) })
	var errs []error
	for _, c := range children {
		if c.to.After(cutoff) || !strings.HasPrefix(c.name, spec.Table+"_p") {
			continue
		}
		// The drop briefly takes ACCESS EXCLUSIVE on the parent; lock_timeout keeps a
		// lock queue from stalling readers. DETACH CONCURRENTLY is not used: it cannot
		// run in a transaction block and is refused while a DEFAULT partition exists.
		if err := m.inLockedTx(ctx, `DROP TABLE `+quote(c.name)); err != nil {
			errs = append(errs, fmt.Errorf("drop %s: %w", c.name, err))
			continue
		}
		report.Dropped = append(report.Dropped, c.name)
	}
	return errors.Join(errs...)
}

// inLockedTx runs one statement under lock_timeout, retrying a lock timeout.
func (m *Manager) inLockedTx(ctx context.Context, statement string) error {
	return m.retry(ctx, func() error {
		tx, err := m.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err = m.setLockTimeout(ctx, tx); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
		return tx.Commit()
	}, sqlLockNotAvailable)
}

func (m *Manager) setLockTimeout(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`SET LOCAL lock_timeout='%dms'`, m.opts.LockTimeout.Milliseconds()))
	return err
}

// retry runs fn up to Attempts times with exponential backoff while it fails with
// one of the retryable SQLSTATE codes.
func (m *Manager) retry(ctx context.Context, fn func() error, codes ...string) error {
	backoff := m.opts.Backoff
	var err error
	for attempt := 1; attempt <= m.opts.Attempts; attempt++ {
		if err = fn(); err == nil || !hasCode(err, codes) {
			return err
		}
		if attempt == m.opts.Attempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return err
}

func hasCode(err error, codes []string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	for _, code := range codes {
		if pgErr.Code == code {
			return true
		}
	}
	return false
}

// observe records the partition count and the DEFAULT partition row count; the
// latter must stay zero.
func (m *Manager) observe(ctx context.Context, spec Spec) error {
	children, err := m.children(ctx, spec)
	if err != nil {
		return err
	}
	var rows int64
	if err = m.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, quote(spec.DefaultName()))).Scan(&rows); err != nil {
		return err
	}
	m.mu.Lock()
	m.stats[spec.Table] = TableStat{Table: spec.Table, DefaultRows: rows, Partitions: len(children)}
	m.mu.Unlock()
	return nil
}
