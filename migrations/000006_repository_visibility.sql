-- +goose Up

-- Repository visibility (ADR 0029, "Data model"). The boolean anonymous_read on
-- hosted_repositories and hosted_groups becomes the ordered visibility enum
-- private < authenticated < public. Existing rows are backfilled (true becomes
-- 'public', false becomes 'private'); the old columns are dropped with no alias.
-- The anonymous_access_policy singleton is the instance kill switch for public
-- visibility and is renamed public_access_policy (still disabled by default).

ALTER TABLE hosted_repositories ADD COLUMN visibility varchar(16) NOT NULL DEFAULT 'private';
ALTER TABLE hosted_groups ADD COLUMN visibility varchar(16) NOT NULL DEFAULT 'private';

UPDATE hosted_repositories SET visibility = CASE WHEN anonymous_read THEN 'public' ELSE 'private' END;
UPDATE hosted_groups SET visibility = CASE WHEN anonymous_read THEN 'public' ELSE 'private' END;

ALTER TABLE hosted_repositories
    ADD CONSTRAINT hosted_repositories_visibility_check CHECK (visibility IN ('private', 'authenticated', 'public'));
ALTER TABLE hosted_groups
    ADD CONSTRAINT hosted_groups_visibility_check CHECK (visibility IN ('private', 'authenticated', 'public'));

ALTER TABLE hosted_repositories DROP COLUMN anonymous_read;
ALTER TABLE hosted_groups DROP COLUMN anonymous_read;

CREATE INDEX hosted_repositories_visibility_name_idx ON hosted_repositories (visibility, name);
CREATE INDEX hosted_groups_visibility_name_idx ON hosted_groups (visibility, name);

ALTER TABLE anonymous_access_policy RENAME TO public_access_policy;
ALTER TABLE public_access_policy RENAME CONSTRAINT anonymous_access_policy_pkey TO public_access_policy_pkey;
ALTER TABLE public_access_policy RENAME CONSTRAINT anonymous_access_policy_singleton_check TO public_access_policy_singleton_check;
