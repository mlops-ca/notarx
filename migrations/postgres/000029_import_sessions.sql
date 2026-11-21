-- +goose Up

-- ADR 0021 (slice 7b, part 1), "Remote targets": the receiver side of a cross-instance promotion. An import session is the
-- durable, limited state of one push: POST /repositories/{id}/imports records what the sender claims (coordinate, digest,
-- the format manifest, the chain path, evidence) and the blobs it intends to send; each blob is streamed into a staging
-- object and verified; the commit publishes through the format. Everything the sender says is a claim until the bytes
-- are verified, so the manifest, the chain path and the evidence are stored as received.
--
-- import_sessions.bytes is the total of the claimed blob sizes, the figure the per-session, staging and quota limits are
-- checked against. UNIQUE (target_repository_id, idempotency_key) makes a replay of the request find its session. A session is
-- open until it is committing, then committed; aborted and expired are the other final states. expires_at is the TTL
-- (NOTARX_IMPORT_SESSION_TTL): the reaper expires a session past it and deletes its staging objects, then the retention
-- janitor deletes the finished row (the blobs go with it) once no staging key is left.
--
-- Like artifact_promotions the sessions carry no foreign key to the repository: the reaper removes the staging objects of a
-- session whose repository is gone. import_blobs.staging_key names the staging object, set when the session is created so
-- the reaper finds the partial upload of a blob that never verified; it is emptied once the object is deleted.
--
-- Indexes: the reaper reads the sessions that are past their expiry by (state, expires_at); the per-principal open session
-- count reads (principal, state); the retention janitor reads the finished sessions by age (updated_at). MySQL (ADR 0022): the twin ships with the generated baseline; the catalogue carries both tables.
CREATE TABLE public.import_sessions (
    id uuid NOT NULL,
    target_repository_id uuid NOT NULL,
    principal text NOT NULL,
    format text NOT NULL,
    coordinate text NOT NULL,
    digest text NOT NULL,
    format_manifest jsonb NOT NULL,
    idempotency_key text NOT NULL,
    chain_path jsonb DEFAULT '[]'::jsonb NOT NULL,
    evidence jsonb,
    state text DEFAULT 'open'::text NOT NULL,
    bytes bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT import_sessions_pkey PRIMARY KEY (id),
    CONSTRAINT import_sessions_target_idempotency_key_key UNIQUE (target_repository_id, idempotency_key),
    CONSTRAINT import_sessions_state_check CHECK ((state = ANY (ARRAY['open'::text, 'committing'::text, 'committed'::text, 'aborted'::text, 'expired'::text]))),
    CONSTRAINT import_sessions_bytes_check CHECK ((bytes >= 0)),
    CONSTRAINT import_sessions_chain_path_check CHECK ((jsonb_typeof(chain_path) = 'array'::text)),
    CONSTRAINT import_sessions_evidence_check CHECK (((evidence IS NULL) OR (jsonb_typeof(evidence) = 'object'::text)))
);

CREATE INDEX import_sessions_state_expires_idx ON public.import_sessions USING btree (state, expires_at);
CREATE INDEX import_sessions_principal_state_idx ON public.import_sessions USING btree (principal, state);
CREATE INDEX import_sessions_updated_at_idx ON public.import_sessions USING btree (updated_at);

CREATE TABLE public.import_blobs (
    session_id uuid NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    staging_key text NOT NULL,
    verified boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT import_blobs_pkey PRIMARY KEY (session_id, digest),
    CONSTRAINT import_blobs_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.import_sessions(id) ON DELETE CASCADE,
    CONSTRAINT import_blobs_size_check CHECK ((size >= 0))
);
