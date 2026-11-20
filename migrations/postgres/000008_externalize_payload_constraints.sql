-- +goose Up

-- ADR 0023 section 9: a payload above NOTARX_INLINE_MAX_BYTES lives in the object store
-- meta namespace and its row keeps body_digest/body_size/body_key. The inline column of such a
-- row is empty, so the CHECKs that demanded a non-empty inline body now accept an empty one
-- when body_digest is set. Rows written before this migration are inline and keep passing.

-- artifact_intelligence moves the vulnerability findings, the one unbounded part of the row,
-- to the object store. The counts the search projections read stay inline.
ALTER TABLE public.artifact_intelligence
    ADD COLUMN body_digest text,
    ADD COLUMN body_size bigint,
    ADD COLUMN body_key text,
    ADD CONSTRAINT artifact_intelligence_body_ref_check CHECK ((((body_digest IS NULL) AND (body_size IS NULL) AND (body_key IS NULL)) OR ((body_digest IS NOT NULL) AND (body_size IS NOT NULL) AND (body_key IS NOT NULL) AND (body_size >= 0))));

ALTER TABLE public.native_cargo_proxy_indexes
    DROP CONSTRAINT native_cargo_proxy_indexes_check,
    ADD CONSTRAINT native_cargo_proxy_indexes_check CHECK ((((status = 200) AND (octet_length(body) > 0)) OR ((status = 200) AND (body_digest IS NOT NULL) AND (octet_length(body) = 0)) OR ((status <> 200) AND (octet_length(body) = 0))));

ALTER TABLE public.native_helm_source_documents
    DROP CONSTRAINT native_helm_source_documents_body_check,
    ADD CONSTRAINT native_helm_source_documents_body_check CHECK ((((octet_length(body) >= 1) AND (octet_length(body) <= 1048576)) OR ((body_digest IS NOT NULL) AND (octet_length(body) = 0))));

ALTER TABLE public.native_helm_publications
    DROP CONSTRAINT native_helm_publications_chart_yaml_check,
    ADD CONSTRAINT native_helm_publications_chart_yaml_check CHECK (((octet_length(chart_yaml) > 0) OR ((body_digest IS NOT NULL) AND (octet_length(chart_yaml) = 0))));

ALTER TABLE public.native_nuget_publications
    DROP CONSTRAINT native_nuget_publications_nuspec_check,
    ADD CONSTRAINT native_nuget_publications_nuspec_check CHECK (((octet_length(nuspec) > 0) OR ((body_digest IS NOT NULL) AND (octet_length(nuspec) = 0))));
