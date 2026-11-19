-- +goose Up

-- ADR 0018 slice 2: the identity provider registry replaces the oidc_settings singleton.
-- Everything here is an ordinary numbered migration; the baseline is not edited.
--
-- identity_providers holds one row per human IdP (OIDC now, SAML in slice 4). The kind
-- specific configuration lives in settings (validated in Go); the OIDC client secret is
-- sealed with internal/secrets into secrets_sealed. user_identities is generalized to
-- (provider_id, subject) with a separate scim_external_id, user_sessions gains the saml
-- kind and a nullable idp_session_id (OIDC sid / SAML SessionIndex, unused in v1), and
-- users gains disabled_reason for the SCIM deactivate rules.
--
-- Upgrade path: the oidc_settings row becomes the provider with slug 'default', keeping
-- its sealed secret and version. A stored identity whose issuer differs from that
-- provider (the issuer was changed after the identity was linked) keeps working: it is
-- bound to a disabled provider created for that issuer, so no link is dropped and no
-- (provider_id, user_id) pair collides. The old jit_default_role 'admin' becomes 'member'
-- because an IdP-driven first login must never mint an administrator by default.
--
-- MySQL (ADR 0022): there is no migrations/mysql directory yet. The twin maps uuid to
-- CHAR(36), jsonb to JSON, the partial unique indexes to unique indexes on generated
-- columns, and gen_random_uuid() to UUID().

CREATE TABLE public.identity_providers (
    id uuid PRIMARY KEY,
    slug text NOT NULL,
    kind text NOT NULL,
    display_name text NOT NULL,
    enabled boolean NOT NULL DEFAULT false,
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    secrets_sealed text,
    attribute_mapping jsonb NOT NULL DEFAULT '{}'::jsonb,
    provisioning_mode text NOT NULL DEFAULT 'disabled',
    email_linking_enabled boolean NOT NULL DEFAULT false,
    jit_default_role text NOT NULL DEFAULT 'none',
    version bigint NOT NULL DEFAULT 1,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT identity_providers_slug_key UNIQUE (slug),
    CONSTRAINT identity_providers_slug_check CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
    CONSTRAINT identity_providers_kind_check CHECK (kind IN ('oidc', 'saml')),
    CONSTRAINT identity_providers_display_name_check CHECK (length(btrim(display_name)) BETWEEN 1 AND 120),
    CONSTRAINT identity_providers_settings_check CHECK (jsonb_typeof(settings) = 'object'),
    CONSTRAINT identity_providers_attribute_mapping_check CHECK (jsonb_typeof(attribute_mapping) = 'object'),
    CONSTRAINT identity_providers_provisioning_mode_check CHECK (provisioning_mode IN ('disabled', 'jit', 'scim_only')),
    CONSTRAINT identity_providers_jit_default_role_check CHECK (jit_default_role IN ('none', 'member')),
    CONSTRAINT identity_providers_version_check CHECK (version > 0)
);

-- The singleton becomes the 'default' provider.
INSERT INTO public.identity_providers (id, slug, kind, display_name, enabled, settings, secrets_sealed,
        attribute_mapping, provisioning_mode, email_linking_enabled, jit_default_role, version, updated_at)
SELECT gen_random_uuid(), 'default', 'oidc', 'Single sign-on', enabled,
       jsonb_build_object('issuer', rtrim(btrim(issuer), '/'), 'audience', audience, 'jwksUrl', jwks_url, 'clientId', client_id,
                          'redirectUrl', redirect_url, 'scopes', scopes, 'adminSubjects', admin_subjects,
                          'memberRoles', member_roles, 'adminRoles', admin_roles, 'algorithms', '["RS256"]'::jsonb),
       NULLIF(client_secret, ''), '{}'::jsonb, provisioning_mode, email_linking_enabled,
       CASE jit_default_role WHEN 'admin' THEN 'member' ELSE jit_default_role END, version, updated_at
  FROM public.oidc_settings;

