-- 节点「变了没」改由通知送达（w10quiet）：静默时节点的 304/204 检查不再进 PG。
--
-- 10k-r1（1 万用户、1000 节点、2c4g）静默 15 分钟：面板 + 数据库 CPU 64.85（单核 = 100），
-- 标准 ≤ 30。大头是节点每 15 秒的「配置变了没 / 名单变了没」：aegis-node 每个请求都要在
-- 一次查询里读下发纪元（00101 的序列）来判断进程内缓存是否过期，1000 个节点每秒 130 多次。
--
-- 这里让「变了」主动送到 aegis-node（它独占一条连接 LISTEN aegis_node_epoch，nodefabric 的
-- epoch_watch.go）：
--   - 'd'：00101 那组纪元触发器推进纪元时一并发出（app.bump_node_delivery_epoch 加一行）；
--   - 'c'：节点行的非遥测列、服务器的状态 / 删除 / 控制节点、生效发布物变了。节点行的列清单
--     与 zz_notify_nodes_update（00122）相同：心跳写的那几列（last_heartbeat_at、版本、资产、
--     健康分）不在里面，心跳不发通知。给 nodes 加列要两处一起加（PG18 守卫对照
--     information_schema，见 api/node 的 roundtrip_pg18_test.go）。
-- NOTIFY 随事务提交送达、同一事务里同一载荷只发一条，提交前什么都看不到：aegis-node 收到
-- 通知时数据一定已经可见。监听断开或探针超时，aegis-node 自动回到逐次查库，不依赖这条链路
-- 保证正确性。
--
-- 回滚：Down 删掉新触发器与函数、把 app.bump_node_delivery_epoch 还原成 00101 原文。旧的
-- aegis-node 不 LISTEN，新的 aegis-node 收不到通知时探针超时、自动退回逐次查库，两个方向都
-- 不需要连同进程一起回退。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.bump_node_delivery_epoch() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
BEGIN
  PERFORM nextval('public.node_delivery_epoch');
  -- （00153）提交后告诉 aegis-node 的纪元监听；同一事务里多次推进只送一条
  PERFORM pg_notify('aegis_node_epoch', 'd');
  RETURN NULL;
END;
$$;

CREATE FUNCTION app.notify_node_config_change() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
BEGIN
  PERFORM pg_notify('aegis_node_epoch', 'c');
  RETURN NULL;
END;
$$;

COMMENT ON FUNCTION app.notify_node_config_change() IS
  '节点配置与认证输入（节点非遥测列、服务器状态、生效发布物）变化时在提交后通知 aegis-node 的纪元监听（00153）。';

-- 节点增删：语句级，整批只发一条
CREATE TRIGGER zz_node_config_notify_nodes_rows
  AFTER INSERT OR DELETE ON public.nodes
  FOR EACH STATEMENT EXECUTE FUNCTION app.notify_node_config_change();

