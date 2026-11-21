-- +goose Up

-- ADR 0021 (slice 1a) with ADR 0023 sections 2 and 6: domain_events replaces webhook_events as the immutable
-- event log. This migration drops and recreates the event side of the webhook pipeline and discards the
-- webhook_events rows and the webhook_deliveries rows that referenced them; the product has no installed
-- base to carry over.
--
-- domain_events is range-partitioned monthly on occurred_at by the partition manager (internal/partitions,
-- retention NOTARX_EVENTS_RETENTION, default 14d, free tier 3d). A unique key on a partitioned table must
-- contain the partition key, so the primary key is (occurred_at, seq) and neither id (a UUIDv7) nor
-- dedupe_key is unique: emission inside the state-change transaction is exactly-once by construction, and
-- a repair path inserts only when no row with the same dedupe_key exists inside the retention window. Nothing
-- may carry a foreign key to the table; webhook_deliveries records the event's occurred_at beside its id so a
-- join by id prunes to one partition. The DEFAULT partition must stay empty (the manager moves any row it
-- finds into a bounded partition).
--
-- MySQL (ADR 0022): a plain table with an AUTO_INCREMENT key, pruned by the retention janitor in batches.
CREATE SEQUENCE public.domain_events_seq_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

CREATE TABLE public.domain_events (
    seq bigint DEFAULT nextval('public.domain_events_seq_seq'::regclass) NOT NULL,
    id uuid NOT NULL,
    type text NOT NULL,
    repository_id uuid,
    format text,
    coordinate text,
    digest text,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    dedupe_key text NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT domain_events_pkey PRIMARY KEY (occurred_at, seq),
    CONSTRAINT domain_events_payload_check CHECK ((jsonb_typeof(payload) = 'object'::text))
)
PARTITION BY RANGE (occurred_at);

ALTER SEQUENCE public.domain_events_seq_seq OWNED BY public.domain_events.seq;

CREATE TABLE public.domain_events_default PARTITION OF public.domain_events DEFAULT;

CREATE INDEX domain_events_dedupe_key_idx ON public.domain_events USING btree (dedupe_key, occurred_at);
CREATE INDEX domain_events_id_idx ON public.domain_events USING btree (id, occurred_at);

-- An internal consumer (a worker reading the log) keeps its position here: the occurred_at and seq of the
-- last event it handled.
CREATE TABLE public.domain_event_cursors (
    consumer text NOT NULL,
    occurred_at timestamp with time zone NOT NULL,
    seq bigint NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT domain_event_cursors_pkey PRIMARY KEY (consumer)
);

DELETE FROM public.webhook_deliveries;
ALTER TABLE public.webhook_deliveries DROP CONSTRAINT webhook_deliveries_event_id_fkey;
ALTER TABLE public.webhook_deliveries ADD COLUMN event_occurred_at timestamp with time zone NOT NULL;
DROP TABLE public.webhook_events;

-- A subscription lists event types of the new catalogue (internal/events). One that still names a Cargo-only type can
-- never fire, so it is deleted rather than rewritten.
ALTER TABLE public.webhook_subscriptions DROP CONSTRAINT webhook_subscriptions_event_types_check;
DELETE FROM public.webhook_subscriptions WHERE NOT (jsonb_typeof(event_types) = 'array' AND event_types <@ '["artifact.published", "artifact.scan.completed", "artifact.quarantined", "artifact.released", "artifact.promoted", "promotion.pending_approval", "promotion.approved", "promotion.rejected", "promotion.failed", "replication.completed", "replication.failed", "repository.visibility_changed"]'::jsonb);
ALTER TABLE public.webhook_subscriptions ADD CONSTRAINT webhook_subscriptions_event_types_check CHECK (((jsonb_typeof(event_types) = 'array'::text) AND (jsonb_array_length(event_types) > 0) AND (event_types <@ '["artifact.published", "artifact.scan.completed", "artifact.quarantined", "artifact.released", "artifact.promoted", "promotion.pending_approval", "promotion.approved", "promotion.rejected", "promotion.failed", "replication.completed", "replication.failed", "repository.visibility_changed"]'::jsonb)));

-- The event dispatcher elects the one node that reads the log for a consumer through a lease row in scheduled_tasks,
-- like the partition manager: an internal row that is never due and that the management API does not list.
ALTER TABLE public.scheduled_tasks
    DROP CONSTRAINT scheduled_tasks_check,
    DROP CONSTRAINT scheduled_tasks_kind_check,
    ADD CONSTRAINT scheduled_tasks_check CHECK ((((kind = 'audit-retention'::text) AND (repository_id IS NULL)) OR ((kind = 'repository-retention'::text) AND (repository_id IS NOT NULL)) OR ((kind = ANY (ARRAY['partition-manager'::text, 'retention-janitor'::text, 'raw-idle-seal'::text, 'event-dispatcher'::text])) AND (repository_id IS NULL) AND (NOT enabled)))),
    ADD CONSTRAINT scheduled_tasks_kind_check CHECK ((kind = ANY (ARRAY['repository-retention'::text, 'audit-retention'::text, 'partition-manager'::text, 'retention-janitor'::text, 'raw-idle-seal'::text, 'event-dispatcher'::text])));

