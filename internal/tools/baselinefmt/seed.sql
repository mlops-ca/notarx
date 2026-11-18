
-- seed: singleton rows the code expects to exist (ADR 0015, step 5).
--
-- Only rows of an empty installation are seeded. Backfills of pre-existing rows
-- (native OCI tombstones, repository grants, cargo webhook ids) and the
-- skipped_existing branch of the presets seed do not apply to a fresh database.
-- Data backfills belong in later numbered migrations, never in this baseline.

-- Originating migration: 000049_audit_retention.sql
INSERT INTO public.audit_retention_policy (singleton) VALUES (true) ON CONFLICT DO NOTHING;

-- Originating migration: 000055_anonymous_access_policy.sql
-- Anonymous read stays disabled: enabled defaults to false.
INSERT INTO public.anonymous_access_policy (singleton) VALUES (true) ON CONFLICT DO NOTHING;

-- Originating migration: 000107_site_settings.sql
INSERT INTO public.site_settings (singleton) VALUES (true) ON CONFLICT (singleton) DO NOTHING;

-- Originating migration: 000174_installation_repository_presets.sql
-- A fresh installation has no repositories, so the catalog is pending.
INSERT INTO public.installation_repository_presets (singleton, catalog_version, state)
VALUES (true, 'standard-v1', 'pending')
ON CONFLICT (singleton) DO NOTHING;
