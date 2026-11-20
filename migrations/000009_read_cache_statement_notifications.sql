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

-- +goose StatementBegin
CREATE FUNCTION public.notarx_notify_cache_statement() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    -- TG_ARGV[0] is the scope, TG_ARGV[1] the key column the scope announces.
    flush_above constant integer := 64;
    changed text;
    keys text[];
    key text;
BEGIN
    IF TG_ARGV[1] NOT IN ('id', 'group_id') THEN
        RAISE EXCEPTION 'notarx_notify_cache_statement: unsupported key column %', TG_ARGV[1];
    END IF;
    IF TG_OP = 'INSERT' THEN
        changed := format('SELECT %1$I::text AS key FROM new_rows', TG_ARGV[1]);
    ELSIF TG_OP = 'DELETE' THEN
        changed := format('SELECT %1$I::text AS key FROM old_rows', TG_ARGV[1]);
    ELSE
        changed := format('SELECT %1$I::text AS key FROM new_rows UNION SELECT %1$I::text AS key FROM old_rows', TG_ARGV[1]);
    END IF;
    -- One more than the threshold is enough to know the statement is a bulk one.
    EXECUTE format('SELECT COALESCE(array_agg(key), ARRAY[]::text[]) FROM (SELECT key FROM (%s) changed LIMIT %s) limited', changed, flush_above + 1)
        INTO keys;
    IF cardinality(keys) > flush_above THEN
        PERFORM pg_notify('notarx_cache', TG_ARGV[0] || ':');
        RETURN NULL;
    END IF;
    FOREACH key IN ARRAY keys LOOP
        PERFORM pg_notify('notarx_cache', TG_ARGV[0] || ':' || COALESCE(key, ''));
    END LOOP;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER hosted_repositories_notify_cache ON public.hosted_repositories;
DROP TRIGGER hosted_groups_notify_cache ON public.hosted_groups;
DROP TRIGGER hosted_group_members_notify_cache ON public.hosted_group_members;

-- A trigger with transition tables names one event, so each table has three.
CREATE TRIGGER hosted_repositories_notify_cache_insert AFTER INSERT ON public.hosted_repositories REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement('repository', 'id');
CREATE TRIGGER hosted_repositories_notify_cache_update AFTER UPDATE ON public.hosted_repositories REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement('repository', 'id');
CREATE TRIGGER hosted_repositories_notify_cache_delete AFTER DELETE ON public.hosted_repositories REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement('repository', 'id');

CREATE TRIGGER hosted_groups_notify_cache_insert AFTER INSERT ON public.hosted_groups REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement('group', 'id');
CREATE TRIGGER hosted_groups_notify_cache_update AFTER UPDATE ON public.hosted_groups REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement('group', 'id');
CREATE TRIGGER hosted_groups_notify_cache_delete AFTER DELETE ON public.hosted_groups REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement('group', 'id');

CREATE TRIGGER hosted_group_members_notify_cache_insert AFTER INSERT ON public.hosted_group_members REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement('group', 'group_id');
CREATE TRIGGER hosted_group_members_notify_cache_update AFTER UPDATE ON public.hosted_group_members REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement('group', 'group_id');
CREATE TRIGGER hosted_group_members_notify_cache_delete AFTER DELETE ON public.hosted_group_members REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement('group', 'group_id');
