-- +goose Up

-- Extensions (ADR 0015, "Generation procedure", step 4).
--
-- pg_trgm is required: 12 GIN indexes below use gin_trgm_ops. It is a trusted
-- extension from PostgreSQL 13, so a migration role with CREATE on the database
-- can install it. The runner and `make preflight` check this before the first
-- statement; there is no fallback without it.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- pg_stat_statements is optional: no code reads it. It is created in a guarded
-- block so a server without it, or a role that may not create it, still
-- migrates. Operators who want statement statistics set
-- shared_preload_libraries themselves.
DO $$
BEGIN
    CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'pg_stat_statements is unavailable (%); continuing without it', SQLERRM;
END
$$;

--
-- Name: artifact_gateway_notify_queue(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.artifact_gateway_notify_queue() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM pg_notify(TG_ARGV[0], COALESCE(NEW.id::text, 'wake'));
    RETURN NEW;
END;
$$;

--
-- Name: assert_apt_repository_capacity_trigger(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.assert_apt_repository_capacity_trigger() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    quota BIGINT;
    used BIGINT;
BEGIN
    SELECT quota_bytes INTO quota FROM repository_capacity_quotas WHERE repository_id=NEW.repository_id FOR UPDATE;
    IF quota IS NULL OR quota = 0 THEN RETURN NEW; END IF;
    SELECT COALESCE(SUM(size),0) INTO used FROM native_apt_assets WHERE repository_id=NEW.repository_id;
    IF used > quota THEN
        RAISE EXCEPTION 'repository capacity quota exceeded' USING ERRCODE='P0001';
    END IF;
    RETURN NEW;
END;
$$;

--
-- Name: assert_cargo_repository_capacity(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.assert_cargo_repository_capacity() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    quota BIGINT;
    used BIGINT;
BEGIN
    SELECT quota_bytes INTO quota FROM repository_capacity_quotas
        WHERE repository_id=NEW.repository_id FOR UPDATE;
    IF quota IS NULL OR quota = 0 THEN RETURN NEW; END IF;
    SELECT COALESCE(SUM(size), 0) INTO used FROM native_cargo_publications
        WHERE repository_id=NEW.repository_id AND collected_at IS NULL;
    IF used > quota THEN
        RAISE EXCEPTION 'repository capacity quota exceeded' USING ERRCODE='P0001';
    END IF;
    RETURN NEW;
END;
$$;

--
-- Name: assert_hosted_repository_capacity(uuid); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.assert_hosted_repository_capacity(p_repository_id uuid) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    quota BIGINT;
    repository_format TEXT;
    used BIGINT;
BEGIN
    SELECT quota_bytes INTO quota FROM repository_capacity_quotas WHERE repository_id=p_repository_id FOR UPDATE;
    IF quota IS NULL OR quota = 0 THEN RETURN; END IF;
    SELECT format INTO repository_format FROM hosted_repositories WHERE id=p_repository_id;
    CASE repository_format
    WHEN 'raw' THEN
        SELECT COALESCE(SUM(o.size),0) INTO used FROM native_raw_assets a JOIN native_raw_objects o ON o.digest=a.digest WHERE a.repository_id=p_repository_id;
    WHEN 'maven' THEN
        SELECT COALESCE(SUM(a.size),0) INTO used FROM native_maven_assets a WHERE a.repository_id=p_repository_id AND EXISTS (SELECT 1 FROM native_maven_artifacts m WHERE m.repository_id=a.repository_id AND m.state='visible' AND left(a.path,length(replace(split_part(m.coordinate,':',1),'.','/') || '/' || split_part(m.coordinate,':',2) || '/' || split_part(m.coordinate,':',3) || '/'))=replace(split_part(m.coordinate,':',1),'.','/') || '/' || split_part(m.coordinate,':',2) || '/' || split_part(m.coordinate,':',3) || '/');
    WHEN 'oci' THEN
        SELECT COALESCE((SELECT SUM(size) FROM native_oci_manifests WHERE repository_id=p_repository_id),0)+COALESCE((SELECT SUM(b.size) FROM native_oci_repository_blobs rb JOIN native_oci_blobs b ON b.digest=rb.digest WHERE rb.repository_id=p_repository_id),0) INTO used;
    WHEN 'conan' THEN
        SELECT COALESCE(SUM(a.size),0) INTO used FROM native_conan_assets a JOIN native_conan_recipe_revisions r ON r.repository_id=a.repository_id AND r.reference=a.reference AND r.revision=a.recipe_revision LEFT JOIN native_conan_package_revisions p ON p.repository_id=a.repository_id AND p.reference=a.reference AND p.recipe_revision=a.recipe_revision AND p.package_id=a.package_id AND p.revision=a.package_revision WHERE a.repository_id=p_repository_id AND r.state='visible' AND (a.package_id='' OR p.state='visible');
    WHEN 'npm' THEN
        SELECT COALESCE(SUM(size),0) INTO used FROM native_npm_versions WHERE repository_id=p_repository_id AND state='visible';
    WHEN 'pypi' THEN
        SELECT COALESCE(SUM(size),0) INTO used FROM native_pypi_files WHERE repository_id=p_repository_id AND state='visible';
    WHEN 'go' THEN
        SELECT COALESCE(SUM(size),0) INTO used FROM native_go_assets WHERE repository_id=p_repository_id AND collected_at IS NULL;
    ELSE RETURN;
    END CASE;
    IF used > quota THEN
        RAISE EXCEPTION 'repository capacity quota exceeded' USING ERRCODE='P0001';
    END IF;
END;
$$;

--
-- Name: assert_hosted_repository_capacity_trigger(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.assert_hosted_repository_capacity_trigger() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM assert_hosted_repository_capacity(NEW.repository_id);
    RETURN NEW;
END;
$$;

--
-- Name: cargo_promotion_state_webhook(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.cargo_promotion_state_webhook() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.state='running' AND NEW.state IN ('completed','failed')
       AND NEW.kind='promotion' AND NEW.payload->>'format'='cargo' THEN
        PERFORM enqueue_cargo_distribution_webhook('promotion',NEW.state,jsonb_build_object(
            'operationId',NEW.id,'kind','promotion','format','cargo',
            'sourceRepositoryId',NEW.payload->>'sourceRepositoryId',
            'targetRepositoryId',NEW.repository_id,
            'coordinate',(NEW.payload->>'name') || '@' || (NEW.payload->>'version'),
            'digest',NEW.payload->>'digest','state',NEW.state,'attempts',NEW.attempts
        ),COALESCE(NEW.completed_at,clock_timestamp()));
    END IF;
    RETURN NEW;
END;
$$;

--
-- Name: cargo_replication_state_webhook(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.cargo_replication_state_webhook() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.state='running' AND NEW.format='cargo'
       AND (NEW.state='completed' OR (NEW.state='failed' AND NEW.next_attempt_at IS NULL)) THEN
        PERFORM enqueue_cargo_distribution_webhook('replication',NEW.state,jsonb_build_object(
            'operationId',NEW.id,'kind','replication','format','cargo',
            'sourceRepositoryId',NEW.source_repository_id,
            'targetRepositoryId',NEW.target_repository_id,
            'coordinate',NEW.coordinate,'digest',NEW.digest,
            'state',NEW.state,'attempts',NEW.attempts
        ),COALESCE(NEW.completed_at,clock_timestamp()));
    END IF;
    RETURN NEW;
END;
$$;

--
-- Name: enqueue_cargo_distribution_webhook(text, text, jsonb, timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.enqueue_cargo_distribution_webhook(kind text, outcome text, data jsonb, occurred timestamp with time zone) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    event_id UUID := gen_random_uuid();
    event_type TEXT := 'cargo.' || kind || '.' || outcome;
BEGIN
    INSERT INTO webhook_events (id,event_type,occurred_at,payload)
    VALUES (event_id,event_type,occurred,data);
    INSERT INTO webhook_deliveries (id,event_id,subscription_id,state,next_attempt_at,created_at,updated_at)
    SELECT gen_random_uuid(),event_id,id,'pending',clock.now,clock.now,clock.now
    FROM webhook_subscriptions CROSS JOIN (SELECT clock_timestamp() AS now) AS clock
    WHERE enabled=true AND event_types ? event_type;
END;
$$;

--
-- Name: anonymous_access_policy; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.anonymous_access_policy (
    singleton boolean DEFAULT true NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    CONSTRAINT anonymous_access_policy_singleton_check CHECK (singleton)
);

--
-- Name: api_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.api_keys (
    id uuid NOT NULL,
    name text NOT NULL,
    secret_hash text NOT NULL,
    roles text[] NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    revoked_at timestamp with time zone,
    expires_at timestamp with time zone,
    last_used_at timestamp with time zone,
    service_account_id uuid,
    CONSTRAINT api_keys_roles_check CHECK ((roles <@ ARRAY['member'::text, 'admin'::text]))
);

--
-- Name: artifact_intelligence; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.artifact_intelligence (
    repository_id uuid NOT NULL,
    format text NOT NULL,
    coordinate text NOT NULL,
    digest text NOT NULL,
    signatures jsonb DEFAULT '[]'::jsonb NOT NULL,
    sboms jsonb DEFAULT '[]'::jsonb NOT NULL,
    provenance jsonb,
    licenses jsonb DEFAULT '[]'::jsonb NOT NULL,
    vulnerability jsonb,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by text DEFAULT ''::text NOT NULL,
    CONSTRAINT artifact_intelligence_coordinate_check CHECK (((char_length(coordinate) >= 1) AND (char_length(coordinate) <= 1024))),
    CONSTRAINT artifact_intelligence_digest_check CHECK ((digest ~ '^sha256:[a-f0-9]{64}$'::text)),
    CONSTRAINT artifact_intelligence_version_check CHECK ((version >= 1))
);

--
-- Name: artifact_quarantines; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.artifact_quarantines (
    repository_id uuid NOT NULL,
    format text NOT NULL,
    coordinate text NOT NULL,
    digest text NOT NULL,
    state text NOT NULL,
    reason text NOT NULL,
    updated_by text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    quarantined_at timestamp with time zone DEFAULT now() NOT NULL,
    released_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT artifact_quarantines_check CHECK ((((state = 'quarantined'::text) AND (released_at IS NULL)) OR ((state = 'released'::text) AND (released_at IS NOT NULL)))),
    CONSTRAINT artifact_quarantines_check1 CHECK (((format <> 'conan'::text) OR (coordinate ~ '^[^/#]+/[^/#]+/[^/#]+/[^/#]+#[^/#]+$'::text))),
    CONSTRAINT artifact_quarantines_coordinate_check CHECK ((((char_length(coordinate) >= 1) AND (char_length(coordinate) <= 1024)) AND (btrim(coordinate) <> ''::text))),
    CONSTRAINT artifact_quarantines_digest_check CHECK ((digest ~ '^sha256:[a-f0-9]{64}$'::text)),
    CONSTRAINT artifact_quarantines_format_check CHECK ((format = ANY (ARRAY['raw'::text, 'oci'::text, 'maven'::text, 'conan'::text, 'npm'::text, 'pypi'::text, 'go'::text, 'apt'::text, 'cargo'::text]))),
    CONSTRAINT artifact_quarantines_reason_check CHECK ((((char_length(reason) >= 1) AND (char_length(reason) <= 1024)) AND (btrim(reason) <> ''::text))),
    CONSTRAINT artifact_quarantines_state_check CHECK ((state = ANY (ARRAY['quarantined'::text, 'released'::text]))),
    CONSTRAINT artifact_quarantines_updated_by_check CHECK ((((char_length(updated_by) >= 1) AND (char_length(updated_by) <= 512)) AND (btrim(updated_by) <> ''::text))),
    CONSTRAINT artifact_quarantines_version_check CHECK ((version >= 1))
);

--
-- Name: native_apt_assets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_apt_assets (
    repository_id uuid NOT NULL,
    path text NOT NULL,
    digest text NOT NULL,
    object_key text NOT NULL,
    size bigint NOT NULL,
    content_type text DEFAULT ''::text NOT NULL,
    source_url text NOT NULL,
    upstream_etag text DEFAULT ''::text NOT NULL,
    upstream_modified text DEFAULT ''::text NOT NULL,
    cached_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    source_version text DEFAULT ''::text NOT NULL,
    CONSTRAINT native_apt_assets_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_apt_assets_object_key_check CHECK ((object_key <> ''::text)),
    CONSTRAINT native_apt_assets_path_check CHECK (((path <> ''::text) AND (length(path) <= 2048) AND (path !~ '(^|/)[.][.]?(/|$)'::text))),
    CONSTRAINT native_apt_assets_size_check CHECK ((size >= 0))
);

--
-- Name: native_apt_repository_snapshots; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_apt_repository_snapshots (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    suite text NOT NULL,
    sequence bigint NOT NULL,
    state text NOT NULL,
    release_digest text DEFAULT ''::text NOT NULL,
    inrelease_digest text DEFAULT ''::text NOT NULL,
    signer_identity text DEFAULT ''::text NOT NULL,
    key_fingerprint text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    published_at timestamp with time zone,
    signature_algorithm text DEFAULT ''::text NOT NULL,
    retired_at timestamp with time zone,
    CONSTRAINT native_apt_repository_snapshots_check CHECK ((((state = ANY (ARRAY['building'::text, 'signed'::text, 'failed'::text])) AND (published_at IS NULL)) OR ((state = ANY (ARRAY['visible'::text, 'retired'::text, 'pruned'::text])) AND (published_at IS NOT NULL)))),
    CONSTRAINT native_apt_repository_snapshots_inrelease_digest_check CHECK (((inrelease_digest = ''::text) OR (inrelease_digest ~ '^sha256:[0-9a-f]{64}$'::text))),
    CONSTRAINT native_apt_repository_snapshots_release_digest_check CHECK (((release_digest = ''::text) OR (release_digest ~ '^sha256:[0-9a-f]{64}$'::text))),
    CONSTRAINT native_apt_repository_snapshots_sequence_check CHECK ((sequence > 0)),
    CONSTRAINT native_apt_repository_snapshots_state_check CHECK ((state = ANY (ARRAY['building'::text, 'signed'::text, 'visible'::text, 'retired'::text, 'failed'::text, 'pruned'::text]))),
    CONSTRAINT native_apt_repository_snapshots_suite_check CHECK ((suite ~ '^[a-z0-9][a-z0-9+.-]{0,127}$'::text))
);

--
-- Name: native_apt_snapshot_assets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_apt_snapshot_assets (
    snapshot_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    path text NOT NULL,
    digest text NOT NULL,
    object_key text NOT NULL,
    size bigint NOT NULL,
    content_type text NOT NULL,
    CONSTRAINT native_apt_snapshot_assets_check CHECK ((object_key = ('native/apt/sha256/'::text || substr(digest, 8)))),
    CONSTRAINT native_apt_snapshot_assets_content_type_check CHECK (((content_type <> ''::text) AND (length(content_type) <= 255))),
    CONSTRAINT native_apt_snapshot_assets_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_apt_snapshot_assets_path_check CHECK (((path <> ''::text) AND (length(path) <= 2048) AND (path !~ '(^/|(^|/)\.\.?(/|$)|[\\\r\n\t?#])'::text))),
    CONSTRAINT native_apt_snapshot_assets_path_root_check CHECK ((path ~ '^(dists|pool)/'::text)),
    CONSTRAINT native_apt_snapshot_assets_size_check CHECK (((size >= 0) AND (size <= 1073741824) AND ((size > 0) OR (digest = 'sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'::text))))
);

--
-- Name: native_conan_publish_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_conan_publish_sessions (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    publisher text NOT NULL,
    kind text NOT NULL,
    reference text NOT NULL,
    recipe_revision text NOT NULL,
    package_id text DEFAULT ''::text NOT NULL,
    package_revision text DEFAULT ''::text NOT NULL,
    state text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    objects jsonb NOT NULL,
    native boolean DEFAULT false NOT NULL,
    version bigint DEFAULT 0 NOT NULL,
    CONSTRAINT native_conan_publish_sessions_kind_check CHECK ((kind = ANY (ARRAY['recipe'::text, 'package'::text]))),
    CONSTRAINT native_conan_publish_sessions_state_check CHECK ((state = ANY (ARRAY['open'::text, 'committed'::text, 'aborted'::text, 'expired'::text]))),
    CONSTRAINT native_conan_publish_sessions_version_check CHECK ((version >= 0))
);

--
-- Name: native_conan_recipe_revisions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_conan_recipe_revisions (
    repository_id uuid NOT NULL,
    reference text NOT NULL,
    revision text NOT NULL,
    digest text NOT NULL,
    state text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_conan_recipe_revisions_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_conan_recipe_revisions_state_check CHECK ((state = ANY (ARRAY['visible'::text, 'deleted'::text])))
);

--
-- Name: native_go_assets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_go_assets (
    repository_id uuid NOT NULL,
    module_path text NOT NULL,
    version text NOT NULL,
    kind text NOT NULL,
    digest text NOT NULL,
    object_key text NOT NULL,
    size bigint NOT NULL,
    source_url text NOT NULL,
    cached_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    collecting_at timestamp with time zone,
    collected_at timestamp with time zone,
    CONSTRAINT native_go_assets_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_go_assets_kind_check CHECK ((kind = ANY (ARRAY['info'::text, 'mod'::text, 'zip'::text]))),
    CONSTRAINT native_go_assets_object_key_check CHECK ((object_key <> ''::text)),
    CONSTRAINT native_go_assets_size_check CHECK ((size > 0))
);

--
-- Name: native_go_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_go_versions (
    repository_id uuid NOT NULL,
    module_path text NOT NULL,
    version text NOT NULL,
    published_at timestamp with time zone,
    publisher text DEFAULT ''::text NOT NULL,
    cached_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_go_versions_module_path_check CHECK (((module_path <> ''::text) AND (length(module_path) <= 1024))),
    CONSTRAINT native_go_versions_version_check CHECK (((version <> ''::text) AND (length(version) <= 512)))
);

--
-- Name: native_maven_artifacts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_maven_artifacts (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    coordinate text NOT NULL,
    digest text NOT NULL,
    state text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    build_number integer DEFAULT 0 NOT NULL,
    source_timestamp text DEFAULT ''::text NOT NULL,
    source_build_number integer DEFAULT 0 NOT NULL,
    CONSTRAINT native_maven_artifacts_state_check CHECK ((state = ANY (ARRAY['visible'::text, 'deleted'::text])))
);

--
-- Name: native_maven_publish_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_maven_publish_sessions (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    publisher text NOT NULL,
    coordinate text NOT NULL,
    pom_object text NOT NULL,
    state text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    objects jsonb NOT NULL,
    client_timestamp text DEFAULT ''::text NOT NULL,
    client_build_number integer DEFAULT 0 NOT NULL,
    CONSTRAINT native_maven_publish_sessions_client_build_number_check CHECK ((client_build_number >= 0)),
    CONSTRAINT native_maven_publish_sessions_state_check CHECK ((state = ANY (ARRAY['open'::text, 'committed'::text, 'aborted'::text, 'expired'::text])))
);

--
-- Name: native_npm_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_npm_packages (
    repository_id uuid NOT NULL,
    name text NOT NULL,
    dist_tags jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    source_endpoint text DEFAULT ''::text NOT NULL,
    upstream_etag text DEFAULT ''::text NOT NULL,
    upstream_modified text DEFAULT ''::text NOT NULL,
    metadata_expires_at timestamp with time zone,
    negative_expires_at timestamp with time zone,
    negative boolean DEFAULT false NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    source_version text DEFAULT ''::text NOT NULL,
    CONSTRAINT native_npm_packages_dist_tags_check CHECK ((jsonb_typeof(dist_tags) = 'object'::text)),
    CONSTRAINT native_npm_packages_revision_check CHECK ((revision > 0))
);

--
-- Name: native_npm_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_npm_versions (
    repository_id uuid NOT NULL,
    package_name text NOT NULL,
    version text NOT NULL,
    digest text DEFAULT ''::text NOT NULL,
    integrity text DEFAULT ''::text,
    shasum text DEFAULT ''::text,
    tarball_name text NOT NULL,
    object_key text DEFAULT ''::text NOT NULL,
    size bigint DEFAULT 0 NOT NULL,
    manifest jsonb NOT NULL,
    publisher text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    upstream_tarball text DEFAULT ''::text NOT NULL,
    cached_at timestamp with time zone,
    state text DEFAULT 'visible'::text NOT NULL,
    deleted_at timestamp with time zone,
    collected_at timestamp with time zone,
    deprecated text DEFAULT ''::text NOT NULL,
    collecting_at timestamp with time zone,
    CONSTRAINT native_npm_versions_cached_object_check CHECK ((((object_key = ''::text) AND (digest = ''::text) AND (size = 0) AND (cached_at IS NULL)) OR ((object_key <> ''::text) AND (digest <> ''::text) AND (size > 0) AND (cached_at IS NOT NULL)))),
    CONSTRAINT native_npm_versions_digest_check CHECK (((digest = ''::text) OR (digest ~ '^sha256:[0-9a-f]{64}$'::text))),
    CONSTRAINT native_npm_versions_integrity_check CHECK (((integrity = ''::text) OR (integrity ~ '^sha512-[A-Za-z0-9+/]+={0,2}$'::text))),
    CONSTRAINT native_npm_versions_manifest_check CHECK ((jsonb_typeof(manifest) = 'object'::text)),
    CONSTRAINT native_npm_versions_shasum_check CHECK (((shasum = ''::text) OR (shasum ~ '^[0-9a-f]{40}$'::text))),
    CONSTRAINT native_npm_versions_size_check CHECK ((size >= 0)),
    CONSTRAINT native_npm_versions_state_check CHECK ((state = ANY (ARRAY['visible'::text, 'deleted'::text])))
);

--
-- Name: native_oci_manifests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_oci_manifests (
    repository_id uuid NOT NULL,
    name text NOT NULL,
    digest text NOT NULL,
    object_key text NOT NULL,
    media_type text NOT NULL,
    size bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    subject_digest text,
    artifact_type text DEFAULT ''::text NOT NULL,
    CONSTRAINT native_oci_manifests_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_oci_manifests_size_check CHECK ((size >= 0))
);

--
-- Name: native_pypi_files; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_pypi_files (
    repository_id uuid NOT NULL,
    project text NOT NULL,
    version text NOT NULL,
    filename text NOT NULL,
    file_type text DEFAULT ''::text NOT NULL,
    python_version text DEFAULT ''::text NOT NULL,
    requires_python text DEFAULT ''::text NOT NULL,
    digest text NOT NULL,
    object_key text DEFAULT ''::text NOT NULL,
    size bigint DEFAULT 0 NOT NULL,
    publisher text DEFAULT ''::text NOT NULL,
    source_url text DEFAULT ''::text NOT NULL,
    state text DEFAULT 'visible'::text NOT NULL,
    cached_at timestamp with time zone,
    deleted_at timestamp with time zone,
    collected_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    yanked boolean DEFAULT false NOT NULL,
    yank_reason text DEFAULT ''::text NOT NULL,
    yank_revision bigint DEFAULT 1 NOT NULL,
    source_version text DEFAULT ''::text NOT NULL,
    CONSTRAINT native_pypi_files_check CHECK ((((object_key = ''::text) AND (size = 0) AND (cached_at IS NULL)) OR ((object_key <> ''::text) AND (size > 0) AND (cached_at IS NOT NULL)))),
    CONSTRAINT native_pypi_files_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_pypi_files_project_check CHECK ((project ~ '^[a-z0-9]([a-z0-9-]{0,253}[a-z0-9])?$'::text)),
    CONSTRAINT native_pypi_files_size_check CHECK ((size >= 0)),
    CONSTRAINT native_pypi_files_state_check CHECK ((state = ANY (ARRAY['visible'::text, 'deleted'::text]))),
    CONSTRAINT native_pypi_files_yank_revision_check CHECK ((yank_revision > 0)),
    CONSTRAINT native_pypi_unyanked_reason CHECK ((yanked OR (yank_reason = ''::text))),
    CONSTRAINT native_pypi_yank_reason_size CHECK ((octet_length(yank_reason) <= 1024))
);

--
-- Name: native_raw_assets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_raw_assets (
    repository_id uuid NOT NULL,
    path text NOT NULL,
    digest text NOT NULL,
    content_type text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: native_raw_objects; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_raw_objects (
    digest text NOT NULL,
    object_key text NOT NULL,
    size bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    collected_at timestamp with time zone,
    repository_id uuid,
    CONSTRAINT native_raw_objects_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_raw_objects_size_check CHECK ((size >= 0))
);

--
-- Name: artifact_search_projection; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.artifact_search_projection AS
 SELECT repository_id,
    format,
    coordinate,
    digest,
    created_at,
    size,
    publisher,
    build_number,
    content_type,
    version
   FROM ( SELECT a.repository_id,
            'maven'::text AS format,
            a.coordinate,
            a.digest,
            a.created_at,
            NULL::bigint AS size,
            COALESCE(p.publisher, ''::text) AS publisher,
            a.build_number,
            NULL::text AS content_type,
            split_part(a.coordinate, ':'::text, 3) AS version
           FROM (public.native_maven_artifacts a
             LEFT JOIN LATERAL ( SELECT s.publisher
                   FROM public.native_maven_publish_sessions s
                  WHERE ((s.repository_id = a.repository_id) AND (s.coordinate = a.coordinate) AND (s.state = 'committed'::text))
                  ORDER BY s.expires_at DESC
                 LIMIT 1) p ON (true))
          WHERE (a.state = 'visible'::text)
        UNION ALL
         SELECT m.repository_id,
            'oci'::text,
            m.name,
            m.digest,
            m.created_at,
            m.size,
            ''::text,
            0,
            m.media_type,
            NULL::text
           FROM public.native_oci_manifests m
        UNION ALL
         SELECT r.repository_id,
            'conan'::text,
            r.reference,
            r.digest,
            r.created_at,
            NULL::bigint AS int8,
            COALESCE(p.publisher, ''::text) AS "coalesce",
            0,
            NULL::text AS text,
            split_part(r.reference, '/'::text, 2) AS split_part
           FROM (public.native_conan_recipe_revisions r
             LEFT JOIN LATERAL ( SELECT s.publisher
                   FROM public.native_conan_publish_sessions s
                  WHERE ((s.repository_id = r.repository_id) AND (s.reference = r.reference) AND (s.state = 'committed'::text))
                  ORDER BY s.expires_at DESC
                 LIMIT 1) p ON (true))
          WHERE (r.state = 'visible'::text)
        UNION ALL
         SELECT a.repository_id,
            'raw'::text,
            a.path,
            a.digest,
            a.updated_at,
            o.size,
            ''::text,
            0,
            a.content_type,
            NULL::text
           FROM (public.native_raw_assets a
             JOIN public.native_raw_objects o ON ((o.digest = a.digest)))
        UNION ALL
         SELECT v.repository_id,
            'npm'::text,
            v.package_name,
            v.digest,
            v.created_at,
            v.size,
            v.publisher,
            0,
            'application/octet-stream'::text,
            v.version
           FROM (public.native_npm_versions v
             JOIN public.native_npm_packages p ON (((p.repository_id = v.repository_id) AND (p.name = v.package_name))))
          WHERE ((NOT p.negative) AND (v.state = 'visible'::text))
        UNION ALL
         SELECT f.repository_id,
            'pypi'::text,
            f.project,
            f.digest,
            f.created_at,
            f.size,
            f.publisher,
            0,
            'application/octet-stream'::text,
            f.version
           FROM public.native_pypi_files f
          WHERE (f.state = 'visible'::text)
        UNION ALL
         SELECT v.repository_id,
            'go'::text,
            v.module_path,
            COALESCE(a.digest, ''::text) AS "coalesce",
            v.created_at,
            a.size,
            v.publisher,
            0,
                CASE
                    WHEN (a.kind = 'zip'::text) THEN 'application/zip'::text
                    ELSE 'text/plain'::text
                END AS "case",
            v.version
           FROM (public.native_go_versions v
             LEFT JOIN LATERAL ( SELECT a_1.digest,
                    a_1.size,
                    a_1.kind
                   FROM public.native_go_assets a_1
                  WHERE ((a_1.repository_id = v.repository_id) AND (a_1.module_path = v.module_path) AND (a_1.version = v.version))
                  ORDER BY
                        CASE a_1.kind
                            WHEN 'zip'::text THEN 0
                            WHEN 'mod'::text THEN 1
                            ELSE 2
                        END
                 LIMIT 1) a ON (true))
        UNION ALL
         SELECT a.repository_id,
            'apt'::text,
            a.path,
            a.digest,
            a.created_at,
            a.size,
            ''::text AS text,
            0,
            a.content_type,
            NULL::text
           FROM public.native_apt_assets a
        UNION ALL
         SELECT a.repository_id,
            'apt'::text,
            a.path,
            a.digest,
            s.created_at,
            a.size,
            ''::text AS text,
            0,
            a.content_type,
            NULL::text
           FROM (public.native_apt_snapshot_assets a
             JOIN public.native_apt_repository_snapshots s ON ((s.id = a.snapshot_id)))
          WHERE (s.state = 'visible'::text)) projection;

--
-- Name: artifact_tombstones; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.artifact_tombstones (
    repository_id uuid NOT NULL,
    format text NOT NULL,
    coordinate text NOT NULL,
    digest text DEFAULT ''::text NOT NULL,
    tombstoned_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT artifact_tombstones_format_check CHECK ((format = ANY (ARRAY['raw'::text, 'oci'::text, 'maven'::text, 'conan'::text, 'npm'::text, 'pypi'::text, 'go'::text, 'apt'::text, 'cargo'::text])))
);

--
-- Name: artifact_usage_stats; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.artifact_usage_stats (
    repository text NOT NULL,
    format text NOT NULL,
    resource text NOT NULL,
    download_count bigint DEFAULT 0 NOT NULL,
    total_bytes bigint DEFAULT 0 NOT NULL,
    first_downloaded_at timestamp with time zone DEFAULT now() NOT NULL,
    last_downloaded_at timestamp with time zone DEFAULT now() NOT NULL,
    last_actor text DEFAULT ''::text NOT NULL
);

--
-- Name: audit_cleanup_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.audit_cleanup_jobs (
    id uuid NOT NULL,
    idempotency_key text NOT NULL,
    policy_version bigint NOT NULL,
    cutoff_at timestamp with time zone NOT NULL,
    batch_size integer NOT NULL,
    deleted integer DEFAULT 0 NOT NULL,
    state text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    CONSTRAINT audit_cleanup_jobs_batch_size_check CHECK (((batch_size >= 1) AND (batch_size <= 1000))),
    CONSTRAINT audit_cleanup_jobs_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'running'::text, 'completed'::text, 'failed'::text])))
);

--
-- Name: audit_retention_policy; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.audit_retention_policy (
    singleton boolean DEFAULT true NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    keep_days integer DEFAULT 0 NOT NULL,
    CONSTRAINT audit_retention_policy_keep_days_check CHECK ((keep_days >= 0)),
    CONSTRAINT audit_retention_policy_singleton_check CHECK (singleton)
);

--
-- Name: authorization_roles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.authorization_roles (
    id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    scopes text[] NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT authorization_roles_scopes_nonempty CHECK ((cardinality(scopes) > 0))
);

--
-- Name: authorization_templates; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.authorization_templates (
    id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    grants jsonb DEFAULT '[]'::jsonb NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: cache_circuit_breakers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.cache_circuit_breakers (
    key text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: cache_control_entries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.cache_control_entries (
    key text NOT NULL,
    value jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: cache_tasks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.cache_tasks (
    id uuid NOT NULL,
    task_type text NOT NULL,
    dedupe_key text NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    claimed_at timestamp with time zone,
    claim_token uuid,
    attempts integer DEFAULT 0 NOT NULL,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT cache_tasks_attempts_check CHECK ((attempts >= 0))
);

--
-- Name: conan_group_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.conan_group_members (
    group_name text NOT NULL,
    name text NOT NULL,
    member_type text NOT NULL,
    endpoint text NOT NULL,
    "position" integer NOT NULL,
    anonymous boolean DEFAULT false NOT NULL,
    allowed_hosts text[] DEFAULT '{}'::text[] NOT NULL,
    repository_id uuid,
    CONSTRAINT conan_group_members_member_type_check CHECK ((member_type = ANY (ARRAY['hosted'::text, 'proxy'::text]))),
    CONSTRAINT conan_group_members_position_check CHECK (("position" >= 0))
);

--
-- Name: conan_groups; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.conan_groups (
    name text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    anonymous boolean DEFAULT false NOT NULL,
    cache_quota_bytes bigint DEFAULT 1073741824 NOT NULL,
    CONSTRAINT conan_groups_cache_quota_bytes_check CHECK ((cache_quota_bytes > 0))
);

--
-- Name: console_theme_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.console_theme_packages (
    id text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT console_theme_packages_id_valid CHECK ((id ~ '^[a-z0-9][a-z0-9-]{1,63}$'::text)),
    CONSTRAINT console_theme_packages_payload_object CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT console_theme_packages_version_check CHECK ((version > 0))
);

--
-- Name: email_targets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_targets (
    id uuid NOT NULL,
    name text NOT NULL,
    locale text NOT NULL,
    recipient_ciphertext text NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT email_targets_locale_check CHECK ((locale = ANY (ARRAY['en'::text, 'zh-CN'::text]))),
    CONSTRAINT email_targets_name_check CHECK (((length(name) >= 1) AND (length(name) <= 128)))
);

--
-- Name: email_test_deliveries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_test_deliveries (
    id uuid NOT NULL,
    event_id uuid NOT NULL,
    request_key uuid NOT NULL,
    target_id uuid NOT NULL,
    target_version bigint NOT NULL,
    scenario text NOT NULL,
    locale text NOT NULL,
    template_version text NOT NULL,
    recipient_ciphertext text NOT NULL,
    from_address text NOT NULL,
    console_origin text NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    possible_duplicate boolean DEFAULT false NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    error_code text DEFAULT ''::text NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_token uuid,
    lease_expires_at timestamp with time zone,
    next_attempt_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    accepted_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    kind text DEFAULT 'test'::text NOT NULL,
    quota_rule_id uuid,
    episode_id uuid,
    event_sequence bigint,
    quota_descriptor jsonb,
    automatic_cancellation_code text DEFAULT ''::text NOT NULL,
    CONSTRAINT email_quota_descriptor_shape CHECK ((((kind = 'test'::text) AND (quota_rule_id IS NULL) AND (episode_id IS NULL) AND (event_sequence IS NULL) AND (quota_descriptor IS NULL)) OR ((kind = 'repository_quota'::text) AND (quota_rule_id IS NOT NULL) AND (episode_id IS NOT NULL) AND (event_sequence > 0) AND (quota_descriptor IS NOT NULL) AND (octet_length((quota_descriptor)::text) <= 8192)))),
    CONSTRAINT email_quota_sequence_required CHECK (((kind <> 'repository_quota'::text) OR (event_sequence IS NOT NULL))),
    CONSTRAINT email_test_deliveries_attempts_check CHECK (((attempts >= 0) AND (attempts <= 8))),
    CONSTRAINT email_test_deliveries_automatic_cancellation_code_check CHECK ((automatic_cancellation_code = ANY (ARRAY[''::text, 'rule_changed'::text, 'rule_disabled'::text, 'rule_deleted'::text]))),
    CONSTRAINT email_test_deliveries_kind_check CHECK ((kind = ANY (ARRAY['test'::text, 'repository_quota'::text]))),
    CONSTRAINT email_test_deliveries_locale_check CHECK ((locale = ANY (ARRAY['en'::text, 'zh-CN'::text]))),
    CONSTRAINT email_test_deliveries_scenario_check CHECK ((scenario = ANY (ARRAY['warning'::text, 'critical'::text, 'resolved'::text]))),
    CONSTRAINT email_test_deliveries_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'delivering'::text, 'retrying'::text, 'accepted'::text, 'dead'::text])))
);

--
-- Name: email_test_requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.email_test_requests (
    id bigint NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL
);

--
-- Name: email_test_requests_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.email_test_requests_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: email_test_requests_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.email_test_requests_id_seq OWNED BY public.email_test_requests.id;

--
-- Name: hosted_group_idempotency; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hosted_group_idempotency (
    actor text NOT NULL,
    key text NOT NULL,
    payload_hash text NOT NULL,
    group_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT hosted_group_idempotency_key_check CHECK (((char_length(key) >= 1) AND (char_length(key) <= 128)))
);

--
-- Name: hosted_group_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hosted_group_members (
    group_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    "position" integer NOT NULL,
    CONSTRAINT hosted_group_members_position_check CHECK (("position" >= 0))
);

--
-- Name: hosted_groups; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hosted_groups (
    id uuid NOT NULL,
    name text NOT NULL,
    format text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    anonymous_read boolean DEFAULT false NOT NULL,
    CONSTRAINT hosted_groups_format_check CHECK ((format = ANY (ARRAY['raw'::text, 'oci'::text, 'maven'::text, 'conan'::text, 'npm'::text, 'pypi'::text, 'go'::text, 'apt'::text, 'cargo'::text, 'nuget'::text, 'helm'::text]))),
    CONSTRAINT hosted_groups_name_check CHECK ((name ~ '^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$'::text))
);

--
-- Name: hosted_repositories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hosted_repositories (
    id uuid NOT NULL,
    name text NOT NULL,
    format text NOT NULL,
    state text DEFAULT 'active'::text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    repo_type text DEFAULT 'hosted'::text NOT NULL,
    endpoint text DEFAULT ''::text NOT NULL,
    allowed_hosts text[] DEFAULT '{}'::text[] NOT NULL,
    anonymous_read boolean DEFAULT false NOT NULL,
    egress_proxy jsonb,
    maven_strict_publication boolean DEFAULT false NOT NULL,
    upstream_auth jsonb,
    oci_bearer jsonb,
    CONSTRAINT hosted_repositories_format_check CHECK ((format = ANY (ARRAY['raw'::text, 'oci'::text, 'maven'::text, 'conan'::text, 'npm'::text, 'pypi'::text, 'go'::text, 'apt'::text, 'cargo'::text, 'nuget'::text, 'helm'::text, 'rubygems'::text]))),
    CONSTRAINT hosted_repositories_maven_strict_publication_check CHECK (((NOT maven_strict_publication) OR ((format = 'maven'::text) AND (repo_type = 'hosted'::text)))),
    CONSTRAINT hosted_repositories_name_check CHECK ((name ~ '^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$'::text)),
    CONSTRAINT hosted_repositories_repo_shape_check CHECK ((((repo_type = 'hosted'::text) AND (endpoint = ''::text)) OR ((repo_type = 'proxy'::text) AND (endpoint <> ''::text)))),
    CONSTRAINT hosted_repositories_repo_type_check CHECK ((repo_type = ANY (ARRAY['hosted'::text, 'proxy'::text]))),
    CONSTRAINT hosted_repositories_state_check CHECK ((state = ANY (ARRAY['active'::text, 'deleting'::text, 'deleted'::text])))
);

--
-- Name: hosted_repository_idempotency; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.hosted_repository_idempotency (
    actor text NOT NULL,
    target text NOT NULL,
    key text NOT NULL,
    payload_hash text NOT NULL,
    repository_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT hosted_repository_idempotency_key_check CHECK (((char_length(key) >= 1) AND (char_length(key) <= 128)))
);

--
-- Name: installation_repository_presets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.installation_repository_presets (
    singleton boolean DEFAULT true NOT NULL,
    catalog_version text NOT NULL,
    state text NOT NULL,
    repositories_created integer DEFAULT 0 NOT NULL,
    groups_created integer DEFAULT 0 NOT NULL,
    finished_at timestamp with time zone,
    CONSTRAINT installation_repository_presets_groups_created_check CHECK ((groups_created >= 0)),
    CONSTRAINT installation_repository_presets_repositories_created_check CHECK ((repositories_created >= 0)),
    CONSTRAINT installation_repository_presets_singleton_check CHECK (singleton),
    CONSTRAINT installation_repository_presets_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'applied'::text, 'skipped_existing'::text])))
);

