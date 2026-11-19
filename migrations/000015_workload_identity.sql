-- +goose Up

-- ADR 0019 slice 2: workload identity federation tables (trusted issuers, trust policies
-- and single-use jti tracking). The baseline is not edited; this is an ordinary numbered
-- migration.
--
-- A policy confers no grants: many policies map to one service account, and the service
-- account owns the grants. workload_jti_seen is a plain table (a global unique key rules
-- out partitioning): fillfactor 80 and tighter autovacuum thresholds, insert-only
-- ON CONFLICT DO NOTHING, and the ADR 0023 retention janitor deletes expired rows in
-- throttled batches (internal/retention, no bespoke sweep).
--
-- MySQL (ADR 0022): there is no migrations/mysql directory yet. The twin maps uuid to
-- BINARY(16) or CHAR(36), the TEXT[] columns to JSON with a CHECK (JSON_LENGTH(audiences) > 0),
-- jsonb to JSON, bytea to VARBINARY(32), and drops the WITH clause.

CREATE TABLE public.workload_issuers (
    id uuid PRIMARY KEY,
    name text NOT NULL,
    kind text NOT NULL,
    issuer text NOT NULL,
    audiences text[] NOT NULL,
    allowed_algs text[] NOT NULL DEFAULT '{RS256,ES256,EdDSA}',
    jwks_mode text NOT NULL,
    jwks_uri text,
    static_jwks jsonb,
    ca_bundle_pem text,
    allow_private_network boolean NOT NULL DEFAULT false,
    state text NOT NULL DEFAULT 'active',
    version bigint NOT NULL DEFAULT 1,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT workload_issuers_name_key UNIQUE (name),
    CONSTRAINT workload_issuers_issuer_key UNIQUE (issuer),
    CONSTRAINT workload_issuers_kind_check CHECK (kind IN ('github_actions', 'gitlab_ci', 'kubernetes', 'generic')),
    CONSTRAINT workload_issuers_audiences_check CHECK (cardinality(audiences) > 0),
    CONSTRAINT workload_issuers_allowed_algs_check CHECK (cardinality(allowed_algs) > 0 AND allowed_algs <@ ARRAY['RS256', 'ES256', 'EdDSA']),
    CONSTRAINT workload_issuers_jwks_mode_check CHECK (jwks_mode IN ('discovery', 'static')),
    CONSTRAINT workload_issuers_static_jwks_check CHECK (jwks_mode <> 'static' OR static_jwks IS NOT NULL),
    CONSTRAINT workload_issuers_state_check CHECK (state IN ('active', 'disabled'))
);

CREATE TABLE public.workload_trust_policies (
    id uuid PRIMARY KEY,
    name text NOT NULL,
    issuer_id uuid NOT NULL,
    service_account_id uuid NOT NULL,
    conditions jsonb NOT NULL,
    max_token_ttl_seconds integer NOT NULL DEFAULT 900,
    single_use boolean NOT NULL DEFAULT false,
    state text NOT NULL DEFAULT 'active',
    version bigint NOT NULL DEFAULT 1,
    expires_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT workload_trust_policies_name_key UNIQUE (name),
    CONSTRAINT workload_trust_policies_issuer_id_fkey FOREIGN KEY (issuer_id) REFERENCES public.workload_issuers(id),
    CONSTRAINT workload_trust_policies_service_account_id_fkey FOREIGN KEY (service_account_id) REFERENCES public.service_accounts(id),
    CONSTRAINT workload_trust_policies_conditions_check CHECK (jsonb_typeof(conditions) = 'array'),
    CONSTRAINT workload_trust_policies_ttl_check CHECK (max_token_ttl_seconds > 0),
    CONSTRAINT workload_trust_policies_state_check CHECK (state IN ('active', 'disabled'))
);

-- The authenticator lists the active policies of one issuer; the service account index
-- serves the foreign key and "which policies map to this account". Both indexes lead with
-- their foreign key column (a partial index would not satisfy the schema index test).
CREATE INDEX workload_trust_policies_issuer_idx ON public.workload_trust_policies USING btree (issuer_id, state);
CREATE INDEX workload_trust_policies_service_account_idx ON public.workload_trust_policies USING btree (service_account_id);

-- No foreign key to workload_issuers: the table is insert-hot and rows are short-lived.
CREATE TABLE public.workload_jti_seen (
    issuer_id uuid NOT NULL,
    jti_hash bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    PRIMARY KEY (issuer_id, jti_hash)
) WITH (fillfactor = 80, autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.02);

CREATE INDEX workload_jti_seen_expires_at_idx ON public.workload_jti_seen USING btree (expires_at);
