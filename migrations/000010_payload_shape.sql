-- +goose Up

-- ADR 0023 slice 10: payload schema shape.
--
-- metadata_json TEXT becomes jsonb in the Helm, NuGet and RubyGems session and
-- publication tables. The jsonb_typeof(metadata_json::jsonb) = 'object' CHECKs are
-- dropped; the rule moved to Go (internal/jsonmeta, applied by the session validators
-- with MaxMetadataJSONBytes) and is tested there. jsonb normalizes key order and
-- whitespace, so stored documents are compared semantically (jsonmeta.Equal) instead of
-- byte-wise. An unset NuGet metadata_json (open session) becomes {}.
--
-- The octet_length(payload::text) <= 8 MiB CHECKs on oci_discovery_snapshots and
-- oci_subject_bindings are dropped; ocidiscovery.Payload enforces MaxPayload (8 MiB)
-- before every write. native_cargo_publications.index_row stays text: it is served
-- byte for byte as the Cargo sparse index line, which jsonb would reorder.
--
-- body_digest, body_size and body_key are added, unused until the object store meta
-- namespace slice, to the tables whose payloads become externalisable: npm version
-- manifests, chart_yaml, nuspec and the proxy document tables. Either all three are
-- set or none.
--
-- Not in this migration: the split of artifact_intelligence into artifact_sbom and
-- artifact_vulnerability. ADR 0023 section 9 assigns the final shape of both tables
-- (with artifact_vulnerability_finding and artifact_scan_history) to ADR 0030
-- section 2, whose slice 2a creates them together with the stores that write them.
--
-- MySQL (ADR 0022): there is no migrations/mysql directory yet, so no twin is added.
-- jsonb maps to JSON, the dropped checks have no equivalent and the body columns are
-- plain nullable columns.

ALTER TABLE public.native_helm_publication_sessions DROP CONSTRAINT native_helm_publication_sessions_metadata_json_check;
ALTER TABLE public.native_helm_publication_sessions ALTER COLUMN metadata_json TYPE jsonb USING metadata_json::jsonb;
ALTER TABLE public.native_helm_publications DROP CONSTRAINT native_helm_publications_metadata_json_check;
ALTER TABLE public.native_helm_publications ALTER COLUMN metadata_json TYPE jsonb USING metadata_json::jsonb;

ALTER TABLE public.native_nuget_publication_sessions DROP CONSTRAINT native_nuget_publication_sessions_check1;
ALTER TABLE public.native_nuget_publication_sessions ALTER COLUMN metadata_json DROP DEFAULT;
ALTER TABLE public.native_nuget_publication_sessions
    ALTER COLUMN metadata_json TYPE jsonb USING (CASE WHEN metadata_json = '' THEN '{}' ELSE metadata_json END)::jsonb;
ALTER TABLE public.native_nuget_publication_sessions ALTER COLUMN metadata_json SET DEFAULT '{}'::jsonb;
ALTER TABLE public.native_nuget_publication_sessions ADD CONSTRAINT native_nuget_publication_sessions_check1
    CHECK (state <> ALL (ARRAY['staged'::text, 'committed'::text]) OR octet_length(nuspec) > 0);
ALTER TABLE public.native_nuget_publications DROP CONSTRAINT native_nuget_publications_metadata_json_check;
ALTER TABLE public.native_nuget_publications ALTER COLUMN metadata_json TYPE jsonb USING metadata_json::jsonb;

ALTER TABLE public.native_rubygems_sessions DROP CONSTRAINT native_rubygems_sessions_metadata_json_check;
ALTER TABLE public.native_rubygems_sessions ALTER COLUMN metadata_json TYPE jsonb USING metadata_json::jsonb;

ALTER TABLE public.oci_discovery_snapshots DROP CONSTRAINT oci_discovery_snapshots_payload_check;
ALTER TABLE public.oci_subject_bindings DROP CONSTRAINT oci_subject_bindings_payload_check;

ALTER TABLE public.native_npm_versions
    ADD COLUMN body_digest text,
    ADD COLUMN body_size bigint,
    ADD COLUMN body_key text,
    ADD CONSTRAINT native_npm_versions_body_ref_check CHECK ((body_digest IS NULL AND body_size IS NULL AND body_key IS NULL)
        OR (body_digest IS NOT NULL AND body_size IS NOT NULL AND body_key IS NOT NULL AND body_size >= 0));
ALTER TABLE public.native_helm_publications
    ADD COLUMN body_digest text,
    ADD COLUMN body_size bigint,
    ADD COLUMN body_key text,
    ADD CONSTRAINT native_helm_publications_body_ref_check CHECK ((body_digest IS NULL AND body_size IS NULL AND body_key IS NULL)
        OR (body_digest IS NOT NULL AND body_size IS NOT NULL AND body_key IS NOT NULL AND body_size >= 0));
ALTER TABLE public.native_nuget_publications
    ADD COLUMN body_digest text,
    ADD COLUMN body_size bigint,
    ADD COLUMN body_key text,
    ADD CONSTRAINT native_nuget_publications_body_ref_check CHECK ((body_digest IS NULL AND body_size IS NULL AND body_key IS NULL)
        OR (body_digest IS NOT NULL AND body_size IS NOT NULL AND body_key IS NOT NULL AND body_size >= 0));
ALTER TABLE public.native_nuget_source_documents
    ADD COLUMN body_digest text,
    ADD COLUMN body_size bigint,
    ADD COLUMN body_key text,
    ADD CONSTRAINT native_nuget_source_documents_body_ref_check CHECK ((body_digest IS NULL AND body_size IS NULL AND body_key IS NULL)
        OR (body_digest IS NOT NULL AND body_size IS NOT NULL AND body_key IS NOT NULL AND body_size >= 0));
ALTER TABLE public.native_helm_source_documents
    ADD COLUMN body_digest text,
    ADD COLUMN body_size bigint,
    ADD COLUMN body_key text,
    ADD CONSTRAINT native_helm_source_documents_body_ref_check CHECK ((body_digest IS NULL AND body_size IS NULL AND body_key IS NULL)
        OR (body_digest IS NOT NULL AND body_size IS NOT NULL AND body_key IS NOT NULL AND body_size >= 0));
ALTER TABLE public.native_cargo_proxy_indexes
    ADD COLUMN body_digest text,
    ADD COLUMN body_size bigint,
    ADD COLUMN body_key text,
    ADD CONSTRAINT native_cargo_proxy_indexes_body_ref_check CHECK ((body_digest IS NULL AND body_size IS NULL AND body_key IS NULL)
        OR (body_digest IS NOT NULL AND body_size IS NOT NULL AND body_key IS NOT NULL AND body_size >= 0));
ALTER TABLE public.oci_discovery_snapshots
    ADD COLUMN body_digest text,
    ADD COLUMN body_size bigint,
    ADD COLUMN body_key text,
    ADD CONSTRAINT oci_discovery_snapshots_body_ref_check CHECK ((body_digest IS NULL AND body_size IS NULL AND body_key IS NULL)
        OR (body_digest IS NOT NULL AND body_size IS NOT NULL AND body_key IS NOT NULL AND body_size >= 0));