--
-- Name: lifecycle_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.lifecycle_jobs (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    kind text NOT NULL,
    idempotency_key text NOT NULL,
    state text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 3 NOT NULL,
    next_attempt_at timestamp with time zone,
    lease_expires_at timestamp with time zone,
    lease_token text DEFAULT ''::text NOT NULL,
    progress_current integer DEFAULT 0 NOT NULL,
    progress_total integer DEFAULT 0 NOT NULL,
    progress_message text DEFAULT ''::text NOT NULL,
    CONSTRAINT lifecycle_jobs_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT lifecycle_jobs_kind_check CHECK ((kind = ANY (ARRAY['retention'::text, 'promotion'::text, 'replication'::text, 'reclaim'::text, 'intelligence'::text, 'scan'::text]))),
    CONSTRAINT lifecycle_jobs_max_attempts_check CHECK (((max_attempts >= 1) AND (max_attempts <= 20))),
    CONSTRAINT lifecycle_jobs_progress_current_check CHECK ((progress_current >= 0)),
    CONSTRAINT lifecycle_jobs_progress_total_check CHECK ((progress_total >= 0)),
    CONSTRAINT lifecycle_jobs_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'running'::text, 'retrying'::text, 'completed'::text, 'failed'::text, 'cancelled'::text])))
);

--
-- Name: maven_group_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.maven_group_members (
    group_name text NOT NULL,
    name text NOT NULL,
    member_type text NOT NULL,
    endpoint text NOT NULL,
    "position" integer NOT NULL,
    anonymous boolean DEFAULT false NOT NULL,
    allowed_hosts text[] DEFAULT '{}'::text[] NOT NULL,
    repository_id uuid,
    CONSTRAINT maven_group_members_member_type_check CHECK ((member_type = ANY (ARRAY['hosted'::text, 'proxy'::text]))),
    CONSTRAINT maven_group_members_position_check CHECK (("position" >= 0))
);

--
-- Name: maven_groups; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.maven_groups (
    name text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    anonymous boolean DEFAULT false NOT NULL
);

--
-- Name: native_apt_archive_object_intents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_apt_archive_object_intents (
    restore_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    object_key text NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    reclaim_scheduled_at timestamp with time zone,
    collected_at timestamp with time zone,
    CONSTRAINT native_apt_archive_object_intents_check CHECK ((object_key = ('native/apt/sha256/'::text || substr(digest, 8)))),
    CONSTRAINT native_apt_archive_object_intents_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_apt_archive_object_intents_size_check CHECK (((size >= 0) AND (size <= 1073741824) AND ((size > 0) OR (digest = 'sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'::text))))
);

