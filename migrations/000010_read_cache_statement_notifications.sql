-- +goose Up

-- ADR 0023 section 6: a statement that changes many rows of hosted_repositories, hosted_groups or
-- hosted_group_members (datagen perf seeding, a repository preset import, a secrets reseal that bumps
-- every repository version) announced one distinct notification per row with the row triggers of
-- migration 000006, and every listening node processed every one of them. A repository change also drops
-- the negative results of the repository cache, so a bulk write emptied that cache too.
--
-- The announcement is now one statement-level trigger per event, reading the transition tables of the
-- statement. A statement that changed at most 64 distinct entities announces each by key, as before
-- (repository:<id>, group:<id>). A larger one announces the scope once (repository:, group:), which the
-- listener applies as one flush of the scope. The number of notifications a statement sends is therefore
-- bounded by 64 however many rows it changes. A mutating node still evicts synchronously and the others
-- within the notification delay or the TTL window, as before.
--
-- The policy table keeps the row trigger of migration 000006: it holds one row.
--
-- MySQL (ADR 0022): the twin bumps the scope epoch once per statement; nothing changes for it here.

-- The functions use static SQL over the transition tables, so PostgreSQL plans each statement once per session
-- and a statement that changes one row pays for one small scan, not for a dynamic query. One function per key
-- column: id for repositories and groups, group_id for the members of a group.

-- +goose StatementBegin
CREATE FUNCTION public.notarx_announce_cache(scope text, keys text[]) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    flush_above constant integer := 64;
    key text;
BEGIN
    IF cardinality(keys) > flush_above THEN
        PERFORM pg_notify('notarx_cache', scope || ':');
        RETURN;
    END IF;
    FOREACH key IN ARRAY keys LOOP
        PERFORM pg_notify('notarx_cache', scope || ':' || COALESCE(key, ''));
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION public.notarx_notify_cache_statement_id() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    -- One more than the announce threshold is enough to know the statement is a bulk one.
    keys text[];
BEGIN
    IF TG_OP = 'INSERT' THEN
        SELECT COALESCE(array_agg(k), ARRAY[]::text[]) INTO keys FROM (SELECT DISTINCT id::text AS k FROM new_rows LIMIT 65) limited;
    ELSIF TG_OP = 'DELETE' THEN
        SELECT COALESCE(array_agg(k), ARRAY[]::text[]) INTO keys FROM (SELECT DISTINCT id::text AS k FROM old_rows LIMIT 65) limited;
    ELSE
        SELECT COALESCE(array_agg(k), ARRAY[]::text[]) INTO keys FROM (
            SELECT id::text AS k FROM new_rows UNION SELECT id::text FROM old_rows LIMIT 65) limited;
    END IF;
    PERFORM public.notarx_announce_cache(TG_ARGV[0], keys);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION public.notarx_notify_cache_statement_group_id() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    keys text[];
BEGIN
    IF TG_OP = 'INSERT' THEN
        SELECT COALESCE(array_agg(k), ARRAY[]::text[]) INTO keys FROM (SELECT DISTINCT group_id::text AS k FROM new_rows LIMIT 65) limited;
    ELSIF TG_OP = 'DELETE' THEN
        SELECT COALESCE(array_agg(k), ARRAY[]::text[]) INTO keys FROM (SELECT DISTINCT group_id::text AS k FROM old_rows LIMIT 65) limited;
    ELSE
        SELECT COALESCE(array_agg(k), ARRAY[]::text[]) INTO keys FROM (
            SELECT group_id::text AS k FROM new_rows UNION SELECT group_id::text FROM old_rows LIMIT 65) limited;
    END IF;
    PERFORM public.notarx_announce_cache(TG_ARGV[0], keys);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER hosted_repositories_notify_cache ON public.hosted_repositories;
DROP TRIGGER hosted_groups_notify_cache ON public.hosted_groups;
DROP TRIGGER hosted_group_members_notify_cache ON public.hosted_group_members;

-- A trigger with transition tables names one event, so each table has three.
CREATE TRIGGER hosted_repositories_notify_cache_insert AFTER INSERT ON public.hosted_repositories REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_id('repository');
CREATE TRIGGER hosted_repositories_notify_cache_update AFTER UPDATE ON public.hosted_repositories REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_id('repository');
CREATE TRIGGER hosted_repositories_notify_cache_delete AFTER DELETE ON public.hosted_repositories REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_id('repository');

CREATE TRIGGER hosted_groups_notify_cache_insert AFTER INSERT ON public.hosted_groups REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_id('group');
CREATE TRIGGER hosted_groups_notify_cache_update AFTER UPDATE ON public.hosted_groups REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_id('group');
CREATE TRIGGER hosted_groups_notify_cache_delete AFTER DELETE ON public.hosted_groups REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_id('group');

CREATE TRIGGER hosted_group_members_notify_cache_insert AFTER INSERT ON public.hosted_group_members REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_group_id('group');
CREATE TRIGGER hosted_group_members_notify_cache_update AFTER UPDATE ON public.hosted_group_members REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_group_id('group');
CREATE TRIGGER hosted_group_members_notify_cache_delete AFTER DELETE ON public.hosted_group_members REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_group_id('group');
