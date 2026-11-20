package retention

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

	"github.com/mlops-ca/notarx/internal/tasklease"
)

// leaseTask is the lease row of the janitor in scheduled_tasks.
var leaseTask = tasklease.Task{ID: "00000000-0000-7000-8000-0000000000a2", Name: "internal:retention-janitor", Kind: "retention-janitor", Description: "Internal lease row for the retention janitor"}

// Dialect selects the SQL the janitor generates.
type Dialect string

const (
	// Postgres deletes by ctid with FOR UPDATE SKIP LOCKED.
	Postgres Dialect = "postgres"
	// MySQL deletes with ORDER BY ... LIMIT in key range order.
	MySQL Dialect = "mysql"
)

const (
	// DefaultInterval is how often a node tries to run a pass.
	DefaultInterval = 15 * time.Minute
	minSleep        = 100 * time.Millisecond
)

// Options tunes a Janitor; zero values select the ADR 0023 defaults.
type Options struct {
	Dialect Dialect
	// Registry replaces DefaultRegistry, for tests.
	Registry []Entry
	// Owner identifies this node in the lease row.
	Owner string
	// LeaseTTL bounds how long a crashed runner blocks the others; the lease is
	// renewed before every batch.
	LeaseTTL time.Duration
	// Now and Sleep are injectable for tests; Sleep must return early on ctx.
	Now    func() time.Time
	Sleep  func(ctx context.Context, d time.Duration)
	Logger *slog.Logger
}

// TableStat is the observable state of one table.
type TableStat struct {
	Table string
	// Deleted is the number of rows deleted by this process since it started.
	Deleted int64
	// LagSeconds is how far past its keep time the oldest eligible row is.
	LagSeconds float64
}

// Report is the outcome of one pass.
type Report struct {
	// Leader is false when another node holds the lease and nothing was done.
	Leader  bool
	Deleted map[string]int64
	// Skipped lists labels not run, with the reason.
	Skipped map[string]string
}

// Janitor prunes the tables of its registry.
type Janitor struct {
	db      *sql.DB
	cfg     Config
	entries []Entry
	opts    Options

	mu    sync.Mutex
	stats map[string]*TableStat
	// leasedAt is when the lease was last taken or renewed by this node.
	leasedAt time.Time
}

// New validates the registry and fills option defaults.
func New(db *sql.DB, cfg Config, opts Options) (*Janitor, error) {
	if db == nil {
		return nil, errors.New("retention: database is required")
	}
	if opts.Dialect == "" {
		opts.Dialect = Postgres
	}
	if opts.Dialect != Postgres && opts.Dialect != MySQL {
		return nil, fmt.Errorf("retention: unsupported dialect %q", opts.Dialect)
	}
	entries := opts.Registry
	if entries == nil {
		entries = DefaultRegistry()
	}
	for _, entry := range entries {
		if err := entry.Validate(); err != nil {
			return nil, err
		}
	}
	if cfg.RunBudget <= 0 {
		cfg.RunBudget = DefaultRunBudget
	}
	if cfg.Profile == "" {
		cfg.Profile = Standard
	}
	if opts.Owner == "" {
		host, _ := os.Hostname()
		opts.Owner = fmt.Sprintf("%s/%d", host, os.Getpid())
	}
	if opts.LeaseTTL <= 0 {
		opts.LeaseTTL = 5 * time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepContext
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	stats := map[string]*TableStat{}
	for _, entry := range entries {
		stats[entry.Table] = &TableStat{Table: entry.Table}
	}
	return &Janitor{db: db, cfg: cfg, entries: append([]Entry(nil), entries...), opts: opts, stats: stats}, nil
}

func sleepContext(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// Stats returns the per-table counters, sorted by table.
func (j *Janitor) Stats() []TableStat {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]TableStat, 0, len(j.stats))
	for _, stat := range j.stats {
		out = append(out, *stat)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Table < out[b].Table })
	return out
}

// Run calls RunOnce every interval until ctx ends.
func (j *Janitor) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if report, err := j.RunOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			j.opts.Logger.Error("retention pass failed", "error", err)
		} else if report.Leader {
			j.opts.Logger.Info("retention pass", "deleted", report.Deleted)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

var errLeaseLost = errors.New("retention: lease lost")

// RunOnce runs one pass over the registry if this node wins the lease. A failing
// table does not stop the others; the errors are joined.
func (j *Janitor) RunOnce(ctx context.Context) (Report, error) {
	report := Report{Deleted: map[string]int64{}, Skipped: map[string]string{}}
	held, err := j.acquire(ctx)
	if err != nil {
		return report, fmt.Errorf("retention: acquire lease: %w", err)
	}
	if !held {
		return report, nil
	}
	report.Leader = true
	j.leasedAt = j.opts.Now()
	defer func() {
		releaseContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = j.leaser().Release(releaseContext, leaseTask, j.opts.Owner, false)
	}()
	var errs []error
	for _, entry := range j.entries {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		keep, on := j.cfg.Keep(entry)
		switch {
		case !on:
			report.Skipped[entry.Name()] = "off"
			continue
		case entry.PartitionedOnPostgres && j.opts.Dialect == Postgres:
			report.Skipped[entry.Name()] = "partitioned"
			continue
		}
		if entry.Optional {
			exists, err := j.tableExists(ctx, entry.Table)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", entry.Name(), err))
				continue
			}
			if !exists {
				report.Skipped[entry.Name()] = "table missing"
				continue
			}
		}
		deleted, err := j.prune(ctx, entry, keep)
		report.Deleted[entry.Name()] = deleted
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", entry.Name(), err))
			if errors.Is(err, errLeaseLost) {
				break
			}
		}
		if lagErr := j.observeLag(ctx, entry, keep); lagErr != nil && ctx.Err() == nil {
			j.opts.Logger.Warn("retention lag query failed", "table", entry.Table, "error", lagErr)
		}
	}
	return report, errors.Join(errs...)
}