--
-- Name: native_apt_archive_restores; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_apt_archive_restores (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    snapshot_id uuid NOT NULL,
    plan_digest text NOT NULL,
    state text NOT NULL,
    reserved_bytes bigint NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    finished_at timestamp with time zone,
    CONSTRAINT native_apt_archive_restores_plan_digest_check CHECK ((plan_digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_apt_archive_restores_reserved_bytes_check CHECK ((reserved_bytes >= 0)),
    CONSTRAINT native_apt_archive_restores_state_check CHECK ((state = ANY (ARRAY['preparing'::text, 'completed'::text, 'failed'::text])))
);

--
-- Name: native_apt_lifecycle_results; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_apt_lifecycle_results (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    request_digest text NOT NULL,
    snapshot_id uuid NOT NULL
);

--
-- Name: native_apt_package_deletions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_apt_package_deletions (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    suite text NOT NULL,
    data jsonb NOT NULL,
    reclaim_scheduled_at timestamp with time zone,
    collected_at timestamp with time zone
);

--
-- Name: native_apt_package_revisions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_apt_package_revisions (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    package_name text NOT NULL,
    version text NOT NULL,
    architecture text NOT NULL,
    canonical_identity text NOT NULL,
    digest text NOT NULL,
    object_key text NOT NULL,
    size bigint NOT NULL,
    object_name text NOT NULL,
    publisher text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_apt_package_revisions_architecture_check CHECK ((architecture ~ '^[a-z0-9][a-z0-9-]*$'::text)),
    CONSTRAINT native_apt_package_revisions_check CHECK (((canonical_identity = ((((package_name || '@'::text) || version) || '#'::text) || architecture)) AND (length(canonical_identity) <= 1024))),
    CONSTRAINT native_apt_package_revisions_check1 CHECK ((object_key = ('native/apt/sha256/'::text || substr(digest, 8)))),
    CONSTRAINT native_apt_package_revisions_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_apt_package_revisions_object_name_check CHECK (((object_name <> ''::text) AND (length(object_name) <= 255) AND (object_name !~ '/'::text) AND (object_name ~~ '%.deb'::text))),
    CONSTRAINT native_apt_package_revisions_package_name_check CHECK ((package_name ~ '^[a-z0-9][a-z0-9+.-]+$'::text)),
    CONSTRAINT native_apt_package_revisions_publisher_check CHECK (((publisher <> ''::text) AND (length(publisher) <= 512))),
    CONSTRAINT native_apt_package_revisions_size_check CHECK (((size > 0) AND (size <= 1073741824))),
    CONSTRAINT native_apt_package_revisions_version_check CHECK (((version ~ '^([0-9]+:)?[0-9][A-Za-z0-9.+:~-]*$'::text) AND (length(version) <= 255)))
);

--
-- Name: native_apt_pool_paths; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_apt_pool_paths (
    repository_id uuid NOT NULL,
    path text NOT NULL,
    digest text NOT NULL,
    object_key text NOT NULL,
    size bigint NOT NULL,
    content_type text NOT NULL,
    CONSTRAINT native_apt_pool_paths_check CHECK ((object_key = ('native/apt/sha256/'::text || substr(digest, 8)))),
    CONSTRAINT native_apt_pool_paths_content_type_check CHECK (((content_type <> ''::text) AND (length(content_type) <= 255))),
    CONSTRAINT native_apt_pool_paths_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_apt_pool_paths_path_check CHECK (((path ~ '^pool/'::text) AND (length(path) <= 2048))),
    CONSTRAINT native_apt_pool_paths_size_check CHECK (((size > 0) AND (size <= 1073741824)))
);

--
-- Name: native_apt_publication_idempotency; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_apt_publication_idempotency (
    actor text NOT NULL,
    target text NOT NULL,
    key text NOT NULL,
    payload_hash text NOT NULL,
    session_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL
);

--
-- Name: native_apt_publication_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_apt_publication_sessions (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    suite text NOT NULL,
    component text NOT NULL,
    publisher text NOT NULL,
    object_name text NOT NULL,
    declared_digest text NOT NULL,
    declared_size bigint NOT NULL,
    expected_identity text DEFAULT ''::text NOT NULL,
    object_key text DEFAULT ''::text NOT NULL,
    package_revision_id uuid,
    state text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    reclaim_scheduled_at timestamp with time zone,
    collected_at timestamp with time zone,
    CONSTRAINT native_apt_publication_sessions_check CHECK ((((state = 'open'::text) AND (object_key = ''::text) AND (package_revision_id IS NULL)) OR ((state = 'uploading'::text) AND (object_key = ('native/apt/sha256/'::text || substr(declared_digest, 8))) AND (package_revision_id IS NULL)) OR ((state = 'staged'::text) AND (object_key = ('native/apt/sha256/'::text || substr(declared_digest, 8))) AND (package_revision_id IS NOT NULL)) OR (state = 'aborted'::text))),
    CONSTRAINT native_apt_publication_sessions_component_check CHECK ((component ~ '^[a-z0-9][a-z0-9+.-]{0,127}$'::text)),
    CONSTRAINT native_apt_publication_sessions_declared_digest_check CHECK ((declared_digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_apt_publication_sessions_declared_size_check CHECK (((declared_size > 0) AND (declared_size <= 1073741824))),
    CONSTRAINT native_apt_publication_sessions_expected_identity_check CHECK ((length(expected_identity) <= 1024)),
    CONSTRAINT native_apt_publication_sessions_object_name_check CHECK (((object_name <> ''::text) AND (length(object_name) <= 255) AND (object_name !~ '/'::text) AND (object_name ~~ '%.deb'::text))),
    CONSTRAINT native_apt_publication_sessions_publisher_check CHECK (((publisher <> ''::text) AND (length(publisher) <= 512))),
    CONSTRAINT native_apt_publication_sessions_state_check CHECK ((state = ANY (ARRAY['open'::text, 'uploading'::text, 'staged'::text, 'aborted'::text]))),
    CONSTRAINT native_apt_publication_sessions_suite_check CHECK ((suite ~ '^[a-z0-9][a-z0-9+.-]{0,127}$'::text))
);

--
-- Name: native_apt_snapshot_object_intents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_apt_snapshot_object_intents (
    snapshot_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    object_key text NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    reclaim_scheduled_at timestamp with time zone,
    collected_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_apt_snapshot_object_intents_check CHECK ((object_key = ('native/apt/sha256/'::text || substr(digest, 8)))),
    CONSTRAINT native_apt_snapshot_object_intents_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_apt_snapshot_object_intents_size_check CHECK (((size >= 0) AND (size <= 1073741824) AND ((size > 0) OR (digest = 'sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'::text))))
);

--
-- Name: native_apt_snapshot_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_apt_snapshot_packages (
    snapshot_id uuid NOT NULL,
    publication_session_id uuid NOT NULL,
    package_revision_id uuid NOT NULL,
    component text NOT NULL,
    architecture text NOT NULL,
    CONSTRAINT native_apt_snapshot_packages_architecture_check CHECK ((architecture ~ '^[a-z0-9][a-z0-9-]*$'::text)),
    CONSTRAINT native_apt_snapshot_packages_component_check CHECK ((component ~ '^[a-z0-9][a-z0-9+.-]{0,127}$'::text))
);

--
-- Name: native_cargo_group_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_cargo_group_versions (
    group_id uuid NOT NULL,
    collision_key text NOT NULL,
    version_key text NOT NULL,
    source_repository_id uuid NOT NULL,
    name text NOT NULL,
    version text NOT NULL,
    checksum text NOT NULL,
    index_row bytea NOT NULL,
    CONSTRAINT native_cargo_group_versions_checksum_check CHECK ((checksum ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT native_cargo_group_versions_index_row_check CHECK ((octet_length(index_row) > 0))
);

--
-- Name: native_cargo_identity_reservations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_cargo_identity_reservations (
    repository_id uuid NOT NULL,
    collision_key text NOT NULL,
    version_key text NOT NULL,
    version text NOT NULL,
    digest text NOT NULL,
    metadata_digest text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_cargo_identity_reservations_check CHECK ((version_key = split_part(version, '+'::text, 1))),
    CONSTRAINT native_cargo_identity_reservations_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_cargo_identity_reservations_metadata_digest_check CHECK ((metadata_digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_cargo_identity_reservations_version_check CHECK (((length(version) >= 1) AND (length(version) <= 256)))
);

--
-- Name: native_cargo_names; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_cargo_names (
    repository_id uuid NOT NULL,
    collision_key text NOT NULL,
    name text NOT NULL,
    normalized_name text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_cargo_names_check CHECK ((normalized_name = lower(name))),
    CONSTRAINT native_cargo_names_check1 CHECK ((collision_key = replace(normalized_name, '_'::text, '-'::text))),
    CONSTRAINT native_cargo_names_name_check CHECK ((name ~ '^[A-Za-z][A-Za-z0-9_-]{0,63}$'::text))
);

--
-- Name: native_cargo_proxy_configs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_cargo_proxy_configs (
    repository_id uuid NOT NULL,
    download_template text NOT NULL,
    search_api text DEFAULT ''::text NOT NULL,
    fetched_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    source_version text DEFAULT ''::text NOT NULL
);

--
-- Name: native_cargo_proxy_crates; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_cargo_proxy_crates (
    repository_id uuid NOT NULL,
    collision_key text NOT NULL,
    version_key text NOT NULL,
    name text NOT NULL,
    version text NOT NULL,
    checksum text NOT NULL,
    object_key text NOT NULL,
    size bigint NOT NULL,
    cached_at timestamp with time zone NOT NULL,
    source_version text DEFAULT ''::text NOT NULL,
    CONSTRAINT native_cargo_proxy_crates_checksum_check CHECK ((checksum ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT native_cargo_proxy_crates_object_key_check CHECK ((object_key ~ '^native/cargo-proxy/sha256/[0-9a-f]{64}$'::text)),
    CONSTRAINT native_cargo_proxy_crates_size_check CHECK ((size > 0))
);

--
-- Name: native_cargo_proxy_indexes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_cargo_proxy_indexes (
    repository_id uuid NOT NULL,
    collision_key text NOT NULL,
    name text NOT NULL,
    body bytea DEFAULT '\x'::bytea NOT NULL,
    status integer NOT NULL,
    upstream_etag text DEFAULT ''::text NOT NULL,
    upstream_modified text DEFAULT ''::text NOT NULL,
    fetched_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    source_version text DEFAULT ''::text NOT NULL,
    CONSTRAINT native_cargo_proxy_indexes_check CHECK ((((status = 200) AND (octet_length(body) > 0)) OR ((status <> 200) AND (octet_length(body) = 0)))),
    CONSTRAINT native_cargo_proxy_indexes_status_check CHECK ((status = ANY (ARRAY[200, 404, 410, 451])))
);

--
-- Name: native_cargo_publications; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_cargo_publications (
    repository_id uuid NOT NULL,
    collision_key text NOT NULL,
    version_key text NOT NULL,
    object_key text NOT NULL,
    size bigint NOT NULL,
    index_row text NOT NULL,
    publisher text NOT NULL,
    published_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    yanked boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    collecting_at timestamp with time zone,
    collected_at timestamp with time zone,
    CONSTRAINT native_cargo_publications_collection_check CHECK (((collecting_at IS NULL) OR (collected_at IS NULL))),
    CONSTRAINT native_cargo_publications_index_row_check CHECK ((jsonb_typeof((index_row)::jsonb) = 'object'::text)),
    CONSTRAINT native_cargo_publications_object_key_check CHECK ((object_key ~ '^native/cargo/sha256/[0-9a-f]{64}$'::text)),
    CONSTRAINT native_cargo_publications_publisher_check CHECK ((publisher <> ''::text)),
    CONSTRAINT native_cargo_publications_size_check CHECK ((size > 0))
);

--
-- Name: native_conan_assets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_conan_assets (
    repository_id uuid NOT NULL,
    reference text NOT NULL,
    recipe_revision text NOT NULL,
    package_id text DEFAULT ''::text NOT NULL,
    package_revision text DEFAULT ''::text NOT NULL,
    path text NOT NULL,
    object_key text NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    CONSTRAINT native_conan_assets_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_conan_assets_size_check CHECK ((size >= 0))
);

--
-- Name: native_conan_object_intents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_conan_object_intents (
    object_key text NOT NULL,
    repository_id uuid NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    claimed_at timestamp with time zone,
    collected_at timestamp with time zone,
    collecting_at timestamp with time zone,
    CONSTRAINT native_conan_object_intents_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_conan_object_intents_size_check CHECK ((size >= 0))
);

--
-- Name: native_conan_package_revisions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_conan_package_revisions (
    repository_id uuid NOT NULL,
    reference text NOT NULL,
    recipe_revision text NOT NULL,
    package_id text NOT NULL,
    revision text NOT NULL,
    digest text NOT NULL,
    state text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_conan_package_revisions_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_conan_package_revisions_state_check CHECK ((state = ANY (ARRAY['visible'::text, 'deleted'::text])))
);

--
-- Name: native_conan_publish_uploads; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_conan_publish_uploads (
    session_id uuid NOT NULL,
    object_name text NOT NULL,
    object_key text NOT NULL
);

--
-- Name: native_helm_group_authorities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_helm_group_authorities (
    group_id uuid NOT NULL,
    name text NOT NULL,
    version text NOT NULL,
    entry_json jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: native_helm_publication_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_helm_publication_sessions (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    name text NOT NULL,
    version text NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    provenance_digest text DEFAULT ''::text NOT NULL,
    provenance_size bigint DEFAULT 0 NOT NULL,
    actor text NOT NULL,
    state text NOT NULL,
    object_key text NOT NULL,
    provenance_key text DEFAULT ''::text NOT NULL,
    metadata_json text NOT NULL,
    chart_yaml bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    committed_at timestamp with time zone,
    collected_at timestamp with time zone,
    source_version text DEFAULT ''::text NOT NULL,
    CONSTRAINT native_helm_publication_sessions_actor_check CHECK ((actor <> ''::text)),
    CONSTRAINT native_helm_publication_sessions_chart_yaml_check CHECK (((octet_length(chart_yaml) >= 1) AND (octet_length(chart_yaml) <= 1048576))),
    CONSTRAINT native_helm_publication_sessions_check CHECK ((((provenance_size = 0) AND (provenance_digest = ''::text) AND (provenance_key = ''::text)) OR ((provenance_size > 0) AND (provenance_digest ~ '^sha256:[0-9a-f]{64}$'::text) AND (provenance_key <> ''::text)))),
    CONSTRAINT native_helm_publication_sessions_check1 CHECK (((collected_at IS NULL) OR (state = 'expired'::text))),
    CONSTRAINT native_helm_publication_sessions_check2 CHECK (((state = 'committed'::text) = (committed_at IS NOT NULL))),
    CONSTRAINT native_helm_publication_sessions_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_helm_publication_sessions_metadata_json_check CHECK ((jsonb_typeof((metadata_json)::jsonb) = 'object'::text)),
    CONSTRAINT native_helm_publication_sessions_name_check CHECK ((((length(name) >= 1) AND (length(name) <= 63)) AND (name ~ '^[a-z0-9][a-z0-9-]*$'::text))),
    CONSTRAINT native_helm_publication_sessions_provenance_size_check CHECK (((provenance_size >= 0) AND (provenance_size <= 1048576))),
    CONSTRAINT native_helm_publication_sessions_size_check CHECK (((size > 0) AND (size <= 67108864))),
    CONSTRAINT native_helm_publication_sessions_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'committed'::text, 'expired'::text]))),
    CONSTRAINT native_helm_publication_sessions_version_check CHECK (((length(version) >= 1) AND (length(version) <= 256)))
);

--
-- Name: native_helm_publications; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_helm_publications (
    repository_id uuid NOT NULL,
    name text NOT NULL,
    version text NOT NULL,
    session_id uuid NOT NULL,
    digest text NOT NULL,
    object_key text NOT NULL,
    size bigint NOT NULL,
    provenance_digest text DEFAULT ''::text NOT NULL,
    provenance_key text DEFAULT ''::text NOT NULL,
    provenance_size bigint DEFAULT 0 NOT NULL,
    metadata_json text NOT NULL,
    chart_yaml bytea NOT NULL,
    publisher text NOT NULL,
    published_at timestamp with time zone NOT NULL,
    governance_version bigint DEFAULT 1 NOT NULL,
    tombstoned boolean DEFAULT false NOT NULL,
    quarantined boolean DEFAULT false NOT NULL,
    governance_updated_at timestamp with time zone,
    governance_updated_by text DEFAULT ''::text NOT NULL,
    governance_reason text DEFAULT ''::text NOT NULL,
    CONSTRAINT native_helm_governance_audit CHECK ((((governance_version = 1) AND (governance_updated_at IS NULL) AND (governance_updated_by = ''::text) AND (governance_reason = ''::text)) OR ((governance_version > 1) AND (governance_updated_at IS NOT NULL) AND (length(governance_updated_by) > 0) AND (length(btrim(governance_reason)) > 0)))),
    CONSTRAINT native_helm_publications_chart_yaml_check CHECK ((octet_length(chart_yaml) > 0)),
    CONSTRAINT native_helm_publications_check CHECK ((((provenance_size = 0) AND (provenance_digest = ''::text) AND (provenance_key = ''::text)) OR ((provenance_size > 0) AND (provenance_digest ~ '^sha256:[0-9a-f]{64}$'::text) AND (provenance_key <> ''::text)))),
    CONSTRAINT native_helm_publications_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_helm_publications_governance_version_check CHECK ((governance_version > 0)),
    CONSTRAINT native_helm_publications_metadata_json_check CHECK ((jsonb_typeof((metadata_json)::jsonb) = 'object'::text)),
    CONSTRAINT native_helm_publications_provenance_size_check CHECK ((provenance_size >= 0)),
    CONSTRAINT native_helm_publications_size_check CHECK ((size > 0))
);

--
-- Name: native_helm_replication_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_helm_replication_items (
    job_id uuid NOT NULL,
    item_index integer NOT NULL,
    name text NOT NULL,
    version text NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    provenance_digest text DEFAULT ''::text NOT NULL,
    provenance_size bigint DEFAULT 0 NOT NULL,
    source_governance_version bigint NOT NULL,
    state text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    code text DEFAULT ''::text NOT NULL,
    target_session_id uuid,
    result text DEFAULT ''::text NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT native_helm_replication_items_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT native_helm_replication_items_check CHECK ((((provenance_size = 0) AND (provenance_digest = ''::text)) OR ((provenance_size > 0) AND (provenance_digest ~ '^sha256:[0-9a-f]{64}$'::text)))),
    CONSTRAINT native_helm_replication_items_check1 CHECK (((state = ANY (ARRAY['succeeded'::text, 'failed'::text])) = (completed_at IS NOT NULL))),
    CONSTRAINT native_helm_replication_items_check2 CHECK (((state <> 'succeeded'::text) OR ((target_session_id IS NOT NULL) AND (result <> ''::text)))),
    CONSTRAINT native_helm_replication_items_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_helm_replication_items_item_index_check CHECK (((item_index >= 0) AND (item_index <= 99))),
    CONSTRAINT native_helm_replication_items_name_check CHECK ((((length(name) >= 1) AND (length(name) <= 63)) AND (name ~ '^[a-z0-9][a-z0-9-]*$'::text))),
    CONSTRAINT native_helm_replication_items_provenance_size_check CHECK (((provenance_size >= 0) AND (provenance_size <= 1048576))),
    CONSTRAINT native_helm_replication_items_result_check CHECK ((result = ANY (ARRAY[''::text, 'published'::text, 'already_present'::text]))),
    CONSTRAINT native_helm_replication_items_size_check CHECK (((size >= 1) AND (size <= 67108864))),
    CONSTRAINT native_helm_replication_items_source_governance_version_check CHECK ((source_governance_version > 0)),
    CONSTRAINT native_helm_replication_items_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'running'::text, 'succeeded'::text, 'failed'::text])))
);

--
-- Name: native_helm_replication_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_helm_replication_jobs (
    id uuid NOT NULL,
    plan_digest text NOT NULL,
    source_repository_id uuid NOT NULL,
    target_repository_id uuid NOT NULL,
    actor text NOT NULL,
    actor_kind text NOT NULL,
    credential_id text DEFAULT ''::text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    state text NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_token text DEFAULT ''::text NOT NULL,
    lease_fence bigint DEFAULT 0 NOT NULL,
    lease_until timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_helm_replication_jobs_actor_check CHECK (((length(actor) >= 1) AND (length(actor) <= 512))),
    CONSTRAINT native_helm_replication_jobs_actor_kind_check CHECK ((actor_kind = ANY (ARRAY['static_admin'::text, 'static_resolver'::text, 'local_session'::text, 'oidc'::text, 'api_key'::text, 'service_account_credential'::text]))),
    CONSTRAINT native_helm_replication_jobs_check CHECK ((source_repository_id <> target_repository_id)),
    CONSTRAINT native_helm_replication_jobs_check1 CHECK (((state = 'running'::text) = ((lease_owner <> ''::text) AND (lease_token <> ''::text) AND (lease_until IS NOT NULL)))),
    CONSTRAINT native_helm_replication_jobs_lease_fence_check CHECK ((lease_fence >= 0)),
    CONSTRAINT native_helm_replication_jobs_plan_digest_check CHECK ((plan_digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_helm_replication_jobs_state_check CHECK ((state = ANY (ARRAY['queued'::text, 'running'::text, 'succeeded'::text, 'failed'::text]))),
    CONSTRAINT native_helm_replication_jobs_version_check CHECK ((version > 0))
);

--
-- Name: native_helm_replication_retries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_helm_replication_retries (
    job_id uuid NOT NULL,
    request_key text NOT NULL,
    payload_digest text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_helm_replication_retries_payload_digest_check CHECK ((payload_digest ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT native_helm_replication_retries_request_key_check CHECK (((length(request_key) >= 1) AND (length(request_key) <= 200)))
);

--
-- Name: native_helm_source_documents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_helm_source_documents (
    repository_id uuid NOT NULL,
    repository_version text NOT NULL,
    body bytea NOT NULL,
    digest text NOT NULL,
    etag text DEFAULT ''::text NOT NULL,
    last_modified text DEFAULT ''::text NOT NULL,
    fetched_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT native_helm_source_documents_body_check CHECK (((octet_length(body) >= 1) AND (octet_length(body) <= 1048576))),
    CONSTRAINT native_helm_source_documents_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text))
);

--
-- Name: native_helm_source_snapshots; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_helm_source_snapshots (
    id uuid NOT NULL,
    view_id uuid NOT NULL,
    view_version text NOT NULL,
    digest text NOT NULL,
    entries_json jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_helm_source_snapshots_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_helm_source_snapshots_entries_json_check CHECK ((octet_length((entries_json)::text) <= 8388608))
);

--
-- Name: native_maven_assets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_maven_assets (
    repository_id uuid NOT NULL,
    path text NOT NULL,
    object_key text NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    CONSTRAINT native_maven_assets_size_check CHECK ((size >= 0))
);

--
-- Name: native_maven_commit_idempotency; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_maven_commit_idempotency (
    session_id uuid NOT NULL,
    key text NOT NULL,
    payload_hash text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT native_maven_commit_idempotency_key_check CHECK (((char_length(key) >= 1) AND (char_length(key) <= 128)))
);

--
-- Name: native_maven_object_intents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_maven_object_intents (
    object_key text NOT NULL,
    session_id uuid NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    claimed_at timestamp with time zone,
    deleted_at timestamp with time zone,
    claimed_token text,
    CONSTRAINT native_maven_object_intents_size_check CHECK ((size >= 0))
);

--
-- Name: native_maven_object_references; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_maven_object_references (
    object_key text NOT NULL,
    repository_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: native_maven_publish_idempotency; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_maven_publish_idempotency (
    actor text NOT NULL,
    target text NOT NULL,
    key text NOT NULL,
    payload_hash text NOT NULL,
    session_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT native_maven_publish_idempotency_key_check CHECK (((char_length(key) >= 1) AND (char_length(key) <= 128)))
);

--
-- Name: native_maven_publish_uploads; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_maven_publish_uploads (
    session_id uuid NOT NULL,
    object_name text NOT NULL,
    object_key text NOT NULL
);

--
-- Name: native_maven_snapshot_imports; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_maven_snapshot_imports (
    repository_id uuid NOT NULL,
    coordinate text NOT NULL,
    target_id text NOT NULL,
    target_binding text NOT NULL,
    source_id text NOT NULL,
    manifest_digest text NOT NULL,
    plan_digest text NOT NULL,
    session_id uuid NOT NULL,
    actor text NOT NULL,
    state text NOT NULL,
    metadata jsonb,
    aliases jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    takeover_key text DEFAULT ''::text NOT NULL,
    takeover_actor text DEFAULT ''::text NOT NULL,
    taken_over_at timestamp with time zone,
    current_build_number integer DEFAULT 0 NOT NULL,
    current_aliases jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT native_maven_snapshot_imports_current_build_number_check CHECK ((current_build_number >= 0)),
    CONSTRAINT native_maven_snapshot_imports_state_check CHECK ((state = ANY (ARRAY['staged'::text, 'committed'::text]))),
    CONSTRAINT native_maven_takeover_receipt_consistent CHECK ((((taken_over_at IS NULL) AND (takeover_key = ''::text) AND (takeover_actor = ''::text) AND (current_build_number = 0)) OR ((taken_over_at IS NOT NULL) AND (state = 'committed'::text) AND (takeover_key <> ''::text) AND (takeover_actor <> ''::text))))
);

--
-- Name: native_nuget_group_authorities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_nuget_group_authorities (
    group_id uuid NOT NULL,
    package_id text NOT NULL,
    version text NOT NULL,
    repository_id uuid NOT NULL,
    repository_version text NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_nuget_group_authorities_check CHECK (((digest = ''::text) = (size = 0))),
    CONSTRAINT native_nuget_group_authorities_digest_check CHECK (((digest = ''::text) OR (digest ~ '^sha256:[0-9a-f]{64}$'::text))),
    CONSTRAINT native_nuget_group_authorities_package_id_check CHECK ((((length(package_id) >= 1) AND (length(package_id) <= 100)) AND (package_id ~ '^[a-z0-9_]+([.-][a-z0-9_]+)*$'::text))),
    CONSTRAINT native_nuget_group_authorities_repository_version_check CHECK ((repository_version ~ '^[1-9][0-9]*$'::text)),
    CONSTRAINT native_nuget_group_authorities_size_check CHECK ((size >= 0)),
    CONSTRAINT native_nuget_group_authorities_version_check CHECK ((((length(version) >= 1) AND (length(version) <= 256)) AND (version = lower(version))))
);

--
-- Name: native_nuget_publication_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_nuget_publication_sessions (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    package_id text NOT NULL,
    version text NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    actor text NOT NULL,
    state text NOT NULL,
    object_key text NOT NULL,
    metadata_json text DEFAULT ''::text NOT NULL,
    nuspec bytea DEFAULT '\x'::bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    committed_at timestamp with time zone,
    collected_at timestamp with time zone,
    source_version text DEFAULT ''::text NOT NULL,
    source_url text DEFAULT ''::text NOT NULL,
    registration_url text DEFAULT ''::text NOT NULL,
    CONSTRAINT native_nuget_publication_sessions_actor_check CHECK ((actor <> ''::text)),
    CONSTRAINT native_nuget_publication_sessions_check CHECK (((collected_at IS NULL) OR (state = 'expired'::text))),
    CONSTRAINT native_nuget_publication_sessions_check1 CHECK (((state <> ALL (ARRAY['staged'::text, 'committed'::text])) OR ((length(metadata_json) > 0) AND (jsonb_typeof((metadata_json)::jsonb) = 'object'::text) AND (octet_length(nuspec) > 0)))),
    CONSTRAINT native_nuget_publication_sessions_check2 CHECK (((state = 'committed'::text) = (committed_at IS NOT NULL))),
    CONSTRAINT native_nuget_publication_sessions_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_nuget_publication_sessions_package_id_check CHECK ((((length(package_id) >= 1) AND (length(package_id) <= 100)) AND (package_id ~ '^[a-z0-9_]+([.-][a-z0-9_]+)*$'::text))),
    CONSTRAINT native_nuget_publication_sessions_size_check CHECK (((size > 0) AND (size <= 268435456))),
    CONSTRAINT native_nuget_publication_sessions_state_check CHECK ((state = ANY (ARRAY['reserved'::text, 'staged'::text, 'committed'::text, 'expired'::text]))),
    CONSTRAINT native_nuget_publication_sessions_version_check CHECK ((((length(version) >= 1) AND (length(version) <= 256)) AND (version = lower(version))))
);

--
-- Name: native_nuget_publications; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_nuget_publications (
    repository_id uuid NOT NULL,
    package_id text NOT NULL,
    version text NOT NULL,
    session_id uuid NOT NULL,
    digest text NOT NULL,
    object_key text NOT NULL,
    size bigint NOT NULL,
    metadata_json text NOT NULL,
    nuspec bytea NOT NULL,
    publisher text NOT NULL,
    published_at timestamp with time zone NOT NULL,
    source_version text DEFAULT ''::text NOT NULL,
    source_url text DEFAULT ''::text NOT NULL,
    registration_url text DEFAULT ''::text NOT NULL,
    governance_version bigint DEFAULT 1 NOT NULL,
    listed boolean DEFAULT true NOT NULL,
    tombstoned boolean DEFAULT false NOT NULL,
    quarantined boolean DEFAULT false NOT NULL,
    governance_updated_at timestamp with time zone,
    governance_updated_by text DEFAULT ''::text NOT NULL,
    governance_reason text DEFAULT ''::text NOT NULL,
    CONSTRAINT native_nuget_publications_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_nuget_publications_governance_reason_check CHECK ((length(governance_reason) <= 2048)),
    CONSTRAINT native_nuget_publications_governance_updated_by_check CHECK ((length(governance_updated_by) <= 512)),
    CONSTRAINT native_nuget_publications_governance_version_check CHECK ((governance_version > 0)),
    CONSTRAINT native_nuget_publications_metadata_json_check CHECK ((jsonb_typeof((metadata_json)::jsonb) = 'object'::text)),
    CONSTRAINT native_nuget_publications_nuspec_check CHECK ((octet_length(nuspec) > 0)),
    CONSTRAINT native_nuget_publications_size_check CHECK ((size > 0))
);

--
-- Name: native_nuget_replication_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_nuget_replication_items (
    job_id uuid NOT NULL,
    item_index integer NOT NULL,
    package_id text NOT NULL,
    version text NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    source_governance_version bigint NOT NULL,
    state text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    code text DEFAULT ''::text NOT NULL,
    target_session_id uuid,
    result text DEFAULT ''::text NOT NULL,
    listed boolean,
    completed_at timestamp with time zone,
    CONSTRAINT native_nuget_replication_items_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT native_nuget_replication_items_check CHECK (((state = ANY (ARRAY['succeeded'::text, 'failed'::text])) = (completed_at IS NOT NULL))),
    CONSTRAINT native_nuget_replication_items_check1 CHECK (((state <> 'succeeded'::text) OR ((target_session_id IS NOT NULL) AND (result <> ''::text) AND (listed IS NOT NULL)))),
    CONSTRAINT native_nuget_replication_items_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_nuget_replication_items_item_index_check CHECK (((item_index >= 0) AND (item_index <= 99))),
    CONSTRAINT native_nuget_replication_items_package_id_check CHECK ((((length(package_id) >= 1) AND (length(package_id) <= 100)) AND (package_id ~ '^[a-z0-9_]+([.-][a-z0-9_]+)*$'::text))),
    CONSTRAINT native_nuget_replication_items_result_check CHECK ((result = ANY (ARRAY[''::text, 'published'::text, 'already_present'::text]))),
    CONSTRAINT native_nuget_replication_items_size_check CHECK (((size >= 1) AND (size <= 268435456))),
    CONSTRAINT native_nuget_replication_items_source_governance_version_check CHECK ((source_governance_version > 0)),
    CONSTRAINT native_nuget_replication_items_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'running'::text, 'succeeded'::text, 'failed'::text])))
);

--
-- Name: native_nuget_replication_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_nuget_replication_jobs (
    id uuid NOT NULL,
    plan_digest text NOT NULL,
    source_repository_id uuid NOT NULL,
    target_repository_id uuid NOT NULL,
    actor text NOT NULL,
    actor_kind text NOT NULL,
    credential_id text DEFAULT ''::text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    state text NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_token text DEFAULT ''::text NOT NULL,
    lease_fence bigint DEFAULT 0 NOT NULL,
    lease_until timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_nuget_replication_jobs_actor_check CHECK (((length(actor) >= 1) AND (length(actor) <= 512))),
    CONSTRAINT native_nuget_replication_jobs_actor_kind_check CHECK ((actor_kind = ANY (ARRAY['static_admin'::text, 'static_resolver'::text, 'local_session'::text, 'oidc'::text, 'api_key'::text, 'service_account_credential'::text]))),
    CONSTRAINT native_nuget_replication_jobs_check CHECK ((source_repository_id <> target_repository_id)),
    CONSTRAINT native_nuget_replication_jobs_check1 CHECK (((state = 'running'::text) = ((lease_owner <> ''::text) AND (lease_token <> ''::text) AND (lease_until IS NOT NULL)))),
    CONSTRAINT native_nuget_replication_jobs_lease_fence_check CHECK ((lease_fence >= 0)),
    CONSTRAINT native_nuget_replication_jobs_plan_digest_check CHECK ((plan_digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_nuget_replication_jobs_state_check CHECK ((state = ANY (ARRAY['queued'::text, 'running'::text, 'succeeded'::text, 'failed'::text]))),
    CONSTRAINT native_nuget_replication_jobs_version_check CHECK ((version > 0))
);

--
-- Name: native_nuget_replication_retries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_nuget_replication_retries (
    job_id uuid NOT NULL,
    request_key text NOT NULL,
    payload_digest text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_nuget_replication_retries_payload_digest_check CHECK ((payload_digest ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT native_nuget_replication_retries_request_key_check CHECK (((length(request_key) >= 1) AND (length(request_key) <= 200)))
);

--
-- Name: native_nuget_source_documents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_nuget_source_documents (
    repository_id uuid NOT NULL,
    source_version text NOT NULL,
    url text NOT NULL,
    status integer NOT NULL,
    body bytea NOT NULL,
    digest text NOT NULL,
    etag text NOT NULL,
    last_modified text NOT NULL,
    fetched_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT native_nuget_source_documents_body_check CHECK ((octet_length(body) <= 8388608)),
    CONSTRAINT native_nuget_source_documents_check CHECK ((expires_at > fetched_at)),
    CONSTRAINT native_nuget_source_documents_check1 CHECK (((status = 200) OR (octet_length(body) = 0))),
    CONSTRAINT native_nuget_source_documents_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_nuget_source_documents_source_version_check CHECK ((source_version ~ '^[1-9][0-9]*$'::text)),
    CONSTRAINT native_nuget_source_documents_status_check CHECK ((status = ANY (ARRAY[200, 404]))),
    CONSTRAINT native_nuget_source_documents_url_check CHECK (((length(url) >= 1) AND (length(url) <= 4096)))
);

--
-- Name: native_oci_blobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_oci_blobs (
    digest text NOT NULL,
    object_key text NOT NULL,
    size bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_oci_blobs_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_oci_blobs_size_check CHECK ((size >= 0))
);

--
-- Name: native_oci_manifest_tombstones; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_oci_manifest_tombstones (
    repository_id uuid NOT NULL,
    name text NOT NULL,
    digest text NOT NULL,
    object_key text NOT NULL,
    media_type text NOT NULL,
    size bigint NOT NULL,
    subject_digest text,
    artifact_type text DEFAULT ''::text NOT NULL,
    tags text[] DEFAULT '{}'::text[] NOT NULL,
    deleted_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_oci_manifest_tombstones_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_oci_manifest_tombstones_size_check CHECK ((size >= 0))
);

--
-- Name: native_oci_object_intents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_oci_object_intents (
    object_key text NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    claimed_at timestamp with time zone,
    collected_at timestamp with time zone,
    repository_id uuid,
    CONSTRAINT native_oci_object_intents_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_oci_object_intents_size_check CHECK ((size >= 0))
);

--
-- Name: native_oci_repository_blobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_oci_repository_blobs (
    repository_id uuid NOT NULL,
    digest text NOT NULL
);

--
-- Name: native_oci_tags; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_oci_tags (
    repository_id uuid NOT NULL,
    name text NOT NULL,
    tag text NOT NULL,
    digest text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: native_oci_uploads; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_oci_uploads (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    name text NOT NULL,
    object_key text NOT NULL,
    byte_offset bigint DEFAULT 0 NOT NULL,
    state text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    collected_at timestamp with time zone,
    CONSTRAINT native_oci_uploads_byte_offset_check CHECK ((byte_offset >= 0)),
    CONSTRAINT native_oci_uploads_state_check CHECK ((state = ANY (ARRAY['open'::text, 'completed'::text, 'expired'::text])))
);

--
-- Name: native_raw_asset_tombstones; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_raw_asset_tombstones (
    repository_id uuid NOT NULL,
    path text NOT NULL,
    digest text NOT NULL,
    content_type text NOT NULL,
    deleted_at timestamp with time zone DEFAULT now() NOT NULL
);

--
-- Name: native_raw_uploads; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_raw_uploads (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    path text NOT NULL,
    object_key text NOT NULL,
    byte_offset bigint DEFAULT 0 NOT NULL,
    state text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    collected_at timestamp with time zone,
    CONSTRAINT native_raw_uploads_byte_offset_check CHECK ((byte_offset >= 0)),
    CONSTRAINT native_raw_uploads_state_check CHECK ((state = ANY (ARRAY['open'::text, 'completed'::text, 'cancelled'::text])))
);

--
-- Name: native_rubygems_quotas; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_rubygems_quotas (
    repository_id uuid NOT NULL,
    quota_bytes bigint NOT NULL,
    CONSTRAINT native_rubygems_quotas_quota_bytes_check CHECK ((quota_bytes >= 0))
);

--
-- Name: native_rubygems_replication_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_rubygems_replication_items (
    job_id uuid NOT NULL,
    item_index integer NOT NULL,
    name text NOT NULL,
    version text NOT NULL,
    canonical_version text NOT NULL,
    platform text NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    source_governance_version bigint NOT NULL,
    state text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    code text DEFAULT ''::text NOT NULL,
    target_session_id uuid,
    result text DEFAULT ''::text NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT native_rubygems_replication_ite_source_governance_version_check CHECK ((source_governance_version > 0)),
    CONSTRAINT native_rubygems_replication_items_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT native_rubygems_replication_items_check CHECK (((state = ANY (ARRAY['succeeded'::text, 'failed'::text])) = (completed_at IS NOT NULL))),
    CONSTRAINT native_rubygems_replication_items_check1 CHECK (((state <> 'succeeded'::text) OR ((target_session_id IS NOT NULL) AND (result <> ''::text)))),
    CONSTRAINT native_rubygems_replication_items_code_check CHECK ((code = ANY (ARRAY[''::text, 'access_denied'::text, 'source_changed'::text, 'target_conflict'::text, 'identity_conflict'::text, 'quota_exceeded'::text, 'transfer_failed'::text]))),
    CONSTRAINT native_rubygems_replication_items_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_rubygems_replication_items_item_index_check CHECK (((item_index >= 0) AND (item_index <= 99))),
    CONSTRAINT native_rubygems_replication_items_name_check CHECK (((length(name) >= 1) AND (length(name) <= 128))),
    CONSTRAINT native_rubygems_replication_items_platform_check CHECK (((length(platform) >= 1) AND (length(platform) <= 128))),
    CONSTRAINT native_rubygems_replication_items_result_check CHECK ((result = ANY (ARRAY[''::text, 'published'::text, 'already_present'::text]))),
    CONSTRAINT native_rubygems_replication_items_size_check CHECK (((size >= 1) AND (size <= 67108864))),
    CONSTRAINT native_rubygems_replication_items_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'running'::text, 'succeeded'::text, 'failed'::text]))),
    CONSTRAINT native_rubygems_replication_items_version_check CHECK (((length(version) >= 1) AND (length(version) <= 128)))
);

--
-- Name: native_rubygems_replication_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_rubygems_replication_jobs (
    id uuid NOT NULL,
    plan_digest text NOT NULL,
    source_repository_id uuid NOT NULL,
    target_repository_id uuid NOT NULL,
    actor text NOT NULL,
    actor_kind text NOT NULL,
    credential_id text DEFAULT ''::text NOT NULL,
    user_id text DEFAULT ''::text NOT NULL,
    session_id text DEFAULT ''::text NOT NULL,
    session_version bigint DEFAULT 0 NOT NULL,
    credential_until timestamp with time zone,
    version bigint DEFAULT 1 NOT NULL,
    state text NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_token text DEFAULT ''::text NOT NULL,
    lease_fence bigint DEFAULT 0 NOT NULL,
    lease_until timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_rubygems_replication_jobs_actor_check CHECK (((length(actor) >= 1) AND (length(actor) <= 256))),
    CONSTRAINT native_rubygems_replication_jobs_actor_kind_check CHECK ((actor_kind = ANY (ARRAY['static_admin'::text, 'static_resolver'::text, 'local_session'::text, 'oidc'::text, 'api_key'::text, 'service_account_credential'::text]))),
    CONSTRAINT native_rubygems_replication_jobs_check CHECK ((source_repository_id <> target_repository_id)),
    CONSTRAINT native_rubygems_replication_jobs_check1 CHECK (((state = 'running'::text) = ((lease_owner <> ''::text) AND (lease_token <> ''::text) AND (lease_until IS NOT NULL)))),
    CONSTRAINT native_rubygems_replication_jobs_lease_fence_check CHECK ((lease_fence >= 0)),
    CONSTRAINT native_rubygems_replication_jobs_plan_digest_check CHECK ((plan_digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_rubygems_replication_jobs_session_version_check CHECK ((session_version >= 0)),
    CONSTRAINT native_rubygems_replication_jobs_state_check CHECK ((state = ANY (ARRAY['queued'::text, 'running'::text, 'succeeded'::text, 'failed'::text]))),
    CONSTRAINT native_rubygems_replication_jobs_version_check CHECK ((version > 0))
);

--
-- Name: native_rubygems_replication_retries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_rubygems_replication_retries (
    job_id uuid NOT NULL,
    request_key text NOT NULL,
    payload_digest text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT native_rubygems_replication_retries_payload_digest_check CHECK ((payload_digest ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT native_rubygems_replication_retries_request_key_check CHECK (((length(request_key) >= 1) AND (length(request_key) <= 200)))
);

--
-- Name: native_rubygems_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.native_rubygems_sessions (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    name text NOT NULL,
    version text NOT NULL,
    canonical_version text NOT NULL,
    platform text NOT NULL,
    full_name text NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    actor text NOT NULL,
    state text NOT NULL,
    object_key text NOT NULL,
    metadata_json text NOT NULL,
    metadata_yaml bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    committed_at timestamp with time zone,
    collected_at timestamp with time zone,
    governance_version bigint DEFAULT 1 NOT NULL,
    yanked boolean DEFAULT false NOT NULL,
    governance_updated_at timestamp with time zone,
    governance_updated_by text DEFAULT ''::text NOT NULL,
    governance_reason text DEFAULT ''::text NOT NULL,
    quarantined boolean DEFAULT false NOT NULL,
    CONSTRAINT native_rubygems_governance_state CHECK ((((governance_version = 1) AND (NOT yanked) AND (governance_updated_at IS NULL) AND (governance_updated_by = ''::text) AND (governance_reason = ''::text)) OR ((governance_version > 1) AND (state = 'committed'::text) AND (governance_updated_at IS NOT NULL) AND ((length(governance_updated_by) >= 1) AND (length(governance_updated_by) <= 256)) AND ((length(governance_reason) >= 1) AND (length(governance_reason) <= 2048))))),
    CONSTRAINT native_rubygems_sessions_actor_check CHECK ((actor <> ''::text)),
    CONSTRAINT native_rubygems_sessions_check CHECK (((state = 'committed'::text) = (committed_at IS NOT NULL))),
    CONSTRAINT native_rubygems_sessions_check1 CHECK (((collected_at IS NULL) OR (state = 'expired'::text))),
    CONSTRAINT native_rubygems_sessions_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT native_rubygems_sessions_governance_version_check CHECK ((governance_version >= 1)),
    CONSTRAINT native_rubygems_sessions_metadata_json_check CHECK ((jsonb_typeof((metadata_json)::jsonb) = 'object'::text)),
    CONSTRAINT native_rubygems_sessions_metadata_yaml_check CHECK (((octet_length(metadata_yaml) >= 1) AND (octet_length(metadata_yaml) <= 1048576))),
    CONSTRAINT native_rubygems_sessions_name_check CHECK (((length(name) >= 1) AND (length(name) <= 128))),
    CONSTRAINT native_rubygems_sessions_platform_check CHECK (((length(platform) >= 1) AND (length(platform) <= 128))),
    CONSTRAINT native_rubygems_sessions_size_check CHECK (((size >= 1) AND (size <= 67108864))),
    CONSTRAINT native_rubygems_sessions_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'committed'::text, 'expired'::text]))),
    CONSTRAINT native_rubygems_sessions_version_check CHECK (((length(version) >= 1) AND (length(version) <= 128)))
);

--
-- Name: nexus_migration_batches; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.nexus_migration_batches (
    id uuid NOT NULL,
    actor text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    request jsonb NOT NULL,
    capacity_evidence jsonb NOT NULL,
    source_id uuid NOT NULL,
    source_version text NOT NULL,
    state text DEFAULT 'scanning'::text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    manifest_sha256 text DEFAULT ''::text NOT NULL,
    scan_complete boolean DEFAULT false NOT NULL,
    metadata_discovered bigint DEFAULT 0 NOT NULL,
    source_verification_bytes bigint DEFAULT 0 NOT NULL,
    entry_count bigint DEFAULT 0 NOT NULL,
    total_bytes bigint,
    lease_token text DEFAULT ''::text NOT NULL,
    lease_epoch bigint DEFAULT 0 NOT NULL,
    lease_expires_at timestamp with time zone,
    pause_requested boolean DEFAULT false NOT NULL,
    cancel_requested boolean DEFAULT false NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    last_scan_failure jsonb,
    schedule_id uuid,
    schedule_epoch bigint,
    actor_kind text DEFAULT ''::text NOT NULL,
    owner_id uuid,
    CONSTRAINT nexus_migration_batches_actor_kind_check CHECK ((actor_kind = ANY (ARRAY[''::text, 'static_admin'::text, 'static_resolver'::text, 'local_session'::text, 'oidc'::text, 'api_key'::text, 'service_account_credential'::text]))),
    CONSTRAINT nexus_migration_batches_entry_count_check CHECK ((entry_count >= 0)),
    CONSTRAINT nexus_migration_batches_last_scan_failure_check CHECK (((last_scan_failure IS NULL) OR (jsonb_typeof(last_scan_failure) = 'object'::text))),
    CONSTRAINT nexus_migration_batches_metadata_discovered_check CHECK ((metadata_discovered >= 0)),
    CONSTRAINT nexus_migration_batches_source_verification_bytes_check CHECK ((source_verification_bytes >= 0)),
    CONSTRAINT nexus_migration_batches_state_check CHECK ((state = ANY (ARRAY['scanning'::text, 'ready'::text, 'running'::text, 'pausing'::text, 'paused'::text, 'blocked'::text, 'completed'::text, 'failed'::text, 'cancelled'::text]))),
    CONSTRAINT nexus_migration_batches_total_bytes_check CHECK ((total_bytes >= 0))
);

--
-- Name: nexus_migration_controls; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.nexus_migration_controls (
    batch_id uuid NOT NULL,
    actor text NOT NULL,
    idempotency_key text NOT NULL,
    operation text NOT NULL,
    request_hash text NOT NULL,
    response jsonb NOT NULL
);

--
-- Name: nexus_migration_entries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.nexus_migration_entries (
    id uuid NOT NULL,
    batch_id uuid NOT NULL,
    ordinal integer NOT NULL,
    source_repository text NOT NULL,
    target_repository_id uuid NOT NULL,
    format text NOT NULL,
    identity text NOT NULL,
    state text NOT NULL,
    assets jsonb NOT NULL,
    session_id text DEFAULT ''::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    transferred_bytes bigint DEFAULT 0 NOT NULL,
    verified_bytes bigint DEFAULT 0 NOT NULL,
    visible_identities bigint DEFAULT 0 NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT nexus_migration_entries_attempts_check CHECK (((attempts >= 0) AND (attempts <= 3))),
    CONSTRAINT nexus_migration_entries_format_check CHECK ((format = ANY (ARRAY['raw'::text, 'maven'::text, 'oci'::text, 'conan'::text, 'npm'::text, 'pypi'::text]))),
    CONSTRAINT nexus_migration_entries_ordinal_check CHECK ((ordinal >= 0)),
    CONSTRAINT nexus_migration_entries_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'uploaded'::text, 'verified'::text, 'committed'::text, 'skipped'::text, 'conflict'::text, 'failed'::text, 'unsupported'::text, 'source_missing'::text, 'source_deleted'::text]))),
    CONSTRAINT nexus_migration_entries_transferred_bytes_check CHECK ((transferred_bytes >= 0)),
    CONSTRAINT nexus_migration_entries_verified_bytes_check CHECK ((verified_bytes >= 0)),
    CONSTRAINT nexus_migration_entries_visible_identities_check CHECK ((visible_identities >= 0))
);

