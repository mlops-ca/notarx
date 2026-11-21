package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/mlops-ca/notarx/internal/ids"
)

const remoteInstanceColumns = `id::text,name,base_url,instance_id::text,auth_kind,audience,token_sealed,trust_evidence,state,last_checked_at,version::text,created_at,updated_at`

func scanRemoteInstance(scanner interface{ Scan(...any) error }) (RemoteInstance, error) {
	var value RemoteInstance
	var token sql.NullString
	var checked sql.NullTime
	if err := scanner.Scan(&value.ID, &value.Name, &value.BaseURL, &value.InstanceID, &value.AuthKind, &value.Audience, &token,
		&value.TrustEvidence, &value.State, &checked, &value.Version, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return RemoteInstance{}, err
	}
	value.TokenSealed = token.String
	if checked.Valid {
		value.LastCheckedAt = checked.Time
	}
	return value, nil
}

// nullableSealedToken stores no token as NULL: the table's check ties the column to the authentication kind.
func nullableSealedToken(sealed string) any {
	if sealed == "" {
		return nil
	}
	return sealed
}

func nullableCheckTime(at time.Time) any {
	if at.IsZero() {
		return nil
	}
	return at
}

func (s *PostgresStore) CreateRemoteInstance(ctx context.Context, value RemoteInstance, audit ...AuditRecord) (RemoteInstance, error) {
	return runAudited(ctx, s, audit, func(q sqlRunner) (RemoteInstance, error) {
		created, err := scanRemoteInstance(q.QueryRowContext(ctx, `INSERT INTO remote_instances (id,name,base_url,instance_id,auth_kind,audience,token_sealed,trust_evidence,state,last_checked_at)
VALUES ($1::uuid,$2,$3,$4::uuid,$5,$6,$7,$8,$9,$10) RETURNING `+remoteInstanceColumns,
			value.ID, value.Name, value.BaseURL, value.InstanceID, value.AuthKind, value.Audience, nullableSealedToken(value.TokenSealed),
			value.TrustEvidence, value.State, nullableCheckTime(value.LastCheckedAt)))
		if isUnique(err) {
			return RemoteInstance{}, ErrRemoteInstanceNameExists
		}
		return created, err
	})
}

// PageRemoteInstances pages instances by name in byte order with a keyset on the unique name, served by
// remote_instances_name_bytes_idx whatever the database collation.
func (s *PostgresStore) PageRemoteInstances(ctx context.Context, query RemoteInstanceQuery) (RemoteInstancePage, error) {
	limit := pageLimit(query.Limit, 500, 500)
	after, present, err := decodeNameToken(query.PageToken, remoteInstancesFilter)
	if err != nil {
		return RemoteInstancePage{}, err
	}
	statement, args := `SELECT `+remoteInstanceColumns+` FROM remote_instances ORDER BY name COLLATE "C" LIMIT $1`, []any{limit + 1}
	if present {
		statement = `SELECT ` + remoteInstanceColumns + ` FROM remote_instances WHERE name COLLATE "C">$1 ORDER BY name COLLATE "C" LIMIT $2`
		args = []any{after, limit + 1}
	}
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return RemoteInstancePage{}, err
	}
	defer func() { _ = rows.Close() }()
	values := make([]RemoteInstance, 0, limit+1)
	for rows.Next() {
		value, scanErr := scanRemoteInstance(rows)
		if scanErr != nil {
			return RemoteInstancePage{}, scanErr
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return RemoteInstancePage{}, err
	}
	page := RemoteInstancePage{Items: values}
	if len(values) > limit {
		page.Items = values[:limit]
		page.NextPageToken = EncodeKeysetToken(remoteInstancesFilter, page.Items[limit-1].Name)
	}
	return page, nil
}

func (s *PostgresStore) GetRemoteInstance(ctx context.Context, id string) (RemoteInstance, error) {
	value, err := scanRemoteInstance(s.db.QueryRowContext(ctx, `SELECT `+remoteInstanceColumns+` FROM remote_instances WHERE id=$1::uuid`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteInstance{}, ErrNotFound
	}
	return value, err
}

func (s *PostgresStore) UpdateRemoteInstance(ctx context.Context, value RemoteInstance, expectedVersion string, audit ...AuditRecord) (RemoteInstance, error) {
	return runAudited(ctx, s, audit, func(q sqlRunner) (RemoteInstance, error) {
		updated, err := scanRemoteInstance(q.QueryRowContext(ctx, `UPDATE remote_instances
SET name=$2,base_url=$3,instance_id=$4::uuid,auth_kind=$5,audience=$6,token_sealed=$7,trust_evidence=$8,state=$9,last_checked_at=$10,version=version+1,updated_at=now()
WHERE id=$1::uuid AND version=$11::text::bigint RETURNING `+remoteInstanceColumns,
			value.ID, value.Name, value.BaseURL, value.InstanceID, value.AuthKind, value.Audience, nullableSealedToken(value.TokenSealed),
			value.TrustEvidence, value.State, nullableCheckTime(value.LastCheckedAt), expectedVersion))
		if isUnique(err) {
			return RemoteInstance{}, ErrRemoteInstanceNameExists
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return updated, err
		}
		var exists bool
		if queryErr := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM remote_instances WHERE id=$1::uuid)`, value.ID).Scan(&exists); queryErr != nil {
			return RemoteInstance{}, queryErr
		}
		if exists {
			return RemoteInstance{}, ErrVersionConflict
		}
		return RemoteInstance{}, ErrNotFound
	})
}

func (s *PostgresStore) DeleteRemoteInstance(ctx context.Context, id string, audit ...AuditRecord) error {
	_, err := runAudited(ctx, s, audit, func(q sqlRunner) (struct{}, error) {
		result, execErr := q.ExecContext(ctx, `DELETE FROM remote_instances WHERE id=$1::uuid`, id)
		if execErr != nil {
			return struct{}{}, execErr
		}
		if n, rowsErr := result.RowsAffected(); rowsErr != nil {
			return struct{}{}, rowsErr
		} else if n == 0 {
			return struct{}{}, ErrNotFound
		}
		return struct{}{}, nil
	})
	return err
}

func (s *PostgresStore) RecordRemoteInstanceCheck(ctx context.Context, id, state string, checkedAt time.Time) (RemoteInstance, error) {
	value, err := scanRemoteInstance(s.db.QueryRowContext(ctx, `UPDATE remote_instances SET state=$2,last_checked_at=$3,updated_at=now()
WHERE id=$1::uuid RETURNING `+remoteInstanceColumns, id, state, checkedAt.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteInstance{}, ErrNotFound
	}
	return value, err
}

func (s *PostgresStore) LocalInstanceID(ctx context.Context) (string, error) {
	const read = `SELECT instance_id::text FROM instance_identity WHERE singleton`
	var id string
	err := s.db.QueryRowContext(ctx, read).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO instance_identity (singleton,instance_id) VALUES (true,$1::uuid) ON CONFLICT (singleton) DO NOTHING`, ids.New()); err != nil {
		return "", err
	}
	err = s.db.QueryRowContext(ctx, read).Scan(&id)
	return id, err
}

var _ RemoteInstanceStore = (*PostgresStore)(nil)
var _ RemoteInstanceStore = (*MemoryStore)(nil)
