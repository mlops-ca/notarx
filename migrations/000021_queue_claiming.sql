-- +goose Up

-- ADR 0023 section 7 (slice 13): queue claiming rework.
--
-- Wake-ups move from one shared channel (notarx_lifecycle_jobs, which every worker runtime of a node woke
-- on) to one channel per job kind (notarx_jobs_<kind>) and one for webhook deliveries (notarx_deliveries),
-- so a node wakes only the runtimes whose kind it enables. The lifecycle triggers of the baseline are
-- re-pointed at the per-kind function. The per-repository concurrency probe of the claim reads only the
-- running set, so it gets a partial index of the size of the worker count.
--
-- MySQL (ADR 0022): there is no migrations/mysql directory yet. The twin has no push notifications (the
-- idle poll of ADR 0022 is the wake-up) and plays the probe through the lock-bucket table in the same
-- transaction position; the partial index becomes a plain (state, repository_id) index.

-- +goose StatementBegin
CREATE FUNCTION public.notarx_notify_job_kind() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM pg_notify('notarx_jobs_' || NEW.kind, COALESCE(NEW.id::text, 'wake'));
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER lifecycle_jobs_notify_insert ON public.lifecycle_jobs;
DROP TRIGGER lifecycle_jobs_notify_update ON public.lifecycle_jobs;

CREATE TRIGGER lifecycle_jobs_notify_insert AFTER INSERT ON public.lifecycle_jobs FOR EACH ROW WHEN ((new.state = ANY (ARRAY['pending'::text, 'retrying'::text]))) EXECUTE FUNCTION public.notarx_notify_job_kind();
CREATE TRIGGER lifecycle_jobs_notify_update AFTER UPDATE OF state, next_attempt_at ON public.lifecycle_jobs FOR EACH ROW WHEN (((new.state = ANY (ARRAY['pending'::text, 'retrying'::text])) AND (old.state IS DISTINCT FROM new.state))) EXECUTE FUNCTION public.notarx_notify_job_kind();

CREATE TRIGGER webhook_deliveries_notify_insert AFTER INSERT ON public.webhook_deliveries FOR EACH ROW WHEN ((new.state = 'pending'::text)) EXECUTE FUNCTION public.notarx_notify_queue('notarx_deliveries');

CREATE INDEX lifecycle_jobs_running_repository_idx ON public.lifecycle_jobs USING btree (repository_id) WHERE (state = 'running'::text);
