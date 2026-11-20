package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/mlops-ca/notarx/internal/ids"
)

const authorizationRoleColumns = `id::text,name,description,array_to_json(scopes),version::text,created_at,updated_at`

func scanAuthorizationRole(scanner interface{ Scan(...any) error }) (AuthorizationRole, error) {
	var role AuthorizationRole
	var scopes []byte
	if err := scanner.Scan(&role.ID, &role.Name, &role.Description, &scopes, &role.Version, &role.CreatedAt, &role.UpdatedAt); err != nil {
		return AuthorizationRole{}, err
	}
	if err := json.Unmarshal(scopes, &role.Scopes); err != nil {
		return AuthorizationRole{}, err
	}
	return role, nil
}

func (s *PostgresStore) CreateAuthorizationRole(ctx context.Context, role AuthorizationRole, audit ...AuditRecord) (AuthorizationRole, error) {
	if role.ID == "" {
		role.ID = ids.New()
	}
	role.Name = strings.TrimSpace(role.Name)
	return runAudited(ctx, s, audit, func(q sqlRunner) (AuthorizationRole, error) {
		created, err := scanAuthorizationRole(q.QueryRowContext(ctx, `INSERT INTO authorization_roles (id,name,description,scopes) VALUES ($1,$2,$3,$4) RETURNING `+authorizationRoleColumns, role.ID, role.Name, role.Description, role.Scopes))
		if isUnique(err) {
			return AuthorizationRole{}, ErrAuthorizationRoleNameExists
		}
		return created, err
	})
}

func (s *PostgresStore) ListAuthorizationRoles(ctx context.Context) ([]AuthorizationRole, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+authorizationRoleColumns+` FROM authorization_roles ORDER BY name,authorization_roles.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []AuthorizationRole{}
	for rows.Next() {
		item, err := scanAuthorizationRole(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) GetAuthorizationRole(ctx context.Context, id string) (AuthorizationRole, error) {
	role, err := scanAuthorizationRole(s.db.QueryRowContext(ctx, `SELECT `+authorizationRoleColumns+` FROM authorization_roles WHERE id=$1::uuid`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizationRole{}, ErrNotFound
	}
	return role, err
}

func (s *PostgresStore) UpdateAuthorizationRole(ctx context.Context, role AuthorizationRole, expectedVersion string, audit ...AuditRecord) (AuthorizationRole, error) {
	role.Name = strings.TrimSpace(role.Name)
	return runAudited(ctx, s, audit, func(q sqlRunner) (AuthorizationRole, error) {
		updated, err := scanAuthorizationRole(q.QueryRowContext(ctx, `UPDATE authorization_roles SET name=$1,description=$2,scopes=$3,version=version+1,updated_at=now() WHERE id=$4::uuid AND version=$5::text::bigint RETURNING `+authorizationRoleColumns, role.Name, role.Description, role.Scopes, role.ID, expectedVersion))
		if errors.Is(err, sql.ErrNoRows) {
			var exists bool
			if queryErr := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_roles WHERE id=$1::uuid)`, role.ID).Scan(&exists); queryErr != nil {
				return AuthorizationRole{}, queryErr
			}
			if !exists {
				return AuthorizationRole{}, ErrNotFound
			}
			return AuthorizationRole{}, ErrVersionConflict
		}
		if isUnique(err) {
			return AuthorizationRole{}, ErrAuthorizationRoleNameExists
		}
		return updated, err
	})
}

func (s *PostgresStore) DeleteAuthorizationRole(ctx context.Context, id string, audit ...AuditRecord) error {
	_, err := runAudited(ctx, s, audit, func(q sqlRunner) (struct{}, error) {
		result, err := q.ExecContext(ctx, `DELETE FROM authorization_roles WHERE id=$1::uuid`, id)
		if err != nil {
			return struct{}{}, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return struct{}{}, err
		}
		if count == 0 {
			return struct{}{}, ErrNotFound
		}
		return struct{}{}, nil
	})
	return err
}
