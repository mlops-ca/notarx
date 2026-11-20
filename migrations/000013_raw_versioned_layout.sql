-- +goose Up

-- ADR 0028 slice 1: the versioned layout of Raw repositories. raw_layout, seal_mode and raw_strict are chosen when the
-- repository is created and never change afterwards (the management API answers 409 layout_immutable). The shape
-- check keeps every non-default value on hosted Raw repositories: proxy and non-Raw repositories keep the defaults,
-- seal_mode idle and raw_strict exist only for the versioned layout.
ALTER TABLE public.hosted_repositories
    ADD COLUMN raw_layout text DEFAULT 'path'::text NOT NULL,
    ADD COLUMN seal_mode text DEFAULT 'explicit'::text NOT NULL,
    ADD COLUMN raw_strict boolean DEFAULT false NOT NULL,
    ADD CONSTRAINT hosted_repositories_raw_layout_check CHECK ((raw_layout = ANY (ARRAY['path'::text, 'versioned'::text]))),
    ADD CONSTRAINT hosted_repositories_seal_mode_check CHECK ((seal_mode = ANY (ARRAY['explicit'::text, 'idle'::text]))),
    ADD CONSTRAINT hosted_repositories_raw_layout_shape_check CHECK ((((raw_layout = 'path'::text) AND (seal_mode = 'explicit'::text) AND (NOT raw_strict)) OR ((raw_layout = 'versioned'::text) AND (format = 'raw'::text) AND (repo_type = 'hosted'::text))));

-- A version of a versioned Raw repository (name/version/file). Its files are the native_raw_assets rows in the path
-- range [name/version/, name/version0); file_count and size_bytes are display aids kept under the row lock of the
-- publish transaction. name and version are bounded VARCHAR(128) validated in Go, so the table ports to MySQL
-- (ADR 0022) with a binary collation and no PostgreSQL-only CHECK. The row outlives a tombstone on purpose: it
-- keeps the name@version identifier burned.
CREATE TABLE public.native_raw_versions (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    name character varying(128) NOT NULL,
    version character varying(128) NOT NULL,
    state text DEFAULT 'open'::text NOT NULL,
    file_count integer DEFAULT 0 NOT NULL,
    size_bytes bigint DEFAULT 0 NOT NULL,
    manifest_digest text,
    created_by text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    sealed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_raw_versions_pkey PRIMARY KEY (id),
    CONSTRAINT native_raw_versions_identity_key UNIQUE (repository_id, name, version),
    CONSTRAINT native_raw_versions_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id),
    CONSTRAINT native_raw_versions_state_check CHECK ((state = ANY (ARRAY['open'::text, 'sealed'::text, 'tombstoned'::text]))),
    CONSTRAINT native_raw_versions_counts_check CHECK (((file_count >= 0) AND (size_bytes >= 0)))
);

-- Version listing of one package, newest seal first; unsealed versions sort last.
CREATE INDEX native_raw_versions_list_idx ON public.native_raw_versions USING btree (repository_id, name, sealed_at DESC NULLS LAST, id DESC);
-- Retention and the version counts of a repository by state.
CREATE INDEX native_raw_versions_repository_state_sealed_idx ON public.native_raw_versions USING btree (repository_id, state, sealed_at);
-- The stale-open report (olderThanDays filter and the sweep metrics) and the idle auto-seal sweep.
CREATE INDEX native_raw_versions_state_created_idx ON public.native_raw_versions USING btree (state, created_at);
CREATE INDEX native_raw_versions_state_updated_idx ON public.native_raw_versions USING btree (state, updated_at);

-- The property map of a version: bounded, frozen at seal, a side table so a lookup is a plain B-tree on both
-- PostgreSQL and MySQL. prop_key avoids the MySQL reserved word key; repository_id is denormalised for the index.
CREATE TABLE public.native_raw_version_properties (
    version_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    prop_key character varying(64) NOT NULL,
    prop_value character varying(256) NOT NULL,
    CONSTRAINT native_raw_version_properties_pkey PRIMARY KEY (version_id, prop_key),
    CONSTRAINT native_raw_version_properties_version_id_fkey FOREIGN KEY (version_id) REFERENCES public.native_raw_versions(id) ON DELETE CASCADE
);
CREATE INDEX native_raw_version_properties_lookup_idx ON public.native_raw_version_properties USING btree (repository_id, prop_key, prop_value);

-- The idle auto-seal sweep (ADR 0028 slice 5) elects its runner through a lease row in scheduled_tasks, like the
-- partition manager and the retention janitor: an internal row that is never due (enabled=false) and that the
-- management API does not list. The kind constraints gain the new internal kind.
ALTER TABLE public.scheduled_tasks
    DROP CONSTRAINT scheduled_tasks_check,
    DROP CONSTRAINT scheduled_tasks_kind_check,
    ADD CONSTRAINT scheduled_tasks_check CHECK ((((kind = 'audit-retention'::text) AND (repository_id IS NULL)) OR ((kind = 'repository-retention'::text) AND (repository_id IS NOT NULL)) OR ((kind = ANY (ARRAY['partition-manager'::text, 'retention-janitor'::text, 'raw-idle-seal'::text])) AND (repository_id IS NULL) AND (NOT enabled)))),
    ADD CONSTRAINT scheduled_tasks_kind_check CHECK ((kind = ANY (ARRAY['repository-retention'::text, 'audit-retention'::text, 'partition-manager'::text, 'retention-janitor'::text, 'raw-idle-seal'::text])));
