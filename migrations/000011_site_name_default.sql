-- +goose Up

-- The product is named notarx (ADR 0013). A site that still shows the old
-- default name moves to the new one; a name an operator chose is kept.
ALTER TABLE site_settings ALTER COLUMN site_name SET DEFAULT 'notarx';
UPDATE site_settings SET site_name = 'notarx' WHERE site_name = 'notarx';