--
-- Name: nexus_migration_scan_pages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.nexus_migration_scan_pages (
    batch_id uuid NOT NULL,
    ordinal bigint NOT NULL,
    source_repository text NOT NULL,
    cursor text NOT NULL,
    cursor_sha256 text NOT NULL,
    page_sha256 text NOT NULL,
    page jsonb NOT NULL,
    CONSTRAINT nexus_migration_scan_pages_cursor_check CHECK ((length(cursor) <= 4096)),
    CONSTRAINT nexus_migration_scan_pages_cursor_sha256_check CHECK ((cursor_sha256 ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT nexus_migration_scan_pages_ordinal_check CHECK ((ordinal >= 0)),
    CONSTRAINT nexus_migration_scan_pages_page_check CHECK ((jsonb_typeof(page) = 'object'::text)),
    CONSTRAINT nexus_migration_scan_pages_page_sha256_check CHECK ((page_sha256 ~ '^[0-9a-f]{64}$'::text))
);

--
-- Name: nexus_migration_scan_paths; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.nexus_migration_scan_paths (
    batch_id uuid NOT NULL,
    source_repository text NOT NULL,
    source_path text NOT NULL
);

--
-- Name: nexus_migration_scans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.nexus_migration_scans (
    batch_id uuid NOT NULL,
    binding_sha256 text NOT NULL,
    repositories jsonb NOT NULL,
    repository_index integer DEFAULT 0 NOT NULL,
    cursor text DEFAULT ''::text NOT NULL,
    page_count bigint DEFAULT 0 NOT NULL,
    verified_pages bigint DEFAULT 0 NOT NULL,
    asset_count bigint DEFAULT 0 NOT NULL,
    metadata_bytes bigint DEFAULT 0 NOT NULL,
    CONSTRAINT nexus_migration_scans_asset_count_check CHECK ((asset_count >= 0)),
    CONSTRAINT nexus_migration_scans_binding_sha256_check CHECK ((binding_sha256 ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT nexus_migration_scans_check CHECK (((verified_pages >= 0) AND (verified_pages <= page_count))),
    CONSTRAINT nexus_migration_scans_cursor_check CHECK ((length(cursor) <= 4096)),
    CONSTRAINT nexus_migration_scans_metadata_bytes_check CHECK ((metadata_bytes >= 0)),
    CONSTRAINT nexus_migration_scans_page_count_check CHECK ((page_count >= 0)),
    CONSTRAINT nexus_migration_scans_repositories_check CHECK ((jsonb_typeof(repositories) = 'array'::text)),
    CONSTRAINT nexus_migration_scans_repository_index_check CHECK ((repository_index >= 0))
);

--
-- Name: nexus_migration_schedule_controls; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.nexus_migration_schedule_controls (
    schedule_id uuid NOT NULL,
    actor text NOT NULL,
    idempotency_key text NOT NULL,
    operation text NOT NULL,
    request_hash text NOT NULL,
    response jsonb NOT NULL
);

--
-- Name: nexus_migration_schedules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.nexus_migration_schedules (
    id uuid NOT NULL,
    actor text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    request jsonb NOT NULL,
    state text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    interval_seconds integer NOT NULL,
    next_run_at timestamp with time zone,
    last_successful_at timestamp with time zone,
    consumed_observed_at timestamp with time zone,
    capacity_evidence jsonb,
    active_batch_id uuid,
    last_batch_id uuid,
    last_error text DEFAULT ''::text NOT NULL,
    dispatch_attempts integer DEFAULT 0 NOT NULL,
    lease_token text DEFAULT ''::text NOT NULL,
    lease_epoch bigint DEFAULT 0 NOT NULL,
    lease_expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    actor_kind text DEFAULT ''::text NOT NULL,
    owner_id uuid,
    CONSTRAINT nexus_migration_schedules_actor_kind_check CHECK ((actor_kind = ANY (ARRAY[''::text, 'static_admin'::text, 'static_resolver'::text, 'local_session'::text, 'oidc'::text, 'api_key'::text, 'service_account_credential'::text]))),
    CONSTRAINT nexus_migration_schedules_check CHECK (((state = 'stopped'::text) = (next_run_at IS NULL))),
    CONSTRAINT nexus_migration_schedules_dispatch_attempts_check CHECK (((dispatch_attempts >= 0) AND (dispatch_attempts <= 3))),
    CONSTRAINT nexus_migration_schedules_interval_seconds_check CHECK (((interval_seconds >= 60) AND (interval_seconds <= 2592000))),
    CONSTRAINT nexus_migration_schedules_last_error_check CHECK ((length(last_error) <= 128)),
    CONSTRAINT nexus_migration_schedules_state_check CHECK ((state = ANY (ARRAY['active'::text, 'waiting_capacity'::text, 'paused'::text, 'blocked'::text, 'stopped'::text]))),
    CONSTRAINT nexus_migration_schedules_version_check CHECK ((version > 0))
);

--
-- Name: nexus_migration_sources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.nexus_migration_sources (
    id uuid NOT NULL,
    name text NOT NULL,
    base_url text NOT NULL,
    credential_ciphertext text DEFAULT ''::text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT nexus_migration_sources_base_url_check CHECK (((base_url ~~ 'https://%'::text) AND (length(base_url) <= 2048))),
    CONSTRAINT nexus_migration_sources_name_check CHECK (((length(name) >= 1) AND (length(name) <= 128))),
    CONSTRAINT nexus_migration_sources_version_check CHECK ((version > 0))
);

--
-- Name: oci_bound_objects; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.oci_bound_objects (
    scope text NOT NULL,
    owner text NOT NULL,
    image text NOT NULL,
    digest text NOT NULL,
    subject text NOT NULL,
    source_id uuid NOT NULL,
    source_version bigint NOT NULL,
    payload jsonb NOT NULL,
    CONSTRAINT oci_bound_objects_digest_check CHECK ((digest ~ '^sha256:[a-f0-9]{64}$'::text)),
    CONSTRAINT oci_bound_objects_owner_check CHECK ((owner ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT oci_bound_objects_source_version_check CHECK ((source_version > 0))
);

--
-- Name: oci_discovery_snapshots; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.oci_discovery_snapshots (
    id uuid NOT NULL,
    owner text NOT NULL,
    kind text NOT NULL,
    payload jsonb NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT oci_discovery_snapshots_kind_check CHECK ((kind = ANY (ARRAY['catalog'::text, 'referrers'::text]))),
    CONSTRAINT oci_discovery_snapshots_payload_check CHECK ((octet_length((payload)::text) <= 8388608))
);

--
-- Name: oci_group_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.oci_group_members (
    group_name text NOT NULL,
    name text NOT NULL,
    member_type text NOT NULL,
    endpoint text NOT NULL,
    "position" integer NOT NULL,
    anonymous boolean DEFAULT false NOT NULL,
    allowed_hosts text[] DEFAULT '{}'::text[] NOT NULL,
    repository_id uuid,
    CONSTRAINT oci_group_members_member_type_check CHECK ((member_type = ANY (ARRAY['hosted'::text, 'proxy'::text]))),
    CONSTRAINT oci_group_members_position_check CHECK (("position" >= 0))
);

--
-- Name: oci_groups; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.oci_groups (
    name text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    anonymous boolean DEFAULT false NOT NULL
);

--
-- Name: oci_subject_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.oci_subject_bindings (
    scope text NOT NULL,
    owner text NOT NULL,
    image text NOT NULL,
    subject text NOT NULL,
    source_id uuid NOT NULL,
    source_version bigint NOT NULL,
    payload jsonb NOT NULL,
    CONSTRAINT oci_subject_bindings_owner_check CHECK ((owner ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT oci_subject_bindings_payload_check CHECK ((octet_length((payload)::text) <= 8388608)),
    CONSTRAINT oci_subject_bindings_source_version_check CHECK ((source_version > 0)),
    CONSTRAINT oci_subject_bindings_subject_check CHECK ((subject ~ '^sha256:[a-f0-9]{64}$'::text))
);

--
-- Name: oidc_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.oidc_settings (
    singleton boolean DEFAULT true NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    issuer text DEFAULT ''::text NOT NULL,
    audience text DEFAULT ''::text NOT NULL,
    jwks_url text DEFAULT ''::text NOT NULL,
    client_id text DEFAULT ''::text NOT NULL,
    client_secret text DEFAULT ''::text NOT NULL,
    redirect_url text DEFAULT ''::text NOT NULL,
    scopes jsonb DEFAULT '["openid", "profile", "email"]'::jsonb NOT NULL,
    admin_subjects jsonb DEFAULT '[]'::jsonb NOT NULL,
    member_roles jsonb DEFAULT '[]'::jsonb NOT NULL,
    admin_roles jsonb DEFAULT '[]'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    provisioning_mode text DEFAULT 'disabled'::text NOT NULL,
    email_linking_enabled boolean DEFAULT false NOT NULL,
    jit_default_role text DEFAULT 'reader'::text NOT NULL,
    CONSTRAINT oidc_settings_admin_roles_check CHECK ((jsonb_typeof(admin_roles) = 'array'::text)),
    CONSTRAINT oidc_settings_admin_subjects_check CHECK ((jsonb_typeof(admin_subjects) = 'array'::text)),
    CONSTRAINT oidc_settings_jit_default_role_check CHECK ((jit_default_role = ANY (ARRAY['none'::text, 'member'::text, 'admin'::text]))),
    CONSTRAINT oidc_settings_provisioning_mode_check CHECK ((provisioning_mode = ANY (ARRAY['disabled'::text, 'jit'::text]))),
    CONSTRAINT oidc_settings_reader_roles_check CHECK ((jsonb_typeof(member_roles) = 'array'::text)),
    CONSTRAINT oidc_settings_scopes_check CHECK ((jsonb_typeof(scopes) = 'array'::text)),
    CONSTRAINT oidc_settings_singleton_check CHECK (singleton),
    CONSTRAINT oidc_settings_version_check CHECK ((version > 0))
);

--
-- Name: raw_group_members; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.raw_group_members (
    group_name text NOT NULL,
    name text NOT NULL,
    member_type text NOT NULL,
    endpoint text NOT NULL,
    "position" integer NOT NULL,
    anonymous boolean DEFAULT false NOT NULL,
    allowed_hosts text[] DEFAULT '{}'::text[] NOT NULL,
    repository_id uuid,
    CONSTRAINT raw_group_members_member_type_check CHECK ((member_type = ANY (ARRAY['hosted'::text, 'proxy'::text]))),
    CONSTRAINT raw_group_members_position_check CHECK (("position" >= 0))
);

--
-- Name: raw_groups; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.raw_groups (
    name text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    anonymous boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    cache_quota_bytes bigint DEFAULT 1073741824 NOT NULL,
    CONSTRAINT raw_groups_cache_quota_bytes_check CHECK ((cache_quota_bytes > 0))
);

--
-- Name: replication_checkpoints; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.replication_checkpoints (
    plan_id uuid NOT NULL,
    object_key text NOT NULL,
    digest text NOT NULL,
    size bigint NOT NULL,
    byte_offset bigint DEFAULT 0 NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    verified_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    source_object_key text NOT NULL,
    CONSTRAINT replication_checkpoints_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT replication_checkpoints_byte_offset_check CHECK ((byte_offset >= 0)),
    CONSTRAINT replication_checkpoints_digest_check CHECK ((digest ~ '^sha256:[0-9a-f]{64}$'::text)),
    CONSTRAINT replication_checkpoints_size_check CHECK ((size >= 0)),
    CONSTRAINT replication_checkpoints_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'copying'::text, 'verified'::text, 'failed'::text])))
);

--
-- Name: replication_plans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.replication_plans (
    id uuid NOT NULL,
    source_repository_id uuid NOT NULL,
    target_repository_id uuid NOT NULL,
    format text NOT NULL,
    idempotency_key text NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT now(),
    lease_expires_at timestamp with time zone,
    lease_token text,
    attempts integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 3 NOT NULL,
    coordinate text DEFAULT ''::text NOT NULL,
    digest text DEFAULT ''::text NOT NULL,
    apt_target_suite text DEFAULT ''::text NOT NULL,
    CONSTRAINT replication_apt_target_suite_format CHECK (((apt_target_suite = ''::text) OR ((format = 'apt'::text) AND (apt_target_suite ~ '^[a-z0-9][a-z0-9.+-]*$'::text) AND (length(apt_target_suite) <= 128)))),
    CONSTRAINT replication_plans_artifact_identity_check CHECK ((((coordinate = ''::text) AND (digest = ''::text)) OR ((coordinate <> ''::text) AND (digest ~ '^sha256:[a-f0-9]{64}$'::text)))),
    CONSTRAINT replication_plans_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT replication_plans_format_check CHECK ((format = ANY (ARRAY['maven'::text, 'oci'::text, 'raw'::text, 'conan'::text, 'npm'::text, 'pypi'::text, 'go'::text, 'apt'::text, 'cargo'::text]))),
    CONSTRAINT replication_plans_max_attempts_check CHECK (((max_attempts >= 1) AND (max_attempts <= 10))),
    CONSTRAINT replication_plans_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'running'::text, 'completed'::text, 'failed'::text, 'cancelled'::text])))
);

--
-- Name: repository_capacity_quotas; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repository_capacity_quotas (
    repository_id uuid NOT NULL,
    quota_bytes bigint DEFAULT 0 NOT NULL,
    CONSTRAINT repository_capacity_quotas_quota_bytes_check CHECK ((quota_bytes >= 0))
);

--
-- Name: repository_grant_sets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repository_grant_sets (
    repository_id uuid NOT NULL,
    version bigint DEFAULT 1 NOT NULL
);

--
-- Name: repository_grants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repository_grants (
    repository_id uuid NOT NULL,
    principal text NOT NULL,
    scopes text[] NOT NULL,
    resource_prefix text DEFAULT ''::text NOT NULL
);

--
-- Name: repository_quarantine_read_policies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repository_quarantine_read_policies (
    repository_id uuid NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    CONSTRAINT repository_quarantine_read_policies_version_check CHECK ((version >= 1))
);

--
-- Name: repository_quota_alert_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repository_quota_alert_events (
    id uuid NOT NULL,
    rule_id uuid NOT NULL,
    rule_version bigint NOT NULL,
    episode_id uuid NOT NULL,
    sequence bigint NOT NULL,
    scenario text NOT NULL,
    snapshot jsonb NOT NULL,
    target_id uuid NOT NULL,
    target_version bigint NOT NULL,
    delivery_id uuid,
    notification_code text NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT repository_quota_alert_events_notification_code_check CHECK ((notification_code = ANY (ARRAY['queued'::text, 'queue_full'::text, 'email_disabled'::text, 'encryption_key_unavailable'::text, 'target_disabled'::text, 'target_changed'::text, 'target_unavailable'::text]))),
    CONSTRAINT repository_quota_alert_events_scenario_check CHECK ((scenario = ANY (ARRAY['warning'::text, 'critical'::text, 'resolved'::text]))),
    CONSTRAINT repository_quota_alert_events_sequence_check CHECK ((sequence > 0)),
    CONSTRAINT repository_quota_alert_events_snapshot_check CHECK ((octet_length((snapshot)::text) <= 8192))
);

--
-- Name: repository_quota_alert_rules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repository_quota_alert_rules (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    target_id uuid NOT NULL,
    target_version bigint NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    deleted boolean DEFAULT false NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    state_version bigint DEFAULT 1 NOT NULL,
    policy jsonb NOT NULL,
    state jsonb NOT NULL,
    active_episode_id uuid,
    last_event_id uuid,
    sequence bigint DEFAULT 0 NOT NULL,
    evaluated_at timestamp with time zone,
    next_evaluate_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    updated_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT repository_quota_alert_rules_check CHECK (((NOT deleted) OR (NOT enabled))),
    CONSTRAINT repository_quota_alert_rules_policy_check CHECK (((jsonb_typeof(policy) = 'object'::text) AND (octet_length((policy)::text) <= 2048))),
    CONSTRAINT repository_quota_alert_rules_policy_check1 CHECK ((policy ?& ARRAY['warningBasisPoints'::text, 'criticalBasisPoints'::text, 'recoveryBelowBasisPoints'::text, 'warningForSeconds'::text, 'criticalForSeconds'::text, 'recoveryForSeconds'::text, 'maxSampleAgeSeconds'::text])),
    CONSTRAINT repository_quota_alert_rules_policy_check2 CHECK (((((policy ->> 'recoveryBelowBasisPoints'::text))::integer > 0) AND (((policy ->> 'recoveryBelowBasisPoints'::text))::integer < ((policy ->> 'warningBasisPoints'::text))::integer) AND (((policy ->> 'warningBasisPoints'::text))::integer < ((policy ->> 'criticalBasisPoints'::text))::integer) AND (((policy ->> 'criticalBasisPoints'::text))::integer <= 10000))),
    CONSTRAINT repository_quota_alert_rules_policy_check3 CHECK ((((((policy ->> 'warningForSeconds'::text))::integer >= 1) AND (((policy ->> 'warningForSeconds'::text))::integer <= 86400)) AND ((((policy ->> 'criticalForSeconds'::text))::integer >= 1) AND (((policy ->> 'criticalForSeconds'::text))::integer <= 86400)) AND ((((policy ->> 'recoveryForSeconds'::text))::integer >= 1) AND (((policy ->> 'recoveryForSeconds'::text))::integer <= 86400)))),
    CONSTRAINT repository_quota_alert_rules_policy_check4 CHECK (((((policy ->> 'maxSampleAgeSeconds'::text))::integer >= 30) AND (((policy ->> 'maxSampleAgeSeconds'::text))::integer <= 3600))),
    CONSTRAINT repository_quota_alert_rules_sequence_check CHECK ((sequence >= 0)),
    CONSTRAINT repository_quota_alert_rules_state_check CHECK (((jsonb_typeof(state) = 'object'::text) AND (octet_length((state)::text) <= 4096)))
);

