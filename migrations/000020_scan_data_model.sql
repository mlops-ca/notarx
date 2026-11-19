-- +goose Up

-- ADR 0030 section 2 (slice 2a): SBOM bodies, vulnerability findings, licences and scan history
-- owned by notarx instead of the scanner's volume. All tables follow ADR 0023: UUIDv7 ids from
-- internal/ids, typed uuid keys, bounded varchar columns wherever a column is indexed (so the MySQL
-- catalogue needs no prefix indexes, ADR 0022), no foreign key into a partitioned table and no
-- partial or expression index.
--
-- artifact_vulnerability_finding has no foreign key to artifact_vulnerability: a scan inserts its
-- findings under a fresh vulnerability_id before the one-row swap, and a batched sweeper deletes the
-- findings of the replaced id afterwards, so no transaction touches 100,000 rows.
--
-- MySQL (ADR 0022): there is no migrations/mysql directory and no neutral catalogue yet (issue 104 is
-- now blocked by this slice and will add these tables). The twin maps uuid to BINARY(16) or CHAR(36),
-- jsonb to JSON and timestamptz to DATETIME(6), and has no counterpart of the partition clause beyond
-- PARTITION BY RANGE COLUMNS (scanned_at).

CREATE TABLE public.artifact_sbom (
    id uuid PRIMARY KEY,
    repository_id uuid NOT NULL,
    artifact_digest varchar(80) NOT NULL,
    kind varchar(32) NOT NULL,
    source varchar(16) NOT NULL,
    generator varchar(64) NOT NULL DEFAULT '',
    generator_version varchar(32) NOT NULL DEFAULT '',
    spec_version varchar(16) NOT NULL DEFAULT '',
    body_digest varchar(80) NOT NULL,
    body_size bigint NOT NULL,
    body_key text NOT NULL DEFAULT '',
    body jsonb,
    component_count integer NOT NULL DEFAULT 0,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT artifact_sbom_unique UNIQUE (repository_id, artifact_digest, kind, source),
    CONSTRAINT artifact_sbom_kind_check CHECK (kind IN ('cyclonedx-json', 'spdx-json')),
    CONSTRAINT artifact_sbom_source_check CHECK (source IN ('generated', 'uploaded', 'promoted')),
    CONSTRAINT artifact_sbom_size_check CHECK (body_size >= 0 AND component_count >= 0),
    CONSTRAINT artifact_sbom_body_check CHECK (body IS NOT NULL OR body_key <> '')
);

CREATE TABLE public.scanner_db_generation (
    id uuid PRIMARY KEY,
    kind varchar(16) NOT NULL,
    oci_digest varchar(80) NOT NULL,
    built_at timestamp with time zone NOT NULL,
    observed_at timestamp with time zone NOT NULL,
    source varchar(16) NOT NULL,
    -- A generation is observed many times; recording one again only moves observed_at.
    CONSTRAINT scanner_db_generation_unique UNIQUE (kind, oci_digest),
    CONSTRAINT scanner_db_generation_kind_check CHECK (kind IN ('trivy-db', 'java-db', 'checks')),
    CONSTRAINT scanner_db_generation_source_check CHECK (source IN ('upstream', 'mirror', 'feed'))
);

CREATE INDEX scanner_db_generation_kind_built_at_idx ON public.scanner_db_generation USING btree (kind, built_at DESC);

CREATE TABLE public.artifact_vulnerability (
    id uuid PRIMARY KEY,
    repository_id uuid NOT NULL,
    artifact_digest varchar(80) NOT NULL,
    scanner varchar(32) NOT NULL,
    scanner_version varchar(32) NOT NULL DEFAULT '',
    db_generation_id uuid,
    status varchar(16) NOT NULL,
    critical integer NOT NULL DEFAULT 0,
    high integer NOT NULL DEFAULT 0,
    medium integer NOT NULL DEFAULT 0,
    low integer NOT NULL DEFAULT 0,
    unknown integer NOT NULL DEFAULT 0,
    suppressed_count integer NOT NULL DEFAULT 0,
    findings_total integer NOT NULL DEFAULT 0,
    truncated boolean NOT NULL DEFAULT false,
    not_scanned_reason text,
    scanned_at timestamp with time zone NOT NULL,
    CONSTRAINT artifact_vulnerability_unique UNIQUE (repository_id, artifact_digest),
    CONSTRAINT artifact_vulnerability_status_check CHECK (status IN ('clean', 'vulnerable', 'stale_db', 'not_scanned', 'error')),
    CONSTRAINT artifact_vulnerability_counts_check CHECK (
        critical >= 0 AND high >= 0 AND medium >= 0 AND low >= 0 AND unknown >= 0
        AND suppressed_count >= 0 AND findings_total >= 0)
);

