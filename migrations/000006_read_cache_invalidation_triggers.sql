-- +goose Up

-- ADR 0023 section 6 (slice 12): every mutation of the access-decision inputs announces itself on the
-- notarx_cache channel in its own transaction, so every node evicts within the notification latency and a
-- lost notification is still bounded by NOTARX_AUTHZ_CACHE_TTL. The announcement is a row trigger rather than
-- a statement in each store method: a path that writes these tables directly (a reseal, a preset import, a
-- future store) cannot forget it. PostgreSQL delivers a NOTIFY at commit and drops one sent by a rolled-back
-- transaction, which is the behaviour the caches need.
--
--   repository:<id>  hosted_repositories row inserted, updated (rename, visibility, state, configuration) or deleted
--   group:<id>       hosted_groups row or hosted_group_members of the group changed
--   policy:<id>      the instance-wide public access gate was replaced
--
-- A cached group holds its own row and member ids only; the access decision reads each member through the
-- repository cache, so a repository change evicts that repository and never the groups. The trigger reads the
-- one key column it announces (TG_ARGV[1]: id, group_id or empty) instead of serializing the whole row, so a
-- row with large configuration columns costs no more to announce than any other.
--
-- MySQL (ADR 0022): there is no push. The MySQL twin bumps the scope epoch of cache_epochs in the mutating
-- transaction (the same triggers, writing the epoch row) and every node polls it.

-- +goose StatementBegin
CREATE FUNCTION public.notarx_notify_cache() RETURNS trigger
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
    ELSIF TG_ARGV[1] <> '' THEN
        RAISE EXCEPTION 'notarx_notify_cache: unsupported key column %', TG_ARGV[1];
    END IF;
    PERFORM pg_notify('notarx_cache', TG_ARGV[0] || ':' || COALESCE(key, ''));
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER hosted_repositories_notify_cache AFTER INSERT OR UPDATE OR DELETE ON public.hosted_repositories FOR EACH ROW EXECUTE FUNCTION public.notarx_notify_cache('repository', 'id');
CREATE TRIGGER hosted_groups_notify_cache AFTER INSERT OR UPDATE OR DELETE ON public.hosted_groups FOR EACH ROW EXECUTE FUNCTION public.notarx_notify_cache('group', 'id');
CREATE TRIGGER hosted_group_members_notify_cache AFTER INSERT OR UPDATE OR DELETE ON public.hosted_group_members FOR EACH ROW EXECUTE FUNCTION public.notarx_notify_cache('group', 'group_id');
CREATE TRIGGER public_access_policy_notify_cache AFTER INSERT OR UPDATE OR DELETE ON public.public_access_policy FOR EACH ROW EXECUTE FUNCTION public.notarx_notify_cache('policy', '');
