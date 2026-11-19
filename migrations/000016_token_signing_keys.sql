-- +goose Up

-- ADR 0020 slice 2: OIDC issuer signing keys and the stable deployment id. The baseline is
-- not edited; this is an ordinary numbered migration.
--
-- token_signing_keys holds the keys notarx signs assertions with. Private keys are sealed
-- with the secrets key-ring (purpose token-signing-key:<kid>), and integrity_mac stops
-- an attacker with database write access alone from planting a trusted public key. The
-- partial unique indexes allow at most one active and one next key per algorithm.
--
-- settings is a small key/value table for deployment wide facts. deployment_id is a random
-- UUID created here, once, and shared by every node (the per-node instance id differs per
-- node and must not name the instance principal). issuer_url records the last issuer URL
-- in use so a change can be warned about at startup.
--
-- MySQL (ADR 0022): there is no migrations/mysql directory yet. The twin maps jsonb to JSON,
-- timestamptz to DATETIME(6) and the two partial unique indexes to generated columns that are
-- NULL unless state is active or next, with a unique key on (alg, generated column).

CREATE TABLE public.token_signing_keys (
    kid text PRIMARY KEY,
    alg text NOT NULL,
    state text NOT NULL,
    public_jwk jsonb NOT NULL,
    private_sealed text NOT NULL,
    sealing_ref text NOT NULL DEFAULT 'local',
    integrity_mac text NOT NULL,
    created_at timestamp with time zone NOT NULL,
    published_at timestamp with time zone,
    activated_at timestamp with time zone,
    retired_at timestamp with time zone,
    expires_at timestamp with time zone,
    CONSTRAINT token_signing_keys_alg_check CHECK (alg IN ('RS256', 'ES256')),
    CONSTRAINT token_signing_keys_state_check CHECK (state IN ('next', 'active', 'retired')),
    CONSTRAINT token_signing_keys_public_jwk_check CHECK (jsonb_typeof(public_jwk) = 'object')
);

CREATE UNIQUE INDEX token_signing_keys_one_active_idx ON public.token_signing_keys USING btree (alg) WHERE state = 'active';
CREATE UNIQUE INDEX token_signing_keys_one_next_idx ON public.token_signing_keys USING btree (alg) WHERE state = 'next';

CREATE TABLE IF NOT EXISTS public.settings (
    key text PRIMARY KEY,
    value text NOT NULL,
    updated_at timestamp with time zone NOT NULL DEFAULT now()
);

INSERT INTO public.settings (key, value) VALUES ('deployment_id', gen_random_uuid()::text) ON CONFLICT (key) DO NOTHING;
