package repository

import (
	"context"
	"sort"
)

// RepositoryGrantIndex answers, for one principal, which repositories hold a whole-repository grant
// (no resource prefix) that carries a scope. It is the candidate set of a cross-repository view such
// as the aggregate lifecycle job listing: the authorizer stays the source of truth and decides on
// each candidate, but the view no longer reads every repository to find them (ADR 0023 section 8).
type RepositoryGrantIndex interface {
	// ListRepositoryIDsGrantedToPrincipal returns the ids, ascending, of the repositories on which
	// principal has a grant without a resource prefix whose scopes include scope.
	ListRepositoryIDsGrantedToPrincipal(ctx context.Context, principal, scope string) ([]string, error)
}

var (
	_ RepositoryGrantIndex = (*PostgresStore)(nil)
	_ RepositoryGrantIndex = (*MemoryStore)(nil)
)

// listRepositoryIDsGrantedToPrincipalQuery is served by repository_grants_principal_idx. ORDER BY names
// the table column, not the selected id text.
const listRepositoryIDsGrantedToPrincipalQuery = `SELECT g.repository_id::text FROM repository_grants g
WHERE g.principal=$1 AND g.resource_prefix='' AND $2=ANY(g.scopes) ORDER BY g.repository_id`

func (s *PostgresStore) ListRepositoryIDsGrantedToPrincipal(ctx context.Context, principal, scope string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, listRepositoryIDsGrantedToPrincipalQuery, principal, scope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *MemoryStore) ListRepositoryIDsGrantedToPrincipal(_ context.Context, principal, scope string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := []string{}
	for repositoryID, set := range s.repositoryGrants {
		if _, ok := s.hostedRepositories[repositoryID]; !ok {
			continue
		}
		for _, grant := range set.Grants {
			if grant.Principal != principal || grant.ResourcePrefix != "" {
				continue
			}
			for _, granted := range grant.Scopes {
				if granted == scope {
					ids = append(ids, repositoryID)
					break
				}
			}
			break
		}
	}
	sort.Strings(ids)
	return ids, nil
}
