package repository

import (
	"context"
	"slices"
	"sort"
)

// RepositoryGrantIndex answers, for one principal, which repositories hold a whole-repository grant
// (no resource prefix) that carries a scope. It is the candidate set of a cross-repository view such
// as the aggregate lifecycle job listing: the authorizer stays the source of truth and decides on
// each candidate, but the view no longer reads every repository to find them (ADR 0023 section 8).
type RepositoryGrantIndex interface {
	// ListRepositoriesGrantedToPrincipal returns the repositories, in no particular order, on which principal has
	// a grant without a resource prefix whose scopes include scope, in one query.
	ListRepositoriesGrantedToPrincipal(ctx context.Context, principal, scope string) ([]HostedRepository, error)
	// ListRepositoriesGrantedToPrincipals is the same for several principals at once: the actor of an account and
	// the group:<id> principals of its user groups (ADR 0018 Decision 5). One query, each repository once.
	ListRepositoriesGrantedToPrincipals(ctx context.Context, principals []string, scope string) ([]HostedRepository, error)
}

var (
	_ RepositoryGrantIndex = (*PostgresStore)(nil)
	_ RepositoryGrantIndex = (*MemoryStore)(nil)
)

// listRepositoriesGrantedToPrincipalQuery reads the candidates through repository_grants_principal_idx. ORDER BY
// names the table column, not the selected id text.
const listRepositoriesGrantedToPrincipalQuery = `SELECT ` + hostedRepositoryColumns + ` FROM hosted_repositories
WHERE id IN (SELECT g.repository_id FROM repository_grants g WHERE g.principal=$1 AND g.resource_prefix='' AND $2=ANY(g.scopes))
ORDER BY hosted_repositories.id`

// listRepositoriesGrantedToPrincipalsQuery is the multi-principal form: the same index, one probe per principal.
const listRepositoriesGrantedToPrincipalsQuery = `SELECT ` + hostedRepositoryColumns + ` FROM hosted_repositories
WHERE id IN (SELECT g.repository_id FROM repository_grants g WHERE g.principal = ANY($1) AND g.resource_prefix='' AND $2=ANY(g.scopes))
ORDER BY hosted_repositories.id`

func (s *PostgresStore) ListRepositoriesGrantedToPrincipals(ctx context.Context, principals []string, scope string) ([]HostedRepository, error) {
	if len(principals) == 0 {
		return []HostedRepository{}, nil
	}
	rows, err := s.db.QueryContext(ctx, listRepositoriesGrantedToPrincipalsQuery, principals, scope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	repos := []HostedRepository{}
	for rows.Next() {
		var repo HostedRepository
		if err := scanHostedRepository(rows, &repo); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, rows.Err()
}

func (s *PostgresStore) ListRepositoriesGrantedToPrincipal(ctx context.Context, principal, scope string) ([]HostedRepository, error) {
	rows, err := s.db.QueryContext(ctx, listRepositoriesGrantedToPrincipalQuery, principal, scope)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	repos := []HostedRepository{}
	for rows.Next() {
		var repo HostedRepository
		if err := scanHostedRepository(rows, &repo); err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	return repos, rows.Err()
}

func (s *MemoryStore) ListRepositoriesGrantedToPrincipal(_ context.Context, principal, scope string) ([]HostedRepository, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	repos := []HostedRepository{}
	for repositoryID, set := range s.repositoryGrants {
		repo, ok := s.hostedRepositories[repositoryID]
		if !ok {
			continue
		}
		for _, grant := range set.Grants {
			if grant.Principal != principal || grant.ResourcePrefix != "" {
				continue
			}
			if slices.Contains(grant.Scopes, scope) {
				repos = append(repos, cloneHostedRepositoryOCIBearer(repo))
			}
			break
		}
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].ID < repos[j].ID })
	return repos, nil
}

func (s *MemoryStore) ListRepositoriesGrantedToPrincipals(ctx context.Context, principals []string, scope string) ([]HostedRepository, error) {
	seen := map[string]bool{}
	repos := []HostedRepository{}
	for _, principal := range principals {
		granted, err := s.ListRepositoriesGrantedToPrincipal(ctx, principal, scope)
		if err != nil {
			return nil, err
		}
		for _, repo := range granted {
			if !seen[repo.ID] {
				seen[repo.ID] = true
				repos = append(repos, repo)
			}
		}
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].ID < repos[j].ID })
	return repos, nil
}
