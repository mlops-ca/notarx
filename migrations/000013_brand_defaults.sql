-- +goose Up

-- The product is named notarx (ADR 0013). Migration 000011 meant to move a site
-- that still shows the old default name to the new one, but its UPDATE compared
-- the column with the new name, so it never matched. This migration changes the
-- column defaults and moves the old default site name ('Artifact Gateway') and
-- the old default brand mark ('AG') to the notarx ones. Any other value is kept.
-- Only a row at version 1 is rewritten: every edit through the API raises the
-- version, so version 1 is a row nobody has changed, and a deliberate 'AG' or
-- 'Artifact Gateway' chosen by an operator is kept. The version stays 1, as on a
-- fresh install.
ALTER TABLE site_settings ALTER COLUMN site_name SET DEFAULT 'notarx';
ALTER TABLE site_settings ALTER COLUMN brand_mark SET DEFAULT 'NX';
UPDATE site_settings
SET site_name = CASE WHEN site_name = 'Artifact Gateway' THEN 'notarx' ELSE site_name END,
    brand_mark = CASE WHEN brand_mark = 'AG' THEN 'NX' ELSE brand_mark END
WHERE version = 1 AND (site_name = 'Artifact Gateway' OR brand_mark = 'AG');
