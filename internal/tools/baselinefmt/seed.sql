
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
