-- +goose Up

-- ADR 0023 slice 9: index hygiene, publisher persistence and vacuum settings.
--
-- Foreign keys: every foreign key now has an index whose leading columns are the key
-- columns, so deleting or updating a parent row (a repository, an upload session, a
-- manifest) does not scan the child table. The three keys left without one are
-- reviewed in internal/repository/schema_fk_index_integration_test.go.
-- native_oci_tags gets (repository_id, digest, name) instead of the key order so
-- "tags that point at a digest" is served too.
--
-- Other indexes: committed publish sessions for Maven and Conan (the lookup that the
-- search projection used per row), the lifecycle job claim shape by kind,
-- native_maven_assets(digest). ADR 0023 also lists user_group_members,
-- promotion_evaluations, artifact_promotions, saml_assertion_ids and
-- saml_request_state; those tables do not exist yet and receive their indexes in the
-- migration that creates them.
--
-- pg_trgm: every GIN index gets fastupdate=on and gin_pending_list_limit=1024. The
-- indexes whose table has a liveness column are already partial (state='visible' or
-- negative=false); the others (apt, go, oci manifests, raw, cargo names) have no such
-- column, so there is nothing to restrict them to.
--
-- Publisher: native_maven_artifacts and native_conan_recipe_revisions persist the
-- publisher at publish time and artifact_search_projection reads the column instead
-- of a per-row LATERAL join over publish sessions.
--
-- Vacuum: queue and hot-update tables get fillfactor=80 and autovacuum scale factors
-- of 0.02. This reserves page space for pruning and frees dead tuples sooner. It does
-- not promise HOT updates for the state-machine queues, whose state and
-- next_attempt_at columns are indexed; verify with pg_stat_user_tables.n_tup_hot_upd.
-- repository_usage_counters (fillfactor 70) is part of the quota counter slice.
--
-- Tables whose primary key repeats a repository name (inventory): none repeats the
-- name of a Gateway repository. nexus_migration_scan_pages and
-- nexus_migration_scan_paths repeat source_repository, the name of a repository on
-- the remote Nexus server, which has no Gateway id and is not re-keyed.
-- resolver_audit_log and security_audit_log keep the historical repository name by
-- design (no foreign key, so audit survives deletion) beside repository_id.
--
-- MySQL (ADR 0022): there is no migrations/mysql directory yet, so no twin is added.
-- The twin is mechanical: plain indexes for the foreign keys; a stored generated
-- column committed_expires = IF(state='committed', expires_at, NULL) with an index
-- on (repository_id, coordinate, committed_expires) replaces the partial publish
-- session indexes; a generated column kind_actionable = IF(state IN
-- ('pending','retrying'), next_attempt_at, NULL) with an index on (kind,
-- kind_actionable) replaces the lifecycle claim index; fillfactor and autovacuum
-- settings and the GIN pending list have no MySQL equivalent.

