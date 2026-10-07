-- 节点心跳不再触发变更通知（w3live）。
--
-- 00021 给 nodes 挂的 zz_notify_nodes 是 AFTER INSERT OR UPDATE OR DELETE、没有 WHEN：
-- 原生节点每 30 秒一次心跳（nodefabric.Heartbeat）、UniProxy 兼容端每次上报状态，
-- 都会 UPDATE 一次节点行，于是每次心跳都 to_jsonb 整行再 pg_notify 一条 nodes.changed。
-- 1000 个节点约 33 条/秒，后台每个标签页按 2 秒节流整表重拉，门户每个连接也都收到。
--
-- 这一版把 UPDATE 拆出来单独挂，WHEN 里去掉心跳类列再比：去掉之后整行没变就不发。
-- 去掉的列（以当前表结构逐列核过，写它们的只有心跳、状态上报与生效回执）：
--   - last_heartbeat_at：心跳时刻
--   - updated_at：trg_nodes_updated_at 每次 UPDATE 都改写
--   - agent_version、runtime_version、config_signing_key_id：心跳每次带上的版本与签名钥
--   - applied_config_version、applied_config_hash：旧式配置的生效回执（心跳带）
--   - applied_effective_release_id / _generation / _hash：生效发布物回执（health_passed 上报）
--   - cpu_cores、memory_mb、disk_gb、health_score：资产快照与健康分
-- 这些列在后台列表里的展示改由前端定时轮询（30 秒）刷新，不跟事件走。
--
-- 用「整行 jsonb 减去心跳列」而不是逐列列出业务列：以后新增的业务列默认就会通知，
-- 漏列的后果只是多发，而不是页面静默不刷新（同 00035 的 row_version 比较写法）。
--
-- 「离线 → 在线」翻转仍要通知：两次心跳间隔达到 90 秒，说明上一次心跳之后节点在
-- 后台列表里已经显示为离线（与 nodefabric 列表的 stale 判定、adminops 的在线计数同一个
-- 90 秒），这一次心跳把它拉回在线。用 NEW 与 OLD 的心跳时刻相减而不是 now()，结果只取决于
-- 行本身。首次心跳（OLD 为 NULL）同理。「在线 → 离线」没有写入可以挂，由 aegis-admin 的
-- 节点在线巡检（nodefabric.PatrolNodeLiveness）在跨过窗口时补发一次。
--
-- 只动 nodes 这一张表，通用函数 app.notify_change() 与其它表的触发器不变。
-- WHEN 引用 OLD 的触发器不能同时挂 INSERT / DELETE，所以拆成两个：
--   zz_notify_nodes        AFTER INSERT OR DELETE（与原来一样，名字保留）
--   zz_notify_nodes_update AFTER UPDATE + WHEN

-- +goose Up
SET LOCAL lock_timeout = '5s';

DROP TRIGGER IF EXISTS zz_notify_nodes ON public.nodes;

CREATE TRIGGER zz_notify_nodes
  AFTER INSERT OR DELETE ON public.nodes
  FOR EACH ROW EXECUTE FUNCTION app.notify_change();

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

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP TRIGGER IF EXISTS zz_notify_nodes_update ON public.nodes;
DROP TRIGGER IF EXISTS zz_notify_nodes ON public.nodes;
CREATE TRIGGER zz_notify_nodes AFTER INSERT OR UPDATE OR DELETE ON public.nodes
  FOR EACH ROW EXECUTE FUNCTION app.notify_change();
