-- +goose Up

-- ADR 0023 section 9: the mark-and-sweep job that deletes unreferenced meta/ objects
-- (internal/app/meta_sweep.go).
--
-- meta_object_pins records when an object of the meta namespace was last written, or first seen by the sweep.
-- A writer pins the digest, in its own committed statement, before it puts the object, and a content-addressed
-- put of bytes that already exist pins the same row again. The sweep deletes an object only when its pin is
-- older than the grace period and no row references the object, and it holds the pin row locked from the
-- reference check until the object is deleted. A writer that pins an object the sweep is about to delete waits
-- on that lock and writes the object again afterwards, so a delete can never remove bytes a row is about to
-- reference. An object with no pin (written by a node that predates the pin, or restored from a backup) is
-- pinned when the sweep first sees it, so it too waits out one grace period.
CREATE TABLE public.meta_object_pins (
    digest text NOT NULL,
    seen_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT meta_object_pins_pkey PRIMARY KEY (digest)
);

-- meta_sweep_lease holds the lease of the one node that runs the sweep, and when it last completed, so a fleet
-- runs one sweep per interval however many schedulers there are. The lease expires, so a node that dies in the
-- middle of a sweep hands it over.
CREATE TABLE public.meta_sweep_lease (
    name text NOT NULL,
    owner text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    last_completed_at timestamp with time zone,
    CONSTRAINT meta_sweep_lease_pkey PRIMARY KEY (name)
);

-- The reference check looks an object key up in every table that can hold one. Only rows that externalised a
-- payload have a key, so the indexes stay small next to the tables they cover.
CREATE INDEX native_npm_versions_body_key_idx ON public.native_npm_versions USING btree (body_key) WHERE (body_key IS NOT NULL);
CREATE INDEX native_cargo_proxy_indexes_body_key_idx ON public.native_cargo_proxy_indexes USING btree (body_key) WHERE (body_key IS NOT NULL);
CREATE INDEX native_helm_publications_body_key_idx ON public.native_helm_publications USING btree (body_key) WHERE (body_key IS NOT NULL);
CREATE INDEX native_helm_source_documents_body_key_idx ON public.native_helm_source_documents USING btree (body_key) WHERE (body_key IS NOT NULL);
CREATE INDEX native_nuget_publications_body_key_idx ON public.native_nuget_publications USING btree (body_key) WHERE (body_key IS NOT NULL);
CREATE INDEX native_nuget_source_documents_body_key_idx ON public.native_nuget_source_documents USING btree (body_key) WHERE (body_key IS NOT NULL);
CREATE INDEX oci_discovery_snapshots_body_key_idx ON public.oci_discovery_snapshots USING btree (body_key) WHERE (body_key IS NOT NULL);
CREATE INDEX artifact_intelligence_body_key_idx ON public.artifact_intelligence USING btree (body_key) WHERE (body_key IS NOT NULL);
CREATE INDEX artifact_sbom_body_key_idx ON public.artifact_sbom USING btree (body_key) WHERE (body_key <> ''::text);
CREATE INDEX repository_vex_document_body_key_idx ON public.repository_vex_document USING btree (body_key) WHERE (body_key <> ''::text);