CREATE INDEX artifact_vulnerability_generation_idx ON public.artifact_vulnerability USING btree (db_generation_id, scanned_at, id);

CREATE TABLE public.artifact_vulnerability_finding (
    vulnerability_id uuid NOT NULL,
    ordinal integer NOT NULL,
    vuln_id varchar(128) NOT NULL,
    source varchar(32) NOT NULL DEFAULT '',
    severity varchar(16) NOT NULL,
    severity_rank smallint NOT NULL,
    component text NOT NULL DEFAULT '',
    version text NOT NULL DEFAULT '',
    fixed_version text NOT NULL DEFAULT '',
    location text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    description varchar(4096),
    url text NOT NULL DEFAULT '',
    cvss_score numeric,
    cvss_vector text NOT NULL DEFAULT '',
    status varchar(16) NOT NULL,
    vex_status varchar(32),
    vex_justification text,
    epss numeric,
    kev boolean,
    PRIMARY KEY (vulnerability_id, ordinal),
    CONSTRAINT artifact_vulnerability_finding_status_check CHECK (status IN ('affected', 'suppressed', 'fixed')),
    CONSTRAINT artifact_vulnerability_finding_severity_check CHECK (severity IN ('critical', 'high', 'medium', 'low', 'unknown'))
);

-- Keyset-ready listing: worst severity first, then the vulnerability id, with ordinal as the tie breaker.
CREATE INDEX artifact_vulnerability_finding_page_idx ON public.artifact_vulnerability_finding USING btree (vulnerability_id, severity_rank, vuln_id, ordinal);

CREATE TABLE public.artifact_license (
    repository_id uuid NOT NULL,
    artifact_digest varchar(80) NOT NULL,
    spdx_id varchar(128) NOT NULL,
    source varchar(32) NOT NULL,
    confidence numeric NOT NULL DEFAULT 1,
    PRIMARY KEY (repository_id, artifact_digest, spdx_id, source)
);

-- Append-only and partitioned by month (ADR 0023 section 5): the partition key leads the primary key
-- because MySQL requires it. internal/partitions keeps the partitions and drops them after
-- NOTARX_SCAN_HISTORY_RETENTION (default 180 days); rows outside every partition land in the DEFAULT one.
CREATE TABLE public.artifact_scan_history (
    id uuid NOT NULL,
    repository_id uuid NOT NULL,
    artifact_digest varchar(80) NOT NULL,
    scanner_version varchar(32) NOT NULL DEFAULT '',
    db_generation_id uuid,
    status varchar(16) NOT NULL,
    critical integer NOT NULL DEFAULT 0,
    high integer NOT NULL DEFAULT 0,
    medium integer NOT NULL DEFAULT 0,
    low integer NOT NULL DEFAULT 0,
    unknown integer NOT NULL DEFAULT 0,
    scanned_at timestamp with time zone NOT NULL,
    CONSTRAINT artifact_scan_history_pkey PRIMARY KEY (scanned_at, id)
) PARTITION BY RANGE (scanned_at);

CREATE TABLE public.artifact_scan_history_default PARTITION OF public.artifact_scan_history DEFAULT;

CREATE INDEX artifact_scan_history_artifact_idx ON public.artifact_scan_history USING btree (repository_id, artifact_digest, scanned_at DESC);

CREATE TABLE public.repository_vex_document (
    id uuid PRIMARY KEY,
    repository_id uuid NOT NULL,
    format varchar(16) NOT NULL,
    body_digest varchar(80) NOT NULL,
    body_key text NOT NULL,
    author text NOT NULL DEFAULT '',
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    created_by uuid,
    CONSTRAINT repository_vex_document_format_check CHECK (format IN ('openvex'))
);

CREATE INDEX repository_vex_document_repository_idx ON public.repository_vex_document USING btree (repository_id, created_at DESC, id);
