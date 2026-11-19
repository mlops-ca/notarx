// Package partitions maintains time-range partitions of append-only PostgreSQL
// tables (ADR 0023 section 2).
//
// A registry lists each partitioned table, its partition column, the granularity
// and its retention. The Manager creates the current and the next few periods,
// drops partitions that are entirely older than the retention, and empties the
// DEFAULT partition into properly bounded partitions. One node at a time runs
// the work: the runner is elected through a lease row in scheduled_tasks, not a
// session advisory lock, so it works through connection poolers.
package partitions

import (
	"fmt"
	"regexp"
	"time"
)

// Granularity is the width of one partition.
type Granularity string

const (
	// Month partitions are named <table>_pYYYYMM.
	Month Granularity = "month"
	// Day partitions are named <table>_pYYYYMMDD.
	Day Granularity = "day"
)

// DefaultAhead is the number of future periods created beyond the current one.
const DefaultAhead = 3

// Spec describes one partitioned table.
type Spec struct {
	// Table and Column are unquoted identifiers in the connection's schema.
	Table, Column string
	Granularity   Granularity
	// Retention is how long a partition is kept after its period ends. Zero keeps
	// partitions forever.
	Retention time.Duration
}

// DefaultRegistry lists the tables partitioned by the migrations.
func DefaultRegistry() []Spec {
	return []Spec{
		{Table: "resolver_audit_log", Column: "occurred_at", Granularity: Month, Retention: 90 * 24 * time.Hour},
		{Table: "security_audit_log", Column: "occurred_at", Granularity: Month, Retention: 730 * 24 * time.Hour},
		{Table: "artifact_scan_history", Column: "scanned_at", Granularity: Month, Retention: 180 * 24 * time.Hour},
	}
}

var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,47}$`)

// Validate rejects specs that could not be used safely in generated DDL.
func (s Spec) Validate() error {
	if !identifier.MatchString(s.Table) || !identifier.MatchString(s.Column) {
		return fmt.Errorf("partitions: table %q and column %q must be lower-case identifiers of at most 48 characters", s.Table, s.Column)
	}
	if s.Granularity != Month && s.Granularity != Day {
		return fmt.Errorf("partitions: %s has unsupported granularity %q", s.Table, s.Granularity)
	}
	if s.Retention < 0 {
		return fmt.Errorf("partitions: %s has a negative retention", s.Table)
	}
	return nil
}

// Start returns the UTC start of the period containing t.
func (g Granularity) Start(t time.Time) time.Time {
	t = t.UTC()
	if g == Day {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// Next returns the start of the period after the one starting at start.
func (g Granularity) Next(start time.Time) time.Time {
	if g == Day {
		return start.AddDate(0, 0, 1)
	}
	return start.AddDate(0, 1, 0)
}

// Name returns the partition name for the period starting at start.
func (s Spec) Name(start time.Time) string {
	if s.Granularity == Day {
		return s.Table + "_p" + start.UTC().Format("20060102")
	}
	return s.Table + "_p" + start.UTC().Format("200601")
}

// DefaultName is the DEFAULT partition of the table.
func (s Spec) DefaultName() string { return s.Table + "_default" }

// Periods returns the starts of the current period and the next ahead periods.
func (s Spec) Periods(now time.Time, ahead int) []time.Time {
	start := s.Granularity.Start(now)
	periods := make([]time.Time, 0, ahead+1)
	for i := 0; i <= ahead; i++ {
		periods = append(periods, start)
		start = s.Granularity.Next(start)
	}
	return periods
}

// boundLiteral renders a partition bound as an unambiguous UTC literal.
func boundLiteral(t time.Time) string {
	return "'" + t.UTC().Format("2006-01-02 15:04:05") + "+00'"
}

var partitionBound = regexp.MustCompile(`^FOR VALUES FROM \('([^']+)'\) TO \('([^']+)'\)$`)

// parseBound extracts the range of a pg_get_expr partition bound produced with
// TimeZone set to UTC. ok is false for the DEFAULT partition and anything else.
func parseBound(expression string) (from, to time.Time, ok bool) {
	m := partitionBound.FindStringSubmatch(expression)
	if m == nil {
		return time.Time{}, time.Time{}, false
	}
	var err error
	if from, err = parseTimestamp(m[1]); err != nil {
		return time.Time{}, time.Time{}, false
	}
	if to, err = parseTimestamp(m[2]); err != nil {
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

func parseTimestamp(value string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05-07", "2006-01-02 15:04:05.999999-07", "2006-01-02 15:04:05-07:00"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised partition bound %q", value)
}
