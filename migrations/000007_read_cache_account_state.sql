-- +goose Up

-- ADR 0023 section 6 (slice 12), second group of read caches: the account and repository state an
-- authenticated read resolves, the grant set, and the identity-federation inputs. Same mechanism as migration
-- 000006: a row trigger announces the change on notarx_cache in the mutating transaction, so no write path can
-- forget it, and a lost notification is still bounded by NOTARX_AUTHZ_CACHE_TTL.
--
--   user:<id>               users row inserted, updated (state, role, session version, password) or deleted
--   quarantine:<repo id>    repository_quarantine_read_policies row of a repository changed
--   grants:<repo id>        repository_grants row of a repository changed
--   signing_keys:           token_signing_keys row changed (rotation, revoke, reseal); flushes the scope
--   workload_issuer:<id>    workload_issuers row changed
--   workload_policy:<id>    workload_trust_policies row changed
--
-- The grant set is cached under (repository id, hosted_repositories.grants_version). Every transaction that
-- replaces, upserts, deletes or templates grants bumps the column, so the version read from the cached
-- repository row turns a stale grant set into a miss even when an eviction is lost. The bump updates the
-- hosted_repositories row, which announces repository:<id> and evicts the cached row on every node too.
--
-- MySQL (ADR 0022): the twin adds grants_version and bumps the scope epoch of cache_epochs from the same
-- triggers; there is no push.

ALTER TABLE public.hosted_repositories ADD COLUMN grants_version bigint DEFAULT 1 NOT NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.notarx_notify_cache() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    changed record;
    key text := '';
BEGIN
    IF TG_OP = 'DELETE' THEN
        changed := OLD;
    ELSE
        changed := NEW;
    END IF;
    IF TG_ARGV[1] = 'id' THEN
        key := changed.id::text;
    ELSIF TG_ARGV[1] = 'group_id' THEN
        key := changed.group_id::text;
    ELSIF TG_ARGV[1] = 'repository_id' THEN
        key := changed.repository_id::text;
    ELSIF TG_ARGV[1] <> '' THEN
        RAISE EXCEPTION 'notarx_notify_cache: unsupported key column %', TG_ARGV[1];
    END IF;
    PERFORM pg_notify('notarx_cache', TG_ARGV[0] || ':' || COALESCE(key, ''));
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER users_notify_cache AFTER INSERT OR UPDATE OR DELETE ON public.users FOR EACH ROW EXECUTE FUNCTION public.notarx_notify_cache('user', 'id');
CREATE TRIGGER repository_quarantine_read_policies_notify_cache AFTER INSERT OR UPDATE OR DELETE ON public.repository_quarantine_read_policies FOR EACH ROW EXECUTE FUNCTION public.notarx_notify_cache('quarantine', 'repository_id');
CREATE TRIGGER repository_grants_notify_cache AFTER INSERT OR UPDATE OR DELETE ON public.repository_grants FOR EACH ROW EXECUTE FUNCTION public.notarx_notify_cache('grants', 'repository_id');
CREATE TRIGGER token_signing_keys_notify_cache AFTER INSERT OR UPDATE OR DELETE ON public.token_signing_keys FOR EACH ROW EXECUTE FUNCTION public.notarx_notify_cache('signing_keys', '');
CREATE TRIGGER workload_issuers_notify_cache AFTER INSERT OR UPDATE OR DELETE ON public.workload_issuers FOR EACH ROW EXECUTE FUNCTION public.notarx_notify_cache('workload_issuer', 'id');
CREATE TRIGGER workload_trust_policies_notify_cache AFTER INSERT OR UPDATE OR DELETE ON public.workload_trust_policies FOR EACH ROW EXECUTE FUNCTION public.notarx_notify_cache('workload_policy', 'id');
