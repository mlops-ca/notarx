-- +goose Up

-- repository_grants_principal_idx serves the list of repositories a principal holds a
-- whole-repository grant on (ListRepositoryIDsGrantedToPrincipal). The primary key leads with
-- repository_id, so finding one principal's grants meant reading the whole table; the aggregate
-- lifecycle job listing asks for it on every request of a caller who is not a global administrator
-- (ADR 0023 section 8). It is partial on the grants without a resource prefix, the ones that decide
-- a repository-wide operation, and the hot-query registry asserts the plan uses it.
CREATE INDEX repository_grants_principal_idx
    ON public.repository_grants USING btree (principal, repository_id)
    WHERE (resource_prefix = ''::text);