--
-- Name: repository_retention_policies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repository_retention_policies (
    repository_id uuid NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    keep_days integer NOT NULL,
    minimum_versions integer NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    snapshot_keep_days integer DEFAULT 30 NOT NULL,
    maximum_versions integer DEFAULT 0 NOT NULL,
    coordinate_patterns text[] DEFAULT '{}'::text[] NOT NULL,
    protected_patterns text[] DEFAULT '{}'::text[] NOT NULL,
    keep_downloaded_days integer DEFAULT 0 NOT NULL,
    CONSTRAINT repository_retention_policies_keep_days_check CHECK ((keep_days >= 1)),
    CONSTRAINT repository_retention_policies_maximum_versions_check CHECK ((maximum_versions >= 0)),
    CONSTRAINT repository_retention_policies_minimum_versions_check CHECK ((minimum_versions >= 1)),
    CONSTRAINT repository_retention_policies_snapshot_keep_days_check CHECK ((snapshot_keep_days >= 1)),
    CONSTRAINT repository_retention_versions_valid CHECK (((maximum_versions = 0) OR (maximum_versions >= minimum_versions)))
);

--
-- Name: repository_security_policies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repository_security_policies (
    repository_id uuid NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    require_signature boolean DEFAULT false NOT NULL,
    require_verified_signature boolean DEFAULT false NOT NULL,
    require_sbom boolean DEFAULT false NOT NULL,
    require_provenance boolean DEFAULT false NOT NULL,
    require_vulnerability_scan boolean DEFAULT false NOT NULL,
    max_allowed_severity text DEFAULT 'critical'::text NOT NULL,
    fail_on_scan_error boolean DEFAULT true NOT NULL,
    allowed_licenses text[] DEFAULT '{}'::text[] NOT NULL,
    auto_scan_on_publish boolean DEFAULT false NOT NULL,
    CONSTRAINT repository_security_policies_max_allowed_severity_check CHECK ((max_allowed_severity = ANY (ARRAY['none'::text, 'low'::text, 'medium'::text, 'high'::text, 'critical'::text]))),
    CONSTRAINT repository_security_policies_version_check CHECK ((version >= 1))
);

--
-- Name: resolver_audit_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.resolver_audit_log (
    id bigint NOT NULL,
    group_name text NOT NULL,
    repository text NOT NULL,
    member_name text,
    outcome text NOT NULL,
    actor text NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    format text,
    resource text,
    representation text,
    member_type text,
    upstream_host text,
    operation text,
    http_status integer,
    cache_disposition text,
    bytes bigint,
    request_id text,
    trace_id text,
    authorization_source text,
    authorization_reason text,
    evidence jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT resolver_audit_log_evidence_check CHECK ((jsonb_typeof(evidence) = 'object'::text))
);

--
-- Name: resolver_audit_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.resolver_audit_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

--
-- Name: resolver_audit_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.resolver_audit_log_id_seq OWNED BY public.resolver_audit_log.id;

--
-- Name: runtime_node_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.runtime_node_sessions (
    session_id text NOT NULL,
    instance_id text NOT NULL,
    roles jsonb DEFAULT '[]'::jsonb NOT NULL,
    worker_formats jsonb DEFAULT '[]'::jsonb NOT NULL,
    worker_kinds jsonb DEFAULT '[]'::jsonb NOT NULL,
    started_at timestamp with time zone NOT NULL,
    last_seen_at timestamp with time zone NOT NULL,
    stopped_at timestamp with time zone,
    build_version text,
    build_revision text
);

--
-- Name: runtime_nodes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.runtime_nodes (
    instance_id text NOT NULL,
    roles jsonb DEFAULT '[]'::jsonb NOT NULL,
    worker_formats jsonb DEFAULT '[]'::jsonb NOT NULL,
    worker_kinds jsonb DEFAULT '[]'::jsonb NOT NULL,
    started_at timestamp with time zone NOT NULL,
    last_seen_at timestamp with time zone NOT NULL,
    build_version text,
    build_revision text
);

--
-- Name: scheduled_task_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.scheduled_task_runs (
    id uuid NOT NULL,
    task_id uuid NOT NULL,
    trigger text NOT NULL,
    state text NOT NULL,
    scheduled_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    target_kind text DEFAULT ''::text NOT NULL,
    target_id text DEFAULT ''::text NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    CONSTRAINT scheduled_task_runs_state_check CHECK ((state = ANY (ARRAY['submitted'::text, 'failed'::text]))),
    CONSTRAINT scheduled_task_runs_trigger_check CHECK ((trigger = ANY (ARRAY['scheduled'::text, 'manual'::text])))
);

--
-- Name: scheduled_tasks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.scheduled_tasks (
    id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    kind text NOT NULL,
    repository_id uuid,
    interval_seconds integer NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    next_run_at timestamp with time zone NOT NULL,
    last_run_at timestamp with time zone,
    last_run_id uuid,
    last_run_state text DEFAULT 'failed'::text NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT scheduled_tasks_check CHECK ((((kind = 'audit-retention'::text) AND (repository_id IS NULL)) OR ((kind = 'repository-retention'::text) AND (repository_id IS NOT NULL)))),
    CONSTRAINT scheduled_tasks_description_check CHECK ((char_length(description) <= 500)),
    CONSTRAINT scheduled_tasks_interval_seconds_check CHECK (((interval_seconds >= 900) AND (interval_seconds <= 31536000))),
    CONSTRAINT scheduled_tasks_kind_check CHECK ((kind = ANY (ARRAY['repository-retention'::text, 'audit-retention'::text]))),
    CONSTRAINT scheduled_tasks_last_run_state_check CHECK ((last_run_state = ANY (ARRAY['submitted'::text, 'failed'::text]))),
    CONSTRAINT scheduled_tasks_name_check CHECK (((char_length(name) >= 1) AND (char_length(name) <= 100))),
    CONSTRAINT scheduled_tasks_version_check CHECK ((version >= 1))
);

--
-- Name: service_accounts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.service_accounts (
    id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    state text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT service_accounts_state_check CHECK ((state = ANY (ARRAY['active'::text, 'disabled'::text])))
);

--
-- Name: site_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.site_settings (
    singleton boolean DEFAULT true NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    site_name text DEFAULT 'Artifact Gateway'::text NOT NULL,
    logo_url text DEFAULT ''::text NOT NULL,
    brand_mark text DEFAULT 'AG'::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    enabled_theme_ids text[] DEFAULT ARRAY['gateway-dark'::text, 'gateway-light'::text, 'aerok-dark'::text, 'aerok-light'::text] NOT NULL,
    default_theme_id text DEFAULT 'gateway-dark'::text NOT NULL,
    CONSTRAINT site_settings_brand_mark_check CHECK (((length(btrim(brand_mark)) >= 1) AND (length(btrim(brand_mark)) <= 8))),
    CONSTRAINT site_settings_default_theme_nonempty CHECK (((length(btrim(default_theme_id)) >= 2) AND (length(btrim(default_theme_id)) <= 64))),
    CONSTRAINT site_settings_enabled_themes_nonempty CHECK (((cardinality(enabled_theme_ids) >= 1) AND (cardinality(enabled_theme_ids) <= 32))),
    CONSTRAINT site_settings_logo_url_check CHECK ((length(logo_url) <= 262144)),
    CONSTRAINT site_settings_singleton_check CHECK (singleton),
    CONSTRAINT site_settings_site_name_check CHECK (((length(btrim(site_name)) >= 1) AND (length(btrim(site_name)) <= 80))),
    CONSTRAINT site_settings_version_check CHECK ((version > 0))
);

--
-- Name: user_identities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_identities (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    issuer text NOT NULL,
    subject text NOT NULL,
    email text DEFAULT ''::text NOT NULL,
    display_name text DEFAULT ''::text NOT NULL,
    email_verified boolean DEFAULT false NOT NULL,
    last_login_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_identities_issuer_check CHECK ((issuer <> ''::text)),
    CONSTRAINT user_identities_kind_check CHECK ((kind = 'oidc'::text)),
    CONSTRAINT user_identities_subject_check CHECK ((subject <> ''::text))
);

--
-- Name: user_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_sessions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    ip_address text DEFAULT ''::text NOT NULL,
    user_agent text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone,
    CONSTRAINT user_sessions_check CHECK ((expires_at > created_at)),
    CONSTRAINT user_sessions_ip_address_check CHECK ((length(ip_address) <= 64)),
    CONSTRAINT user_sessions_kind_check CHECK ((kind = ANY (ARRAY['local_session'::text, 'oidc'::text]))),
    CONSTRAINT user_sessions_user_agent_check CHECK ((length(user_agent) <= 512))
);

--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id uuid NOT NULL,
    name text NOT NULL,
    secret_hash text NOT NULL,
    role text NOT NULL,
    state text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    display_name text DEFAULT ''::text NOT NULL,
    email text DEFAULT ''::text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    last_login_at timestamp with time zone,
    password_changed_at timestamp with time zone,
    failed_login_attempts integer DEFAULT 0 NOT NULL,
    locked_until timestamp with time zone,
    must_change_password boolean DEFAULT false NOT NULL,
    session_version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT users_failed_login_attempts_check CHECK ((failed_login_attempts >= 0)),
    CONSTRAINT users_role_check CHECK ((role = ANY (ARRAY['none'::text, 'member'::text, 'admin'::text]))),
    CONSTRAINT users_session_version_check CHECK ((session_version > 0))
);

--
-- Name: webhook_deliveries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.webhook_deliveries (
    id uuid NOT NULL,
    event_id uuid NOT NULL,
    subscription_id uuid NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone,
    lease_owner text,
    lease_token uuid,
    lease_expires_at timestamp with time zone,
    last_status integer,
    last_error text DEFAULT ''::text NOT NULL,
    delivered_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT webhook_deliveries_attempts_check CHECK (((attempts >= 0) AND (attempts <= 8))),
    CONSTRAINT webhook_deliveries_check CHECK (((state = 'delivering'::text) = ((lease_owner IS NOT NULL) AND (lease_token IS NOT NULL) AND (lease_expires_at IS NOT NULL)))),
    CONSTRAINT webhook_deliveries_check1 CHECK (((state = 'succeeded'::text) = (delivered_at IS NOT NULL))),
    CONSTRAINT webhook_deliveries_check2 CHECK (((state = ANY (ARRAY['pending'::text, 'retrying'::text])) = (next_attempt_at IS NOT NULL))),
    CONSTRAINT webhook_deliveries_last_error_check CHECK ((length(last_error) <= 1024)),
    CONSTRAINT webhook_deliveries_last_status_check CHECK (((last_status IS NULL) OR ((last_status >= 100) AND (last_status <= 599)))),
    CONSTRAINT webhook_deliveries_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'delivering'::text, 'retrying'::text, 'succeeded'::text, 'dead'::text])))
);

--
-- Name: webhook_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.webhook_events (
    id uuid NOT NULL,
    event_type text NOT NULL,
    occurred_at timestamp with time zone NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT webhook_events_event_type_check CHECK ((event_type = ANY (ARRAY['artifact.quarantined'::text, 'artifact.released'::text, 'cargo.promotion.completed'::text, 'cargo.promotion.failed'::text, 'cargo.replication.completed'::text, 'cargo.replication.failed'::text]))),
    CONSTRAINT webhook_events_payload_check CHECK ((jsonb_typeof(payload) = 'object'::text))
);

--
-- Name: webhook_subscriptions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.webhook_subscriptions (
    id uuid NOT NULL,
    name text NOT NULL,
    endpoint_url text NOT NULL,
    secret_ciphertext text NOT NULL,
    event_types jsonb NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT webhook_subscriptions_endpoint_url_check CHECK (((length(endpoint_url) >= 1) AND (length(endpoint_url) <= 2048))),
    CONSTRAINT webhook_subscriptions_event_types_check CHECK (((jsonb_typeof(event_types) = 'array'::text) AND (jsonb_array_length(event_types) > 0) AND (event_types <@ '["artifact.quarantined", "artifact.released", "cargo.promotion.completed", "cargo.promotion.failed", "cargo.replication.completed", "cargo.replication.failed"]'::jsonb))),
    CONSTRAINT webhook_subscriptions_name_check CHECK (((length(name) >= 1) AND (length(name) <= 128))),
    CONSTRAINT webhook_subscriptions_secret_ciphertext_check CHECK ((secret_ciphertext <> ''::text)),
    CONSTRAINT webhook_subscriptions_version_check CHECK ((version >= 1))
);

--
-- Name: email_test_requests id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_test_requests ALTER COLUMN id SET DEFAULT nextval('public.email_test_requests_id_seq'::regclass);

--
-- Name: resolver_audit_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resolver_audit_log ALTER COLUMN id SET DEFAULT nextval('public.resolver_audit_log_id_seq'::regclass);

--
-- Name: anonymous_access_policy anonymous_access_policy_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.anonymous_access_policy
    ADD CONSTRAINT anonymous_access_policy_pkey PRIMARY KEY (singleton);

--
-- Name: api_keys api_keys_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_pkey PRIMARY KEY (id);

--
-- Name: api_keys api_keys_secret_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_secret_hash_key UNIQUE (secret_hash);

--
-- Name: artifact_intelligence artifact_intelligence_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.artifact_intelligence
    ADD CONSTRAINT artifact_intelligence_pkey PRIMARY KEY (repository_id, format, coordinate, digest);

--
-- Name: artifact_quarantines artifact_quarantines_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.artifact_quarantines
    ADD CONSTRAINT artifact_quarantines_pkey PRIMARY KEY (repository_id, format, coordinate, digest);

--
-- Name: artifact_tombstones artifact_tombstones_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.artifact_tombstones
    ADD CONSTRAINT artifact_tombstones_pkey PRIMARY KEY (repository_id, format, coordinate);

--
-- Name: artifact_usage_stats artifact_usage_stats_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.artifact_usage_stats
    ADD CONSTRAINT artifact_usage_stats_pkey PRIMARY KEY (repository, format, resource);

--
-- Name: audit_cleanup_jobs audit_cleanup_jobs_idempotency_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_cleanup_jobs
    ADD CONSTRAINT audit_cleanup_jobs_idempotency_key_key UNIQUE (idempotency_key);

--
-- Name: audit_cleanup_jobs audit_cleanup_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_cleanup_jobs
    ADD CONSTRAINT audit_cleanup_jobs_pkey PRIMARY KEY (id);

--
-- Name: audit_retention_policy audit_retention_policy_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_retention_policy
    ADD CONSTRAINT audit_retention_policy_pkey PRIMARY KEY (singleton);

--
-- Name: authorization_roles authorization_roles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.authorization_roles
    ADD CONSTRAINT authorization_roles_pkey PRIMARY KEY (id);

--
-- Name: authorization_templates authorization_templates_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.authorization_templates
    ADD CONSTRAINT authorization_templates_pkey PRIMARY KEY (id);

--
-- Name: cache_circuit_breakers cache_circuit_breakers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cache_circuit_breakers
    ADD CONSTRAINT cache_circuit_breakers_pkey PRIMARY KEY (key);

--
-- Name: cache_control_entries cache_control_entries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cache_control_entries
    ADD CONSTRAINT cache_control_entries_pkey PRIMARY KEY (key);

--
-- Name: cache_tasks cache_tasks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cache_tasks
    ADD CONSTRAINT cache_tasks_pkey PRIMARY KEY (id);

--
-- Name: conan_group_members conan_group_members_group_name_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conan_group_members
    ADD CONSTRAINT conan_group_members_group_name_name_key UNIQUE (group_name, name);

--
-- Name: conan_group_members conan_group_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conan_group_members
    ADD CONSTRAINT conan_group_members_pkey PRIMARY KEY (group_name, "position");

--
-- Name: conan_group_members conan_group_proxy_allowlist_required; Type: CHECK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE public.conan_group_members
    ADD CONSTRAINT conan_group_proxy_allowlist_required CHECK (((member_type <> 'proxy'::text) OR (cardinality(allowed_hosts) > 0))) NOT VALID;

--
-- Name: conan_groups conan_groups_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conan_groups
    ADD CONSTRAINT conan_groups_pkey PRIMARY KEY (name);

--
-- Name: console_theme_packages console_theme_packages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.console_theme_packages
    ADD CONSTRAINT console_theme_packages_pkey PRIMARY KEY (id);

--
-- Name: email_targets email_targets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_targets
    ADD CONSTRAINT email_targets_pkey PRIMARY KEY (id);

--
-- Name: email_test_deliveries email_test_deliveries_event_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_test_deliveries
    ADD CONSTRAINT email_test_deliveries_event_id_key UNIQUE (event_id);

--
-- Name: email_test_deliveries email_test_deliveries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_test_deliveries
    ADD CONSTRAINT email_test_deliveries_pkey PRIMARY KEY (id);

--
-- Name: email_test_deliveries email_test_deliveries_request_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_test_deliveries
    ADD CONSTRAINT email_test_deliveries_request_key_key UNIQUE (request_key);

--
-- Name: email_test_requests email_test_requests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_test_requests
    ADD CONSTRAINT email_test_requests_pkey PRIMARY KEY (id);

--
-- Name: hosted_group_idempotency hosted_group_idempotency_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hosted_group_idempotency
    ADD CONSTRAINT hosted_group_idempotency_pkey PRIMARY KEY (actor, key);

--
-- Name: hosted_group_members hosted_group_members_group_id_repository_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hosted_group_members
    ADD CONSTRAINT hosted_group_members_group_id_repository_id_key UNIQUE (group_id, repository_id);

--
-- Name: hosted_group_members hosted_group_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hosted_group_members
    ADD CONSTRAINT hosted_group_members_pkey PRIMARY KEY (group_id, "position");

--
-- Name: hosted_groups hosted_groups_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hosted_groups
    ADD CONSTRAINT hosted_groups_name_key UNIQUE (name);

--
-- Name: hosted_groups hosted_groups_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hosted_groups
    ADD CONSTRAINT hosted_groups_pkey PRIMARY KEY (id);

--
-- Name: hosted_repositories hosted_repositories_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hosted_repositories
    ADD CONSTRAINT hosted_repositories_name_key UNIQUE (name);

--
-- Name: hosted_repositories hosted_repositories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hosted_repositories
    ADD CONSTRAINT hosted_repositories_pkey PRIMARY KEY (id);

--
-- Name: hosted_repository_idempotency hosted_repository_idempotency_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hosted_repository_idempotency
    ADD CONSTRAINT hosted_repository_idempotency_pkey PRIMARY KEY (actor, target, key);

--
-- Name: installation_repository_presets installation_repository_presets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.installation_repository_presets
    ADD CONSTRAINT installation_repository_presets_pkey PRIMARY KEY (singleton);

--
-- Name: lifecycle_jobs lifecycle_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.lifecycle_jobs
    ADD CONSTRAINT lifecycle_jobs_pkey PRIMARY KEY (id);

--
-- Name: lifecycle_jobs lifecycle_jobs_repository_id_kind_idempotency_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.lifecycle_jobs
    ADD CONSTRAINT lifecycle_jobs_repository_id_kind_idempotency_key_key UNIQUE (repository_id, kind, idempotency_key);

--
-- Name: maven_group_members maven_group_members_group_name_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.maven_group_members
    ADD CONSTRAINT maven_group_members_group_name_name_key UNIQUE (group_name, name);

--
-- Name: maven_group_members maven_group_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.maven_group_members
    ADD CONSTRAINT maven_group_members_pkey PRIMARY KEY (group_name, "position");

--
-- Name: maven_groups maven_groups_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.maven_groups
    ADD CONSTRAINT maven_groups_pkey PRIMARY KEY (name);

--
-- Name: native_apt_archive_object_intents native_apt_archive_object_intents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_archive_object_intents
    ADD CONSTRAINT native_apt_archive_object_intents_pkey PRIMARY KEY (restore_id, object_key);

--
-- Name: native_apt_archive_restores native_apt_archive_restores_id_repository_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_archive_restores
    ADD CONSTRAINT native_apt_archive_restores_id_repository_id_key UNIQUE (id, repository_id);

--
-- Name: native_apt_archive_restores native_apt_archive_restores_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_archive_restores
    ADD CONSTRAINT native_apt_archive_restores_pkey PRIMARY KEY (id);

--
-- Name: native_apt_assets native_apt_assets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_assets
    ADD CONSTRAINT native_apt_assets_pkey PRIMARY KEY (repository_id, path);

--
-- Name: native_apt_lifecycle_results native_apt_lifecycle_results_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_lifecycle_results
    ADD CONSTRAINT native_apt_lifecycle_results_pkey PRIMARY KEY (id);

--
-- Name: native_apt_package_deletions native_apt_package_deletions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_package_deletions
    ADD CONSTRAINT native_apt_package_deletions_pkey PRIMARY KEY (id);

--
-- Name: native_apt_package_revisions native_apt_package_revisions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_package_revisions
    ADD CONSTRAINT native_apt_package_revisions_pkey PRIMARY KEY (id);

--
-- Name: native_apt_package_revisions native_apt_package_revisions_repository_id_canonical_identi_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_package_revisions
    ADD CONSTRAINT native_apt_package_revisions_repository_id_canonical_identi_key UNIQUE (repository_id, canonical_identity);

--
-- Name: native_apt_pool_paths native_apt_pool_paths_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_pool_paths
    ADD CONSTRAINT native_apt_pool_paths_pkey PRIMARY KEY (repository_id, path);

--
-- Name: native_apt_publication_idempotency native_apt_publication_idempotency_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_publication_idempotency
    ADD CONSTRAINT native_apt_publication_idempotency_pkey PRIMARY KEY (actor, target, key);

--
-- Name: native_apt_publication_sessions native_apt_publication_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_publication_sessions
    ADD CONSTRAINT native_apt_publication_sessions_pkey PRIMARY KEY (id);

--
-- Name: native_apt_repository_snapshots native_apt_repository_snapshot_repository_id_suite_sequence_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_repository_snapshots
    ADD CONSTRAINT native_apt_repository_snapshot_repository_id_suite_sequence_key UNIQUE (repository_id, suite, sequence);

--
-- Name: native_apt_repository_snapshots native_apt_repository_snapshots_id_repository_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_repository_snapshots
    ADD CONSTRAINT native_apt_repository_snapshots_id_repository_unique UNIQUE (id, repository_id);

--
-- Name: native_apt_repository_snapshots native_apt_repository_snapshots_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_repository_snapshots
    ADD CONSTRAINT native_apt_repository_snapshots_pkey PRIMARY KEY (id);

--
-- Name: native_apt_snapshot_assets native_apt_snapshot_assets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_snapshot_assets
    ADD CONSTRAINT native_apt_snapshot_assets_pkey PRIMARY KEY (snapshot_id, path);

--
-- Name: native_apt_snapshot_object_intents native_apt_snapshot_object_intents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_snapshot_object_intents
    ADD CONSTRAINT native_apt_snapshot_object_intents_pkey PRIMARY KEY (snapshot_id, object_key);

--
-- Name: native_apt_snapshot_packages native_apt_snapshot_packages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_snapshot_packages
    ADD CONSTRAINT native_apt_snapshot_packages_pkey PRIMARY KEY (snapshot_id, publication_session_id);

--
-- Name: native_apt_snapshot_packages native_apt_snapshot_packages_snapshot_id_package_revision_i_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_snapshot_packages
    ADD CONSTRAINT native_apt_snapshot_packages_snapshot_id_package_revision_i_key UNIQUE (snapshot_id, package_revision_id, component, architecture);

--
-- Name: native_cargo_group_versions native_cargo_group_versions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_cargo_group_versions
    ADD CONSTRAINT native_cargo_group_versions_pkey PRIMARY KEY (group_id, collision_key, version_key);

--
-- Name: native_cargo_identity_reservations native_cargo_identity_reservations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_cargo_identity_reservations
    ADD CONSTRAINT native_cargo_identity_reservations_pkey PRIMARY KEY (repository_id, collision_key, version_key);

--
-- Name: native_cargo_names native_cargo_names_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_cargo_names
    ADD CONSTRAINT native_cargo_names_pkey PRIMARY KEY (repository_id, collision_key);

--
-- Name: native_cargo_proxy_configs native_cargo_proxy_configs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_cargo_proxy_configs
    ADD CONSTRAINT native_cargo_proxy_configs_pkey PRIMARY KEY (repository_id);

--
-- Name: native_cargo_proxy_crates native_cargo_proxy_crates_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_cargo_proxy_crates
    ADD CONSTRAINT native_cargo_proxy_crates_pkey PRIMARY KEY (repository_id, collision_key, version_key);

--
-- Name: native_cargo_proxy_indexes native_cargo_proxy_indexes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_cargo_proxy_indexes
    ADD CONSTRAINT native_cargo_proxy_indexes_pkey PRIMARY KEY (repository_id, collision_key);

--
-- Name: native_cargo_publications native_cargo_publications_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_cargo_publications
    ADD CONSTRAINT native_cargo_publications_pkey PRIMARY KEY (repository_id, collision_key, version_key);

--
-- Name: native_conan_assets native_conan_assets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_conan_assets
    ADD CONSTRAINT native_conan_assets_pkey PRIMARY KEY (repository_id, reference, recipe_revision, package_id, package_revision, path);

--
-- Name: native_conan_object_intents native_conan_object_intents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_conan_object_intents
    ADD CONSTRAINT native_conan_object_intents_pkey PRIMARY KEY (object_key);

--
-- Name: native_conan_package_revisions native_conan_package_revisions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_conan_package_revisions
    ADD CONSTRAINT native_conan_package_revisions_pkey PRIMARY KEY (repository_id, reference, recipe_revision, package_id, revision);

--
-- Name: native_conan_publish_sessions native_conan_publish_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_conan_publish_sessions
    ADD CONSTRAINT native_conan_publish_sessions_pkey PRIMARY KEY (id);

--
-- Name: native_conan_publish_uploads native_conan_publish_uploads_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_conan_publish_uploads
    ADD CONSTRAINT native_conan_publish_uploads_pkey PRIMARY KEY (session_id, object_name);

--
-- Name: native_conan_recipe_revisions native_conan_recipe_revisions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_conan_recipe_revisions
    ADD CONSTRAINT native_conan_recipe_revisions_pkey PRIMARY KEY (repository_id, reference, revision);

--
-- Name: native_go_assets native_go_assets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_go_assets
    ADD CONSTRAINT native_go_assets_pkey PRIMARY KEY (repository_id, module_path, version, kind);

--
-- Name: native_go_versions native_go_versions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_go_versions
    ADD CONSTRAINT native_go_versions_pkey PRIMARY KEY (repository_id, module_path, version);

--
-- Name: native_helm_group_authorities native_helm_group_authorities_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_group_authorities
    ADD CONSTRAINT native_helm_group_authorities_pkey PRIMARY KEY (group_id, name, version);

--
-- Name: native_helm_publication_sessions native_helm_publication_sessions_object_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_publication_sessions
    ADD CONSTRAINT native_helm_publication_sessions_object_key_key UNIQUE (object_key);

--
-- Name: native_helm_publication_sessions native_helm_publication_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_publication_sessions
    ADD CONSTRAINT native_helm_publication_sessions_pkey PRIMARY KEY (id);

--
-- Name: native_helm_publications native_helm_publications_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_publications
    ADD CONSTRAINT native_helm_publications_pkey PRIMARY KEY (repository_id, name, version);

--
-- Name: native_helm_publications native_helm_publications_session_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_publications
    ADD CONSTRAINT native_helm_publications_session_id_key UNIQUE (session_id);

--
-- Name: native_helm_replication_items native_helm_replication_items_job_id_name_version_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_replication_items
    ADD CONSTRAINT native_helm_replication_items_job_id_name_version_key UNIQUE (job_id, name, version);

--
-- Name: native_helm_replication_items native_helm_replication_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_replication_items
    ADD CONSTRAINT native_helm_replication_items_pkey PRIMARY KEY (job_id, item_index);

--
-- Name: native_helm_replication_jobs native_helm_replication_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_replication_jobs
    ADD CONSTRAINT native_helm_replication_jobs_pkey PRIMARY KEY (id);

