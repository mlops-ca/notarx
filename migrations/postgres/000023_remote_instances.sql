-- +goose Up

-- ADR 0021 (slice 7a), "Remote targets": the registry of peer notarx instances a promotion rule can push to,
-- and the id of this deployment.
--
-- remote_instances.token_sealed holds a static token sealed by internal/secrets (stored as TEXT, NULL for a
-- workload_identity instance, the check ties the column to the authentication kind); it is write-only in the
-- API. instance_id is the id the remote reported at registration; a remote may not report this deployment's id
-- (checked by the API before a row is written). The table does not reference artifact_promotions or any other
-- promotion table; the rule table of a later slice references this one.
--
-- instance_identity is a singleton: the id of this deployment, a UUIDv7 inserted by the first node that needs it
-- (INSERT ... ON CONFLICT DO NOTHING, then read) and shared by every node. GET /.well-known/notarx serves it.
--
-- remote_instances_name_bytes_idx serves the keyset listing by name in byte order (COLLATE "C"), like
-- webhook_subscriptions (migration 000005). Both tables are tiny configuration tables: no fillfactor or autovacuum
-- tuning (ADR 0023).
--
-- MySQL (ADR 0022): the twin ships with the generated baseline; the catalogue carries both tables.
CREATE TABLE public.remote_instances (
    id uuid NOT NULL,
    name text NOT NULL,
    base_url text NOT NULL,
    instance_id uuid NOT NULL,
    auth_kind text NOT NULL,
    audience text DEFAULT ''::text NOT NULL,
    token_sealed text,
    trust_evidence boolean DEFAULT false NOT NULL,
    state text DEFAULT 'active'::text NOT NULL,
    last_checked_at timestamp with time zone,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT remote_instances_pkey PRIMARY KEY (id),
    CONSTRAINT remote_instances_name_key UNIQUE (name),
    CONSTRAINT remote_instances_name_check CHECK (((length(name) >= 1) AND (length(name) <= 128))),
    CONSTRAINT remote_instances_base_url_check CHECK (((length(base_url) >= 1) AND (length(base_url) <= 2048))),
    CONSTRAINT remote_instances_auth_kind_check CHECK ((auth_kind = ANY (ARRAY['workload_identity'::text, 'static_token'::text]))),
    CONSTRAINT remote_instances_audience_check CHECK (((length(audience) <= 512) AND ((auth_kind <> 'workload_identity'::text) OR (audience <> ''::text)))),
    CONSTRAINT remote_instances_token_check CHECK ((((auth_kind = 'static_token'::text) AND (token_sealed IS NOT NULL) AND (token_sealed <> ''::text)) OR ((auth_kind = 'workload_identity'::text) AND (token_sealed IS NULL)))),
    CONSTRAINT remote_instances_state_check CHECK ((state = ANY (ARRAY['active'::text, 'unreachable'::text, 'disabled'::text]))),
    CONSTRAINT remote_instances_version_check CHECK ((version >= 1))
);

CREATE INDEX remote_instances_name_bytes_idx ON public.remote_instances USING btree (name COLLATE "C");

CREATE TABLE public.instance_identity (
    singleton boolean DEFAULT true NOT NULL,
    instance_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT instance_identity_pkey PRIMARY KEY (singleton),
    CONSTRAINT instance_identity_singleton_check CHECK (singleton)
);
