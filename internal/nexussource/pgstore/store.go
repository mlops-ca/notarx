// Package pgstore persists Nexus source settings without expanding GatewayStore.
package pgstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/mlops-ca/notarx/internal/nexussource"
	"github.com/mlops-ca/notarx/internal/repository"
)

type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

var _ nexussource.Store = (*Store)(nil)

const columns = `id::text,name,base_url,credential_ciphertext,version::text,created_at,updated_at`

func scan(row interface{ Scan(...any) error }) (nexussource.Source, error) {
	var v nexussource.Source
	err := row.Scan(&v.ID, &v.Name, &v.BaseURL, &v.CredentialCiphertext, &v.Version, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = nexussource.ErrNotFound
	}
	return v, err
}

// withAudit runs the change on the pool, or with audit in one transaction with the audit records so
// a failing audit insert rolls the change back (ADR 0023 section 1).
func (s *Store) withAudit(ctx context.Context, audit []repository.AuditRecord, change func(row func(string, ...any) *sql.Row) error) error {
	if len(audit) == 0 {
		return change(func(query string, args ...any) *sql.Row { return s.db.QueryRowContext(ctx, query, args...) })
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = change(func(query string, args ...any) *sql.Row { return tx.QueryRowContext(ctx, query, args...) }); err != nil {
		return err
	}
	if err = repository.InsertAuditsTx(ctx, tx, audit...); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Create(ctx context.Context, v nexussource.Source, audit ...repository.AuditRecord) (nexussource.Source, error) {
	var out nexussource.Source
	err := s.withAudit(ctx, audit, func(row func(string, ...any) *sql.Row) (err error) {
		out, err = scan(row(`INSERT INTO nexus_migration_sources(id,name,base_url,credential_ciphertext) VALUES($1,$2,$3,$4) RETURNING `+columns, v.ID, v.Name, v.BaseURL, v.CredentialCiphertext))
		return err
	})
	return out, err
}
func (s *Store) Get(ctx context.Context, id string) (nexussource.Source, error) {
	return scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM nexus_migration_sources WHERE id=$1::uuid`, id))
}
func (s *Store) List(ctx context.Context, limit int, cursor string) ([]nexussource.Source, string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM nexus_migration_sources WHERE ($1='' OR id > NULLIF($1,'')::uuid) ORDER BY id LIMIT $2`, cursor, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	out := make([]nexussource.Source, 0)
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[len(out)-1].ID
	}
	return out, next, nil
}
func (s *Store) Update(ctx context.Context, v nexussource.Source, expected string, audit ...repository.AuditRecord) (nexussource.Source, error) {
	var out nexussource.Source
	err := s.withAudit(ctx, audit, func(row func(string, ...any) *sql.Row) (err error) {
		out, err = scan(row(`UPDATE nexus_migration_sources SET name=$2,base_url=$3,credential_ciphertext=$4,version=version+1,updated_at=clock_timestamp() WHERE id=$1::uuid AND version=$5::text::bigint RETURNING `+columns, v.ID, v.Name, v.BaseURL, v.CredentialCiphertext, expected))
		return err
	})
	return out, s.classify(ctx, v.ID, err)
}
func (s *Store) Delete(ctx context.Context, id, expected string, audit ...repository.AuditRecord) error {
	err := s.withAudit(ctx, audit, func(row func(string, ...any) *sql.Row) error {
		var deleted string
		err := row(`DELETE FROM nexus_migration_sources WHERE id=$1::uuid AND version=$2::text::bigint RETURNING id::text`, id, expected).Scan(&deleted)
		if errors.Is(err, sql.ErrNoRows) {
			err = nexussource.ErrNotFound
		}
		return err
	})
	return s.classify(ctx, id, err)
}
func (s *Store) classify(ctx context.Context, id string, err error) error {
	if errors.Is(err, nexussource.ErrNotFound) {
		if _, e := s.Get(ctx, id); e == nil {
			return nexussource.ErrVersionConflict
		} else if !errors.Is(e, nexussource.ErrNotFound) {
			return e
		}
	}
	return err
}