CREATE INDEX api_keys_service_account_id_fk_idx ON public.api_keys USING btree (service_account_id);
CREATE INDEX conan_group_members_repository_id_fk_idx ON public.conan_group_members USING btree (repository_id);
CREATE INDEX email_test_deliveries_quota_rule_id_fk_idx ON public.email_test_deliveries USING btree (quota_rule_id);
CREATE INDEX email_test_deliveries_target_id_fk_idx ON public.email_test_deliveries USING btree (target_id);
CREATE INDEX hosted_group_idempotency_group_id_fk_idx ON public.hosted_group_idempotency USING btree (group_id);
CREATE INDEX hosted_group_members_repository_id_fk_idx ON public.hosted_group_members USING btree (repository_id);
CREATE INDEX hosted_repository_idempotency_repository_id_fk_idx ON public.hosted_repository_idempotency USING btree (repository_id);
CREATE INDEX maven_group_members_repository_id_fk_idx ON public.maven_group_members USING btree (repository_id);
CREATE INDEX native_apt_archive_object_intents_restore_id_repository_8_fk ON public.native_apt_archive_object_intents USING btree (restore_id, repository_id);
CREATE INDEX native_apt_archive_restores_repository_id_fk_idx ON public.native_apt_archive_restores USING btree (repository_id);
CREATE INDEX native_apt_lifecycle_results_repository_id_fk_idx ON public.native_apt_lifecycle_results USING btree (repository_id);
CREATE INDEX native_apt_lifecycle_results_snapshot_id_fk_idx ON public.native_apt_lifecycle_results USING btree (snapshot_id);
CREATE INDEX native_apt_publication_idempotency_session_id_fk_idx ON public.native_apt_publication_idempotency USING btree (session_id);
CREATE INDEX native_apt_publication_sessions_package_revision_id_fk_idx ON public.native_apt_publication_sessions USING btree (package_revision_id);
CREATE INDEX native_apt_publication_sessions_repository_id_fk_idx ON public.native_apt_publication_sessions USING btree (repository_id);
CREATE INDEX native_apt_snapshot_assets_snapshot_id_repository_id_fk_idx ON public.native_apt_snapshot_assets USING btree (snapshot_id, repository_id);
CREATE INDEX native_apt_snapshot_object_intents_snapshot_id_reposito_16_fk ON public.native_apt_snapshot_object_intents USING btree (snapshot_id, repository_id);
CREATE INDEX native_apt_snapshot_packages_package_revision_id_fk_idx ON public.native_apt_snapshot_packages USING btree (package_revision_id);
CREATE INDEX native_apt_snapshot_packages_publication_session_id_fk_idx ON public.native_apt_snapshot_packages USING btree (publication_session_id);
CREATE INDEX native_conan_object_intents_repository_id_fk_idx ON public.native_conan_object_intents USING btree (repository_id);
CREATE INDEX native_conan_publish_sessions_repository_id_fk_idx ON public.native_conan_publish_sessions USING btree (repository_id);
CREATE INDEX native_helm_publication_sessions_repository_id_fk_idx ON public.native_helm_publication_sessions USING btree (repository_id);
CREATE INDEX native_helm_replication_items_target_session_id_fk_idx ON public.native_helm_replication_items USING btree (target_session_id);
CREATE INDEX native_helm_replication_jobs_source_repository_id_fk_idx ON public.native_helm_replication_jobs USING btree (source_repository_id);
CREATE INDEX native_helm_replication_jobs_target_repository_id_fk_idx ON public.native_helm_replication_jobs USING btree (target_repository_id);
CREATE INDEX native_maven_object_intents_session_id_fk_idx ON public.native_maven_object_intents USING btree (session_id);
CREATE INDEX native_maven_object_references_repository_id_fk_idx ON public.native_maven_object_references USING btree (repository_id);
CREATE INDEX native_maven_publish_idempotency_session_id_fk_idx ON public.native_maven_publish_idempotency USING btree (session_id);
CREATE INDEX native_maven_publish_sessions_repository_id_fk_idx ON public.native_maven_publish_sessions USING btree (repository_id);
CREATE INDEX native_maven_snapshot_imports_session_id_fk_idx ON public.native_maven_snapshot_imports USING btree (session_id);
CREATE INDEX native_nuget_group_authorities_repository_id_fk_idx ON public.native_nuget_group_authorities USING btree (repository_id);
CREATE INDEX native_nuget_publication_sessions_repository_id_fk_idx ON public.native_nuget_publication_sessions USING btree (repository_id);
CREATE INDEX native_nuget_replication_items_target_session_id_fk_idx ON public.native_nuget_replication_items USING btree (target_session_id);
CREATE INDEX native_nuget_replication_jobs_source_repository_id_fk_idx ON public.native_nuget_replication_jobs USING btree (source_repository_id);
CREATE INDEX native_nuget_replication_jobs_target_repository_id_fk_idx ON public.native_nuget_replication_jobs USING btree (target_repository_id);
CREATE INDEX native_oci_object_intents_repository_id_fk_idx ON public.native_oci_object_intents USING btree (repository_id);
CREATE INDEX native_oci_repository_blobs_digest_fk_idx ON public.native_oci_repository_blobs USING btree (digest);
CREATE INDEX native_oci_tags_repository_id_digest_name_fk_idx ON public.native_oci_tags USING btree (repository_id, digest, name);
CREATE INDEX native_oci_uploads_repository_id_fk_idx ON public.native_oci_uploads USING btree (repository_id);
CREATE INDEX native_raw_objects_repository_id_fk_idx ON public.native_raw_objects USING btree (repository_id);
CREATE INDEX native_raw_uploads_repository_id_fk_idx ON public.native_raw_uploads USING btree (repository_id);
CREATE INDEX native_rubygems_replication_items_target_session_id_fk_idx ON public.native_rubygems_replication_items USING btree (target_session_id);
CREATE INDEX native_rubygems_replication_jobs_source_repository_id_fk_idx ON public.native_rubygems_replication_jobs USING btree (source_repository_id);
CREATE INDEX native_rubygems_replication_jobs_target_repository_id_fk_idx ON public.native_rubygems_replication_jobs USING btree (target_repository_id);
CREATE INDEX native_rubygems_sessions_repository_id_fk_idx ON public.native_rubygems_sessions USING btree (repository_id);
CREATE INDEX oci_bound_objects_scope_owner_image_subject_fk_idx ON public.oci_bound_objects USING btree (scope, owner, image, subject);
CREATE INDEX oci_bound_objects_source_id_fk_idx ON public.oci_bound_objects USING btree (source_id);
CREATE INDEX oci_group_members_repository_id_fk_idx ON public.oci_group_members USING btree (repository_id);
CREATE INDEX oci_subject_bindings_source_id_fk_idx ON public.oci_subject_bindings USING btree (source_id);
CREATE INDEX raw_group_members_repository_id_fk_idx ON public.raw_group_members USING btree (repository_id);
CREATE INDEX replication_plans_source_repository_id_fk_idx ON public.replication_plans USING btree (source_repository_id);
CREATE INDEX repository_quota_alert_events_delivery_id_fk_idx ON public.repository_quota_alert_events USING btree (delivery_id);
CREATE INDEX repository_quota_alert_events_target_id_fk_idx ON public.repository_quota_alert_events USING btree (target_id);
CREATE INDEX repository_quota_alert_rules_target_id_fk_idx ON public.repository_quota_alert_rules USING btree (target_id);
CREATE INDEX scheduled_tasks_repository_id_fk_idx ON public.scheduled_tasks USING btree (repository_id);
CREATE INDEX user_sessions_user_id_fk_idx ON public.user_sessions USING btree (user_id);

