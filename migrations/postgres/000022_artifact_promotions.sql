-- +goose Up

-- ADR 0021 (slice 2b), ADR 0023 section 5: artifact_promotions is the provenance of every promotion, one row per attempt.
-- A manual or rule promotion, local or imported, writes its row in the transaction that enqueues the promotion job
-- (job_id is set then); the job's state changes keep the row's state, and the terminal ones emit artifact.promoted or
-- promotion.failed in the same transaction as the change (the trigger below, through the emit_domain_event function
-- that internal/events.Emit also calls). The state column therefore follows the job: pending and retrying are queued,
-- running is running. pending_approval, rejected and expired belong to the approval slice.
--
-- Provenance is kept: the table has no retention (ADR 0023 section 2). It carries no foreign keys: lifecycle_jobs rows are
-- pruned by the retention janitor and a deleted repository must not erase the history of what was promoted into it.
--
-- chain_path is [{instance, repository, ruleId, at}], appended and never rewritten: a before-update trigger refuses any
-- change that is not an append. approvals is [{approver, at}] and is written by the approval slice.
--
-- Indexes (ADR 0023 section 5): the idempotent-replay and "already at the target" lookup (target, coordinate, digest);
-- the partial index of the pending-state sweep; job_id for the trigger and the claim-time policy check; and the keyset
-- listing order. MySQL gets the partial index as a generated-column index in the baseline (migrations/mysql-decisions.yaml).
CREATE TABLE public.artifact_promotions (
    id uuid NOT NULL,
    target_repository_id uuid NOT NULL,
    format text NOT NULL,
    coordinate text NOT NULL,
    digest text NOT NULL,
    origin_instance_id uuid,
    origin_repository text DEFAULT ''::text NOT NULL,
    rule_id uuid,
    rule_version bigint,
    chain_path jsonb DEFAULT '[]'::jsonb NOT NULL,
    requested_by text NOT NULL,
    state text NOT NULL,
    attempt integer DEFAULT 1 NOT NULL,
    idempotency_key text NOT NULL,
    job_id uuid,
    approvals jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT artifact_promotions_pkey PRIMARY KEY (id),
    CONSTRAINT artifact_promotions_idempotency_key_attempt_key UNIQUE (idempotency_key, attempt),
    CONSTRAINT artifact_promotions_state_check CHECK ((state = ANY (ARRAY['pending_approval'::text, 'queued'::text, 'running'::text, 'completed'::text, 'failed'::text, 'rejected'::text, 'expired'::text, 'cancelled'::text]))),
    CONSTRAINT artifact_promotions_attempt_check CHECK ((attempt >= 1)),
    CONSTRAINT artifact_promotions_chain_path_check CHECK ((jsonb_typeof(chain_path) = 'array'::text)),
    CONSTRAINT artifact_promotions_approvals_check CHECK ((jsonb_typeof(approvals) = 'array'::text))
)
WITH (fillfactor='80', autovacuum_vacuum_scale_factor='0.02', autovacuum_analyze_scale_factor='0.02');

CREATE INDEX artifact_promotions_target_coordinate_digest_idx ON public.artifact_promotions USING btree (target_repository_id, coordinate, digest);
CREATE INDEX artifact_promotions_pending_idx ON public.artifact_promotions USING btree (state) WHERE (state = ANY (ARRAY['pending_approval'::text, 'queued'::text, 'running'::text]));
CREATE INDEX artifact_promotions_job_idx ON public.artifact_promotions USING btree (job_id) WHERE (job_id IS NOT NULL);
CREATE INDEX artifact_promotions_created_idx ON public.artifact_promotions USING btree (created_at DESC, id DESC);

-- +goose StatementBegin
CREATE FUNCTION public.artifact_promotions_chain_path_append_only() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.chain_path IS DISTINCT FROM OLD.chain_path AND (
        jsonb_array_length(NEW.chain_path) < jsonb_array_length(OLD.chain_path)
        OR (SELECT COALESCE(jsonb_agg(hop.value ORDER BY hop.ordinality), '[]'::jsonb)
              FROM jsonb_array_elements(NEW.chain_path) WITH ORDINALITY AS hop(value, ordinality)
             WHERE hop.ordinality <= jsonb_array_length(OLD.chain_path)) IS DISTINCT FROM OLD.chain_path
    ) THEN
        RAISE EXCEPTION 'artifact_promotions.chain_path is append-only' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER artifact_promotions_chain_path_append_only BEFORE UPDATE OF chain_path ON public.artifact_promotions FOR EACH ROW EXECUTE FUNCTION public.artifact_promotions_chain_path_append_only();

-- The Cargo-only promotion trigger is replaced by one for every format: a promotion job's state change updates its
-- provenance row and, when the job reaches completed or failed, emits artifact.promoted or promotion.failed. A job
-- without a provenance row emits nothing; every promotion is requested through the PromotionService, which writes the row
-- with the job. The version of the dedupe key is the provenance row id, so a retried attempt emits its own outcome.
DROP TRIGGER cargo_promotion_state_webhook_after_update ON public.lifecycle_jobs;
DROP FUNCTION public.cargo_promotion_state_webhook();

-- +goose StatementBegin
CREATE FUNCTION public.promotion_job_state_change() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    promotion public.artifact_promotions%ROWTYPE;
    next_state text;
    event_type text;
BEGIN
    SELECT * INTO promotion FROM public.artifact_promotions WHERE job_id = NEW.id ORDER BY attempt DESC LIMIT 1;
    IF NOT FOUND THEN
        RETURN NEW;
    END IF;
    next_state := CASE NEW.state
        WHEN 'running' THEN 'running'
        WHEN 'completed' THEN 'completed'
        WHEN 'failed' THEN 'failed'
        WHEN 'cancelled' THEN 'cancelled'
        ELSE 'queued'
    END;
    UPDATE public.artifact_promotions
       SET state = next_state,
           completed_at = CASE WHEN next_state IN ('completed', 'failed', 'cancelled') THEN now() ELSE NULL END
     WHERE id = promotion.id;
    IF next_state IN ('completed', 'failed') THEN
        event_type := CASE WHEN next_state = 'completed' THEN 'artifact.promoted' ELSE 'promotion.failed' END;
        PERFORM public.emit_domain_event(gen_random_uuid(), event_type, promotion.target_repository_id, promotion.format,
            promotion.coordinate, promotion.digest,
            jsonb_build_object(
                'operationId', NEW.id, 'promotionId', promotion.id, 'kind', 'promotion', 'format', promotion.format,
                'sourceRepositoryId', NEW.payload->>'sourceRepositoryId',
                'targetRepositoryId', promotion.target_repository_id,
                'coordinate', promotion.coordinate, 'digest', promotion.digest,
                'state', next_state, 'attempts', NEW.attempts, 'attempt', promotion.attempt
            ),
            event_type || '|' || promotion.target_repository_id || '|' || promotion.coordinate || '|' || promotion.digest || '|' || promotion.id,
            false);
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER lifecycle_jobs_promotion_state AFTER UPDATE OF state ON public.lifecycle_jobs FOR EACH ROW WHEN (((new.kind = 'promotion'::text) AND (old.state IS DISTINCT FROM new.state))) EXECUTE FUNCTION public.promotion_job_state_change();
