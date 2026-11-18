-- +goose Up

-- The product is named notarx (ADR 0013). Migration 000011 meant to move a site
-- that still shows the old default name to the new one, but its UPDATE compared
-- the column with the new name, so it never matched. This migration changes the
-- column defaults and moves the old default site name ('Artifact Gateway') and
-- the old default brand mark ('AG') to the notarx ones. Any other value is kept.
-- A changed row gets a new version and updated_at, so a console tab or a client
-- holding the old version gets a version conflict instead of overwriting it.
ALTER TABLE site_settings ALTER COLUMN site_name SET DEFAULT 'notarx';
ALTER TABLE site_settings ALTER COLUMN brand_mark SET DEFAULT 'NX';
UPDATE site_settings
SET site_name = CASE WHEN site_name = 'Artifact Gateway' THEN 'notarx' ELSE site_name END,
    brand_mark = CASE WHEN brand_mark = 'AG' THEN 'NX' ELSE brand_mark END,
    version = version + 1,
    updated_at = now()
WHERE site_name = 'Artifact Gateway' OR brand_mark = 'AG';