CREATE INDEX native_maven_assets_digest_idx ON public.native_maven_assets USING btree (digest);
CREATE INDEX native_maven_publish_sessions_committed_idx ON public.native_maven_publish_sessions USING btree (repository_id, coordinate, expires_at DESC) WHERE state = 'committed';
CREATE INDEX native_conan_publish_sessions_committed_idx ON public.native_conan_publish_sessions USING btree (repository_id, reference, expires_at DESC) WHERE state = 'committed';
CREATE INDEX lifecycle_jobs_kind_actionable_idx ON public.lifecycle_jobs USING btree (kind, next_attempt_at) WHERE state IN ('pending', 'retrying');

ALTER INDEX public.native_apt_assets_path_trgm_idx SET (fastupdate = on, gin_pending_list_limit = 1024);
ALTER INDEX public.native_apt_snapshot_assets_path_trgm_idx SET (fastupdate = on, gin_pending_list_limit = 1024);
ALTER INDEX public.native_cargo_names_name_trgm_idx SET (fastupdate = on, gin_pending_list_limit = 1024);
ALTER INDEX public.native_cargo_proxy_indexes_positive_name_trgm_idx SET (fastupdate = on, gin_pending_list_limit = 1024);
ALTER INDEX public.native_conan_recipe_revisions_visible_reference_trgm_idx SET (fastupdate = on, gin_pending_list_limit = 1024);
ALTER INDEX public.native_go_versions_module_path_trgm_idx SET (fastupdate = on, gin_pending_list_limit = 1024);
ALTER INDEX public.native_maven_artifacts_visible_coordinate_trgm_idx SET (fastupdate = on, gin_pending_list_limit = 1024);
ALTER INDEX public.native_npm_packages_name_trgm_idx SET (fastupdate = on, gin_pending_list_limit = 1024);
ALTER INDEX public.native_npm_versions_visible_name_trgm_idx SET (fastupdate = on, gin_pending_list_limit = 1024);
ALTER INDEX public.native_oci_manifests_name_trgm_idx SET (fastupdate = on, gin_pending_list_limit = 1024);
ALTER INDEX public.native_pypi_files_visible_project_trgm_idx SET (fastupdate = on, gin_pending_list_limit = 1024);
ALTER INDEX public.native_raw_assets_path_trgm_idx SET (fastupdate = on, gin_pending_list_limit = 1024);

ALTER TABLE public.lifecycle_jobs SET (fillfactor = 80, autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.02);
ALTER TABLE public.webhook_deliveries SET (fillfactor = 80, autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.02);
ALTER TABLE public.replication_plans SET (fillfactor = 80, autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.02);
ALTER TABLE public.cache_tasks SET (fillfactor = 80, autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.02);
ALTER TABLE public.artifact_usage_stats SET (fillfactor = 80, autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.02);
ALTER TABLE public.api_keys SET (fillfactor = 80, autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.02);
ALTER TABLE public.user_sessions SET (fillfactor = 80, autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.02);
ALTER TABLE public.runtime_nodes SET (fillfactor = 80, autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.02);

