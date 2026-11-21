-- +goose Up

-- ADR 0019: a workload_federation principal (a service account reached through a workload token, the trust policy id
-- as credential id) may create and run Helm, NuGet and RubyGems replication jobs. The execution path revalidates the
-- policy, its issuer and the service account at every commit.
ALTER TABLE public.native_helm_replication_jobs DROP CONSTRAINT native_helm_replication_jobs_actor_kind_check;
ALTER TABLE public.native_helm_replication_jobs ADD CONSTRAINT native_helm_replication_jobs_actor_kind_check CHECK ((actor_kind = ANY (ARRAY['static_admin'::text, 'static_resolver'::text, 'local_session'::text, 'oidc'::text, 'api_key'::text, 'service_account_credential'::text, 'workload_federation'::text])));
ALTER TABLE public.native_nuget_replication_jobs DROP CONSTRAINT native_nuget_replication_jobs_actor_kind_check;
ALTER TABLE public.native_nuget_replication_jobs ADD CONSTRAINT native_nuget_replication_jobs_actor_kind_check CHECK ((actor_kind = ANY (ARRAY['static_admin'::text, 'static_resolver'::text, 'local_session'::text, 'oidc'::text, 'api_key'::text, 'service_account_credential'::text, 'workload_federation'::text])));
ALTER TABLE public.native_rubygems_replication_jobs DROP CONSTRAINT native_rubygems_replication_jobs_actor_kind_check;
ALTER TABLE public.native_rubygems_replication_jobs ADD CONSTRAINT native_rubygems_replication_jobs_actor_kind_check CHECK ((actor_kind = ANY (ARRAY['static_admin'::text, 'static_resolver'::text, 'local_session'::text, 'oidc'::text, 'api_key'::text, 'service_account_credential'::text, 'workload_federation'::text])));

-- +goose Down
-- The narrower check cannot hold workload jobs; they, their items and their retries are deleted first.
DELETE FROM public.native_helm_replication_items WHERE job_id IN (SELECT id FROM public.native_helm_replication_jobs WHERE actor_kind = 'workload_federation');
DELETE FROM public.native_helm_replication_retries WHERE job_id IN (SELECT id FROM public.native_helm_replication_jobs WHERE actor_kind = 'workload_federation');
DELETE FROM public.native_nuget_replication_items WHERE job_id IN (SELECT id FROM public.native_nuget_replication_jobs WHERE actor_kind = 'workload_federation');
DELETE FROM public.native_nuget_replication_retries WHERE job_id IN (SELECT id FROM public.native_nuget_replication_jobs WHERE actor_kind = 'workload_federation');
DELETE FROM public.native_rubygems_replication_items WHERE job_id IN (SELECT id FROM public.native_rubygems_replication_jobs WHERE actor_kind = 'workload_federation');
DELETE FROM public.native_rubygems_replication_retries WHERE job_id IN (SELECT id FROM public.native_rubygems_replication_jobs WHERE actor_kind = 'workload_federation');
DELETE FROM public.native_helm_replication_jobs WHERE actor_kind = 'workload_federation';
DELETE FROM public.native_nuget_replication_jobs WHERE actor_kind = 'workload_federation';
DELETE FROM public.native_rubygems_replication_jobs WHERE actor_kind = 'workload_federation';
ALTER TABLE public.native_helm_replication_jobs DROP CONSTRAINT native_helm_replication_jobs_actor_kind_check;
ALTER TABLE public.native_helm_replication_jobs ADD CONSTRAINT native_helm_replication_jobs_actor_kind_check CHECK ((actor_kind = ANY (ARRAY['static_admin'::text, 'static_resolver'::text, 'local_session'::text, 'oidc'::text, 'api_key'::text, 'service_account_credential'::text])));
ALTER TABLE public.native_nuget_replication_jobs DROP CONSTRAINT native_nuget_replication_jobs_actor_kind_check;
ALTER TABLE public.native_nuget_replication_jobs ADD CONSTRAINT native_nuget_replication_jobs_actor_kind_check CHECK ((actor_kind = ANY (ARRAY['static_admin'::text, 'static_resolver'::text, 'local_session'::text, 'oidc'::text, 'api_key'::text, 'service_account_credential'::text])));
ALTER TABLE public.native_rubygems_replication_jobs DROP CONSTRAINT native_rubygems_replication_jobs_actor_kind_check;
ALTER TABLE public.native_rubygems_replication_jobs ADD CONSTRAINT native_rubygems_replication_jobs_actor_kind_check CHECK ((actor_kind = ANY (ARRAY['static_admin'::text, 'static_resolver'::text, 'local_session'::text, 'oidc'::text, 'api_key'::text, 'service_account_credential'::text])));
