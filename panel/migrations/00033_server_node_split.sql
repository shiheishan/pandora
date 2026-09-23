-- +goose Up
-- Server/Node 语义拆分第一阶段：Server 是运行 Agent 的宿主，Node 是订阅可见的
-- 逻辑代理入口。保留 nodes 旧宿主字段和旧 API，采用 expand/backfill 迁移。

CREATE TABLE servers (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name              text NOT NULL,
  status            text NOT NULL DEFAULT 'draft' CHECK (status IN (
                      'draft','ready','draining','maintenance',
                      'unhealthy','quarantined','retired')),
  status_reason     text,
  row_version       bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
  region            text,
  hostname          text,
  public_ipv4       inet,
  public_ipv6       inet,
  private_ipv4      inet,
  architecture      text,
  os_name           text,
  agent_version     text,
  last_heartbeat_at timestamptz,
  cpu_cores         int CHECK (cpu_cores IS NULL OR cpu_cores > 0),
  memory_mb         int CHECK (memory_mb IS NULL OR memory_mb > 0),
  disk_gb           int CHECK (disk_gb IS NULL OR disk_gb > 0),
  capacity_nodes    int NOT NULL DEFAULT 32 CHECK (capacity_nodes > 0),
  notes             text,
  control_node_id   uuid,
  entered_status_at timestamptz NOT NULL DEFAULT now(),
  retired_at        timestamptz,
  deleted_at        timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT servers_tenant_name_unique UNIQUE (tenant_id, name),
  CONSTRAINT servers_tenant_id_id_key UNIQUE (tenant_id, id),
  CONSTRAINT servers_control_node_fk FOREIGN KEY (tenant_id, control_node_id)
    REFERENCES nodes (tenant_id, id) ON DELETE RESTRICT
);

CREATE INDEX idx_servers_status ON servers (tenant_id, status) WHERE deleted_at IS NULL;
CREATE INDEX idx_servers_heartbeat ON servers (last_heartbeat_at)
  WHERE status = 'ready' AND deleted_at IS NULL;
CREATE TRIGGER trg_servers_updated_at BEFORE UPDATE ON servers
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();
SELECT app.enable_tenant_rls('servers');

ALTER TABLE nodes
  ADD COLUMN server_id uuid,
  ADD COLUMN serving_status text NOT NULL DEFAULT 'draft' CHECK (serving_status IN (
    'draft','active','draining','disabled','retired')),
  ADD COLUMN protocol_schema_version smallint NOT NULL DEFAULT 0
    CHECK (protocol_schema_version >= 0),
  ADD COLUMN config_validated_at timestamptz;

ALTER TABLE nodes
  ADD CONSTRAINT nodes_server_fk FOREIGN KEY (tenant_id, server_id)
  REFERENCES servers (tenant_id, id) ON DELETE RESTRICT;
CREATE INDEX idx_nodes_server ON nodes (tenant_id, server_id, serving_status);

-- ID 与旧 node_id 保持一致：现有 Agent 无需重新 bootstrap。
INSERT INTO servers (
  id, tenant_id, name, status, region, hostname, public_ipv4, public_ipv6,
  private_ipv4, agent_version, last_heartbeat_at, cpu_cores, memory_mb, disk_gb,
  control_node_id, entered_status_at, retired_at, deleted_at, created_at, updated_at
)
SELECT
  n.id, n.tenant_id, n.name,
	CASE WHEN n.status IN ('active','canary') THEN 'ready'
       WHEN n.status IN ('draining','maintenance','unhealthy','quarantined','retired')
         THEN n.status
       ELSE 'draft' END,
  n.region, n.hostname, n.public_ipv4, n.public_ipv6, n.private_ipv4,
  n.agent_version, n.last_heartbeat_at, n.cpu_cores, n.memory_mb, n.disk_gb,
  n.id, n.entered_status_at, n.retired_at, n.destroyed_at, n.created_at, n.updated_at
FROM nodes n
ON CONFLICT (id) DO NOTHING;

UPDATE nodes
SET server_id = id,
    serving_status = CASE
		WHEN status IN ('active','canary') AND node_type IS NOT NULL THEN 'active'
      WHEN status IN ('draining','retired') THEN status
      ELSE 'draft'
    END
