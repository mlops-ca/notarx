-- +goose Up

-- ADR 0021, Provenance and approval: a promotion row is one attempt. When a failed or cancelled promotion job is queued
-- again (an operator retry), the failed row stays as it was, with its outcome and its events, and a new row records the
-- retry: the same key, job and request, the next attempt number, state queued. The attempt is one more than the newest
-- attempt under the key, because a rule retry that was requested again holds its own rows under the same key. The
-- provenance lookup by job reads the newest attempt, so the retried job's state follows the new row.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.promotion_job_state_change() RETURNS trigger
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
    IF promotion.state IN ('failed', 'cancelled') AND next_state = 'queued' THEN
        INSERT INTO public.artifact_promotions (id,target_repository_id,format,coordinate,digest,origin_instance_id,origin_repository,rule_id,rule_version,chain_path,requested_by,state,attempt,idempotency_key,job_id,approvals)
        SELECT gen_random_uuid(),target_repository_id,format,coordinate,digest,origin_instance_id,origin_repository,rule_id,rule_version,chain_path,requested_by,'queued',
               (SELECT max(a.attempt) FROM public.artifact_promotions a WHERE a.idempotency_key = promotion.idempotency_key) + 1,idempotency_key,job_id,approvals
          FROM public.artifact_promotions WHERE id = promotion.id;
        RETURN NEW;
    END IF;
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