-- emit_domain_event is the one implementation of the emission rule, shared by internal/events.Emit and the
-- database triggers below: in the caller's transaction it inserts the event and one webhook_deliveries row per
-- matching enabled subscription, and writes nothing when no subscription and no internal consumer wants the
-- type. A NULL p_occurred_at takes the database clock, which keeps occurred_at on the clock that the dispatcher's settle
-- delay and cursor use. It returns whether the event was written.
-- +goose StatementBegin
CREATE FUNCTION public.emit_domain_event(p_id uuid, p_type text, p_repository_id uuid, p_format text, p_coordinate text, p_digest text, p_payload jsonb, p_dedupe_key text, p_occurred_at timestamp with time zone, p_internal boolean) RETURNS boolean
    LANGUAGE plpgsql
    AS $
DECLARE
    occurred timestamp with time zone := COALESCE(p_occurred_at, clock_timestamp());
BEGIN
    IF NOT p_internal AND NOT EXISTS (SELECT 1 FROM public.webhook_subscriptions WHERE enabled AND event_types @> jsonb_build_array(p_type)) THEN
        RETURN false;
    END IF;
    INSERT INTO public.domain_events (id,type,repository_id,format,coordinate,digest,payload,dedupe_key,occurred_at)
    VALUES (p_id,p_type,p_repository_id,NULLIF(p_format,''),NULLIF(p_coordinate,''),NULLIF(p_digest,''),p_payload,p_dedupe_key,occurred);
    INSERT INTO public.webhook_deliveries (id,event_id,event_occurred_at,subscription_id,state,next_attempt_at,created_at,updated_at)
    SELECT gen_random_uuid(),p_id,occurred,s.id,'pending',clock.now,clock.now,clock.now
    FROM public.webhook_subscriptions s CROSS JOIN (SELECT clock_timestamp() AS now) AS clock
    WHERE s.enabled AND s.event_types @> jsonb_build_array(p_type);
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- The Cargo triggers emit the format-neutral names: a completed promotion is artifact.promoted, a failed one
-- promotion.failed, and a replication reaches replication.completed or replication.failed. The format is a
-- payload field. The version of the dedupe key is the operation id, with the attempt count for replication
-- because an explicit replay of a plan is a new run that can emit a later outcome.
DROP FUNCTION public.enqueue_cargo_distribution_webhook(text, text, jsonb, timestamp with time zone);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.cargo_promotion_state_webhook() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    event_type text;
    coordinate text := COALESCE(NEW.payload->>'name','') || '@' || COALESCE(NEW.payload->>'version','');
BEGIN
    IF OLD.state='running' AND NEW.state IN ('completed','failed')
       AND NEW.kind='promotion' AND NEW.payload->>'format'='cargo' THEN
        event_type := CASE WHEN NEW.state='completed' THEN 'artifact.promoted' ELSE 'promotion.failed' END;
        PERFORM public.emit_domain_event(gen_random_uuid(),event_type,NEW.repository_id,'cargo',coordinate,NEW.payload->>'digest',
            jsonb_build_object(
                'operationId',NEW.id,'kind','promotion','format','cargo',
                'sourceRepositoryId',NEW.payload->>'sourceRepositoryId',
                'targetRepositoryId',NEW.repository_id,
                'coordinate',coordinate,
                'digest',NEW.payload->>'digest','state',NEW.state,'attempts',NEW.attempts
            ),
            event_type||'|'||NEW.repository_id||'|'||coordinate||'|'||COALESCE(NEW.payload->>'digest','')||'|'||NEW.id,
            COALESCE(NEW.completed_at,clock_timestamp()),false);
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.cargo_replication_state_webhook() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    event_type text;
BEGIN
    IF OLD.state='running' AND NEW.format='cargo'
       AND (NEW.state='completed' OR (NEW.state='failed' AND NEW.next_attempt_at IS NULL)) THEN
        event_type := 'replication.' || NEW.state;
        PERFORM public.emit_domain_event(gen_random_uuid(),event_type,NEW.target_repository_id,'cargo',NEW.coordinate,NEW.digest,
            jsonb_build_object(
                'operationId',NEW.id,'kind','replication','format','cargo',
                'sourceRepositoryId',NEW.source_repository_id,
                'targetRepositoryId',NEW.target_repository_id,
                'coordinate',NEW.coordinate,'digest',NEW.digest,
                'state',NEW.state,'attempts',NEW.attempts
            ),
            event_type||'|'||NEW.target_repository_id||'|'||COALESCE(NEW.coordinate,'')||'|'||COALESCE(NEW.digest,'')||'|'||NEW.id||':'||NEW.attempts,
            COALESCE(NEW.completed_at,clock_timestamp()),false);
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
