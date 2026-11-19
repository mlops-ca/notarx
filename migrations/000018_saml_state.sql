-- +goose Up

-- ADR 0018 slice 4 (SAML SP login) with the ADR 0023 amendment: single-use AuthnRequest
-- state and assertion replay tracking. Both are plain tables (the global unique key on
-- (provider_id, assertion_id) rules out partitioning), insert-hot and short-lived:
-- fillfactor 80 and tighter autovacuum thresholds, a btree index on expires_at for the
-- retention janitor's batched deletes (internal/retention, no bespoke sweep), and no
-- foreign key to identity_providers (rows expire within minutes; a deleted provider's
-- rows are pruned by the janitor).
--
-- MySQL (ADR 0022): there is no migrations/mysql directory yet. The twin maps uuid to
-- BINARY(16) or CHAR(36) and drops the WITH clause.

CREATE TABLE public.saml_request_state (
    id uuid PRIMARY KEY,
    provider_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL
) WITH (fillfactor = 80, autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.02);

CREATE INDEX saml_request_state_expires_at_idx ON public.saml_request_state USING btree (expires_at);

CREATE TABLE public.saml_assertion_ids (
    provider_id uuid NOT NULL,
    assertion_id text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    PRIMARY KEY (provider_id, assertion_id),
    CONSTRAINT saml_assertion_ids_assertion_id_check CHECK (length(assertion_id) BETWEEN 1 AND 256)
) WITH (fillfactor = 80, autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.02);

CREATE INDEX saml_assertion_ids_expires_at_idx ON public.saml_assertion_ids USING btree (expires_at);
