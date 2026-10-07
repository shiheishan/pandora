-- 节点心跳成为 HOT 更新，变更通知的 WHEN 改成列清单比较（w3node）。
--
-- 一、删掉 idx_nodes_heartbeat（00005）。心跳每 30 秒改一次 last_heartbeat_at，而这个部分索引
--    正好索引它，于是每次心跳都是非 HOT 更新：nodes 上每个索引各插一条（r3：每次心跳扫
--    35.7 个缓冲块、写 1361 B WAL）。全仓按 last_heartbeat_at 过滤或排序的查询（后台待办、
--    节点列表、在线巡检、路由页在线数、看板与系统状态计数、订阅新鲜度）条件都是
--    status <> 'destroyed' 之类，推不出索引谓词 status IN ('standby','canary','active')，
--    没有一条用得上它；PG18 用例在临时重建它的事务里对这些查询逐条 EXPLAIN 核过。
--
-- 二、nodes 设 fillfactor = 85：给同页 HOT 更新留空间。只影响以后写入的页，已有的页要等
--    VACUUM FULL 或表自然重写才按新值排布；迁移里不做重写，免得锁表。
--
-- 三、zz_notify_nodes_update（00110）的 WHEN 原先对 OLD、NEW 各做一次 to_jsonb(整行) 再减去
--    心跳列比较，每次心跳都要序列化两遍整行。改成显式列清单的行比较，行为不变：
--    列清单 = nodes 的全部列 − 00110 排除的心跳类列（last_heartbeat_at、updated_at、
--    agent_version、runtime_version、config_signing_key_id、applied_config_version、
--    applied_config_hash、applied_effective_release_id / _generation / _hash、cpu_cores、
--    memory_mb、disk_gb、health_score）。「离线 → 在线」翻转（两次心跳间隔达到 90 秒）照旧通知。
--    代价是以后给 nodes 加列要同步加进这张清单：PG18 用例拿 information_schema 对照，
--    漏列即红（00110 选 jsonb 写法正是为了「新列默认通知」，这里用守卫换回同样的保证）。

-- +goose Up
SET LOCAL lock_timeout = '5s';

DROP INDEX IF EXISTS public.idx_nodes_heartbeat;

ALTER TABLE public.nodes SET (fillfactor = 85);

DROP TRIGGER IF EXISTS zz_notify_nodes_update ON public.nodes;

CREATE TRIGGER zz_notify_nodes_update
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
       OLD.country_code, OLD.server_token_issued_at, OLD.server_token_issued_by)
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
       NEW.country_code, NEW.server_token_issued_at, NEW.server_token_issued_by)
    OR (NEW.last_heartbeat_at IS NOT NULL
        AND (OLD.last_heartbeat_at IS NULL
             OR NEW.last_heartbeat_at - OLD.last_heartbeat_at >= interval '90 seconds'))
  )
  EXECUTE FUNCTION app.notify_change();

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP TRIGGER IF EXISTS zz_notify_nodes_update ON public.nodes;

CREATE TRIGGER zz_notify_nodes_update
  AFTER UPDATE ON public.nodes
  FOR EACH ROW
  WHEN (
    (to_jsonb(OLD) - ARRAY[
       'last_heartbeat_at', 'updated_at',
       'agent_version', 'runtime_version', 'config_signing_key_id',
       'applied_config_version', 'applied_config_hash',
       'applied_effective_release_id', 'applied_effective_generation', 'applied_effective_hash',
       'cpu_cores', 'memory_mb', 'disk_gb', 'health_score']::text[])
    IS DISTINCT FROM
    (to_jsonb(NEW) - ARRAY[
       'last_heartbeat_at', 'updated_at',
       'agent_version', 'runtime_version', 'config_signing_key_id',
       'applied_config_version', 'applied_config_hash',
       'applied_effective_release_id', 'applied_effective_generation', 'applied_effective_hash',
       'cpu_cores', 'memory_mb', 'disk_gb', 'health_score']::text[])
    OR (NEW.last_heartbeat_at IS NOT NULL
        AND (OLD.last_heartbeat_at IS NULL
             OR NEW.last_heartbeat_at - OLD.last_heartbeat_at >= interval '90 seconds'))
  )
  EXECUTE FUNCTION app.notify_change();

ALTER TABLE public.nodes RESET (fillfactor);

CREATE INDEX IF NOT EXISTS idx_nodes_heartbeat ON public.nodes (last_heartbeat_at)
  WHERE status IN ('standby', 'canary', 'active');
