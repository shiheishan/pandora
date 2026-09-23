-- +goose Up
-- Nodes acknowledge the config signing key they currently pin. Operators use
-- this as the removal gate for AEGIS_PREVIOUS_CONFIG_SIGNING_SEED.
ALTER TABLE nodes
  ADD COLUMN config_signing_key_id text
  CHECK (config_signing_key_id IS NULL OR config_signing_key_id ~ '^[A-Za-z0-9_-]{11}$');

CREATE INDEX idx_nodes_config_signing_key_adoption
  ON nodes (tenant_id, config_signing_key_id)
  WHERE status NOT IN ('destroyed','retired') AND serving_status <> 'retired';

-- +goose Down
DROP INDEX IF EXISTS idx_nodes_config_signing_key_adoption;
ALTER TABLE nodes DROP COLUMN IF EXISTS config_signing_key_id;