WHERE server_id IS NULL;

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'aegis_app') THEN
    GRANT SELECT, INSERT, UPDATE, DELETE ON servers TO aegis_app;
  END IF;
END $$;
-- +goose StatementEnd

COMMENT ON TABLE servers IS
  '运行 Aegis Agent 的物理或虚拟宿主；一台 Server 可承载多个订阅可见 Node。';
COMMENT ON COLUMN nodes.protocol_schema_version IS
  '0 表示迁移前的兼容配置；大于 0 的配置必须通过对应版本的服务端 Schema。';

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN

	IF EXISTS (
	  SELECT 1 FROM nodes
	   WHERE server_id IS NULL OR server_id IS DISTINCT FROM id
	) THEN
	  RAISE EXCEPTION 'cannot rollback Server split after a Node was detached or remapped';
	END IF;

	IF EXISTS (
	  SELECT 1 FROM servers s
	   WHERE NOT EXISTS (
	     SELECT 1 FROM nodes n
	      WHERE n.tenant_id=s.tenant_id AND n.id=s.id AND n.server_id=s.id
	   )
	) THEN
	  RAISE EXCEPTION 'cannot rollback Server split while non-legacy or orphan Servers exist';
	END IF;

  IF EXISTS (
    SELECT 1 FROM nodes WHERE server_id IS NOT NULL
    GROUP BY tenant_id, server_id HAVING count(*) > 1
  ) THEN
    RAISE EXCEPTION 'cannot rollback Server split while a Server owns multiple Nodes';
  END IF;

	IF EXISTS (
	  SELECT 1
	    FROM nodes n
	    JOIN servers s ON s.tenant_id=n.tenant_id AND s.id=n.server_id
	   WHERE s.name IS DISTINCT FROM n.name
	      OR s.region IS DISTINCT FROM n.region
	      OR s.hostname IS DISTINCT FROM n.hostname
	      OR s.public_ipv4 IS DISTINCT FROM n.public_ipv4
	      OR s.public_ipv6 IS DISTINCT FROM n.public_ipv6
	      OR s.private_ipv4 IS DISTINCT FROM n.private_ipv4
	      OR s.agent_version IS DISTINCT FROM n.agent_version
	      OR s.last_heartbeat_at IS DISTINCT FROM n.last_heartbeat_at
	      OR s.cpu_cores IS DISTINCT FROM n.cpu_cores
	      OR s.memory_mb IS DISTINCT FROM n.memory_mb
	      OR s.disk_gb IS DISTINCT FROM n.disk_gb
	      OR s.retired_at IS DISTINCT FROM n.retired_at
	      OR s.deleted_at IS DISTINCT FROM n.destroyed_at
	      OR s.control_node_id IS DISTINCT FROM n.id
	) THEN
	  RAISE EXCEPTION 'cannot rollback Server split while Server facts differ from legacy Node columns';
	END IF;

	IF EXISTS (
	  SELECT 1
	    FROM servers s
	    JOIN nodes n ON n.tenant_id=s.tenant_id AND n.id=s.id
	   WHERE s.status IS DISTINCT FROM CASE
	           WHEN n.status IN ('active','canary') THEN 'ready'
	           WHEN n.status IN ('draining','maintenance','unhealthy','quarantined','retired')
	             THEN n.status
	           WHEN n.status='destroyed' THEN 'retired'
	           ELSE 'draft' END
	      OR s.status_reason IS NOT NULL
	      OR s.row_version <> 1
	      OR s.architecture IS NOT NULL
	      OR s.os_name IS NOT NULL
	      OR s.capacity_nodes <> 32
	      OR s.notes IS NOT NULL
	) THEN
	  RAISE EXCEPTION 'cannot rollback Server split after Server-only lifecycle or metadata changed';
	END IF;
END;
$$;
-- +goose StatementEnd

ALTER TABLE nodes DROP CONSTRAINT IF EXISTS nodes_server_fk;
DROP INDEX IF EXISTS idx_nodes_server;
ALTER TABLE nodes
  DROP COLUMN IF EXISTS config_validated_at,
  DROP COLUMN IF EXISTS protocol_schema_version,
  DROP COLUMN IF EXISTS serving_status,
  DROP COLUMN IF EXISTS server_id;
DROP TABLE IF EXISTS servers;
