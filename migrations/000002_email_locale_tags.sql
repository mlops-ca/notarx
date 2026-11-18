-- +goose Up

-- Email locale tags become exact BCP 47 values (ADR 0014, "Backend locale"):
-- en-CA, fr-CA and zh-CN. The legacy bare 'en' tag is retired with no alias.

ALTER TABLE email_targets DROP CONSTRAINT IF EXISTS email_targets_locale_check;
ALTER TABLE email_test_deliveries DROP CONSTRAINT IF EXISTS email_test_deliveries_locale_check;

UPDATE email_targets SET locale = 'en-CA' WHERE locale = 'en';
UPDATE email_test_deliveries SET locale = 'en-CA' WHERE locale = 'en';

ALTER TABLE email_targets ALTER COLUMN locale SET DEFAULT 'en-CA';
ALTER TABLE email_test_deliveries ALTER COLUMN locale SET DEFAULT 'en-CA';

ALTER TABLE email_targets
    ADD CONSTRAINT email_targets_locale_check CHECK (locale IN ('en-CA', 'fr-CA', 'zh-CN'));
ALTER TABLE email_test_deliveries
    ADD CONSTRAINT email_test_deliveries_locale_check CHECK (locale IN ('en-CA', 'fr-CA', 'zh-CN'));
