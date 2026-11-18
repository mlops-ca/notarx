-- +goose Up

-- The product is named notarx (ADR 0013). Migration 000011 meant to move a site
-- that still shows the old default name to the new one, but its UPDATE compared
-- the column with the new name, so it never matched. This migration moves the
-- old default name and the old default brand mark ('AG') to the notarx ones and
-- changes the column defaults. A name or mark an operator chose is kept.
ALTER TABLE site_settings ALTER COLUMN site_name SET DEFAULT 'notarx';
ALTER TABLE site_settings ALTER COLUMN brand_mark SET DEFAULT 'NX';
UPDATE site_settings SET site_name = 'notarx' WHERE site_name = 'Artifact Gateway';
UPDATE site_settings SET brand_mark = 'NX' WHERE brand_mark = 'AG';
