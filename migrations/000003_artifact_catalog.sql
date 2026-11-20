-- +goose Up

-- artifact_catalog is one row per coordinate: the latest version, digest and publish time,
-- the publisher, how many versions the coordinate has and the size of the latest one
-- (ADR 0023 section 8). Search reads it instead of ranking the per-format rows with
-- row_number() windows over artifact_search_projection, and the trigram index sits on
-- this much smaller table instead of on every per-format row.
--
-- The store keeps it exact: every publish, yank, restore, tombstone and delete recomputes
-- the rows of the coordinates it touched from artifact_search_projection inside its own
-- transaction (internal/repository/postgres_artifact_catalog.go), and a resumable backfill
-- fills it for rows that existed before this migration. latest_* follow the ranking the
-- search used (newest created_at, then highest digest); version_count counts distinct
-- versions; size_bytes is the size of the latest row and NULL when the format has no size
-- for it. A coordinate with no visible row has no row here.
CREATE TABLE public.artifact_catalog (
    repository_id uuid NOT NULL,
    format text NOT NULL,
    coordinate text NOT NULL,
    latest_version text DEFAULT ''::text NOT NULL,
    latest_digest text DEFAULT ''::text NOT NULL,
    latest_published_at timestamp with time zone,
    publisher text DEFAULT ''::text NOT NULL,
    version_count integer DEFAULT 0 NOT NULL,
    size_bytes bigint,
    CONSTRAINT artifact_catalog_pkey PRIMARY KEY (repository_id, format, coordinate),
    CONSTRAINT artifact_catalog_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id),
    CONSTRAINT artifact_catalog_version_count_check CHECK ((version_count >= 0))
);

CREATE INDEX artifact_catalog_coordinate_trgm_idx
    ON public.artifact_catalog USING gin (coordinate public.gin_trgm_ops)
    WITH (fastupdate='on', gin_pending_list_limit='1024');

-- artifact_catalog_backfill records how far the backfill of each format has come, so an
-- interrupted run resumes from its cursor. The backfill is idempotent: it recomputes the
-- rows of a batch, so running it again, or after live writes, changes nothing.
CREATE TABLE public.artifact_catalog_backfill (
    format text NOT NULL,
    cursor_repository_id uuid DEFAULT '00000000-0000-0000-0000-000000000000'::uuid NOT NULL,
    cursor_coordinate text DEFAULT ''::text NOT NULL,
    completed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT artifact_catalog_backfill_pkey PRIMARY KEY (format)
);
