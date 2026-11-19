package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/mlops-ca/notarx/internal/ids"
)

func scanAuthorizationTemplate(scanner interface{ Scan(...any) error }) (AuthorizationTemplate, error) {
	var template AuthorizationTemplate
	var grants []byte
	if err := scanner.Scan(&template.ID, &template.Name, &template.Description, &grants, &template.Version, &template.CreatedAt, &template.UpdatedAt); err != nil {
		return AuthorizationTemplate{}, err
	}
	if err := json.Unmarshal(grants, &template.Grants); err != nil {
		return AuthorizationTemplate{}, err
	}
	return template, nil
}

const authorizationTemplateColumns = `id::text,name,description,grants,version::text,created_at,updated_at`

func (s *PostgresStore) CreateAuthorizationTemplate(ctx context.Context, template AuthorizationTemplate, audit ...AuditRecord) (AuthorizationTemplate, error) {
	if template.ID == "" {
		template.ID = ids.New()
	}
	template.Name = strings.TrimSpace(template.Name)
	encoded, err := json.Marshal(template.Grants)
	if err != nil {
		return AuthorizationTemplate{}, err
	}
	return runAudited(ctx, s, audit, func(q sqlRunner) (AuthorizationTemplate, error) {
		created, err := scanAuthorizationTemplate(q.QueryRowContext(ctx, `INSERT INTO authorization_templates (id,name,description,grants) VALUES ($1,$2,$3,$4) RETURNING `+authorizationTemplateColumns, template.ID, template.Name, template.Description, encoded))
		if isUnique(err) {
			return AuthorizationTemplate{}, ErrTemplateNameExists
		}
		return created, err
	})
}

func (s *PostgresStore) ListAuthorizationTemplates(ctx context.Context) ([]AuthorizationTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+authorizationTemplateColumns+` FROM authorization_templates ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := []AuthorizationTemplate{}
	for rows.Next() {
		item, err := scanAuthorizationTemplate(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *PostgresStore) GetAuthorizationTemplate(ctx context.Context, id string) (AuthorizationTemplate, error) {
	template, err := scanAuthorizationTemplate(s.db.QueryRowContext(ctx, `SELECT `+authorizationTemplateColumns+` FROM authorization_templates WHERE id=$1::uuid`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizationTemplate{}, ErrNotFound
	}
	return template, err
}

func (s *PostgresStore) UpdateAuthorizationTemplate(ctx context.Context, template AuthorizationTemplate, expectedVersion string, audit ...AuditRecord) (AuthorizationTemplate, error) {
	encoded, err := json.Marshal(template.Grants)
	if err != nil {
		return AuthorizationTemplate{}, err
	}
	template.Name = strings.TrimSpace(template.Name)
	return runAudited(ctx, s, audit, func(q sqlRunner) (AuthorizationTemplate, error) {
		updated, err := scanAuthorizationTemplate(q.QueryRowContext(ctx, `UPDATE authorization_templates SET name=$1,description=$2,grants=$3,version=version+1,updated_at=now() WHERE id=$4::uuid AND version=$5::text::bigint RETURNING `+authorizationTemplateColumns, template.Name, template.Description, encoded, template.ID, expectedVersion))
		if errors.Is(err, sql.ErrNoRows) {
			var exists bool
			if queryErr := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_templates WHERE id=$1::uuid)`, template.ID).Scan(&exists); queryErr != nil {
				return AuthorizationTemplate{}, queryErr
			}
			if !exists {
				return AuthorizationTemplate{}, ErrNotFound
			}
			return AuthorizationTemplate{}, ErrVersionConflict
		}
		if isUnique(err) {
			return AuthorizationTemplate{}, ErrTemplateNameExists
		}
		return updated, err
	})
}

func (s *PostgresStore) DeleteAuthorizationTemplate(ctx context.Context, id string, audit ...AuditRecord) error {
	_, err := runAudited(ctx, s, audit, func(q sqlRunner) (struct{}, error) {
		result, err := q.ExecContext(ctx, `DELETE FROM authorization_templates WHERE id=$1::uuid`, id)
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

func (s *PostgresStore) ApplyAuthorizationTemplate(ctx context.Context, templateID, repositoryID, expectedVersion string, audit ...AuditRecord) (RepositoryGrantSet, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RepositoryGrantSet{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var template AuthorizationTemplate
	var encoded []byte
	err = tx.QueryRowContext(ctx, `SELECT `+authorizationTemplateColumns+` FROM authorization_templates WHERE id=$1::uuid FOR SHARE`, templateID).Scan(&template.ID, &template.Name, &template.Description, &encoded, &template.Version, &template.CreatedAt, &template.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryGrantSet{}, ErrNotFound
	}
	if err != nil {
		return RepositoryGrantSet{}, err
	}
	if err := json.Unmarshal(encoded, &template.Grants); err != nil {
		return RepositoryGrantSet{}, err
	}
	var repositoryExists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosted_repositories WHERE id=$1::uuid)`, repositoryID).Scan(&repositoryExists); err != nil {
		return RepositoryGrantSet{}, err
	}
	if !repositoryExists {
		return RepositoryGrantSet{}, ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO repository_grant_sets (repository_id,version) VALUES ($1,1) ON CONFLICT DO NOTHING`, repositoryID); err != nil {
		return RepositoryGrantSet{}, err
	}
	var version string
	err = tx.QueryRowContext(ctx, `UPDATE repository_grant_sets SET version=version+1 WHERE repository_id=$1::uuid AND version=$2::text::bigint RETURNING version::text`, repositoryID, expectedVersion).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if queryErr := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosted_repositories WHERE id=$1::uuid)`, repositoryID).Scan(&exists); queryErr != nil {
			return RepositoryGrantSet{}, queryErr
		}
		if !exists {
			return RepositoryGrantSet{}, ErrNotFound
		}
		return RepositoryGrantSet{}, ErrVersionConflict
	}
	if err != nil {
		return RepositoryGrantSet{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM repository_grants WHERE repository_id=$1::uuid`, repositoryID); err != nil {
		return RepositoryGrantSet{}, err
	}
	for _, grant := range template.Grants {
		if _, err = tx.ExecContext(ctx, `INSERT INTO repository_grants (repository_id,principal,scopes,resource_prefix) VALUES ($1,$2,$3,$4)`, repositoryID, grant.Principal, grant.Scopes, grant.ResourcePrefix); err != nil {
			return RepositoryGrantSet{}, err
		}
	}
	if err = insertAudits(ctx, tx, audit); err != nil {
		return RepositoryGrantSet{}, err
	}
	if err = tx.Commit(); err != nil {
		return RepositoryGrantSet{}, err
	}
	return RepositoryGrantSet{Version: version, Grants: append([]RepositoryGrant(nil), template.Grants...)}, nil
}
