-- +goose Up

-- ADR 0021 (slice 6), "Batch and version sets": POST /promotion-batches takes up to 100 (coordinate, digest) items from one
-- source repository into one target. The batch row is the durable record of the admitted request: its items are a JSONB
-- array [{coordinate, digest, aptTargetSuite?}] written once, in request order, and never updated, so the table is
-- insert-only (no fillfactor or autovacuum tuning, ADR 0023). The aggregate state of a batch is not stored: it is computed
-- from the artifact_promotions rows of the items, whose idempotency keys derive from the batch id and the item index.
--
-- The row is written only after every item passed admission (all-or-nothing). UNIQUE (target_repository_id,
-- idempotency_key) makes a replay of the request find its batch; request_hash (SHA-256 of the canonical request) tells a
-- replay from a different request that reuses the key. Like artifact_promotions the table carries no foreign keys and no
-- retention: a deleted repository must not erase the history of what was promoted (ADR 0023 section 5).
--
-- MySQL (ADR 0022): the twin ships with the generated baseline; the catalogue carries the table.
CREATE TABLE public.promotion_batches (
    id uuid NOT NULL,
    source_repository_id uuid NOT NULL,
    target_repository_id uuid NOT NULL,
    requested_by text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    items jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT promotion_batches_pkey PRIMARY KEY (id),
    CONSTRAINT promotion_batches_target_idempotency_key_key UNIQUE (target_repository_id, idempotency_key),
    CONSTRAINT promotion_batches_items_check CHECK (((jsonb_typeof(items) = 'array'::text) AND (jsonb_array_length(items) >= 1) AND (jsonb_array_length(items) <= 100)))
);
