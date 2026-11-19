-- +goose Up

-- ADR 0018 Decision 3 follow-up: a SAML login may link an existing account by email only when an
-- administrator has verified that email (a SAML assertion never carries a provider-verified email).
-- The flag and its audit trail live on the account row: who verified the address and when. Changing the
-- address clears the verification (enforced in the store), and the check below keeps the three columns
-- consistent: a verified account always names the verifier and the time, an unverified one carries neither.
--
-- MySQL (ADR 0022): there is no migrations/mysql directory yet. The twin maps boolean to TINYINT(1) and
-- timestamptz to DATETIME(6).

ALTER TABLE public.users
    ADD COLUMN email_verified_by_admin boolean NOT NULL DEFAULT false,
    ADD COLUMN email_verified_at timestamp with time zone,
    ADD COLUMN email_verified_by text NOT NULL DEFAULT '',
    ADD CONSTRAINT users_email_verified_audit_check CHECK (
        (email_verified_by_admin AND email_verified_at IS NOT NULL AND email_verified_by <> '' AND email <> '')
        OR (NOT email_verified_by_admin AND email_verified_at IS NULL AND email_verified_by = '')
    );