-- 节点改动：只看非遥测列（与 zz_notify_nodes_update 同一份清单），心跳写不触发
CREATE TRIGGER zz_node_config_notify_nodes_update
  AFTER UPDATE ON public.nodes
  FOR EACH ROW
  WHEN (
    ROW(
       OLD.id, OLD.tenant_id, OLD.name, OLD.pool_id, OLD.template_id, OLD.provider_id,
       OLD.provider_resource_id, OLD.region, OLD.availability_zone, OLD.status,
       OLD.public_ipv4, OLD.public_ipv6, OLD.private_ipv4, OLD.hostname,
       OLD.desired_config_version, OLD.hard_fault, OLD.hard_fault_reason, OLD.weight,
       OLD.canary_group, OLD.cost_center, OLD.monthly_cost, OLD.cost_currency,
       OLD.entered_status_at, OLD.retired_at, OLD.destroyed_at, OLD.created_at, OLD.node_type,
       OLD.server_host, OLD.server_port, OLD.protocol_config, OLD.traffic_rate,
       OLD.server_token_hash, OLD.display_name, OLD.sort_order, OLD.kernel, OLD.server_id,
       OLD.serving_status, OLD.protocol_schema_version, OLD.config_validated_at,
       OLD.row_version, OLD.config_source_generation, OLD.desired_effective_release_id,
       OLD.desired_effective_generation, OLD.node_no, OLD.silenced_by_server_delete,
       OLD.country_code, OLD.server_token_issued_at, OLD.server_token_issued_by,
       OLD.listen_l4, OLD.runtime_status, OLD.runtime_reason, OLD.runtime_state_at)
    IS DISTINCT FROM
    ROW(
       NEW.id, NEW.tenant_id, NEW.name, NEW.pool_id, NEW.template_id, NEW.provider_id,
       NEW.provider_resource_id, NEW.region, NEW.availability_zone, NEW.status,
       NEW.public_ipv4, NEW.public_ipv6, NEW.private_ipv4, NEW.hostname,
       NEW.desired_config_version, NEW.hard_fault, NEW.hard_fault_reason, NEW.weight,
       NEW.canary_group, NEW.cost_center, NEW.monthly_cost, NEW.cost_currency,
       NEW.entered_status_at, NEW.retired_at, NEW.destroyed_at, NEW.created_at, NEW.node_type,
       NEW.server_host, NEW.server_port, NEW.protocol_config, NEW.traffic_rate,
       NEW.server_token_hash, NEW.display_name, NEW.sort_order, NEW.kernel, NEW.server_id,
       NEW.serving_status, NEW.protocol_schema_version, NEW.config_validated_at,
       NEW.row_version, NEW.config_source_generation, NEW.desired_effective_release_id,
       NEW.desired_effective_generation, NEW.node_no, NEW.silenced_by_server_delete,
       NEW.country_code, NEW.server_token_issued_at, NEW.server_token_issued_by,
       NEW.listen_l4, NEW.runtime_status, NEW.runtime_reason, NEW.runtime_state_at)
  )
  EXECUTE FUNCTION app.notify_node_config_change();

-- 服务器：UniProxy 认证看它的状态、是否删除与控制节点；心跳写的列不触发
CREATE TRIGGER zz_node_config_notify_servers_update
  AFTER UPDATE OF status, deleted_at, control_node_id ON public.servers
  FOR EACH ROW
  WHEN (OLD.status IS DISTINCT FROM NEW.status
        OR OLD.deleted_at IS DISTINCT FROM NEW.deleted_at
        OR OLD.control_node_id IS DISTINCT FROM NEW.control_node_id)
  EXECUTE FUNCTION app.notify_node_config_change();

CREATE TRIGGER zz_node_config_notify_servers_rows
  AFTER DELETE ON public.servers
  FOR EACH STATEMENT EXECUTE FUNCTION app.notify_node_config_change();

-- 生效发布物：物化新代际、换钥重签
CREATE TRIGGER zz_node_config_notify_releases
  AFTER INSERT OR UPDATE OR DELETE ON public.node_effective_config_releases
  FOR EACH STATEMENT EXECUTE FUNCTION app.notify_node_config_change();
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
DROP TRIGGER IF EXISTS zz_node_config_notify_releases ON public.node_effective_config_releases;
DROP TRIGGER IF EXISTS zz_node_config_notify_servers_rows ON public.servers;
DROP TRIGGER IF EXISTS zz_node_config_notify_servers_update ON public.servers;
DROP TRIGGER IF EXISTS zz_node_config_notify_nodes_update ON public.nodes;
DROP TRIGGER IF EXISTS zz_node_config_notify_nodes_rows ON public.nodes;
DROP FUNCTION IF EXISTS app.notify_node_config_change();

-- 还原成 00101 的原文
CREATE OR REPLACE FUNCTION app.bump_node_delivery_epoch() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
BEGIN
  PERFORM nextval('public.node_delivery_epoch');
  RETURN NULL;
END;
$$;
-- +goose StatementEnd
