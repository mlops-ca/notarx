-- +goose Up

-- ADR 0021 (slice 3c), Evaluator, with ADR 0023 sections 2, 5, 6 and 7: promotion_evaluations is the queue of rule
-- evaluations. events.Emit inserts one row per enabled rule that matches a published artifact in the transaction of the
-- publication, so a missed event cannot happen silently; a worker claims the rows, re-checks the match and the
-- conditions, and either skips with a reason, waits, or asks the PromotionService for the promotion.
--
-- domain_events is partitioned and expiring, so the row names its event by (event_seq, event_occurred_at) and carries no
-- foreign key to it. UNIQUE(event_seq, rule_id) makes a repeated delivery of one event insert one evaluation per rule.
-- The rule is a foreign key with cascade: deleting a rule deletes its unfinished evaluations.
--
-- States: pending and waiting rows are claimable (next_attempt_at is the due time); a claim moves next_attempt_at to the
-- lease expiry and sets the lease, so a worker that dies leaves a row that becomes due again by itself (lease recovery
-- needs no scheduler). queued means the promotion was requested, skipped and failed are final. The three final states
-- are terminal for the RetentionJanitor, which prunes them after 7 days (1 day on the free tier).
--
-- Indexes (ADR 0023 section 5): the claim index (next_attempt_at) over pending and waiting rows; (repository_id, digest)
-- over waiting rows, which the scan-completed re-pend of slice 4 uses; rule_id for the cascade. The table churns on every
-- state change, so fillfactor is 80 and autovacuum runs at 2 percent. On MySQL the partial indexes are generated-column
-- indexes (migrations/mysql-decisions.yaml).
CREATE TABLE public.promotion_evaluations (
    id uuid NOT NULL,
    event_seq bigint NOT NULL,
    event_occurred_at timestamp with time zone NOT NULL,
    rule_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    coordinate text NOT NULL,
    digest text NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    lease_owner text,
    lease_token uuid,
    lease_expires_at timestamp with time zone,
    last_reason text DEFAULT ''::text NOT NULL,
    promotion_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT promotion_evaluations_pkey PRIMARY KEY (id),
    CONSTRAINT promotion_evaluations_event_seq_rule_id_key UNIQUE (event_seq, rule_id),
    CONSTRAINT promotion_evaluations_rule_id_fkey FOREIGN KEY (rule_id) REFERENCES public.promotion_rules(id) ON DELETE CASCADE,
    CONSTRAINT promotion_evaluations_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'waiting'::text, 'queued'::text, 'skipped'::text, 'failed'::text]))),
    CONSTRAINT promotion_evaluations_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT promotion_evaluations_lease_owner_check CHECK (((lease_owner IS NULL) = (lease_token IS NULL))),
    CONSTRAINT promotion_evaluations_lease_expiry_check CHECK (((lease_token IS NULL) = (lease_expires_at IS NULL))),
    CONSTRAINT promotion_evaluations_last_reason_check CHECK ((length(last_reason) <= 1024))
)
WITH (fillfactor='80', autovacuum_vacuum_scale_factor='0.02', autovacuum_analyze_scale_factor='0.02');

CREATE INDEX promotion_evaluations_claim_idx ON public.promotion_evaluations USING btree (next_attempt_at) WHERE (state = ANY (ARRAY['pending'::text, 'waiting'::text]));
CREATE INDEX promotion_evaluations_waiting_idx ON public.promotion_evaluations USING btree (repository_id, digest) WHERE (state = 'waiting'::text);
CREATE INDEX promotion_evaluations_rule_idx ON public.promotion_evaluations USING btree (rule_id);
CREATE INDEX promotion_evaluations_retention_idx ON public.promotion_evaluations USING btree (updated_at) WHERE (state = ANY (ARRAY['queued'::text, 'skipped'::text, 'failed'::text]));

-- A new evaluation wakes the worker through its own channel. The quota evaluator already owns notarx_evaluations, so the
-- promotion evaluator listens on notarx_promotion_evaluations.
CREATE TRIGGER promotion_evaluations_notify_insert AFTER INSERT ON public.promotion_evaluations FOR EACH ROW WHEN ((new.state = 'pending'::text)) EXECUTE FUNCTION public.notarx_notify_queue('notarx_promotion_evaluations');

-- A rule is parked when its run-as service account loses the authority to run it (ADR 0021, Rules): authorization_lost,
-- with the reason the evaluator recorded. paused is the state of a rule whose target became public (ADR 0029). Only an
-- active, enabled rule fans out; an edit of a rule whose account is valid again returns authorization_lost to active.
ALTER TABLE public.promotion_rules
    ADD COLUMN state text DEFAULT 'active'::text NOT NULL,
    ADD COLUMN state_reason text DEFAULT ''::text NOT NULL,
    ADD CONSTRAINT promotion_rules_state_check CHECK ((state = ANY (ARRAY['active'::text, 'authorization_lost'::text, 'paused'::text]))),
    ADD CONSTRAINT promotion_rules_state_reason_check CHECK ((length(state_reason) <= 1024));

-- events.Emit consults the enabled rules of the source repository through an in-process cache (ADR 0023 section 6); a
-- change to any rule flushes the scope on every node, and NOTARX_AUTHZ_CACHE_TTL bounds a lost notification.
CREATE TRIGGER promotion_rules_notify_cache_insert AFTER INSERT ON public.promotion_rules REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_scope('promotion_rule');
CREATE TRIGGER promotion_rules_notify_cache_update AFTER UPDATE ON public.promotion_rules REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_scope('promotion_rule');
CREATE TRIGGER promotion_rules_notify_cache_delete AFTER DELETE ON public.promotion_rules REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_scope('promotion_rule');
