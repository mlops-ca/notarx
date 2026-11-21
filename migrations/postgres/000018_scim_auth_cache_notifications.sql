-- +goose Up

-- ADR 0018 section 4 and ADR 0023 section 6: the SCIM server authenticates a request with one bearer token lookup and one
-- provider lookup, and caches both for NOTARX_AUTHZ_CACHE_TTL. A revoked or expired token, a disabled provider and a
-- deleted provider (its tokens cascade) must stop authenticating on every node within that window, shortened to the
-- notification latency by these statement-level triggers. Both tables hold few rows, so each statement announces the whole
-- scope (scim_token:, identity_provider:), which a listener applies as one flush. A statement that changed no row announces
-- nothing. MySQL polls the scope epoch instead (ADR 0022).

CREATE TRIGGER identity_provider_scim_tokens_notify_cache_insert AFTER INSERT ON public.identity_provider_scim_tokens REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_scope('scim_token');
CREATE TRIGGER identity_provider_scim_tokens_notify_cache_update AFTER UPDATE ON public.identity_provider_scim_tokens REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_scope('scim_token');
CREATE TRIGGER identity_provider_scim_tokens_notify_cache_delete AFTER DELETE ON public.identity_provider_scim_tokens REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_scope('scim_token');

CREATE TRIGGER identity_providers_notify_cache_insert AFTER INSERT ON public.identity_providers REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_scope('identity_provider');
CREATE TRIGGER identity_providers_notify_cache_update AFTER UPDATE ON public.identity_providers REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_scope('identity_provider');
CREATE TRIGGER identity_providers_notify_cache_delete AFTER DELETE ON public.identity_providers REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION public.notarx_notify_cache_statement_scope('identity_provider');