ALTER TABLE public.native_maven_artifacts ADD COLUMN publisher text NOT NULL DEFAULT '';
UPDATE public.native_maven_artifacts a
   SET publisher = COALESCE((SELECT s.publisher FROM public.native_maven_publish_sessions s
                              WHERE s.repository_id = a.repository_id AND s.coordinate = a.coordinate AND s.state = 'committed'
                              ORDER BY s.expires_at DESC LIMIT 1), '');
ALTER TABLE public.native_conan_recipe_revisions ADD COLUMN publisher text NOT NULL DEFAULT '';
UPDATE public.native_conan_recipe_revisions r
   SET publisher = COALESCE((SELECT s.publisher FROM public.native_conan_publish_sessions s
                              WHERE s.repository_id = r.repository_id AND s.reference = r.reference AND s.state = 'committed'
                              ORDER BY s.expires_at DESC LIMIT 1), '');

CREATE OR REPLACE VIEW public.artifact_search_projection AS
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
            a.publisher,
            a.build_number,
            NULL::text AS content_type,
            split_part(a.coordinate, ':'::text, 3) AS version
           FROM native_maven_artifacts a
          WHERE a.state = 'visible'::text
        UNION ALL
         SELECT m.repository_id,
            'oci'::text AS text,
            m.name,
            m.digest,
            m.created_at,
            m.size,
            ''::text AS text,
            0,
            m.media_type,
            NULL::text AS text
           FROM native_oci_manifests m
        UNION ALL
         SELECT r.repository_id,
            'conan'::text AS text,
            r.reference,
            r.digest,
            r.created_at,
            NULL::bigint AS int8,
            r.publisher,
            0,
            NULL::text AS text,
            split_part(r.reference, '/'::text, 2) AS split_part
           FROM native_conan_recipe_revisions r
          WHERE r.state = 'visible'::text
        UNION ALL
         SELECT a.repository_id,
            'raw'::text AS text,
            a.path,
            a.digest,
            a.updated_at,
            o.size,
            ''::text AS text,
            0,
            a.content_type,
            NULL::text AS text
           FROM native_raw_assets a
             JOIN native_raw_objects o ON o.digest = a.digest
        UNION ALL
         SELECT v.repository_id,
            'npm'::text AS text,
            v.package_name,
            v.digest,
            v.created_at,
            v.size,
            v.publisher,
            0,
            'application/octet-stream'::text AS text,
            v.version
           FROM native_npm_versions v
             JOIN native_npm_packages p ON p.repository_id = v.repository_id AND p.name = v.package_name
          WHERE NOT p.negative AND v.state = 'visible'::text
        UNION ALL
         SELECT f.repository_id,
            'pypi'::text AS text,
            f.project,
            f.digest,
            f.created_at,
            f.size,
            f.publisher,
            0,
            'application/octet-stream'::text AS text,
            f.version
           FROM native_pypi_files f
          WHERE f.state = 'visible'::text
        UNION ALL
         SELECT v.repository_id,
            'go'::text AS text,
            v.module_path,
            COALESCE(a.digest, ''::text) AS "coalesce",
            v.created_at,
            a.size,
            v.publisher,
            0,
                CASE
                    WHEN a.kind = 'zip'::text THEN 'application/zip'::text
                    ELSE 'text/plain'::text
                END AS "case",
            v.version
           FROM native_go_versions v
             LEFT JOIN LATERAL ( SELECT a_1.digest,
                    a_1.size,
                    a_1.kind
                   FROM native_go_assets a_1
                  WHERE a_1.repository_id = v.repository_id AND a_1.module_path = v.module_path AND a_1.version = v.version
                  ORDER BY (
                        CASE a_1.kind
                            WHEN 'zip'::text THEN 0
                            WHEN 'mod'::text THEN 1
                            ELSE 2
                        END)
                 LIMIT 1) a ON true
        UNION ALL
         SELECT a.repository_id,
            'apt'::text AS text,
            a.path,
            a.digest,
            a.created_at,
            a.size,
            ''::text AS text,
            0,
            a.content_type,
            NULL::text AS text
           FROM native_apt_assets a
        UNION ALL
         SELECT a.repository_id,
            'apt'::text AS text,
            a.path,
            a.digest,
            s.created_at,
            a.size,
            ''::text AS text,
            0,
            a.content_type,
            NULL::text AS text
           FROM native_apt_snapshot_assets a
             JOIN native_apt_repository_snapshots s ON s.id = a.snapshot_id
          WHERE s.state = 'visible'::text) projection;
