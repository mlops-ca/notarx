// Package tasklease is the one implementation of the lease on an internal scheduled_tasks row that a single node at a
// time holds while it runs a background job: the partition manager, the retention janitor and the Raw idle seal
// (ADR 0023 section 2). It serves PostgreSQL and MySQL (ADR 0022), so a fix to the lease statements lands once.
package tasklease

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Dialect selects the SQL variant of the statements.
type Dialect string

const (
	// Postgres is PostgreSQL, the default store.
	Postgres Dialect = "postgres"
	// MySQL is MySQL 8.4 (ADR 0022).
	MySQL Dialect = "mysql"
)

// Task names the lease row of a background job. The row lives in scheduled_tasks, is never due and is hidden from the
// management API.
type Task struct {
	// ID is the fixed uuid of the row, Name its unique name, Kind one of the internal kinds of the table's check.
	ID, Name, Kind, Description string
	// IntervalSeconds is the interval_seconds the row is created with, informational as the row is never due; zero
	// selects 900.
	IntervalSeconds int
}

func (t Task) interval() int {
	if t.IntervalSeconds <= 0 {
		return 900
	}
	return t.IntervalSeconds
}

// Leaser takes and releases lease rows through a database handle.
type Leaser struct {
	DB      *sql.DB
	Dialect Dialect
}

// Acquire takes or renews the lease for owner. It reports false while another owner holds an unexpired lease, and
// while the last completed run is younger than minInterval, so a fleet runs the job once per interval however many
// nodes tick. The owner of the lease may always renew it, so a long run keeps its lease however young the last
// completion was.
//
// A node that does not win issues one UPDATE that matches no row and so locks none, then an INSERT that finds the row
// and does nothing; only a node that can take the lease updates it. A zero minInterval ignores last_run_at.
func (l Leaser) Acquire(ctx context.Context, task Task, owner string, ttl, minInterval time.Duration) (bool, error) {
	switch l.Dialect {
	case Postgres:
		return l.acquirePostgres(ctx, task, owner, ttl, minInterval)
	case MySQL:
		return l.acquireMySQL(ctx, task, owner, ttl, minInterval)
	}
	return false, fmt.Errorf("tasklease: unsupported dialect %q", l.Dialect)
}

// Release hands the lease back. completed records the run as finished now, which starts the minimum interval; an
// interrupted run releases uncompleted, so another node may retry at once.
func (l Leaser) Release(ctx context.Context, task Task, owner string, completed bool) error {
	var err error
	switch l.Dialect {
	case Postgres:
		_, err = l.DB.ExecContext(ctx, `UPDATE scheduled_tasks SET lease_owner='',lease_until=NULL,last_run_at=CASE WHEN $3 THEN now() ELSE last_run_at END WHERE id=$1::uuid AND lease_owner=$2`, task.ID, owner, completed)
	case MySQL:
		_, err = l.DB.ExecContext(ctx, `UPDATE scheduled_tasks SET lease_owner='',lease_until=NULL,last_run_at=IF(?,NOW(6),last_run_at) WHERE id=? AND CAST(lease_owner AS BINARY)=CAST(? AS BINARY)`, completed, task.ID, owner)
	default:
		err = fmt.Errorf("tasklease: unsupported dialect %q", l.Dialect)
	}
	return err
}

func (l Leaser) acquirePostgres(ctx context.Context, task Task, owner string, ttl, minInterval time.Duration) (bool, error) {
	var held string
	err := l.DB.QueryRowContext(ctx, `UPDATE scheduled_tasks SET lease_owner=$2,lease_until=now()+$3::bigint*interval '1 millisecond'
WHERE id=$1::uuid AND (lease_owner=$2
   OR ((lease_until IS NULL OR lease_until<=now()) AND ($4::bigint<=0 OR last_run_at IS NULL OR last_run_at<=now()-$4::bigint*interval '1 millisecond')))
RETURNING lease_owner`, task.ID, owner, ttl.Milliseconds(), minInterval.Milliseconds()).Scan(&held)
	if err == nil {
		return held == owner, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	// Either another node holds the lease or the row does not exist yet. Creating it makes the creator the holder; a
	// concurrent creator loses on the primary key or on the unique name, whichever of the two indexes it meets first
	// (so the conflict target is not named), and is not the holder.
	result, err := l.DB.ExecContext(ctx, `INSERT INTO scheduled_tasks (id,name,description,kind,interval_seconds,enabled,next_run_at,lease_owner,lease_until)
VALUES ($1::uuid,$2,$3,$4,$5,false,now(),$6,now()+$7::bigint*interval '1 millisecond')
ON CONFLICT DO NOTHING`, task.ID, task.Name, task.Description, task.Kind, task.interval(), owner, ttl.Milliseconds())
	if err != nil {
		return false, err
	}
	inserted, _ := result.RowsAffected()
	return inserted == 1, nil
}

func (l Leaser) acquireMySQL(ctx context.Context, task Task, owner string, ttl, minInterval time.Duration) (bool, error) {
	result, err := l.DB.ExecContext(ctx, `UPDATE scheduled_tasks SET lease_owner=?,lease_until=DATE_ADD(NOW(6),INTERVAL ? MICROSECOND)
WHERE id=? AND (CAST(lease_owner AS BINARY)=CAST(? AS BINARY)
   OR ((lease_until IS NULL OR lease_until<=NOW(6)) AND (?<=0 OR last_run_at IS NULL OR last_run_at<=DATE_SUB(NOW(6),INTERVAL ? MICROSECOND))))`,
		owner, ttl.Microseconds(), task.ID, owner, minInterval.Microseconds(), minInterval.Microseconds())
	if err != nil {
		return false, err
	}
	if updated, _ := result.RowsAffected(); updated > 0 {
		return true, nil
	}
	result, err = l.DB.ExecContext(ctx, `INSERT INTO scheduled_tasks (id,name,description,kind,interval_seconds,enabled,next_run_at,lease_owner,lease_until)
VALUES (?,?,?,?,?,false,NOW(6),?,DATE_ADD(NOW(6),INTERVAL ? MICROSECOND))
ON DUPLICATE KEY UPDATE id=id`, task.ID, task.Name, task.Description, task.Kind, task.interval(), owner, ttl.Microseconds())
	if err != nil {
		return false, err
	}
	if inserted, _ := result.RowsAffected(); inserted > 0 {
		return true, nil
	}
	// MySQL counts changed rows, so a renewal that rewrites identical values, like a row that already exists, reports
	// none; read the holder back. A creation error (a check or unique name violation) has surfaced above as itself.
	var held string
	if err := l.DB.QueryRowContext(ctx, "SELECT lease_owner FROM scheduled_tasks WHERE id=?", task.ID).Scan(&held); err != nil {
		return false, err
	}
	return held == owner, nil
}
