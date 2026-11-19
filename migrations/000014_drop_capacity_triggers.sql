-- +goose Up

-- ADR 0023 section 3 (slice 8, last step): quota enforcement moves entirely to the
-- application. Every hosted format calls capacity.Reserve as the last statement of its
-- publishing transaction (the registry test in internal/capacity shows every format
-- covered), so the capacity triggers, which summed the whole repository from an
-- AFTER INSERT OR UPDATE ... FOR EACH ROW trigger, are dropped. This supersedes the
-- AssertRepositoryCapacity of ADR 0022 slice 7. Lowering a quota below current usage is
-- rejected by the quota API (ReplaceRepositoryCapacityQuota) against the counter row.
--
-- MySQL (ADR 0022): there is no migrations/mysql directory yet; the twin has no triggers
-- to create.

DROP TRIGGER IF EXISTS apt_asset_capacity_check ON public.native_apt_assets;
DROP TRIGGER IF EXISTS cargo_publication_capacity_check ON public.native_cargo_publications;
DROP TRIGGER IF EXISTS conan_asset_capacity_check ON public.native_conan_assets;
DROP TRIGGER IF EXISTS conan_package_capacity_check ON public.native_conan_package_revisions;
DROP TRIGGER IF EXISTS conan_recipe_capacity_check ON public.native_conan_recipe_revisions;
DROP TRIGGER IF EXISTS go_asset_capacity_check ON public.native_go_assets;
DROP TRIGGER IF EXISTS maven_artifact_capacity_check ON public.native_maven_artifacts;
DROP TRIGGER IF EXISTS maven_asset_capacity_check ON public.native_maven_assets;
DROP TRIGGER IF EXISTS npm_version_capacity_check ON public.native_npm_versions;
DROP TRIGGER IF EXISTS oci_blob_capacity_check ON public.native_oci_repository_blobs;
DROP TRIGGER IF EXISTS oci_manifest_capacity_check ON public.native_oci_manifests;
DROP TRIGGER IF EXISTS pypi_file_capacity_check ON public.native_pypi_files;
DROP TRIGGER IF EXISTS raw_asset_capacity_check ON public.native_raw_assets;
DROP TRIGGER IF EXISTS repository_capacity_quota_check ON public.repository_capacity_quotas;

DROP FUNCTION IF EXISTS public.assert_hosted_repository_capacity_trigger();
DROP FUNCTION IF EXISTS public.assert_hosted_repository_capacity(uuid);
DROP FUNCTION IF EXISTS public.assert_apt_repository_capacity_trigger();
DROP FUNCTION IF EXISTS public.assert_cargo_repository_capacity();