// prune deletes eligible rows of one entry in throttled batches until a short
// batch, the run budget (unless the entry drains) or an error.
func (j *Janitor) prune(ctx context.Context, entry Entry, keep time.Duration) (int64, error) {
	cutoff := j.opts.Now().UTC().Add(-keep)
	statement := deleteSQL(entry, j.opts.Dialect)
	batch := entry.BatchSize()
	started := j.opts.Now()
	var total int64
	for {
		if err := j.renew(ctx); err != nil {
			return total, err
		}
		began := j.opts.Now()
		// A batch that outlives half the lease is cancelled, so a statement never keeps
		// running after another node could have taken the lease over.
		batchContext, cancel := context.WithTimeout(ctx, j.opts.LeaseTTL/2)
		result, err := j.db.ExecContext(batchContext, statement, cutoff, batch)
		cancel()
		if err != nil {
			return total, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		elapsed := j.opts.Now().Sub(began)
		total += n
		j.mu.Lock()
		j.stats[entry.Table].Deleted += n
		j.mu.Unlock()
		if n < int64(batch) {
			return total, nil
		}
		if !entry.Drain && j.opts.Now().Sub(started) >= j.cfg.RunBudget {
			return total, nil
		}
		j.opts.Sleep(ctx, max(minSleep, 2*elapsed))
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
	}
}

func (j *Janitor) observeLag(ctx context.Context, entry Entry, keep time.Duration) error {
	var oldest sql.NullTime
	if err := j.db.QueryRowContext(ctx, oldestSQL(entry, j.opts.Dialect)).Scan(&oldest); err != nil {
		return err
	}
	lag := 0.0
	if oldest.Valid {
		lag = max(0, j.opts.Now().Sub(oldest.Time.Add(keep)).Seconds())
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	stat := j.stats[entry.Table]
	// Several entries of one table report the worst lag.
	if lag > stat.LagSeconds || j.firstEntry(entry) {
		stat.LagSeconds = lag
	}
	return nil
}

func (j *Janitor) firstEntry(entry Entry) bool {
	for _, candidate := range j.entries {
		if candidate.Table == entry.Table {
			return candidate.Name() == entry.Name()
		}
	}
	return true
}

// quote returns an identifier in the dialect's quoting.
func quote(d Dialect, name string) string {
	if d == MySQL {
		return "`" + name + "`"
	}
	return "\"" + name + "\""
}

// deleteSQL returns the batch statement with the cutoff and the batch size as its
// two parameters.
func deleteSQL(e Entry, d Dialect) string {
	table, column := quote(d, e.Table), quote(d, e.TimeColumn)
	if d == MySQL {
		keys := make([]string, len(e.KeyColumns))
		for i, key := range e.KeyColumns {
			keys[i] = quote(d, key)
		}
		return fmt.Sprintf("DELETE FROM %s WHERE (%s) AND %s < ? ORDER BY %s LIMIT ?", table, e.Terminal, column, strings.Join(keys, ", "))
	}
	return fmt.Sprintf("DELETE FROM %s WHERE ctid = ANY(ARRAY(SELECT ctid FROM %s WHERE (%s) AND %s < $1 ORDER BY %s LIMIT $2 FOR UPDATE SKIP LOCKED))", table, table, e.Terminal, column, column)
}

func oldestSQL(e Entry, d Dialect) string {
	return fmt.Sprintf("SELECT min(%s) FROM %s WHERE (%s)", quote(d, e.TimeColumn), quote(d, e.Table), e.Terminal)
}

func (j *Janitor) tableExists(ctx context.Context, table string) (bool, error) {
	var exists bool
	query := "SELECT to_regclass($1) IS NOT NULL"
	if j.opts.Dialect == MySQL {
		query = "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?)"
	}
	err := j.db.QueryRowContext(ctx, query, table).Scan(&exists)
	return exists, err
}

// leaser is the shared scheduled_tasks lease (internal/tasklease) in the janitor's dialect.
func (j *Janitor) leaser() tasklease.Leaser {
	return tasklease.Leaser{DB: j.db, Dialect: tasklease.Dialect(j.opts.Dialect)}
}

// acquire takes or renews the lease row; the row is created on first use and is never due as a scheduled task.
func (j *Janitor) acquire(ctx context.Context) (bool, error) {
	return j.leaser().Acquire(ctx, leaseTask, j.opts.Owner, j.opts.LeaseTTL, 0)
}

// renew extends the lease once a quarter of its time to live has passed, so small
// batches do not pay an upsert each; it fails if another node holds the lease now.
func (j *Janitor) renew(ctx context.Context) error {
	if j.opts.Now().Sub(j.leasedAt) < j.opts.LeaseTTL/4 {
		return nil
	}
	held, err := j.acquire(ctx)
	if err != nil {
		return err
	}
	if !held {
		return errLeaseLost
	}
	j.leasedAt = j.opts.Now()
	return nil
}
