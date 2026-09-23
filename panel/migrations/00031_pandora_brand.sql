-- +goose Up
-- Public product rename. Internal binary/package identifiers intentionally
-- remain stable so service upgrades and rollback paths are unaffected.
UPDATE tenants
   SET display_name = '潘多拉面板', updated_at = now()
 WHERE id = '00000000-0000-7000-8000-000000000001'
   AND display_name = 'AegisPanel';

UPDATE system_settings
   SET value = '"潘多拉面板"'::jsonb,
       version = version + 1,
       updated_at = now()
 WHERE tenant_id = '00000000-0000-7000-8000-000000000001'
   AND key = 'mail.from_name'
   AND value = '"AegisPanel"'::jsonb;

-- +goose Down
UPDATE system_settings
   SET value = '"AegisPanel"'::jsonb,
       version = version + 1,
       updated_at = now()
 WHERE tenant_id = '00000000-0000-7000-8000-000000000001'
   AND key = 'mail.from_name'
   AND value = '"潘多拉面板"'::jsonb;

UPDATE tenants
   SET display_name = 'AegisPanel', updated_at = now()
 WHERE id = '00000000-0000-7000-8000-000000000001'
   AND display_name = '潘多拉面板';
