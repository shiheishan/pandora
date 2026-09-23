-- +goose Up

-- Immutable, node-specific configuration releases.  A release is the exact
-- runnable payload plus the complete manifest of inputs that produced it;
-- legacy layer version integers are deliberately not used as its identity.
-- +goose StatementBegin
ALTER TABLE nodes
  ADD COLUMN config_source_generation bigint NOT NULL DEFAULT 1
    CHECK (config_source_generation > 0),
  ADD COLUMN desired_effective_release_id uuid,
  ADD COLUMN desired_effective_generation bigint
    CHECK (desired_effective_generation IS NULL OR desired_effective_generation > 0),
  ADD COLUMN applied_effective_release_id uuid,
  ADD COLUMN applied_effective_generation bigint
    CHECK (applied_effective_generation IS NULL OR applied_effective_generation > 0),
  ADD COLUMN applied_effective_hash bytea
    CHECK (applied_effective_hash IS NULL OR octet_length(applied_effective_hash) = 32);

CREATE TABLE node_effective_config_releases (
  id                   uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id            uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  node_id              uuid NOT NULL,
  generation           bigint NOT NULL CHECK (generation > 0),
  payload              jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
  content_hash         bytea NOT NULL CHECK (octet_length(content_hash) = 32),
  source_manifest      jsonb NOT NULL CHECK (jsonb_typeof(source_manifest) = 'object'),
  source_manifest_hash bytea NOT NULL CHECK (octet_length(source_manifest_hash) = 32),
  key_id               text NOT NULL CHECK (key_id ~ '^[A-Za-z0-9_-]{11}$'),
  created_at           timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, node_id)
    REFERENCES nodes(tenant_id, id) ON DELETE CASCADE,
  UNIQUE (tenant_id, node_id, generation),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, node_id, id, generation),
  UNIQUE (tenant_id, node_id, id, generation, content_hash)
);

CREATE INDEX idx_node_effective_releases_node_created
  ON node_effective_config_releases (tenant_id, node_id, created_at DESC);

SELECT app.enable_tenant_rls('node_effective_config_releases');
SELECT app.make_append_only('node_effective_config_releases');
GRANT SELECT, INSERT ON node_effective_config_releases TO aegis_app;
REVOKE UPDATE, DELETE, TRUNCATE ON node_effective_config_releases FROM aegis_app;

ALTER TABLE nodes
  ADD CONSTRAINT nodes_desired_effective_release_fk
    FOREIGN KEY (tenant_id, id, desired_effective_release_id, desired_effective_generation)
    REFERENCES node_effective_config_releases(tenant_id, node_id, id, generation)
    DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT nodes_applied_effective_release_fk
    FOREIGN KEY (tenant_id, id, applied_effective_release_id, applied_effective_generation, applied_effective_hash)
    REFERENCES node_effective_config_releases(tenant_id, node_id, id, generation, content_hash)
    DEFERRABLE INITIALLY DEFERRED,
  ADD CONSTRAINT nodes_desired_effective_pair_check CHECK (
    (desired_effective_release_id IS NULL) = (desired_effective_generation IS NULL)
  ),
  ADD CONSTRAINT nodes_applied_effective_pair_check CHECK (
    num_nonnulls(applied_effective_release_id, applied_effective_generation, applied_effective_hash) IN (0, 3)
  );

ALTER TABLE node_config_applications
  ALTER COLUMN config_id DROP NOT NULL,
  ADD COLUMN effective_release_id uuid,
  ADD COLUMN effective_generation bigint
    CHECK (effective_generation IS NULL OR effective_generation > 0),
  ADD COLUMN effective_content_hash bytea
    CHECK (effective_content_hash IS NULL OR octet_length(effective_content_hash) = 32),
  ADD COLUMN report_id uuid,
  ADD CONSTRAINT node_config_apps_effective_release_fk
    FOREIGN KEY (tenant_id, node_id, effective_release_id, effective_generation, effective_content_hash)
    REFERENCES node_effective_config_releases(tenant_id, node_id, id, generation, content_hash)
    ON DELETE RESTRICT,
  ADD CONSTRAINT node_config_apps_one_config_source CHECK (
    (config_id IS NOT NULL AND report_id IS NULL AND num_nonnulls(effective_release_id, effective_generation, effective_content_hash) = 0)
    OR
    (config_id IS NULL AND num_nonnulls(effective_release_id, effective_generation, effective_content_hash) = 3)
  ),
  ADD CONSTRAINT node_config_apps_effective_report_id_check CHECK (
    (effective_release_id IS NULL) OR
    (report_id IS NOT NULL AND report_id <> '00000000-0000-0000-0000-000000000000'::uuid)
  );

CREATE UNIQUE INDEX node_config_apps_effective_report_unique
  ON node_config_applications (tenant_id, node_id, report_id)
  WHERE report_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
-- Once a release exists it is auditable production evidence. Refuse a lossy
-- rollback even when the node has not reported an application phase yet.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM node_effective_config_releases) THEN
    RAISE EXCEPTION 'cannot roll back effective releases while release evidence exists';
  END IF;
END $$;

DROP INDEX IF EXISTS node_config_apps_effective_report_unique;
ALTER TABLE node_config_applications
  DROP CONSTRAINT IF EXISTS node_config_apps_effective_report_id_check,
  DROP CONSTRAINT IF EXISTS node_config_apps_one_config_source,
  DROP CONSTRAINT IF EXISTS node_config_apps_effective_release_fk;

ALTER TABLE node_config_applications
  DROP COLUMN report_id,
  DROP COLUMN effective_content_hash,
  DROP COLUMN effective_generation,
  DROP COLUMN effective_release_id,
  ALTER COLUMN config_id SET NOT NULL;

ALTER TABLE nodes
  DROP CONSTRAINT IF EXISTS nodes_applied_effective_pair_check,
  DROP CONSTRAINT IF EXISTS nodes_desired_effective_pair_check,
  DROP CONSTRAINT IF EXISTS nodes_applied_effective_release_fk,
  DROP CONSTRAINT IF EXISTS nodes_desired_effective_release_fk,
  DROP COLUMN applied_effective_hash,
  DROP COLUMN applied_effective_generation,
  DROP COLUMN applied_effective_release_id,
  DROP COLUMN desired_effective_generation,
  DROP COLUMN desired_effective_release_id,
  DROP COLUMN config_source_generation;

DROP TABLE node_effective_config_releases;
-- +goose StatementEnd
