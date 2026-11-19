-- +goose Up

-- Extensions (ADR 0015, "Generation procedure", step 4).
--
-- pg_trgm is required: 12 GIN indexes below use gin_trgm_ops. It is a trusted
-- extension from PostgreSQL 13, so a migration role with CREATE on the database
-- can install it. The runner and `make preflight` check this before the first
-- statement; there is no fallback without it.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- pg_stat_statements is optional: no code reads it. It is created in a guarded
-- block so a server without it, or a role that may not create it, still
-- migrates. Operators who want statement statistics set
-- shared_preload_libraries themselves.
DO $$
BEGIN
    CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'pg_stat_statements is unavailable (%); continuing without it', SQLERRM;
END
$$;

--
-- Name: touch(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.touch() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;

--
-- Name: items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.items (
    id uuid NOT NULL,
    name text NOT NULL
);

--
-- Name: items items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.items
    ADD CONSTRAINT items_pkey PRIMARY KEY (id);

--
-- Name: items_name_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX items_name_trgm_idx ON public.items USING gin (name public.gin_trgm_ops);


-- seed: singleton rows the code expects to exist (ADR 0015, step 5).
--
-- Only rows of an empty installation are seeded. Backfills of pre-existing rows
-- (native OCI tombstones, repository grants, cargo webhook ids, visibility,
-- identity providers, usage counters and rollups) and the
-- skipped_existing branch of the presets seed do not apply to a fresh database.
-- Data backfills belong in later numbered migrations, never in this baseline.

-- Originating migration: 000049_audit_retention.sql (defaults raised by 000008_retention_janitor.sql: enabled, 90 days)
INSERT INTO public.audit_retention_policy (singleton) VALUES (true) ON CONFLICT DO NOTHING;

-- Originating migration: 000055_ (the instance public-access switch, renamed by 000006_repository_visibility.sql)
-- Public access stays disabled: enabled defaults to false.
INSERT INTO public.public_access_policy (singleton) VALUES (true) ON CONFLICT DO NOTHING;

-- Originating migration: 000107_site_settings.sql (notarx defaults by 000013_brand_defaults.sql)
INSERT INTO public.site_settings (singleton) VALUES (true) ON CONFLICT (singleton) DO NOTHING;

-- Originating migration: 000174_installation_repository_presets.sql
-- A fresh installation has no repositories, so the catalog is pending.
INSERT INTO public.installation_repository_presets (singleton, catalog_version, state)
VALUES (true, 'standard-v1', 'pending')
ON CONFLICT (singleton) DO NOTHING;

-- Originating migration: 000016_token_signing_keys.sql
-- The deployment identifier is generated once per installation.
INSERT INTO public.settings (key, value) VALUES ('deployment_id', gen_random_uuid()::text) ON CONFLICT (key) DO NOTHING;
