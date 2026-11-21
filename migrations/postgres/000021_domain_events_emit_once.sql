-- +goose Up

-- ADR 0021 (slice 1b) with ADR 0023 section 2: dedupe_key is not unique, so the catch-up reconciler and the repair
-- path cannot lean on a unique violation. emit_domain_event_once takes an advisory lock on the dedupe key, writes the
-- event only when no row with the key exists at or after the retention floor, and otherwise returns NULL. Two
-- reconcilers racing for one publication therefore write one event, and an event older than the floor (its partition
-- is dropped or about to be) does not block a new one. The lock is transaction scoped; the existence check runs as a
-- new statement after the lock is held, so under READ COMMITTED it sees the row of the reconciler that held it first.
-- +goose StatementBegin
CREATE FUNCTION public.emit_domain_event_once(p_id uuid, p_type text, p_repository_id uuid, p_format text, p_coordinate text, p_digest text, p_payload jsonb, p_dedupe_key text, p_internal boolean, p_floor timestamp with time zone) RETURNS timestamp with time zone
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(p_dedupe_key, 0));
    IF EXISTS (SELECT 1 FROM public.domain_events WHERE dedupe_key = p_dedupe_key AND occurred_at >= p_floor) THEN
        RETURN NULL;
    END IF;
    RETURN public.emit_domain_event(p_id, p_type, p_repository_id, p_format, p_coordinate, p_digest, p_payload, p_dedupe_key, p_internal);
END;
$$;
-- +goose StatementEnd