--
-- Name: native_helm_replication_retries native_helm_replication_retries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_replication_retries
    ADD CONSTRAINT native_helm_replication_retries_pkey PRIMARY KEY (job_id, request_key);

--
-- Name: native_helm_source_documents native_helm_source_documents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_source_documents
    ADD CONSTRAINT native_helm_source_documents_pkey PRIMARY KEY (repository_id);

--
-- Name: native_helm_source_snapshots native_helm_source_snapshots_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_source_snapshots
    ADD CONSTRAINT native_helm_source_snapshots_pkey PRIMARY KEY (id);

--
-- Name: native_maven_artifacts native_maven_artifacts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_artifacts
    ADD CONSTRAINT native_maven_artifacts_pkey PRIMARY KEY (id);

--
-- Name: native_maven_assets native_maven_assets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_assets
    ADD CONSTRAINT native_maven_assets_pkey PRIMARY KEY (repository_id, path);

--
-- Name: native_maven_commit_idempotency native_maven_commit_idempotency_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_commit_idempotency
    ADD CONSTRAINT native_maven_commit_idempotency_pkey PRIMARY KEY (session_id);

--
-- Name: native_maven_object_intents native_maven_object_intents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_object_intents
    ADD CONSTRAINT native_maven_object_intents_pkey PRIMARY KEY (object_key);

--
-- Name: native_maven_object_references native_maven_object_references_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_object_references
    ADD CONSTRAINT native_maven_object_references_pkey PRIMARY KEY (object_key);

--
-- Name: native_maven_publish_idempotency native_maven_publish_idempotency_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_publish_idempotency
    ADD CONSTRAINT native_maven_publish_idempotency_pkey PRIMARY KEY (actor, target, key);

--
-- Name: native_maven_publish_sessions native_maven_publish_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_publish_sessions
    ADD CONSTRAINT native_maven_publish_sessions_pkey PRIMARY KEY (id);

--
-- Name: native_maven_publish_uploads native_maven_publish_uploads_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_publish_uploads
    ADD CONSTRAINT native_maven_publish_uploads_pkey PRIMARY KEY (session_id, object_name);

--
-- Name: native_maven_snapshot_imports native_maven_snapshot_imports_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_snapshot_imports
    ADD CONSTRAINT native_maven_snapshot_imports_pkey PRIMARY KEY (repository_id, coordinate);

--
-- Name: native_npm_packages native_npm_packages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_npm_packages
    ADD CONSTRAINT native_npm_packages_pkey PRIMARY KEY (repository_id, name);

--
-- Name: native_npm_versions native_npm_versions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_npm_versions
    ADD CONSTRAINT native_npm_versions_pkey PRIMARY KEY (repository_id, package_name, version);

--
-- Name: native_npm_versions native_npm_versions_repository_id_package_name_tarball_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_npm_versions
    ADD CONSTRAINT native_npm_versions_repository_id_package_name_tarball_name_key UNIQUE (repository_id, package_name, tarball_name);

--
-- Name: native_nuget_group_authorities native_nuget_group_authorities_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_group_authorities
    ADD CONSTRAINT native_nuget_group_authorities_pkey PRIMARY KEY (group_id, package_id, version);

--
-- Name: native_nuget_publication_sessions native_nuget_publication_sessions_object_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_publication_sessions
    ADD CONSTRAINT native_nuget_publication_sessions_object_key_key UNIQUE (object_key);

--
-- Name: native_nuget_publication_sessions native_nuget_publication_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_publication_sessions
    ADD CONSTRAINT native_nuget_publication_sessions_pkey PRIMARY KEY (id);

--
-- Name: native_nuget_publications native_nuget_publications_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_publications
    ADD CONSTRAINT native_nuget_publications_pkey PRIMARY KEY (repository_id, package_id, version, source_version);

--
-- Name: native_nuget_publications native_nuget_publications_session_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_publications
    ADD CONSTRAINT native_nuget_publications_session_id_key UNIQUE (session_id);

--
-- Name: native_nuget_replication_items native_nuget_replication_items_job_id_package_id_version_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_replication_items
    ADD CONSTRAINT native_nuget_replication_items_job_id_package_id_version_key UNIQUE (job_id, package_id, version);

--
-- Name: native_nuget_replication_items native_nuget_replication_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_replication_items
    ADD CONSTRAINT native_nuget_replication_items_pkey PRIMARY KEY (job_id, item_index);

--
-- Name: native_nuget_replication_jobs native_nuget_replication_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_replication_jobs
    ADD CONSTRAINT native_nuget_replication_jobs_pkey PRIMARY KEY (id);

--
-- Name: native_nuget_replication_retries native_nuget_replication_retries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_replication_retries
    ADD CONSTRAINT native_nuget_replication_retries_pkey PRIMARY KEY (job_id, request_key);

--
-- Name: native_nuget_source_documents native_nuget_source_documents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_source_documents
    ADD CONSTRAINT native_nuget_source_documents_pkey PRIMARY KEY (repository_id, source_version, url);

--
-- Name: native_oci_blobs native_oci_blobs_object_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_blobs
    ADD CONSTRAINT native_oci_blobs_object_key_key UNIQUE (object_key);

--
-- Name: native_oci_blobs native_oci_blobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_blobs
    ADD CONSTRAINT native_oci_blobs_pkey PRIMARY KEY (digest);

--
-- Name: native_oci_manifest_tombstones native_oci_manifest_tombstones_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_manifest_tombstones
    ADD CONSTRAINT native_oci_manifest_tombstones_pkey PRIMARY KEY (repository_id, name, digest);

--
-- Name: native_oci_manifests native_oci_manifests_object_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_manifests
    ADD CONSTRAINT native_oci_manifests_object_key_key UNIQUE (object_key);

--
-- Name: native_oci_manifests native_oci_manifests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_manifests
    ADD CONSTRAINT native_oci_manifests_pkey PRIMARY KEY (repository_id, name, digest);

--
-- Name: native_oci_object_intents native_oci_object_intents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_object_intents
    ADD CONSTRAINT native_oci_object_intents_pkey PRIMARY KEY (object_key);

--
-- Name: native_oci_repository_blobs native_oci_repository_blobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_repository_blobs
    ADD CONSTRAINT native_oci_repository_blobs_pkey PRIMARY KEY (repository_id, digest);

--
-- Name: native_oci_tags native_oci_tags_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_tags
    ADD CONSTRAINT native_oci_tags_pkey PRIMARY KEY (repository_id, name, tag);

--
-- Name: native_oci_uploads native_oci_uploads_object_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_uploads
    ADD CONSTRAINT native_oci_uploads_object_key_key UNIQUE (object_key);

--
-- Name: native_oci_uploads native_oci_uploads_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_uploads
    ADD CONSTRAINT native_oci_uploads_pkey PRIMARY KEY (id);

--
-- Name: native_pypi_files native_pypi_files_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_pypi_files
    ADD CONSTRAINT native_pypi_files_pkey PRIMARY KEY (repository_id, filename);

--
-- Name: native_raw_asset_tombstones native_raw_asset_tombstones_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_raw_asset_tombstones
    ADD CONSTRAINT native_raw_asset_tombstones_pkey PRIMARY KEY (repository_id, path);

--
-- Name: native_raw_assets native_raw_assets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_raw_assets
    ADD CONSTRAINT native_raw_assets_pkey PRIMARY KEY (repository_id, path);

--
-- Name: native_raw_objects native_raw_objects_object_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_raw_objects
    ADD CONSTRAINT native_raw_objects_object_key_key UNIQUE (object_key);

--
-- Name: native_raw_objects native_raw_objects_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_raw_objects
    ADD CONSTRAINT native_raw_objects_pkey PRIMARY KEY (digest);

--
-- Name: native_raw_uploads native_raw_uploads_object_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_raw_uploads
    ADD CONSTRAINT native_raw_uploads_object_key_key UNIQUE (object_key);

--
-- Name: native_raw_uploads native_raw_uploads_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_raw_uploads
    ADD CONSTRAINT native_raw_uploads_pkey PRIMARY KEY (id);

--
-- Name: native_rubygems_quotas native_rubygems_quotas_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_rubygems_quotas
    ADD CONSTRAINT native_rubygems_quotas_pkey PRIMARY KEY (repository_id);

--
-- Name: native_rubygems_replication_items native_rubygems_replication_i_job_id_name_canonical_version_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_rubygems_replication_items
    ADD CONSTRAINT native_rubygems_replication_i_job_id_name_canonical_version_key UNIQUE (job_id, name, canonical_version, platform);

--
-- Name: native_rubygems_replication_items native_rubygems_replication_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_rubygems_replication_items
    ADD CONSTRAINT native_rubygems_replication_items_pkey PRIMARY KEY (job_id, item_index);

--
-- Name: native_rubygems_replication_jobs native_rubygems_replication_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_rubygems_replication_jobs
    ADD CONSTRAINT native_rubygems_replication_jobs_pkey PRIMARY KEY (id);

--
-- Name: native_rubygems_replication_retries native_rubygems_replication_retries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_rubygems_replication_retries
    ADD CONSTRAINT native_rubygems_replication_retries_pkey PRIMARY KEY (job_id, request_key);

--
-- Name: native_rubygems_sessions native_rubygems_sessions_object_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_rubygems_sessions
    ADD CONSTRAINT native_rubygems_sessions_object_key_key UNIQUE (object_key);

--
-- Name: native_rubygems_sessions native_rubygems_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_rubygems_sessions
    ADD CONSTRAINT native_rubygems_sessions_pkey PRIMARY KEY (id);

--
-- Name: nexus_migration_batches nexus_migration_batches_actor_idempotency_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_batches
    ADD CONSTRAINT nexus_migration_batches_actor_idempotency_key_key UNIQUE (actor, idempotency_key);

--
-- Name: nexus_migration_batches nexus_migration_batches_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_batches
    ADD CONSTRAINT nexus_migration_batches_pkey PRIMARY KEY (id);

--
-- Name: nexus_migration_controls nexus_migration_controls_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_controls
    ADD CONSTRAINT nexus_migration_controls_pkey PRIMARY KEY (batch_id, actor, idempotency_key);

--
-- Name: nexus_migration_entries nexus_migration_entries_batch_id_ordinal_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_entries
    ADD CONSTRAINT nexus_migration_entries_batch_id_ordinal_key UNIQUE (batch_id, ordinal);

--
-- Name: nexus_migration_entries nexus_migration_entries_batch_id_target_repository_id_ident_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_entries
    ADD CONSTRAINT nexus_migration_entries_batch_id_target_repository_id_ident_key UNIQUE (batch_id, target_repository_id, identity);

--
-- Name: nexus_migration_entries nexus_migration_entries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_entries
    ADD CONSTRAINT nexus_migration_entries_pkey PRIMARY KEY (id);

--
-- Name: nexus_migration_scan_pages nexus_migration_scan_pages_batch_id_source_repository_curso_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_scan_pages
    ADD CONSTRAINT nexus_migration_scan_pages_batch_id_source_repository_curso_key UNIQUE (batch_id, source_repository, cursor_sha256);

--
-- Name: nexus_migration_scan_pages nexus_migration_scan_pages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_scan_pages
    ADD CONSTRAINT nexus_migration_scan_pages_pkey PRIMARY KEY (batch_id, ordinal);

--
-- Name: nexus_migration_scan_paths nexus_migration_scan_paths_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_scan_paths
    ADD CONSTRAINT nexus_migration_scan_paths_pkey PRIMARY KEY (batch_id, source_repository, source_path);

--
-- Name: nexus_migration_scans nexus_migration_scans_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_scans
    ADD CONSTRAINT nexus_migration_scans_pkey PRIMARY KEY (batch_id);

--
-- Name: nexus_migration_schedule_controls nexus_migration_schedule_controls_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_schedule_controls
    ADD CONSTRAINT nexus_migration_schedule_controls_pkey PRIMARY KEY (schedule_id, actor, idempotency_key);

--
-- Name: nexus_migration_schedules nexus_migration_schedules_actor_idempotency_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_schedules
    ADD CONSTRAINT nexus_migration_schedules_actor_idempotency_key_key UNIQUE (actor, idempotency_key);

--
-- Name: nexus_migration_schedules nexus_migration_schedules_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_schedules
    ADD CONSTRAINT nexus_migration_schedules_pkey PRIMARY KEY (id);

--
-- Name: nexus_migration_sources nexus_migration_sources_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_sources
    ADD CONSTRAINT nexus_migration_sources_pkey PRIMARY KEY (id);

--
-- Name: oci_bound_objects oci_bound_objects_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oci_bound_objects
    ADD CONSTRAINT oci_bound_objects_pkey PRIMARY KEY (scope, owner, image, digest);

--
-- Name: oci_discovery_snapshots oci_discovery_snapshots_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oci_discovery_snapshots
    ADD CONSTRAINT oci_discovery_snapshots_pkey PRIMARY KEY (id);

--
-- Name: oci_group_members oci_group_members_group_name_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oci_group_members
    ADD CONSTRAINT oci_group_members_group_name_name_key UNIQUE (group_name, name);

--
-- Name: oci_group_members oci_group_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oci_group_members
    ADD CONSTRAINT oci_group_members_pkey PRIMARY KEY (group_name, "position");

--
-- Name: oci_groups oci_groups_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oci_groups
    ADD CONSTRAINT oci_groups_pkey PRIMARY KEY (name);

--
-- Name: oci_subject_bindings oci_subject_bindings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oci_subject_bindings
    ADD CONSTRAINT oci_subject_bindings_pkey PRIMARY KEY (scope, owner, image, subject);

--
-- Name: oidc_settings oidc_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oidc_settings
    ADD CONSTRAINT oidc_settings_pkey PRIMARY KEY (singleton);

--
-- Name: raw_group_members raw_group_members_group_name_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.raw_group_members
    ADD CONSTRAINT raw_group_members_group_name_name_key UNIQUE (group_name, name);

--
-- Name: raw_group_members raw_group_members_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.raw_group_members
    ADD CONSTRAINT raw_group_members_pkey PRIMARY KEY (group_name, "position");

--
-- Name: raw_group_members raw_group_proxy_allowlist_required; Type: CHECK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE public.raw_group_members
    ADD CONSTRAINT raw_group_proxy_allowlist_required CHECK (((member_type <> 'proxy'::text) OR (cardinality(allowed_hosts) > 0))) NOT VALID;

--
-- Name: raw_groups raw_groups_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.raw_groups
    ADD CONSTRAINT raw_groups_pkey PRIMARY KEY (name);

--
-- Name: replication_checkpoints replication_checkpoints_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.replication_checkpoints
    ADD CONSTRAINT replication_checkpoints_pkey PRIMARY KEY (plan_id, object_key);

--
-- Name: replication_plans replication_plans_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.replication_plans
    ADD CONSTRAINT replication_plans_pkey PRIMARY KEY (id);

--
-- Name: replication_plans replication_plans_target_repository_id_idempotency_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.replication_plans
    ADD CONSTRAINT replication_plans_target_repository_id_idempotency_key_key UNIQUE (target_repository_id, idempotency_key);

--
-- Name: repository_capacity_quotas repository_capacity_quotas_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_capacity_quotas
    ADD CONSTRAINT repository_capacity_quotas_pkey PRIMARY KEY (repository_id);

--
-- Name: repository_grant_sets repository_grant_sets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_grant_sets
    ADD CONSTRAINT repository_grant_sets_pkey PRIMARY KEY (repository_id);

--
-- Name: repository_grants repository_grants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_grants
    ADD CONSTRAINT repository_grants_pkey PRIMARY KEY (repository_id, principal, resource_prefix);

--
-- Name: repository_quarantine_read_policies repository_quarantine_read_policies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_quarantine_read_policies
    ADD CONSTRAINT repository_quarantine_read_policies_pkey PRIMARY KEY (repository_id);

--
-- Name: repository_quota_alert_events repository_quota_alert_events_episode_id_scenario_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_quota_alert_events
    ADD CONSTRAINT repository_quota_alert_events_episode_id_scenario_key UNIQUE (episode_id, scenario);

--
-- Name: repository_quota_alert_events repository_quota_alert_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_quota_alert_events
    ADD CONSTRAINT repository_quota_alert_events_pkey PRIMARY KEY (id);

--
-- Name: repository_quota_alert_events repository_quota_alert_events_rule_id_sequence_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_quota_alert_events
    ADD CONSTRAINT repository_quota_alert_events_rule_id_sequence_key UNIQUE (rule_id, sequence);

--
-- Name: repository_quota_alert_rules repository_quota_alert_rules_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_quota_alert_rules
    ADD CONSTRAINT repository_quota_alert_rules_pkey PRIMARY KEY (id);

--
-- Name: repository_retention_policies repository_retention_policies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_retention_policies
    ADD CONSTRAINT repository_retention_policies_pkey PRIMARY KEY (repository_id);

--
-- Name: repository_security_policies repository_security_policies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_security_policies
    ADD CONSTRAINT repository_security_policies_pkey PRIMARY KEY (repository_id);

--
-- Name: resolver_audit_log resolver_audit_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resolver_audit_log
    ADD CONSTRAINT resolver_audit_log_pkey PRIMARY KEY (id);

--
-- Name: runtime_node_sessions runtime_node_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.runtime_node_sessions
    ADD CONSTRAINT runtime_node_sessions_pkey PRIMARY KEY (session_id);

--
-- Name: runtime_nodes runtime_nodes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.runtime_nodes
    ADD CONSTRAINT runtime_nodes_pkey PRIMARY KEY (instance_id);

--
-- Name: scheduled_task_runs scheduled_task_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scheduled_task_runs
    ADD CONSTRAINT scheduled_task_runs_pkey PRIMARY KEY (id);

--
-- Name: scheduled_tasks scheduled_tasks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scheduled_tasks
    ADD CONSTRAINT scheduled_tasks_pkey PRIMARY KEY (id);

--
-- Name: service_accounts service_accounts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_accounts
    ADD CONSTRAINT service_accounts_pkey PRIMARY KEY (id);

--
-- Name: site_settings site_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.site_settings
    ADD CONSTRAINT site_settings_pkey PRIMARY KEY (singleton);

--
-- Name: user_identities user_identities_kind_issuer_subject_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_identities
    ADD CONSTRAINT user_identities_kind_issuer_subject_key UNIQUE (kind, issuer, subject);

--
-- Name: user_identities user_identities_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_identities
    ADD CONSTRAINT user_identities_pkey PRIMARY KEY (id);

--
-- Name: user_identities user_identities_user_id_kind_issuer_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_identities
    ADD CONSTRAINT user_identities_user_id_kind_issuer_key UNIQUE (user_id, kind, issuer);

--
-- Name: user_sessions user_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_sessions
    ADD CONSTRAINT user_sessions_pkey PRIMARY KEY (id);

--
-- Name: users users_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_name_key UNIQUE (name);

--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

--
-- Name: webhook_deliveries webhook_deliveries_event_id_subscription_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_event_id_subscription_id_key UNIQUE (event_id, subscription_id);

--
-- Name: webhook_deliveries webhook_deliveries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_pkey PRIMARY KEY (id);

--
-- Name: webhook_events webhook_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_events
    ADD CONSTRAINT webhook_events_pkey PRIMARY KEY (id);

--
-- Name: webhook_subscriptions webhook_subscriptions_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_subscriptions
    ADD CONSTRAINT webhook_subscriptions_name_key UNIQUE (name);

--
-- Name: webhook_subscriptions webhook_subscriptions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_subscriptions
    ADD CONSTRAINT webhook_subscriptions_pkey PRIMARY KEY (id);

--
-- Name: api_keys_active_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX api_keys_active_expiry_idx ON public.api_keys USING btree (expires_at) WHERE (revoked_at IS NULL);

--
-- Name: api_keys_service_account_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX api_keys_service_account_idx ON public.api_keys USING btree (service_account_id, id) WHERE (service_account_id IS NOT NULL);

--
-- Name: artifact_intelligence_updated_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX artifact_intelligence_updated_idx ON public.artifact_intelligence USING btree (updated_at DESC);

--
-- Name: artifact_tombstones_tombstoned_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX artifact_tombstones_tombstoned_at_idx ON public.artifact_tombstones USING btree (tombstoned_at);

--
-- Name: artifact_usage_stats_repository_address_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX artifact_usage_stats_repository_address_idx ON public.artifact_usage_stats USING btree (repository, resource COLLATE "C", format COLLATE "C");

--
-- Name: artifact_usage_stats_repository_usage_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX artifact_usage_stats_repository_usage_idx ON public.artifact_usage_stats USING btree (repository, download_count DESC);

--
-- Name: audit_cleanup_jobs_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX audit_cleanup_jobs_claim_idx ON public.audit_cleanup_jobs USING btree (created_at) WHERE (state = ANY (ARRAY['pending'::text, 'failed'::text]));

--
-- Name: audit_cleanup_jobs_running_started_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX audit_cleanup_jobs_running_started_idx ON public.audit_cleanup_jobs USING btree (started_at) WHERE (state = 'running'::text);

--
-- Name: authorization_roles_name_ci_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX authorization_roles_name_ci_idx ON public.authorization_roles USING btree (lower(name));

--
-- Name: authorization_roles_updated_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX authorization_roles_updated_at_idx ON public.authorization_roles USING btree (updated_at DESC, id DESC);

--
-- Name: authorization_templates_name_ci_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX authorization_templates_name_ci_idx ON public.authorization_templates USING btree (lower(name));

--
-- Name: authorization_templates_updated_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX authorization_templates_updated_at_idx ON public.authorization_templates USING btree (updated_at DESC, id DESC);

--
-- Name: cache_circuit_breakers_expires_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX cache_circuit_breakers_expires_at_idx ON public.cache_circuit_breakers USING btree (expires_at);

--
-- Name: cache_control_entries_key_prefix_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX cache_control_entries_key_prefix_idx ON public.cache_control_entries USING btree (key text_pattern_ops);

--
-- Name: cache_control_entries_proxy_browse_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX cache_control_entries_proxy_browse_idx ON public.cache_control_entries USING btree (md5(COALESCE((value ->> 'repository'::text), (value ->> 'Repository'::text))), md5(COALESCE((value ->> 'endpoint'::text), (value ->> 'Endpoint'::text))), "left"(COALESCE((value ->> 'path'::text), (value ->> 'Path'::text)), 256) text_pattern_ops) WHERE ((key ~~ 'maven/index/%'::text) OR (key ~~ 'raw/index/%'::text));

--
-- Name: cache_tasks_active_dedupe_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX cache_tasks_active_dedupe_idx ON public.cache_tasks USING btree (dedupe_key) WHERE (completed_at IS NULL);

--
-- Name: cache_tasks_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX cache_tasks_claim_idx ON public.cache_tasks USING btree (task_type, available_at, created_at) WHERE (completed_at IS NULL);

--
-- Name: conan_group_members_repository_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX conan_group_members_repository_id_idx ON public.conan_group_members USING btree (repository_id) WHERE (repository_id IS NOT NULL);

--
-- Name: email_quota_order; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX email_quota_order ON public.email_test_deliveries USING btree (quota_rule_id, event_sequence) WHERE (kind = 'repository_quota'::text);

--
-- Name: email_test_delivery_due; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX email_test_delivery_due ON public.email_test_deliveries USING btree (next_attempt_at) WHERE (state = ANY (ARRAY['pending'::text, 'retrying'::text, 'delivering'::text]));

--
-- Name: email_test_requests_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX email_test_requests_time ON public.email_test_requests USING btree (created_at);

--
-- Name: hosted_repositories_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX hosted_repositories_created_at_idx ON public.hosted_repositories USING btree (created_at, id);

--
-- Name: hosted_repository_idempotency_expires_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX hosted_repository_idempotency_expires_at_idx ON public.hosted_repository_idempotency USING btree (expires_at);

--
-- Name: lifecycle_jobs_actionable_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX lifecycle_jobs_actionable_idx ON public.lifecycle_jobs USING btree (next_attempt_at, created_at) WHERE (state = ANY (ARRAY['pending'::text, 'retrying'::text]));

--
-- Name: lifecycle_jobs_artifact_scan_identity_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX lifecycle_jobs_artifact_scan_identity_idx ON public.lifecycle_jobs USING btree (repository_id, ((payload ->> 'format'::text)), ((payload ->> 'coordinate'::text)), ((payload ->> 'digest'::text)), created_at DESC) WHERE (kind = 'scan'::text);

--
-- Name: lifecycle_jobs_created_at_brin_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX lifecycle_jobs_created_at_brin_idx ON public.lifecycle_jobs USING brin (created_at) WITH (pages_per_range='128');

--
-- Name: lifecycle_jobs_lease_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX lifecycle_jobs_lease_idx ON public.lifecycle_jobs USING btree (lease_expires_at) WHERE (state = 'running'::text);

--
-- Name: lifecycle_jobs_pending_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX lifecycle_jobs_pending_idx ON public.lifecycle_jobs USING btree (created_at) WHERE (state = 'pending'::text);

--
-- Name: native_apt_archive_object_intents_object_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_archive_object_intents_object_idx ON public.native_apt_archive_object_intents USING btree (object_key);

--
-- Name: native_apt_archive_object_intents_reclaim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_archive_object_intents_reclaim_idx ON public.native_apt_archive_object_intents USING btree (restore_id, object_key) WHERE ((reclaim_scheduled_at IS NULL) AND (collected_at IS NULL));

--
-- Name: native_apt_archive_restores_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX native_apt_archive_restores_active_idx ON public.native_apt_archive_restores USING btree (snapshot_id) WHERE (state = 'preparing'::text);

--
-- Name: native_apt_archive_restores_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_archive_restores_expiry_idx ON public.native_apt_archive_restores USING btree (created_at, id) WHERE (state = 'preparing'::text);

--
-- Name: native_apt_assets_digest_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_assets_digest_idx ON public.native_apt_assets USING btree (repository_id, digest, path);

--
-- Name: native_apt_assets_path_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_assets_path_idx ON public.native_apt_assets USING btree (repository_id, path text_pattern_ops);

--
-- Name: native_apt_assets_path_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_assets_path_trgm_idx ON public.native_apt_assets USING gin (path public.gin_trgm_ops);

--
-- Name: native_apt_package_deletions_repository_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_package_deletions_repository_idx ON public.native_apt_package_deletions USING btree (repository_id, suite);

--
-- Name: native_apt_package_revisions_digest_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_package_revisions_digest_idx ON public.native_apt_package_revisions USING btree (repository_id, digest);

--
-- Name: native_apt_package_revisions_object_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_package_revisions_object_idx ON public.native_apt_package_revisions USING btree (object_key);

--
-- Name: native_apt_publication_idempotency_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_publication_idempotency_expiry_idx ON public.native_apt_publication_idempotency USING btree (expires_at);

--
-- Name: native_apt_publication_sessions_collection_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_publication_sessions_collection_idx ON public.native_apt_publication_sessions USING btree (expires_at, id) WHERE ((state = 'aborted'::text) AND (object_key <> ''::text) AND (collected_at IS NULL));

--
-- Name: native_apt_publication_sessions_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_publication_sessions_expiry_idx ON public.native_apt_publication_sessions USING btree (expires_at, id) WHERE (state = ANY (ARRAY['open'::text, 'uploading'::text]));

--
-- Name: native_apt_publication_sessions_reclaim_schedule_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_publication_sessions_reclaim_schedule_idx ON public.native_apt_publication_sessions USING btree (expires_at, id) WHERE ((state = 'aborted'::text) AND (object_key <> ''::text) AND (reclaim_scheduled_at IS NULL) AND (collected_at IS NULL));

--
-- Name: native_apt_repository_snapshots_visible_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX native_apt_repository_snapshots_visible_idx ON public.native_apt_repository_snapshots USING btree (repository_id, suite) WHERE (state = 'visible'::text);

