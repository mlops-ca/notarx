-- +goose Up

-- ADR 0021, Domain events: the dispatcher reads only below the oldest transaction that can still commit an event, so a
-- long transaction that never emits (an import, a job, an idle-in-transaction session) must not hold the log back. The
-- first emission of a transaction therefore marks its session with application_name = 'notarx-emit:<microseconds since the
-- epoch>' for the rest of the transaction (set_config is_local reverts it at commit or rollback): the stamp is taken
-- before the row's own occurred_at, so every event the transaction writes is at or after it. The read horizon is the
-- oldest stamp, not the transaction start, so a transaction is only counted from its first emission to its end.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.emit_domain_event(p_id uuid, p_type text, p_repository_id uuid, p_format text, p_coordinate text, p_digest text, p_payload jsonb, p_dedupe_key text, p_internal boolean) RETURNS timestamp with time zone
    LANGUAGE plpgsql
    AS $$
DECLARE
    stamp timestamp with time zone := clock_timestamp();
    occurred timestamp with time zone;
BEGIN
    IF NOT p_internal AND NOT EXISTS (SELECT 1 FROM public.webhook_subscriptions WHERE enabled AND event_types @> jsonb_build_array(p_type)) THEN
        RETURN NULL;
    END IF;
    IF current_setting('application_name') NOT LIKE 'notarx-emit:%' THEN
        PERFORM set_config('application_name', 'notarx-emit:' || (extract(epoch FROM stamp) * 1000000)::bigint::text, true);
    END IF;
    occurred := clock_timestamp();
    INSERT INTO public.domain_events (id,type,repository_id,format,coordinate,digest,payload,dedupe_key,occurred_at)
    VALUES (p_id,p_type,p_repository_id,NULLIF(p_format,''),NULLIF(p_coordinate,''),NULLIF(p_digest,''),p_payload,p_dedupe_key,occurred);
    INSERT INTO public.webhook_deliveries (id,event_id,event_occurred_at,subscription_id,state,next_attempt_at,created_at,updated_at)
    SELECT gen_random_uuid(),p_id,occurred,s.id,'pending',clock.now,clock.now,clock.now
    FROM public.webhook_subscriptions s CROSS JOIN (SELECT clock_timestamp() AS now) AS clock
    WHERE s.enabled AND s.event_types @> jsonb_build_array(p_type);
    RETURN occurred;
END;
$$;
-- +goose StatementEnd
