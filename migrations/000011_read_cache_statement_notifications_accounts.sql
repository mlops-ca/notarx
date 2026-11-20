-- +goose Up

-- ADR 0023 section 6: migration 000010 made the triggers of hosted_repositories, hosted_groups and
-- hosted_group_members statement-level and bounded. The triggers migration 000007 added on users,
-- repository_grants, repository_quarantine_read_policies, token_signing_keys, workload_issuers and
-- workload_trust_policies were still row-level, so a bulk write (a template apply that rewrites many grants, a
-- user import, a workload policy reseal) sent one pg_notify per row and every node's listener processed each.
--
-- Each is now one statement-level trigger per event over the transition tables of the statement, with the same
-- bound as migration 000010: a statement that changed at most 64 distinct entities announces each by key
-- (user:<id>, grants:<repository id>, ...), a larger one announces the scope once (user:, grants:), which the
-- listener applies as one flush of the scope. token_signing_keys holds a handful of rows and its scope is
-- flushed as a whole, so a statement announces signing_keys: once however many rows it changed. Revocation
-- semantics are unchanged: a mutating node still evicts synchronously, the others on the notification or within
-- NOTARX_AUTHZ_CACHE_TTL.
--
-- MySQL (ADR 0022): the twin bumps the scope epoch once per statement; nothing changes for it here.

-- +goose StatementBegin
CREATE FUNCTION public.notarx_notify_cache_statement_scope() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    changed boolean;
BEGIN
    -- A statement that changed no row announces nothing, as the row trigger it replaces never fired for it.
    IF TG_OP = 'DELETE' THEN
        SELECT EXISTS (SELECT 1 FROM old_rows) INTO changed;
    ELSE
        SELECT EXISTS (SELECT 1 FROM new_rows) INTO changed;
    END IF;
    IF changed THEN
        PERFORM pg_notify('notarx_cache', TG_ARGV[0] || ':');
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION public.notarx_notify_cache_statement_repository_id() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    keys text[];
BEGIN
    IF TG_OP = 'INSERT' THEN
        SELECT COALESCE(array_agg(k), ARRAY[]::text[]) INTO keys FROM (SELECT DISTINCT repository_id::text AS k FROM new_rows LIMIT 65) limited;
    ELSIF TG_OP = 'DELETE' THEN
        SELECT COALESCE(array_agg(k), ARRAY[]::text[]) INTO keys FROM (SELECT DISTINCT repository_id::text AS k FROM old_rows LIMIT 65) limited;
    ELSE
        SELECT COALESCE(array_agg(k), ARRAY[]::text[]) INTO keys FROM (
            SELECT repository_id::text AS k FROM new_rows UNION SELECT repository_id::text FROM old_rows LIMIT 65) limited;
    END IF;
    PERFORM public.notarx_announce_cache(TG_ARGV[0], keys);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER users_notify_cache ON public.users;
DROP TRIGGER repository_quarantine_read_policies_notify_cache ON public.repository_quarantine_read_policies;
DROP TRIGGER repository_grants_notify_cache ON public.repository_grants;
DROP TRIGGER workload_issuers_notify_cache ON public.workload_issuers;
DROP TRIGGER workload_trust_policies_notify_cache ON public.workload_trust_policies;
DROP TRIGGER token_signing_keys_notify_cache ON public.token_signing_keys;

CREATE TRIGGER users_notify_cache_insert AFTER INSERT ON public.users REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_id('user');
CREATE TRIGGER users_notify_cache_update AFTER UPDATE ON public.users REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_id('user');
CREATE TRIGGER users_notify_cache_delete AFTER DELETE ON public.users REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_id('user');

CREATE TRIGGER repository_quarantine_read_policies_notify_cache_insert AFTER INSERT ON public.repository_quarantine_read_policies REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_repository_id('quarantine');
CREATE TRIGGER repository_quarantine_read_policies_notify_cache_update AFTER UPDATE ON public.repository_quarantine_read_policies REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_repository_id('quarantine');
CREATE TRIGGER repository_quarantine_read_policies_notify_cache_delete AFTER DELETE ON public.repository_quarantine_read_policies REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_repository_id('quarantine');

CREATE TRIGGER repository_grants_notify_cache_insert AFTER INSERT ON public.repository_grants REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_repository_id('grants');
CREATE TRIGGER repository_grants_notify_cache_update AFTER UPDATE ON public.repository_grants REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_repository_id('grants');
CREATE TRIGGER repository_grants_notify_cache_delete AFTER DELETE ON public.repository_grants REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_repository_id('grants');

CREATE TRIGGER workload_issuers_notify_cache_insert AFTER INSERT ON public.workload_issuers REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_id('workload_issuer');
CREATE TRIGGER workload_issuers_notify_cache_update AFTER UPDATE ON public.workload_issuers REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_id('workload_issuer');
CREATE TRIGGER workload_issuers_notify_cache_delete AFTER DELETE ON public.workload_issuers REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_id('workload_issuer');

CREATE TRIGGER workload_trust_policies_notify_cache_insert AFTER INSERT ON public.workload_trust_policies REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_id('workload_policy');
CREATE TRIGGER workload_trust_policies_notify_cache_update AFTER UPDATE ON public.workload_trust_policies REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_id('workload_policy');
CREATE TRIGGER workload_trust_policies_notify_cache_delete AFTER DELETE ON public.workload_trust_policies REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_id('workload_policy');

CREATE TRIGGER token_signing_keys_notify_cache_insert AFTER INSERT ON public.token_signing_keys REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_scope('signing_keys');
CREATE TRIGGER token_signing_keys_notify_cache_update AFTER UPDATE ON public.token_signing_keys REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_scope('signing_keys');
CREATE TRIGGER token_signing_keys_notify_cache_delete AFTER DELETE ON public.token_signing_keys REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_scope('signing_keys');
