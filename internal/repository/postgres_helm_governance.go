package repository

import (
	"context"
	"database/sql"
	"errors"
)

// The repository row serializes governance and policy writes with publication
// commits and source configuration. Publication bytes and identity stay intact.
func (s *PostgresStore) helmGovernanceTransaction(ctx context.Context, id string) (*sql.Tx, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	var name string
	err = tx.QueryRowContext(ctx, `SELECT name FROM hosted_repositories WHERE id=$1::uuid AND format='helm' AND repo_type IN ('hosted','proxy') AND state='active' FOR UPDATE`, id).Scan(&name)
	if err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrNotFound
		}
		return nil, "", err
	}
	return tx, name, nil
}

func (s *PostgresStore) TransitionHelmGovernance(ctx context.Context, in HelmGovernanceChange) (HelmPublication, error) {
	if !validHelmGovernanceChange(in) {
		return HelmPublication{}, ErrInvalidHelmPublication
	}
	tx, name, err := s.helmGovernanceTransaction(ctx, in.RepositoryID)
	if err != nil {
		return HelmPublication{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// A transition needs the identity and governance state only, so an externalised chart.yaml is not read (and a
	// missing object does not block it): the returned publication leaves it empty.
	p, _, err := scanHelmPublicationRef(tx.QueryRowContext(ctx, `SELECT `+helmPublicationColumns+` FROM native_helm_publications WHERE repository_id=$1::uuid AND name=$2 AND version=$3 FOR UPDATE`, in.RepositoryID, in.Name, in.Version))
	if err != nil {
		return HelmPublication{}, err
	}
	p, changed, err := transitionHelmGovernance(p, in)
	if err != nil {
		return HelmPublication{}, err
	}
	if changed {
		g := p.Governance
		if _, err = tx.ExecContext(ctx, `UPDATE native_helm_publications SET governance_version=$2,tombstoned=$3,quarantined=$4,governance_updated_at=$5,governance_updated_by=$6,governance_reason=$7 WHERE session_id=$1::uuid`, p.SessionID, g.Version, g.Tombstoned, g.Quarantined, g.UpdatedAt, g.UpdatedBy, g.Reason); err != nil {
			return HelmPublication{}, err
		}
		if err = insertAudit(ctx, tx, helmGovernanceAudit(p, in, name)); err != nil {
			return HelmPublication{}, err
		}
	}
	return p, tx.Commit()
}

func (s *PostgresStore) ReplaceHelmQuarantineReadPolicy(ctx context.Context, id string, policy RepositoryQuarantineReadPolicy, expectedVersion, actor string) (RepositoryQuarantineReadPolicy, error) {
	policy, err := s.replaceHelmQuarantineReadPolicyMutation(ctx, id, policy, expectedVersion, actor)
	if err == nil {
		s.invalidateLocal(CacheScopeQuarantine, id)
	}
	return policy, err
}

func (s *PostgresStore) replaceHelmQuarantineReadPolicyMutation(ctx context.Context, id string, policy RepositoryQuarantineReadPolicy, expectedVersion, actor string) (RepositoryQuarantineReadPolicy, error) {
	if !sourceVersionValid(expectedVersion) || !validHelmGovernanceActor(actor) {
		return RepositoryQuarantineReadPolicy{}, ErrInvalidHelmPublication
	}
	tx, name, err := s.helmGovernanceTransaction(ctx, id)
	if err != nil {
		return RepositoryQuarantineReadPolicy{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT INTO repository_quarantine_read_policies(repository_id,version,enabled) VALUES($1,1,false) ON CONFLICT DO NOTHING`, id); err != nil {
		return RepositoryQuarantineReadPolicy{}, err
	}
	var current RepositoryQuarantineReadPolicy
	err = tx.QueryRowContext(ctx, `SELECT version::text,enabled FROM repository_quarantine_read_policies WHERE repository_id=$1::uuid FOR UPDATE`, id).Scan(&current.Version, &current.Enabled)
	if err != nil {
		return RepositoryQuarantineReadPolicy{}, err
	}
	if current.Version != expectedVersion {
		return RepositoryQuarantineReadPolicy{}, ErrVersionConflict
	}
	if current.Enabled == policy.Enabled {
		return current, tx.Commit()
	}
	err = tx.QueryRowContext(ctx, `UPDATE repository_quarantine_read_policies SET version=version+1,enabled=$2 WHERE repository_id=$1::uuid RETURNING version::text`, id, policy.Enabled).Scan(&policy.Version)
	if err != nil {
		return RepositoryQuarantineReadPolicy{}, err
	}
	if err = insertAudit(ctx, tx, helmReadPolicyAudit(HostedRepository{ID: id, Name: name}, policy, actor)); err != nil {
		return RepositoryQuarantineReadPolicy{}, err
	}
	return policy, tx.Commit()
}
