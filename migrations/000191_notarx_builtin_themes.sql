-- +goose Up
ALTER TABLE site_settings
    ALTER COLUMN enabled_theme_ids SET DEFAULT ARRAY['notarx-dark', 'notarx-light']::TEXT[],
    ALTER COLUMN default_theme_id SET DEFAULT 'notarx-dark';

-- The gateway-* and aerok-* built-ins are removed (ADR 0013); stored choices
-- of them fall back to the notarx defaults and custom themes are kept.
UPDATE site_settings
SET enabled_theme_ids = ARRAY['notarx-dark', 'notarx-light']::TEXT[] || ARRAY(
        SELECT theme_id FROM unnest(enabled_theme_ids) AS theme_id
        WHERE theme_id NOT IN ('gateway-dark', 'gateway-light', 'aerok-dark', 'aerok-light')),
    default_theme_id = CASE
        WHEN default_theme_id IN ('gateway-dark', 'gateway-light', 'aerok-dark', 'aerok-light') THEN 'notarx-dark'
        ELSE default_theme_id
    END;

-- +goose Down
UPDATE site_settings
SET enabled_theme_ids = ARRAY['gateway-dark', 'gateway-light', 'aerok-dark', 'aerok-light']::TEXT[] || ARRAY(
        SELECT theme_id FROM unnest(enabled_theme_ids) AS theme_id
        WHERE theme_id NOT IN ('notarx-dark', 'notarx-light')),
    default_theme_id = CASE
        WHEN default_theme_id IN ('notarx-dark', 'notarx-light') THEN 'gateway-dark'
        ELSE default_theme_id
    END;

ALTER TABLE site_settings
    ALTER COLUMN enabled_theme_ids SET DEFAULT ARRAY['gateway-dark', 'gateway-light', 'aerok-dark', 'aerok-light']::TEXT[],
    ALTER COLUMN default_theme_id SET DEFAULT 'gateway-dark';