-- Issuers of stored identities that no provider covers yet. When there is no singleton
-- (an environment-only deployment), the most recently used issuer becomes 'default' so the
-- environment seed can complete it; the others get a stable generated slug.
INSERT INTO public.identity_providers (id, slug, kind, display_name, enabled, settings)
SELECT gen_random_uuid(),
       CASE WHEN issuers.rank = 1 AND NOT EXISTS (SELECT 1 FROM public.identity_providers WHERE slug = 'default')
            THEN 'default' ELSE 'issuer-' || left(md5(issuers.issuer), 12) END,
       'oidc', left(issuers.issuer, 120), false, jsonb_build_object('issuer', issuers.issuer)
  FROM (SELECT issuer, row_number() OVER (ORDER BY max(COALESCE(last_login_at, created_at)) DESC, issuer) AS rank
          FROM public.user_identities
         WHERE kind = 'oidc'
           AND NOT EXISTS (SELECT 1 FROM public.identity_providers AS provider WHERE rtrim(btrim(provider.settings->>'issuer'), '/') = rtrim(btrim(user_identities.issuer), '/'))
         GROUP BY issuer) AS issuers;

ALTER TABLE public.user_identities
    ADD COLUMN provider_id uuid,
    ADD COLUMN scim_external_id text;

-- Identities of the singleton's issuer, or of a generated provider, bind by issuer; any
-- remaining identity (the issuer matches no provider) falls to 'default'.
UPDATE public.user_identities AS identity
   SET provider_id = COALESCE(
       (SELECT provider.id FROM public.identity_providers AS provider WHERE rtrim(btrim(provider.settings->>'issuer'), '/') = rtrim(btrim(identity.issuer), '/') ORDER BY provider.slug LIMIT 1),
       (SELECT provider.id FROM public.identity_providers AS provider WHERE provider.slug = 'default'));

ALTER TABLE public.user_identities
    ALTER COLUMN provider_id SET NOT NULL,
    ALTER COLUMN subject DROP NOT NULL,
    DROP CONSTRAINT user_identities_kind_issuer_subject_key,
    DROP CONSTRAINT user_identities_user_id_kind_issuer_key,
    DROP CONSTRAINT user_identities_kind_check,
    DROP CONSTRAINT user_identities_subject_check,
    ADD CONSTRAINT user_identities_kind_check CHECK (kind IN ('oidc', 'saml')),
    ADD CONSTRAINT user_identities_subject_check CHECK (subject IS NULL OR subject <> ''),
    ADD CONSTRAINT user_identities_scim_external_id_check CHECK (scim_external_id IS NULL OR scim_external_id <> ''),
    ADD CONSTRAINT user_identities_key_check CHECK (subject IS NOT NULL OR scim_external_id IS NOT NULL),
    ADD CONSTRAINT user_identities_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.identity_providers(id),
    ADD CONSTRAINT user_identities_provider_user_key UNIQUE (provider_id, user_id);

CREATE UNIQUE INDEX user_identities_provider_subject_idx ON public.user_identities USING btree (provider_id, subject)
    WHERE subject IS NOT NULL;
CREATE UNIQUE INDEX user_identities_provider_scim_external_id_idx ON public.user_identities USING btree (provider_id, scim_external_id)
    WHERE scim_external_id IS NOT NULL;

ALTER TABLE public.user_sessions DROP CONSTRAINT user_sessions_kind_check;
ALTER TABLE public.user_sessions ADD CONSTRAINT user_sessions_kind_check CHECK (kind IN ('local_session', 'oidc', 'saml'));
ALTER TABLE public.user_sessions ADD COLUMN idp_session_id text;

ALTER TABLE public.users ADD COLUMN disabled_reason text;
ALTER TABLE public.users ADD CONSTRAINT users_disabled_reason_check CHECK (disabled_reason IS NULL OR disabled_reason IN ('admin', 'scim'));

DROP TABLE public.oidc_settings;
