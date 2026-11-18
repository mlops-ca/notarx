
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