--
-- Name: native_apt_snapshot_assets_path_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_snapshot_assets_path_trgm_idx ON public.native_apt_snapshot_assets USING gin (path public.gin_trgm_ops);

--
-- Name: native_apt_snapshot_assets_visible_lookup_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_snapshot_assets_visible_lookup_idx ON public.native_apt_snapshot_assets USING btree (repository_id, path, snapshot_id);

--
-- Name: native_apt_snapshot_object_intents_age_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_snapshot_object_intents_age_idx ON public.native_apt_snapshot_object_intents USING btree (created_at, snapshot_id) WHERE (collected_at IS NULL);

--
-- Name: native_apt_snapshot_object_intents_reclaim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_snapshot_object_intents_reclaim_idx ON public.native_apt_snapshot_object_intents USING btree (snapshot_id, object_key) WHERE ((reclaim_scheduled_at IS NULL) AND (collected_at IS NULL));

--
-- Name: native_apt_snapshot_packages_component_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_apt_snapshot_packages_component_idx ON public.native_apt_snapshot_packages USING btree (snapshot_id, component, architecture);

--
-- Name: native_cargo_names_name_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_cargo_names_name_trgm_idx ON public.native_cargo_names USING gin (name public.gin_trgm_ops);

--
-- Name: native_cargo_proxy_indexes_positive_name_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_cargo_proxy_indexes_positive_name_trgm_idx ON public.native_cargo_proxy_indexes USING gin (name public.gin_trgm_ops) WHERE (status = 200);

--
-- Name: native_cargo_publications_reclaim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_cargo_publications_reclaim_idx ON public.native_cargo_publications USING btree (object_key) WHERE (collected_at IS NULL);

--
-- Name: native_conan_assets_object_key_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_conan_assets_object_key_idx ON public.native_conan_assets USING btree (object_key);

--
-- Name: native_conan_object_intents_collect_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_conan_object_intents_collect_idx ON public.native_conan_object_intents USING btree (created_at) WHERE (collected_at IS NULL);

--
-- Name: native_conan_open_publication_identity; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX native_conan_open_publication_identity ON public.native_conan_publish_sessions USING btree (repository_id, publisher, kind, reference, recipe_revision, package_id, package_revision) WHERE (native AND (state = 'open'::text));

--
-- Name: native_conan_recipe_revisions_created_at_brin_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_conan_recipe_revisions_created_at_brin_idx ON public.native_conan_recipe_revisions USING brin (created_at) WITH (pages_per_range='128');

--
-- Name: native_conan_recipe_revisions_digest_search_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_conan_recipe_revisions_digest_search_idx ON public.native_conan_recipe_revisions USING btree (repository_id, digest, reference, created_at DESC) WHERE (state = 'visible'::text);

--
-- Name: native_conan_recipe_revisions_visible_reference_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_conan_recipe_revisions_visible_reference_idx ON public.native_conan_recipe_revisions USING btree (repository_id, reference) WHERE (state = 'visible'::text);

--
-- Name: native_conan_recipe_revisions_visible_reference_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_conan_recipe_revisions_visible_reference_trgm_idx ON public.native_conan_recipe_revisions USING gin (reference public.gin_trgm_ops) WHERE (state = 'visible'::text);

--
-- Name: native_conan_recipe_visible_reference_pattern_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_conan_recipe_visible_reference_pattern_idx ON public.native_conan_recipe_revisions USING btree (repository_id, reference text_pattern_ops) WHERE (state = 'visible'::text);

--
-- Name: native_go_assets_digest_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_go_assets_digest_idx ON public.native_go_assets USING btree (repository_id, digest, module_path, version);

--
-- Name: native_go_assets_object_reclaim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_go_assets_object_reclaim_idx ON public.native_go_assets USING btree (object_key, module_path, version) WHERE (collected_at IS NULL);

--
-- Name: native_go_assets_reclaim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_go_assets_reclaim_idx ON public.native_go_assets USING btree (repository_id, collected_at, module_path, version);

--
-- Name: native_go_versions_module_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_go_versions_module_idx ON public.native_go_versions USING btree (repository_id, module_path text_pattern_ops, version);

--
-- Name: native_go_versions_module_path_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_go_versions_module_path_trgm_idx ON public.native_go_versions USING gin (module_path public.gin_trgm_ops);

--
-- Name: native_helm_active_identity; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX native_helm_active_identity ON public.native_helm_publication_sessions USING btree (repository_id, name, version) WHERE (state = ANY (ARRAY['pending'::text, 'committed'::text]));

--
-- Name: native_helm_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_helm_expiry ON public.native_helm_publication_sessions USING btree (expires_at, id) WHERE (state = 'pending'::text);

--
-- Name: native_helm_provenance_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX native_helm_provenance_key ON public.native_helm_publication_sessions USING btree (provenance_key) WHERE (provenance_key <> ''::text);

--
-- Name: native_helm_replication_claim; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_helm_replication_claim ON public.native_helm_replication_jobs USING btree (created_at, id) WHERE (state = ANY (ARRAY['queued'::text, 'running'::text]));

--
-- Name: native_helm_source_snapshots_view; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_helm_source_snapshots_view ON public.native_helm_source_snapshots USING btree (view_id);

--
-- Name: native_maven_artifacts_digest_search_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_maven_artifacts_digest_search_idx ON public.native_maven_artifacts USING btree (repository_id, digest, coordinate, build_number) WHERE (state = 'visible'::text);

--
-- Name: native_maven_artifacts_repository_coordinate_build_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX native_maven_artifacts_repository_coordinate_build_key ON public.native_maven_artifacts USING btree (repository_id, coordinate, build_number);

--
-- Name: native_maven_artifacts_visible_coordinate_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_maven_artifacts_visible_coordinate_idx ON public.native_maven_artifacts USING btree (repository_id, coordinate, build_number) WHERE (state = 'visible'::text);

--
-- Name: native_maven_artifacts_visible_coordinate_pattern_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_maven_artifacts_visible_coordinate_pattern_idx ON public.native_maven_artifacts USING btree (repository_id, coordinate text_pattern_ops, build_number) WHERE (state = 'visible'::text);

--
-- Name: native_maven_artifacts_visible_coordinate_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_maven_artifacts_visible_coordinate_trgm_idx ON public.native_maven_artifacts USING gin (coordinate public.gin_trgm_ops) WHERE (state = 'visible'::text);

--
-- Name: native_maven_object_intents_collect_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_maven_object_intents_collect_idx ON public.native_maven_object_intents USING btree (created_at) WHERE (claimed_at IS NULL);

--
-- Name: native_maven_object_intents_deleted_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_maven_object_intents_deleted_at_idx ON public.native_maven_object_intents USING btree (deleted_at) WHERE (deleted_at IS NOT NULL);

--
-- Name: native_maven_open_publisher_coordinate_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX native_maven_open_publisher_coordinate_idx ON public.native_maven_publish_sessions USING btree (repository_id, coordinate, publisher) WHERE ((state = 'open'::text) AND (client_timestamp = ''::text));

--
-- Name: native_maven_publish_idempotency_expires_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_maven_publish_idempotency_expires_at_idx ON public.native_maven_publish_idempotency USING btree (expires_at);

--
-- Name: native_maven_snapshot_client_receipt_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX native_maven_snapshot_client_receipt_idx ON public.native_maven_publish_sessions USING btree (repository_id, coordinate, publisher, client_timestamp, client_build_number) WHERE (client_timestamp <> ''::text);

--
-- Name: native_maven_source_snapshot_identity; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX native_maven_source_snapshot_identity ON public.native_maven_artifacts USING btree (repository_id, coordinate, source_timestamp, source_build_number) WHERE (source_timestamp <> ''::text);

--
-- Name: native_npm_packages_name_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_npm_packages_name_idx ON public.native_npm_packages USING btree (repository_id, name text_pattern_ops);

--
-- Name: native_npm_packages_name_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_npm_packages_name_trgm_idx ON public.native_npm_packages USING gin (name public.gin_trgm_ops) WHERE (NOT negative);

--
-- Name: native_npm_packages_proxy_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_npm_packages_proxy_expiry_idx ON public.native_npm_packages USING btree (repository_id, metadata_expires_at, negative_expires_at) WHERE (source_endpoint <> ''::text);

--
-- Name: native_npm_versions_digest_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_npm_versions_digest_idx ON public.native_npm_versions USING btree (repository_id, digest, package_name, version) WHERE (digest <> ''::text);

--
-- Name: native_npm_versions_reclaim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_npm_versions_reclaim_idx ON public.native_npm_versions USING btree (deleted_at, object_key) WHERE ((state = 'deleted'::text) AND (collected_at IS NULL) AND (object_key <> ''::text));

--
-- Name: native_npm_versions_visible_name_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_npm_versions_visible_name_trgm_idx ON public.native_npm_versions USING gin (package_name public.gin_trgm_ops) WHERE (state = 'visible'::text);

--
-- Name: native_nuget_active_identity; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX native_nuget_active_identity ON public.native_nuget_publication_sessions USING btree (repository_id, package_id, version, source_version) WHERE (state = ANY (ARRAY['reserved'::text, 'staged'::text, 'committed'::text]));

--
-- Name: native_nuget_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_nuget_expiry ON public.native_nuget_publication_sessions USING btree (expires_at, id) WHERE (state = ANY (ARRAY['reserved'::text, 'staged'::text]));

--
-- Name: native_nuget_replication_claim; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_nuget_replication_claim ON public.native_nuget_replication_jobs USING btree (created_at, id) WHERE (state = ANY (ARRAY['queued'::text, 'running'::text]));

--
-- Name: native_oci_manifests_created_at_brin_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_oci_manifests_created_at_brin_idx ON public.native_oci_manifests USING brin (created_at) WITH (pages_per_range='128');

--
-- Name: native_oci_manifests_digest_search_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_oci_manifests_digest_search_idx ON public.native_oci_manifests USING btree (repository_id, digest, name, created_at DESC);

--
-- Name: native_oci_manifests_name_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_oci_manifests_name_trgm_idx ON public.native_oci_manifests USING gin (name public.gin_trgm_ops);

--
-- Name: native_oci_manifests_referrers_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_oci_manifests_referrers_idx ON public.native_oci_manifests USING btree (repository_id, name, subject_digest, digest) WHERE (subject_digest IS NOT NULL);

--
-- Name: native_oci_manifests_repository_name_pattern_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_oci_manifests_repository_name_pattern_idx ON public.native_oci_manifests USING btree (repository_id, name text_pattern_ops);

--
-- Name: native_oci_object_intents_reclaimable_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_oci_object_intents_reclaimable_idx ON public.native_oci_object_intents USING btree (repository_id, created_at) WHERE ((repository_id IS NOT NULL) AND (claimed_at IS NULL) AND (collected_at IS NULL));

--
-- Name: native_oci_object_intents_unclaimed_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_oci_object_intents_unclaimed_idx ON public.native_oci_object_intents USING btree (created_at) WHERE ((claimed_at IS NULL) AND (collected_at IS NULL));

--
-- Name: native_oci_uploads_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_oci_uploads_expiry_idx ON public.native_oci_uploads USING btree (expires_at) WHERE (state = 'open'::text);

--
-- Name: native_pypi_files_digest_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_pypi_files_digest_idx ON public.native_pypi_files USING btree (repository_id, digest, project, version) WHERE (state = 'visible'::text);

--
-- Name: native_pypi_files_project_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_pypi_files_project_idx ON public.native_pypi_files USING btree (repository_id, project text_pattern_ops, created_at DESC) WHERE (state = 'visible'::text);

--
-- Name: native_pypi_files_reclaim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_pypi_files_reclaim_idx ON public.native_pypi_files USING btree (deleted_at, object_key) WHERE ((state = 'deleted'::text) AND (collected_at IS NULL) AND (object_key <> ''::text));

--
-- Name: native_pypi_files_visible_project_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_pypi_files_visible_project_trgm_idx ON public.native_pypi_files USING gin (project public.gin_trgm_ops) WHERE (state = 'visible'::text);

--
-- Name: native_raw_asset_tombstones_deleted_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_raw_asset_tombstones_deleted_at_idx ON public.native_raw_asset_tombstones USING btree (deleted_at);

--
-- Name: native_raw_assets_digest_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_raw_assets_digest_idx ON public.native_raw_assets USING btree (digest);

--
-- Name: native_raw_assets_path_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_raw_assets_path_trgm_idx ON public.native_raw_assets USING gin (path public.gin_trgm_ops);

--
-- Name: native_raw_assets_repository_digest_search_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_raw_assets_repository_digest_search_idx ON public.native_raw_assets USING btree (repository_id, digest, path);

--
-- Name: native_raw_assets_repository_path_pattern_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_raw_assets_repository_path_pattern_idx ON public.native_raw_assets USING btree (repository_id, path text_pattern_ops);

--
-- Name: native_raw_objects_reclaimable_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_raw_objects_reclaimable_idx ON public.native_raw_objects USING btree (repository_id, created_at) WHERE ((repository_id IS NOT NULL) AND (collected_at IS NULL));

--
-- Name: native_raw_uploads_open_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_raw_uploads_open_idx ON public.native_raw_uploads USING btree (repository_id, expires_at) WHERE (state = 'open'::text);

--
-- Name: native_raw_uploads_reclaim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_raw_uploads_reclaim_idx ON public.native_raw_uploads USING btree (expires_at, id) WHERE ((state = 'open'::text) OR ((state = ANY (ARRAY['completed'::text, 'cancelled'::text])) AND (collected_at IS NULL)));

--
-- Name: native_rubygems_active_identity; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX native_rubygems_active_identity ON public.native_rubygems_sessions USING btree (repository_id, name, canonical_version, platform) WHERE (state = ANY (ARRAY['pending'::text, 'committed'::text]));

--
-- Name: native_rubygems_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_rubygems_expiry ON public.native_rubygems_sessions USING btree (expires_at, id) WHERE ((collected_at IS NULL) AND (state <> 'committed'::text));

--
-- Name: native_rubygems_replication_claim; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX native_rubygems_replication_claim ON public.native_rubygems_replication_jobs USING btree (created_at, id) WHERE (state = ANY (ARRAY['queued'::text, 'running'::text]));

--
-- Name: native_rubygems_visible_filename; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX native_rubygems_visible_filename ON public.native_rubygems_sessions USING btree (repository_id, full_name) WHERE (state = 'committed'::text);

--
-- Name: nexus_migration_batches_claim; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX nexus_migration_batches_claim ON public.nexus_migration_batches USING btree (state, created_at);

--
-- Name: nexus_migration_schedule_occurrence; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX nexus_migration_schedule_occurrence ON public.nexus_migration_batches USING btree (schedule_id, schedule_epoch) WHERE (schedule_id IS NOT NULL);

--
-- Name: nexus_migration_schedules_due; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX nexus_migration_schedules_due ON public.nexus_migration_schedules USING btree (next_run_at) WHERE (state = 'active'::text);

--
-- Name: oci_discovery_snapshot_owner_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX oci_discovery_snapshot_owner_expiry ON public.oci_discovery_snapshots USING btree (owner, expires_at);

--
-- Name: replication_checkpoints_pending_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX replication_checkpoints_pending_idx ON public.replication_checkpoints USING btree (plan_id, state, updated_at);

--
-- Name: replication_checkpoints_updated_at_brin_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX replication_checkpoints_updated_at_brin_idx ON public.replication_checkpoints USING brin (updated_at) WITH (pages_per_range='128');

--
-- Name: replication_plans_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX replication_plans_claim_idx ON public.replication_plans USING btree (created_at) WHERE (state = ANY (ARRAY['pending'::text, 'failed'::text]));

--
-- Name: replication_plans_retry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX replication_plans_retry_idx ON public.replication_plans USING btree (next_attempt_at, created_at) WHERE (state = ANY (ARRAY['pending'::text, 'failed'::text]));

--
-- Name: repository_quota_rule_due; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX repository_quota_rule_due ON public.repository_quota_alert_rules USING btree (next_evaluate_at, id) WHERE (enabled AND (NOT deleted));

--
-- Name: repository_quota_rule_scope; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX repository_quota_rule_scope ON public.repository_quota_alert_rules USING btree (repository_id) WHERE (NOT deleted);

--
-- Name: resolver_audit_log_group_occurred_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resolver_audit_log_group_occurred_at_idx ON public.resolver_audit_log USING btree (group_name, occurred_at DESC);

--
-- Name: resolver_audit_log_occurred_at_brin_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resolver_audit_log_occurred_at_brin_idx ON public.resolver_audit_log USING brin (occurred_at) WITH (pages_per_range='128');

--
-- Name: resolver_audit_log_occurred_at_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resolver_audit_log_occurred_at_id_idx ON public.resolver_audit_log USING btree (occurred_at DESC, id DESC);

--
-- Name: resolver_audit_log_occurred_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resolver_audit_log_occurred_at_idx ON public.resolver_audit_log USING btree (occurred_at);

--
-- Name: resolver_audit_log_repository_occurred_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resolver_audit_log_repository_occurred_at_idx ON public.resolver_audit_log USING btree (repository, occurred_at DESC);

--
-- Name: runtime_node_sessions_instance_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX runtime_node_sessions_instance_idx ON public.runtime_node_sessions USING btree (instance_id, last_seen_at DESC);

--
-- Name: runtime_node_sessions_last_seen_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX runtime_node_sessions_last_seen_idx ON public.runtime_node_sessions USING btree (last_seen_at DESC);

--
-- Name: runtime_nodes_last_seen_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX runtime_nodes_last_seen_idx ON public.runtime_nodes USING btree (last_seen_at DESC);

--
-- Name: scheduled_task_runs_task_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX scheduled_task_runs_task_idx ON public.scheduled_task_runs USING btree (task_id, created_at DESC);

--
-- Name: scheduled_tasks_due_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX scheduled_tasks_due_idx ON public.scheduled_tasks USING btree (next_run_at) WHERE enabled;

--
-- Name: scheduled_tasks_name_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX scheduled_tasks_name_idx ON public.scheduled_tasks USING btree (lower(name));

--
-- Name: service_accounts_name_unique_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX service_accounts_name_unique_idx ON public.service_accounts USING btree (lower(name));

--
-- Name: user_identities_user_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_identities_user_idx ON public.user_identities USING btree (user_id, created_at, id);

--
-- Name: user_sessions_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_sessions_expiry_idx ON public.user_sessions USING btree (expires_at, id);

--
-- Name: user_sessions_user_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX user_sessions_user_active_idx ON public.user_sessions USING btree (user_id, created_at DESC, id) WHERE (revoked_at IS NULL);

--
-- Name: users_management_filter_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX users_management_filter_idx ON public.users USING btree (state, role, created_at, id);

--
-- Name: users_name_case_insensitive_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX users_name_case_insensitive_idx ON public.users USING btree (lower(name));

--
-- Name: webhook_deliveries_actionable_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX webhook_deliveries_actionable_idx ON public.webhook_deliveries USING btree (COALESCE(next_attempt_at, lease_expires_at), created_at) WHERE (state = ANY (ARRAY['pending'::text, 'retrying'::text, 'delivering'::text]));

--
-- Name: webhook_deliveries_subscription_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX webhook_deliveries_subscription_idx ON public.webhook_deliveries USING btree (subscription_id, created_at DESC);

--
-- Name: native_apt_assets apt_asset_capacity_check; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER apt_asset_capacity_check AFTER INSERT OR UPDATE ON public.native_apt_assets FOR EACH ROW EXECUTE FUNCTION public.assert_apt_repository_capacity_trigger();

--
-- Name: audit_cleanup_jobs audit_cleanup_jobs_notify_insert; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER audit_cleanup_jobs_notify_insert AFTER INSERT ON public.audit_cleanup_jobs FOR EACH ROW WHEN ((new.state = 'pending'::text)) EXECUTE FUNCTION public.artifact_gateway_notify_queue('artifact_gateway_audit_cleanup');

--
-- Name: audit_cleanup_jobs audit_cleanup_jobs_notify_update; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER audit_cleanup_jobs_notify_update AFTER UPDATE OF state ON public.audit_cleanup_jobs FOR EACH ROW WHEN (((new.state = 'pending'::text) AND (old.state IS DISTINCT FROM new.state))) EXECUTE FUNCTION public.artifact_gateway_notify_queue('artifact_gateway_audit_cleanup');

--
-- Name: lifecycle_jobs cargo_promotion_state_webhook_after_update; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER cargo_promotion_state_webhook_after_update AFTER UPDATE ON public.lifecycle_jobs FOR EACH ROW EXECUTE FUNCTION public.cargo_promotion_state_webhook();

--
-- Name: native_cargo_publications cargo_publication_capacity_check; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER cargo_publication_capacity_check AFTER INSERT ON public.native_cargo_publications FOR EACH ROW EXECUTE FUNCTION public.assert_cargo_repository_capacity();

--
-- Name: replication_plans cargo_replication_state_webhook_after_update; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER cargo_replication_state_webhook_after_update AFTER UPDATE ON public.replication_plans FOR EACH ROW EXECUTE FUNCTION public.cargo_replication_state_webhook();

--
-- Name: native_conan_assets conan_asset_capacity_check; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER conan_asset_capacity_check AFTER INSERT OR UPDATE ON public.native_conan_assets FOR EACH ROW EXECUTE FUNCTION public.assert_hosted_repository_capacity_trigger();

--
-- Name: native_conan_package_revisions conan_package_capacity_check; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER conan_package_capacity_check AFTER INSERT OR UPDATE OF state ON public.native_conan_package_revisions FOR EACH ROW EXECUTE FUNCTION public.assert_hosted_repository_capacity_trigger();

--
-- Name: native_conan_recipe_revisions conan_recipe_capacity_check; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER conan_recipe_capacity_check AFTER INSERT OR UPDATE OF state ON public.native_conan_recipe_revisions FOR EACH ROW EXECUTE FUNCTION public.assert_hosted_repository_capacity_trigger();

--
-- Name: native_go_assets go_asset_capacity_check; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER go_asset_capacity_check AFTER INSERT OR UPDATE ON public.native_go_assets FOR EACH ROW EXECUTE FUNCTION public.assert_hosted_repository_capacity_trigger();

--
-- Name: hosted_repositories hosted_repositories_deletion_notify; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER hosted_repositories_deletion_notify AFTER UPDATE OF state ON public.hosted_repositories FOR EACH ROW WHEN ((new.state = 'deleting'::text)) EXECUTE FUNCTION public.artifact_gateway_notify_queue('artifact_gateway_repository_deletions');

--
-- Name: lifecycle_jobs lifecycle_jobs_notify_insert; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER lifecycle_jobs_notify_insert AFTER INSERT ON public.lifecycle_jobs FOR EACH ROW WHEN ((new.state = ANY (ARRAY['pending'::text, 'retrying'::text]))) EXECUTE FUNCTION public.artifact_gateway_notify_queue('artifact_gateway_lifecycle_jobs');

--
-- Name: lifecycle_jobs lifecycle_jobs_notify_update; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER lifecycle_jobs_notify_update AFTER UPDATE OF state, next_attempt_at ON public.lifecycle_jobs FOR EACH ROW WHEN (((new.state = ANY (ARRAY['pending'::text, 'retrying'::text])) AND (old.state IS DISTINCT FROM new.state))) EXECUTE FUNCTION public.artifact_gateway_notify_queue('artifact_gateway_lifecycle_jobs');

--
-- Name: native_maven_artifacts maven_artifact_capacity_check; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER maven_artifact_capacity_check AFTER INSERT OR UPDATE OF state ON public.native_maven_artifacts FOR EACH ROW EXECUTE FUNCTION public.assert_hosted_repository_capacity_trigger();

--
-- Name: native_maven_assets maven_asset_capacity_check; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER maven_asset_capacity_check AFTER INSERT OR UPDATE ON public.native_maven_assets FOR EACH ROW EXECUTE FUNCTION public.assert_hosted_repository_capacity_trigger();

--
-- Name: native_npm_versions npm_version_capacity_check; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER npm_version_capacity_check AFTER INSERT OR UPDATE ON public.native_npm_versions FOR EACH ROW EXECUTE FUNCTION public.assert_hosted_repository_capacity_trigger();

--
-- Name: native_oci_repository_blobs oci_blob_capacity_check; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER oci_blob_capacity_check AFTER INSERT OR UPDATE ON public.native_oci_repository_blobs FOR EACH ROW EXECUTE FUNCTION public.assert_hosted_repository_capacity_trigger();

--
-- Name: native_oci_manifests oci_manifest_capacity_check; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER oci_manifest_capacity_check AFTER INSERT OR UPDATE ON public.native_oci_manifests FOR EACH ROW EXECUTE FUNCTION public.assert_hosted_repository_capacity_trigger();

--
-- Name: native_pypi_files pypi_file_capacity_check; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER pypi_file_capacity_check AFTER INSERT OR UPDATE ON public.native_pypi_files FOR EACH ROW EXECUTE FUNCTION public.assert_hosted_repository_capacity_trigger();

--
-- Name: native_raw_assets raw_asset_capacity_check; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER raw_asset_capacity_check AFTER INSERT OR UPDATE ON public.native_raw_assets FOR EACH ROW EXECUTE FUNCTION public.assert_hosted_repository_capacity_trigger();

--
-- Name: replication_plans replication_plans_notify_insert; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER replication_plans_notify_insert AFTER INSERT ON public.replication_plans FOR EACH ROW WHEN ((new.state = ANY (ARRAY['pending'::text, 'failed'::text]))) EXECUTE FUNCTION public.artifact_gateway_notify_queue('artifact_gateway_replication_plans');

--
-- Name: replication_plans replication_plans_notify_update; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER replication_plans_notify_update AFTER UPDATE OF state, next_attempt_at ON public.replication_plans FOR EACH ROW WHEN (((new.state = 'pending'::text) AND (old.state IS DISTINCT FROM new.state))) EXECUTE FUNCTION public.artifact_gateway_notify_queue('artifact_gateway_replication_plans');

--
-- Name: repository_capacity_quotas repository_capacity_quota_check; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER repository_capacity_quota_check AFTER INSERT OR UPDATE OF quota_bytes ON public.repository_capacity_quotas FOR EACH ROW EXECUTE FUNCTION public.assert_hosted_repository_capacity_trigger();

--
-- Name: api_keys api_keys_service_account_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_service_account_id_fkey FOREIGN KEY (service_account_id) REFERENCES public.service_accounts(id);

--
-- Name: artifact_intelligence artifact_intelligence_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.artifact_intelligence
    ADD CONSTRAINT artifact_intelligence_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: artifact_quarantines artifact_quarantines_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.artifact_quarantines
    ADD CONSTRAINT artifact_quarantines_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: artifact_tombstones artifact_tombstones_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.artifact_tombstones
    ADD CONSTRAINT artifact_tombstones_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: conan_group_members conan_group_members_group_name_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conan_group_members
    ADD CONSTRAINT conan_group_members_group_name_fkey FOREIGN KEY (group_name) REFERENCES public.conan_groups(name) ON DELETE CASCADE;

--
-- Name: conan_group_members conan_group_members_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.conan_group_members
    ADD CONSTRAINT conan_group_members_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: email_test_deliveries email_test_deliveries_quota_rule_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_test_deliveries
    ADD CONSTRAINT email_test_deliveries_quota_rule_id_fkey FOREIGN KEY (quota_rule_id) REFERENCES public.repository_quota_alert_rules(id);

--
-- Name: email_test_deliveries email_test_deliveries_target_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.email_test_deliveries
    ADD CONSTRAINT email_test_deliveries_target_id_fkey FOREIGN KEY (target_id) REFERENCES public.email_targets(id);

