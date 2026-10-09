-- 缓存纪元与节点目录纪元（性能总方案第 0 波 C 缓存收拢，w12cache；给 W1-a 订阅渲染的节点集合缓存铺路）。
--
-- 面板的进程内缓存收成一个包（platform/cache）：条目按「纪元」判有效。00101 的下发纪元只管节点
-- 用户名单的输入；订阅渲染还要知道「订阅里能看到哪些节点、连接参数是什么」，这部分现在没有纪元，
-- subscription 的节点缓存只好靠 20 秒 TTL 加 Valkey 广播（丢了就晚 20 秒）。
--
-- 这里建一个通用的纪元推进函数和第一个缓存纪元：
--   - app.bump_cache_epoch('<种类>')：提交时推进序列 <种类>_epoch，并在 aegis_cache_epoch 通道上发
--     载荷 '<种类>'（NOTIFY 随事务提交送达，同一事务里同一载荷只发一条）。读方要么在本来就要跑的
--     查询里读序列（cache.EpochSQL），要么 LISTEN 这条通道（cache.Watch，监听不健康时退回读序列）。
--     种类名与 Go 的约定在 platform/cache 的 epoch.go。
--   - node_catalog_epoch：订阅渲染读的节点侧输入——
--       节点增删；节点的非遥测列（与 00153 的 zz_node_config_notify_nodes_update 同一份清单，含池归属
--       pool_id 与连接参数）；已应用的发布物（applied_effective_*，「配置下发失败」的判定要用）；
--       首次心跳与掉线后恢复心跳（心跳间隔超过 10 分钟的那一次写：订阅优先给心跳新鲜的节点，窗口
--       与 subscription.HeartbeatFreshWindow 相同，守卫 TestCacheEpochPG18 钉住）；
--       服务器的状态 / 删除 / 控制节点；配置应用记录（最近一次的阶段）。
--     例行心跳、遥测写都不推进。套餐绑池、池的用户组限定、用户换组已在下发纪元里，读方两个一起读。
--     新鲜窗口内「到点变旧」没有写，读方按条目里最早的心跳时刻硬过期（同名单的 nextExpiry）。
--
-- 不改 app.bump_node_delivery_epoch()：节点目录的变化不该让 aegis-node 的用户名单重算（节点编辑、
-- 恢复心跳、配置应用都和「谁能连」无关），所以另起一个序列，不往下发纪元上加。
--
-- 锁：建序列、函数、触发器；触发器对 nodes、servers、node_config_applications 拿 SHARE ROW EXCLUSIVE，
-- 不扫表，毫秒级。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
CREATE FUNCTION app.bump_cache_epoch() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
BEGIN
  -- TG_ARGV[0] 是种类名，由建触发器的迁移写死；序列名 = 种类 || '_epoch'
  PERFORM nextval(format('public.%I', TG_ARGV[0] || '_epoch')::regclass);
  PERFORM pg_notify('aegis_cache_epoch', TG_ARGV[0]);
  RETURN NULL;
END;
$$;

COMMENT ON FUNCTION app.bump_cache_epoch() IS
  '缓存纪元：输入变化时在提交时推进序列 <种类>_epoch 并通知 aegis_cache_epoch（载荷为种类名），进程内缓存据此作废（00155）。';

CREATE SEQUENCE public.node_catalog_epoch AS bigint;
GRANT SELECT ON SEQUENCE public.node_catalog_epoch TO aegis_app;
COMMENT ON SEQUENCE public.node_catalog_epoch IS
  '节点目录纪元：订阅里能看到的节点集合与连接参数变化时推进（00155）。';

-- 节点增删
CREATE CONSTRAINT TRIGGER zz_node_catalog_epoch_nodes_rows
  AFTER INSERT OR DELETE ON public.nodes
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_cache_epoch('node_catalog');

-- 节点改动：非遥测列（同 00153 的清单）、已应用的发布物、首次心跳与掉线后恢复心跳。例行心跳不推进。
CREATE CONSTRAINT TRIGGER zz_node_catalog_epoch_nodes_update
  AFTER UPDATE ON public.nodes
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
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
       OLD.listen_l4, OLD.runtime_status, OLD.runtime_reason, OLD.runtime_state_at,
       OLD.applied_effective_release_id, OLD.applied_effective_generation)
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
       NEW.listen_l4, NEW.runtime_status, NEW.runtime_reason, NEW.runtime_state_at,
       NEW.applied_effective_release_id, NEW.applied_effective_generation)
    -- 首次心跳（从未心跳过的节点不下发）
    OR (OLD.last_heartbeat_at IS NULL) IS DISTINCT FROM (NEW.last_heartbeat_at IS NULL)
    -- 掉线后恢复：这次心跳离上一次超过新鲜窗口，节点从「心跳超时」回到「新鲜」
    OR OLD.last_heartbeat_at < NEW.last_heartbeat_at - interval '10 minutes'
  )
  EXECUTE FUNCTION app.bump_cache_epoch('node_catalog');

-- 服务器：下发资格看它的状态、是否删除与控制节点
CREATE CONSTRAINT TRIGGER zz_node_catalog_epoch_servers_update
  AFTER UPDATE OF status, deleted_at, control_node_id ON public.servers
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  WHEN (OLD.status IS DISTINCT FROM NEW.status
        OR OLD.deleted_at IS DISTINCT FROM NEW.deleted_at
        OR OLD.control_node_id IS DISTINCT FROM NEW.control_node_id)
  EXECUTE FUNCTION app.bump_cache_epoch('node_catalog');

CREATE CONSTRAINT TRIGGER zz_node_catalog_epoch_servers_rows
  AFTER DELETE ON public.servers
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_cache_epoch('node_catalog');

-- 配置应用记录：「配置下发失败」看最近一次应用的阶段
CREATE CONSTRAINT TRIGGER zz_node_catalog_epoch_config_applications
  AFTER INSERT OR UPDATE OR DELETE ON public.node_config_applications
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION app.bump_cache_epoch('node_catalog');
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- +goose StatementBegin
DROP TRIGGER IF EXISTS zz_node_catalog_epoch_config_applications ON public.node_config_applications;
DROP TRIGGER IF EXISTS zz_node_catalog_epoch_servers_rows ON public.servers;
DROP TRIGGER IF EXISTS zz_node_catalog_epoch_servers_update ON public.servers;
DROP TRIGGER IF EXISTS zz_node_catalog_epoch_nodes_update ON public.nodes;
DROP TRIGGER IF EXISTS zz_node_catalog_epoch_nodes_rows ON public.nodes;
DROP SEQUENCE IF EXISTS public.node_catalog_epoch;
DROP FUNCTION IF EXISTS app.bump_cache_epoch();
-- +goose StatementEnd
