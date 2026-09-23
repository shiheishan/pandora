-- +goose Up
-- Node 管理并发控制，以及 Server 控制节点的归属/唯一性约束。
ALTER TABLE nodes
  ADD COLUMN row_version bigint NOT NULL DEFAULT 1 CHECK (row_version > 0);

-- PostgreSQL 的复合外键要求被引用列具有唯一约束。把 server_id 放入键中，
-- 使 Server.control_node_id 只能引用“确实属于该 Server”的 Node。
ALTER TABLE nodes
  ADD CONSTRAINT nodes_tenant_server_id_id_key UNIQUE (tenant_id, server_id, id);

-- 两边均使用可延迟 NO ACTION，避免 Server/Node 循环外键阻断租户级联删除，
-- 同时仍保证事务提交时不存在悬空归属。
ALTER TABLE nodes DROP CONSTRAINT IF EXISTS nodes_server_fk;
ALTER TABLE nodes
  ADD CONSTRAINT nodes_server_fk FOREIGN KEY (tenant_id, server_id)
  REFERENCES servers (tenant_id, id) ON DELETE NO ACTION
  DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_control_node_fk;
ALTER TABLE servers
  ADD CONSTRAINT servers_control_node_membership_fk
  FOREIGN KEY (tenant_id, id, control_node_id)
  REFERENCES nodes (tenant_id, server_id, id) ON DELETE NO ACTION
  DEFERRABLE INITIALLY DEFERRED;

CREATE UNIQUE INDEX servers_control_node_unique
  ON servers (tenant_id, control_node_id)
  WHERE control_node_id IS NOT NULL AND deleted_at IS NULL;

COMMENT ON COLUMN nodes.row_version IS
  '管理员写操作的乐观并发版本；每次可见配置、排序、归属或服务状态变化时递增。';

-- +goose Down
DROP INDEX IF EXISTS servers_control_node_unique;
ALTER TABLE servers DROP CONSTRAINT IF EXISTS servers_control_node_membership_fk;
ALTER TABLE servers
  ADD CONSTRAINT servers_control_node_fk FOREIGN KEY (tenant_id, control_node_id)
  REFERENCES nodes (tenant_id, id) ON DELETE RESTRICT;
ALTER TABLE nodes DROP CONSTRAINT IF EXISTS nodes_server_fk;
ALTER TABLE nodes
  ADD CONSTRAINT nodes_server_fk FOREIGN KEY (tenant_id, server_id)
  REFERENCES servers (tenant_id, id) ON DELETE RESTRICT;
ALTER TABLE nodes DROP CONSTRAINT IF EXISTS nodes_tenant_server_id_id_key;
ALTER TABLE nodes DROP COLUMN IF EXISTS row_version;
