-- +goose Up

-- ADR 0020 slice 4: outbound assertion minting for service accounts. A service account may mint a short-lived JWT
-- only for an audience on its own allow-list; there is no wildcard and no default allow-list. The subject the
-- assertion carries is stored on the account when it is created and never changes (a rename does not touch it),
-- because an Entra federated credential matches iss, sub and aud by exact string.
--
-- Every key column is bounded so the tables port to MySQL (ADR 0022): no TEXT in a key, audiences and subjects are
-- at most 255 characters, enforced by a check constraint and in Go.
CREATE TABLE public.service_account_assertion_audiences (
    service_account_id uuid NOT NULL,
    audience character varying(255) NOT NULL,
    max_lifetime_seconds integer DEFAULT 600 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT service_account_assertion_audiences_pkey PRIMARY KEY (service_account_id, audience),
    CONSTRAINT service_account_assertion_audiences_service_account_id_fkey FOREIGN KEY (service_account_id) REFERENCES public.service_accounts(id) ON DELETE CASCADE,
    CONSTRAINT service_account_assertion_audiences_audience_check CHECK ((length(btrim((audience)::text)) >= 1)),
    CONSTRAINT service_account_assertion_audiences_lifetime_check CHECK (((max_lifetime_seconds >= 60) AND (max_lifetime_seconds <= 600)))
);

ALTER TABLE public.service_accounts ADD COLUMN assertion_subject character varying(255);

-- Subjects are reserved for good. Deleting an issuer-enabled account keeps its row (the foreign key nulls the owner),
-- so a later account of the same name cannot take the subject over and inherit the Entra trust built on it; only an
-- explicit administrator release removes the row.
CREATE TABLE public.service_account_assertion_subjects (
    subject character varying(255) NOT NULL,
    service_account_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT service_account_assertion_subjects_pkey PRIMARY KEY (subject),
    CONSTRAINT service_account_assertion_subjects_service_account_id_fkey FOREIGN KEY (service_account_id) REFERENCES public.service_accounts(id) ON DELETE SET NULL,
    CONSTRAINT service_account_assertion_subjects_subject_check CHECK ((length(btrim((subject)::text)) >= 1))
);

CREATE INDEX service_account_assertion_subjects_account_idx ON public.service_account_assertion_subjects USING btree (service_account_id);

-- Existing accounts get the default subject when their name is a legal subject component; the others stay without
-- one and need a custom subject to be enabled for assertions.
INSERT INTO public.service_account_assertion_subjects (subject, service_account_id)
SELECT 'notarx:service-account:' || name, id FROM public.service_accounts WHERE name ~ '^[A-Za-z0-9._:-]{1,128}$';

UPDATE public.service_accounts SET assertion_subject = 'notarx:service-account:' || name WHERE name ~ '^[A-Za-z0-9._:-]{1,128}$';
