-- +goose Up

-- native_npm_versions_object_key_idx serves the reference check of the npm object
-- collector (NPMObjectHasVisibleReference: WHERE object_key=$1 AND state='visible'),
-- which had no index and scanned the table once per collected object. It is partial on
-- the visible rows the check looks for, so it stays small next to the primary key. The
-- hot-query registry (internal/repository/hotqueries.go) asserts the plan uses it
-- (ADR 0023 section 11).
CREATE INDEX native_npm_versions_object_key_idx
    ON public.native_npm_versions USING btree (object_key)
    WHERE (state = 'visible'::text);