--
-- Name: hosted_group_idempotency hosted_group_idempotency_group_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hosted_group_idempotency
    ADD CONSTRAINT hosted_group_idempotency_group_id_fkey FOREIGN KEY (group_id) REFERENCES public.hosted_groups(id) ON DELETE CASCADE;

--
-- Name: hosted_group_members hosted_group_members_group_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hosted_group_members
    ADD CONSTRAINT hosted_group_members_group_id_fkey FOREIGN KEY (group_id) REFERENCES public.hosted_groups(id) ON DELETE CASCADE;

--
-- Name: hosted_group_members hosted_group_members_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hosted_group_members
    ADD CONSTRAINT hosted_group_members_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: hosted_repository_idempotency hosted_repository_idempotency_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.hosted_repository_idempotency
    ADD CONSTRAINT hosted_repository_idempotency_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: lifecycle_jobs lifecycle_jobs_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.lifecycle_jobs
    ADD CONSTRAINT lifecycle_jobs_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: maven_group_members maven_group_members_group_name_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.maven_group_members
    ADD CONSTRAINT maven_group_members_group_name_fkey FOREIGN KEY (group_name) REFERENCES public.maven_groups(name) ON DELETE CASCADE;

--
-- Name: maven_group_members maven_group_members_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.maven_group_members
    ADD CONSTRAINT maven_group_members_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_apt_archive_object_intents native_apt_archive_object_intents_restore_id_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_archive_object_intents
    ADD CONSTRAINT native_apt_archive_object_intents_restore_id_repository_id_fkey FOREIGN KEY (restore_id, repository_id) REFERENCES public.native_apt_archive_restores(id, repository_id) ON DELETE CASCADE;

--
-- Name: native_apt_archive_restores native_apt_archive_restores_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_archive_restores
    ADD CONSTRAINT native_apt_archive_restores_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_apt_assets native_apt_assets_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_assets
    ADD CONSTRAINT native_apt_assets_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_apt_lifecycle_results native_apt_lifecycle_results_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_lifecycle_results
    ADD CONSTRAINT native_apt_lifecycle_results_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_apt_lifecycle_results native_apt_lifecycle_results_snapshot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_lifecycle_results
    ADD CONSTRAINT native_apt_lifecycle_results_snapshot_id_fkey FOREIGN KEY (snapshot_id) REFERENCES public.native_apt_repository_snapshots(id);

--
-- Name: native_apt_package_deletions native_apt_package_deletions_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_package_deletions
    ADD CONSTRAINT native_apt_package_deletions_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_apt_package_revisions native_apt_package_revisions_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_package_revisions
    ADD CONSTRAINT native_apt_package_revisions_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_apt_pool_paths native_apt_pool_paths_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_pool_paths
    ADD CONSTRAINT native_apt_pool_paths_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_apt_publication_idempotency native_apt_publication_idempotency_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_publication_idempotency
    ADD CONSTRAINT native_apt_publication_idempotency_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.native_apt_publication_sessions(id) ON DELETE CASCADE;

--
-- Name: native_apt_publication_sessions native_apt_publication_sessions_package_revision_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_publication_sessions
    ADD CONSTRAINT native_apt_publication_sessions_package_revision_fk FOREIGN KEY (package_revision_id) REFERENCES public.native_apt_package_revisions(id);

--
-- Name: native_apt_publication_sessions native_apt_publication_sessions_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_publication_sessions
    ADD CONSTRAINT native_apt_publication_sessions_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_apt_repository_snapshots native_apt_repository_snapshots_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_repository_snapshots
    ADD CONSTRAINT native_apt_repository_snapshots_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_apt_snapshot_assets native_apt_snapshot_assets_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_snapshot_assets
    ADD CONSTRAINT native_apt_snapshot_assets_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_apt_snapshot_assets native_apt_snapshot_assets_snapshot_repository_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_snapshot_assets
    ADD CONSTRAINT native_apt_snapshot_assets_snapshot_repository_fk FOREIGN KEY (snapshot_id, repository_id) REFERENCES public.native_apt_repository_snapshots(id, repository_id) ON DELETE CASCADE;

--
-- Name: native_apt_snapshot_object_intents native_apt_snapshot_object_inten_snapshot_id_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_snapshot_object_intents
    ADD CONSTRAINT native_apt_snapshot_object_inten_snapshot_id_repository_id_fkey FOREIGN KEY (snapshot_id, repository_id) REFERENCES public.native_apt_repository_snapshots(id, repository_id) ON DELETE CASCADE;

--
-- Name: native_apt_snapshot_packages native_apt_snapshot_packages_package_revision_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_snapshot_packages
    ADD CONSTRAINT native_apt_snapshot_packages_package_revision_id_fkey FOREIGN KEY (package_revision_id) REFERENCES public.native_apt_package_revisions(id);

--
-- Name: native_apt_snapshot_packages native_apt_snapshot_packages_publication_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_snapshot_packages
    ADD CONSTRAINT native_apt_snapshot_packages_publication_session_id_fkey FOREIGN KEY (publication_session_id) REFERENCES public.native_apt_publication_sessions(id);

--
-- Name: native_apt_snapshot_packages native_apt_snapshot_packages_snapshot_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_apt_snapshot_packages
    ADD CONSTRAINT native_apt_snapshot_packages_snapshot_id_fkey FOREIGN KEY (snapshot_id) REFERENCES public.native_apt_repository_snapshots(id) ON DELETE CASCADE;

--
-- Name: native_cargo_group_versions native_cargo_group_versions_group_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_cargo_group_versions
    ADD CONSTRAINT native_cargo_group_versions_group_id_fkey FOREIGN KEY (group_id) REFERENCES public.hosted_groups(id) ON DELETE CASCADE;

--
-- Name: native_cargo_identity_reservations native_cargo_identity_reservat_repository_id_collision_key_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_cargo_identity_reservations
    ADD CONSTRAINT native_cargo_identity_reservat_repository_id_collision_key_fkey FOREIGN KEY (repository_id, collision_key) REFERENCES public.native_cargo_names(repository_id, collision_key) ON DELETE CASCADE;

--
-- Name: native_cargo_names native_cargo_names_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_cargo_names
    ADD CONSTRAINT native_cargo_names_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_cargo_proxy_configs native_cargo_proxy_configs_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_cargo_proxy_configs
    ADD CONSTRAINT native_cargo_proxy_configs_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_cargo_proxy_crates native_cargo_proxy_crates_repository_id_collision_key_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_cargo_proxy_crates
    ADD CONSTRAINT native_cargo_proxy_crates_repository_id_collision_key_fkey FOREIGN KEY (repository_id, collision_key) REFERENCES public.native_cargo_proxy_indexes(repository_id, collision_key);

--
-- Name: native_cargo_proxy_crates native_cargo_proxy_crates_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_cargo_proxy_crates
    ADD CONSTRAINT native_cargo_proxy_crates_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_cargo_proxy_indexes native_cargo_proxy_indexes_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_cargo_proxy_indexes
    ADD CONSTRAINT native_cargo_proxy_indexes_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_cargo_publications native_cargo_publications_repository_id_collision_key_vers_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_cargo_publications
    ADD CONSTRAINT native_cargo_publications_repository_id_collision_key_vers_fkey FOREIGN KEY (repository_id, collision_key, version_key) REFERENCES public.native_cargo_identity_reservations(repository_id, collision_key, version_key) ON DELETE CASCADE;

--
-- Name: native_conan_assets native_conan_assets_object_key_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_conan_assets
    ADD CONSTRAINT native_conan_assets_object_key_fkey FOREIGN KEY (object_key) REFERENCES public.native_conan_object_intents(object_key);

--
-- Name: native_conan_assets native_conan_assets_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_conan_assets
    ADD CONSTRAINT native_conan_assets_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_conan_object_intents native_conan_object_intents_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_conan_object_intents
    ADD CONSTRAINT native_conan_object_intents_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_conan_package_revisions native_conan_package_revision_repository_id_reference_reci_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_conan_package_revisions
    ADD CONSTRAINT native_conan_package_revision_repository_id_reference_reci_fkey FOREIGN KEY (repository_id, reference, recipe_revision) REFERENCES public.native_conan_recipe_revisions(repository_id, reference, revision);

--
-- Name: native_conan_package_revisions native_conan_package_revisions_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_conan_package_revisions
    ADD CONSTRAINT native_conan_package_revisions_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_conan_publish_sessions native_conan_publish_sessions_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_conan_publish_sessions
    ADD CONSTRAINT native_conan_publish_sessions_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_conan_publish_uploads native_conan_publish_uploads_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_conan_publish_uploads
    ADD CONSTRAINT native_conan_publish_uploads_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.native_conan_publish_sessions(id) ON DELETE CASCADE;

--
-- Name: native_conan_recipe_revisions native_conan_recipe_revisions_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_conan_recipe_revisions
    ADD CONSTRAINT native_conan_recipe_revisions_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_go_assets native_go_assets_repository_id_module_path_version_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_go_assets
    ADD CONSTRAINT native_go_assets_repository_id_module_path_version_fkey FOREIGN KEY (repository_id, module_path, version) REFERENCES public.native_go_versions(repository_id, module_path, version) ON DELETE CASCADE;

--
-- Name: native_go_versions native_go_versions_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_go_versions
    ADD CONSTRAINT native_go_versions_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_helm_group_authorities native_helm_group_authorities_group_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_group_authorities
    ADD CONSTRAINT native_helm_group_authorities_group_id_fkey FOREIGN KEY (group_id) REFERENCES public.hosted_groups(id);

--
-- Name: native_helm_publication_sessions native_helm_publication_sessions_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_publication_sessions
    ADD CONSTRAINT native_helm_publication_sessions_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_helm_publications native_helm_publications_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_publications
    ADD CONSTRAINT native_helm_publications_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_helm_publications native_helm_publications_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_publications
    ADD CONSTRAINT native_helm_publications_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.native_helm_publication_sessions(id);

--
-- Name: native_helm_replication_items native_helm_replication_items_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_replication_items
    ADD CONSTRAINT native_helm_replication_items_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.native_helm_replication_jobs(id);

--
-- Name: native_helm_replication_items native_helm_replication_items_target_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_replication_items
    ADD CONSTRAINT native_helm_replication_items_target_session_id_fkey FOREIGN KEY (target_session_id) REFERENCES public.native_helm_publication_sessions(id);

--
-- Name: native_helm_replication_jobs native_helm_replication_jobs_source_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_replication_jobs
    ADD CONSTRAINT native_helm_replication_jobs_source_repository_id_fkey FOREIGN KEY (source_repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_helm_replication_jobs native_helm_replication_jobs_target_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_replication_jobs
    ADD CONSTRAINT native_helm_replication_jobs_target_repository_id_fkey FOREIGN KEY (target_repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_helm_replication_retries native_helm_replication_retries_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_replication_retries
    ADD CONSTRAINT native_helm_replication_retries_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.native_helm_replication_jobs(id);

--
-- Name: native_helm_source_documents native_helm_source_documents_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_helm_source_documents
    ADD CONSTRAINT native_helm_source_documents_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_maven_artifacts native_maven_artifacts_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_artifacts
    ADD CONSTRAINT native_maven_artifacts_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_maven_assets native_maven_assets_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_assets
    ADD CONSTRAINT native_maven_assets_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_maven_commit_idempotency native_maven_commit_idempotency_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_commit_idempotency
    ADD CONSTRAINT native_maven_commit_idempotency_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.native_maven_publish_sessions(id);

--
-- Name: native_maven_object_intents native_maven_object_intents_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_object_intents
    ADD CONSTRAINT native_maven_object_intents_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.native_maven_publish_sessions(id) ON DELETE CASCADE;

--
-- Name: native_maven_object_references native_maven_object_references_object_key_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_object_references
    ADD CONSTRAINT native_maven_object_references_object_key_fkey FOREIGN KEY (object_key) REFERENCES public.native_maven_object_intents(object_key) ON DELETE CASCADE;

--
-- Name: native_maven_object_references native_maven_object_references_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_object_references
    ADD CONSTRAINT native_maven_object_references_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_maven_publish_idempotency native_maven_publish_idempotency_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_publish_idempotency
    ADD CONSTRAINT native_maven_publish_idempotency_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.native_maven_publish_sessions(id);

--
-- Name: native_maven_publish_sessions native_maven_publish_sessions_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_publish_sessions
    ADD CONSTRAINT native_maven_publish_sessions_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_maven_publish_uploads native_maven_publish_uploads_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_publish_uploads
    ADD CONSTRAINT native_maven_publish_uploads_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.native_maven_publish_sessions(id) ON DELETE CASCADE;

--
-- Name: native_maven_snapshot_imports native_maven_snapshot_imports_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_snapshot_imports
    ADD CONSTRAINT native_maven_snapshot_imports_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_maven_snapshot_imports native_maven_snapshot_imports_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_maven_snapshot_imports
    ADD CONSTRAINT native_maven_snapshot_imports_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.native_maven_publish_sessions(id);

--
-- Name: native_npm_packages native_npm_packages_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_npm_packages
    ADD CONSTRAINT native_npm_packages_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_npm_versions native_npm_versions_repository_id_package_name_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_npm_versions
    ADD CONSTRAINT native_npm_versions_repository_id_package_name_fkey FOREIGN KEY (repository_id, package_name) REFERENCES public.native_npm_packages(repository_id, name) ON DELETE CASCADE;

--
-- Name: native_nuget_group_authorities native_nuget_group_authorities_group_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_group_authorities
    ADD CONSTRAINT native_nuget_group_authorities_group_id_fkey FOREIGN KEY (group_id) REFERENCES public.hosted_groups(id) ON DELETE CASCADE;

--
-- Name: native_nuget_group_authorities native_nuget_group_authorities_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_group_authorities
    ADD CONSTRAINT native_nuget_group_authorities_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_nuget_publication_sessions native_nuget_publication_sessions_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_publication_sessions
    ADD CONSTRAINT native_nuget_publication_sessions_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_nuget_publications native_nuget_publications_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_publications
    ADD CONSTRAINT native_nuget_publications_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_nuget_publications native_nuget_publications_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_publications
    ADD CONSTRAINT native_nuget_publications_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.native_nuget_publication_sessions(id);

--
-- Name: native_nuget_replication_items native_nuget_replication_items_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_replication_items
    ADD CONSTRAINT native_nuget_replication_items_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.native_nuget_replication_jobs(id);

--
-- Name: native_nuget_replication_items native_nuget_replication_items_target_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_replication_items
    ADD CONSTRAINT native_nuget_replication_items_target_session_id_fkey FOREIGN KEY (target_session_id) REFERENCES public.native_nuget_publication_sessions(id);

--
-- Name: native_nuget_replication_jobs native_nuget_replication_jobs_source_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_replication_jobs
    ADD CONSTRAINT native_nuget_replication_jobs_source_repository_id_fkey FOREIGN KEY (source_repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_nuget_replication_jobs native_nuget_replication_jobs_target_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_replication_jobs
    ADD CONSTRAINT native_nuget_replication_jobs_target_repository_id_fkey FOREIGN KEY (target_repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_nuget_replication_retries native_nuget_replication_retries_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_replication_retries
    ADD CONSTRAINT native_nuget_replication_retries_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.native_nuget_replication_jobs(id);

--
-- Name: native_nuget_source_documents native_nuget_source_documents_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_nuget_source_documents
    ADD CONSTRAINT native_nuget_source_documents_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_oci_manifest_tombstones native_oci_manifest_tombstones_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_manifest_tombstones
    ADD CONSTRAINT native_oci_manifest_tombstones_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_oci_manifests native_oci_manifests_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_manifests
    ADD CONSTRAINT native_oci_manifests_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_oci_object_intents native_oci_object_intents_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_object_intents
    ADD CONSTRAINT native_oci_object_intents_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_oci_repository_blobs native_oci_repository_blobs_digest_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_repository_blobs
    ADD CONSTRAINT native_oci_repository_blobs_digest_fkey FOREIGN KEY (digest) REFERENCES public.native_oci_blobs(digest);

--
-- Name: native_oci_repository_blobs native_oci_repository_blobs_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_repository_blobs
    ADD CONSTRAINT native_oci_repository_blobs_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_oci_tags native_oci_tags_repository_id_name_digest_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_tags
    ADD CONSTRAINT native_oci_tags_repository_id_name_digest_fkey FOREIGN KEY (repository_id, name, digest) REFERENCES public.native_oci_manifests(repository_id, name, digest);

--
-- Name: native_oci_uploads native_oci_uploads_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_oci_uploads
    ADD CONSTRAINT native_oci_uploads_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_pypi_files native_pypi_files_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_pypi_files
    ADD CONSTRAINT native_pypi_files_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_raw_asset_tombstones native_raw_asset_tombstones_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_raw_asset_tombstones
    ADD CONSTRAINT native_raw_asset_tombstones_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_raw_assets native_raw_assets_digest_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_raw_assets
    ADD CONSTRAINT native_raw_assets_digest_fkey FOREIGN KEY (digest) REFERENCES public.native_raw_objects(digest);

--
-- Name: native_raw_assets native_raw_assets_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_raw_assets
    ADD CONSTRAINT native_raw_assets_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_raw_objects native_raw_objects_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_raw_objects
    ADD CONSTRAINT native_raw_objects_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_raw_uploads native_raw_uploads_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_raw_uploads
    ADD CONSTRAINT native_raw_uploads_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_rubygems_quotas native_rubygems_quotas_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_rubygems_quotas
    ADD CONSTRAINT native_rubygems_quotas_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: native_rubygems_replication_items native_rubygems_replication_items_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_rubygems_replication_items
    ADD CONSTRAINT native_rubygems_replication_items_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.native_rubygems_replication_jobs(id);

--
-- Name: native_rubygems_replication_items native_rubygems_replication_items_target_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_rubygems_replication_items
    ADD CONSTRAINT native_rubygems_replication_items_target_session_id_fkey FOREIGN KEY (target_session_id) REFERENCES public.native_rubygems_sessions(id);

--
-- Name: native_rubygems_replication_jobs native_rubygems_replication_jobs_source_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_rubygems_replication_jobs
    ADD CONSTRAINT native_rubygems_replication_jobs_source_repository_id_fkey FOREIGN KEY (source_repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_rubygems_replication_jobs native_rubygems_replication_jobs_target_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_rubygems_replication_jobs
    ADD CONSTRAINT native_rubygems_replication_jobs_target_repository_id_fkey FOREIGN KEY (target_repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: native_rubygems_replication_retries native_rubygems_replication_retries_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_rubygems_replication_retries
    ADD CONSTRAINT native_rubygems_replication_retries_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.native_rubygems_replication_jobs(id);

--
-- Name: native_rubygems_sessions native_rubygems_sessions_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.native_rubygems_sessions
    ADD CONSTRAINT native_rubygems_sessions_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: nexus_migration_batches nexus_migration_batches_schedule_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_batches
    ADD CONSTRAINT nexus_migration_batches_schedule_id_fkey FOREIGN KEY (schedule_id) REFERENCES public.nexus_migration_schedules(id);

--
-- Name: nexus_migration_controls nexus_migration_controls_batch_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_controls
    ADD CONSTRAINT nexus_migration_controls_batch_id_fkey FOREIGN KEY (batch_id) REFERENCES public.nexus_migration_batches(id);

--
-- Name: nexus_migration_entries nexus_migration_entries_batch_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_entries
    ADD CONSTRAINT nexus_migration_entries_batch_id_fkey FOREIGN KEY (batch_id) REFERENCES public.nexus_migration_batches(id) ON DELETE CASCADE;

--
-- Name: nexus_migration_scan_pages nexus_migration_scan_pages_batch_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_scan_pages
    ADD CONSTRAINT nexus_migration_scan_pages_batch_id_fkey FOREIGN KEY (batch_id) REFERENCES public.nexus_migration_scans(batch_id) ON DELETE CASCADE;

--
-- Name: nexus_migration_scan_paths nexus_migration_scan_paths_batch_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_scan_paths
    ADD CONSTRAINT nexus_migration_scan_paths_batch_id_fkey FOREIGN KEY (batch_id) REFERENCES public.nexus_migration_scans(batch_id) ON DELETE CASCADE;

--
-- Name: nexus_migration_scans nexus_migration_scans_batch_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_scans
    ADD CONSTRAINT nexus_migration_scans_batch_id_fkey FOREIGN KEY (batch_id) REFERENCES public.nexus_migration_batches(id) ON DELETE CASCADE;

--
-- Name: nexus_migration_schedule_controls nexus_migration_schedule_controls_schedule_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_schedule_controls
    ADD CONSTRAINT nexus_migration_schedule_controls_schedule_id_fkey FOREIGN KEY (schedule_id) REFERENCES public.nexus_migration_schedules(id);

--
-- Name: nexus_migration_schedules nexus_migration_schedules_active_batch_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_schedules
    ADD CONSTRAINT nexus_migration_schedules_active_batch_id_fkey FOREIGN KEY (active_batch_id) REFERENCES public.nexus_migration_batches(id);

--
-- Name: nexus_migration_schedules nexus_migration_schedules_last_batch_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.nexus_migration_schedules
    ADD CONSTRAINT nexus_migration_schedules_last_batch_id_fkey FOREIGN KEY (last_batch_id) REFERENCES public.nexus_migration_batches(id);

--
-- Name: oci_bound_objects oci_bound_objects_scope_owner_image_subject_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oci_bound_objects
    ADD CONSTRAINT oci_bound_objects_scope_owner_image_subject_fkey FOREIGN KEY (scope, owner, image, subject) REFERENCES public.oci_subject_bindings(scope, owner, image, subject);

--
-- Name: oci_bound_objects oci_bound_objects_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oci_bound_objects
    ADD CONSTRAINT oci_bound_objects_source_id_fkey FOREIGN KEY (source_id) REFERENCES public.hosted_repositories(id);

--
-- Name: oci_group_members oci_group_members_group_name_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oci_group_members
    ADD CONSTRAINT oci_group_members_group_name_fkey FOREIGN KEY (group_name) REFERENCES public.oci_groups(name) ON DELETE CASCADE;

--
-- Name: oci_group_members oci_group_members_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oci_group_members
    ADD CONSTRAINT oci_group_members_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: oci_subject_bindings oci_subject_bindings_source_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.oci_subject_bindings
    ADD CONSTRAINT oci_subject_bindings_source_id_fkey FOREIGN KEY (source_id) REFERENCES public.hosted_repositories(id);

--
-- Name: raw_group_members raw_group_members_group_name_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.raw_group_members
    ADD CONSTRAINT raw_group_members_group_name_fkey FOREIGN KEY (group_name) REFERENCES public.raw_groups(name) ON DELETE CASCADE;

--
-- Name: raw_group_members raw_group_members_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.raw_group_members
    ADD CONSTRAINT raw_group_members_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: replication_checkpoints replication_checkpoints_plan_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.replication_checkpoints
    ADD CONSTRAINT replication_checkpoints_plan_id_fkey FOREIGN KEY (plan_id) REFERENCES public.replication_plans(id) ON DELETE CASCADE;

--
-- Name: replication_plans replication_plans_source_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.replication_plans
    ADD CONSTRAINT replication_plans_source_repository_id_fkey FOREIGN KEY (source_repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: replication_plans replication_plans_target_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.replication_plans
    ADD CONSTRAINT replication_plans_target_repository_id_fkey FOREIGN KEY (target_repository_id) REFERENCES public.hosted_repositories(id);

--
-- Name: repository_capacity_quotas repository_capacity_quotas_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_capacity_quotas
    ADD CONSTRAINT repository_capacity_quotas_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: repository_grant_sets repository_grant_sets_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_grant_sets
    ADD CONSTRAINT repository_grant_sets_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: repository_grants repository_grants_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_grants
    ADD CONSTRAINT repository_grants_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repository_grant_sets(repository_id) ON DELETE CASCADE;

--
-- Name: repository_quarantine_read_policies repository_quarantine_read_policies_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_quarantine_read_policies
    ADD CONSTRAINT repository_quarantine_read_policies_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: repository_quota_alert_events repository_quota_alert_events_delivery_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_quota_alert_events
    ADD CONSTRAINT repository_quota_alert_events_delivery_id_fkey FOREIGN KEY (delivery_id) REFERENCES public.email_test_deliveries(id);

--
-- Name: repository_quota_alert_events repository_quota_alert_events_rule_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_quota_alert_events
    ADD CONSTRAINT repository_quota_alert_events_rule_id_fkey FOREIGN KEY (rule_id) REFERENCES public.repository_quota_alert_rules(id);

--
-- Name: repository_quota_alert_events repository_quota_alert_events_target_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_quota_alert_events
    ADD CONSTRAINT repository_quota_alert_events_target_id_fkey FOREIGN KEY (target_id) REFERENCES public.email_targets(id);

--
-- Name: repository_quota_alert_rules repository_quota_alert_rules_target_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_quota_alert_rules
    ADD CONSTRAINT repository_quota_alert_rules_target_id_fkey FOREIGN KEY (target_id) REFERENCES public.email_targets(id);

--
-- Name: repository_retention_policies repository_retention_policies_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_retention_policies
    ADD CONSTRAINT repository_retention_policies_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: repository_security_policies repository_security_policies_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_security_policies
    ADD CONSTRAINT repository_security_policies_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: scheduled_task_runs scheduled_task_runs_task_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scheduled_task_runs
    ADD CONSTRAINT scheduled_task_runs_task_id_fkey FOREIGN KEY (task_id) REFERENCES public.scheduled_tasks(id) ON DELETE CASCADE;

--
-- Name: scheduled_tasks scheduled_tasks_repository_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.scheduled_tasks
    ADD CONSTRAINT scheduled_tasks_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.hosted_repositories(id) ON DELETE CASCADE;

--
-- Name: user_identities user_identities_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_identities
    ADD CONSTRAINT user_identities_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: user_sessions user_sessions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_sessions
    ADD CONSTRAINT user_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- Name: webhook_deliveries webhook_deliveries_event_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_event_id_fkey FOREIGN KEY (event_id) REFERENCES public.webhook_events(id) ON DELETE CASCADE;

--
-- Name: webhook_deliveries webhook_deliveries_subscription_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.webhook_deliveries
    ADD CONSTRAINT webhook_deliveries_subscription_id_fkey FOREIGN KEY (subscription_id) REFERENCES public.webhook_subscriptions(id) ON DELETE CASCADE;


-- seed: singleton rows the code expects to exist (ADR 0015, step 5).
--
-- Only rows of an empty installation are seeded. Backfills of pre-existing rows
-- (native OCI tombstones, repository grants, cargo webhook ids) and the
-- skipped_existing branch of the presets seed do not apply to a fresh database.
-- Data backfills belong in later numbered migrations, never in this baseline.

-- Originating migration: 000049_audit_retention.sql
INSERT INTO public.audit_retention_policy (singleton) VALUES (true) ON CONFLICT DO NOTHING;

-- Originating migration: 000055_anonymous_access_policy.sql
-- Anonymous read stays disabled: enabled defaults to false.
INSERT INTO public.anonymous_access_policy (singleton) VALUES (true) ON CONFLICT DO NOTHING;

-- Originating migration: 000107_site_settings.sql
INSERT INTO public.site_settings (singleton) VALUES (true) ON CONFLICT (singleton) DO NOTHING;

-- Originating migration: 000174_installation_repository_presets.sql
-- A fresh installation has no repositories, so the catalog is pending.
INSERT INTO public.installation_repository_presets (singleton, catalog_version, state)
VALUES (true, 'standard-v1', 'pending')
ON CONFLICT (singleton) DO NOTHING;
