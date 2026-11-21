-- +goose Up

-- ADR 0018 slice 6, Decision 4: SCIM bearer tokens. A token belongs to exactly one identity provider and acts only for
-- it; the users and groups it creates carry that provider_id. The plaintext is shown once at creation and never
-- stored: secret_hash is the digest of the same function that hashes api_keys.secret_hash, and the unique constraint
-- is the lookup index of every SCIM request. expires_at is mandatory; the 365 day cap is enforced by the store and
-- the management API (a constraint comparing two columns would not port to MySQL, ADR 0022).
--
-- Deleting the provider deletes its tokens (ON DELETE CASCADE): they authorize nothing without it.
CREATE TABLE public.identity_provider_scim_tokens (
    id uuid NOT NULL,
    provider_id uuid NOT NULL,
    name text NOT NULL,
    secret_hash text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone,
    CONSTRAINT identity_provider_scim_tokens_pkey PRIMARY KEY (id),
    CONSTRAINT identity_provider_scim_tokens_secret_hash_key UNIQUE (secret_hash),
    CONSTRAINT identity_provider_scim_tokens_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.identity_providers(id) ON DELETE CASCADE,
    CONSTRAINT identity_provider_scim_tokens_name_check CHECK (((length(btrim(name)) >= 1) AND (length(btrim(name)) <= 128)))
);

-- The tokens of one provider, listed by creation order; also serves the foreign key.
CREATE INDEX identity_provider_scim_tokens_provider_idx ON public.identity_provider_scim_tokens USING btree (provider_id, id);

-- SCIM lists (ADR 0018 section 4) page by keyset on (created_at, id) within one provider, so each page reads one
-- index range instead of sorting the provider's rows; no OFFSET and no unbounded COUNT(*) (ADR 0023 section 8). The
-- users of a provider are the rows of its user_identities, the groups the rows of user_groups with its provider_id.
CREATE INDEX user_identities_provider_created_idx ON public.user_identities USING btree (provider_id, created_at, id);
CREATE INDEX user_groups_provider_created_idx ON public.user_groups USING btree (provider_id, created_at, id);
