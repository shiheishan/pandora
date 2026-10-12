-- 用途：节点在线情况：总数与在线数、按服务状态拆分、应在线却失联的节点、运行状态 degraded 的节点。
-- 身份：aegis_app + 租户。
-- 变量：tenant、timeout。
-- 「在线」= last_heartbeat_at 在 90 秒内（nodefabric.NodeStaleAfter，后台节点列表、看板、系统状态同一口径）。
-- 只有 serving_status = 'active' 的节点才「应在线」；draft/disabled/retired 没心跳是正常的，draining 不看心跳。
\if :{?tenant}
\else
  \set tenant '00000000-0000-7000-8000-000000000001'
\endif
\if :{?timeout}
\else
  \set timeout 15s
\endif
BEGIN READ ONLY;
SET LOCAL ROLE aegis_app;
SET LOCAL search_path TO pg_catalog, public, pg_temp;
SET LOCAL jit = off;
SET LOCAL statement_timeout = :'timeout';
SELECT set_config('app.tenant_id', :'tenant', true) AS tenant_set \gset

\echo == 1 总数与在线数（排除 destroyed 和 retired，与看板同口径）
SELECT count(*) AS total,
       count(*) FILTER (WHERE last_heartbeat_at >= now() - interval '90 seconds') AS online
  FROM nodes
 WHERE status <> 'destroyed' AND serving_status <> 'retired';

\echo == 2 按服务状态拆分
SELECT serving_status, count(*) AS n,
       count(*) FILTER (WHERE last_heartbeat_at >= now() - interval '90 seconds') AS online
  FROM nodes
 WHERE status <> 'destroyed'
 GROUP BY serving_status
 ORDER BY serving_status;

\echo == 3 应在线却失联：serving_status = active，心跳为空或超过 90 秒（沉默最久的在前，至多 50 行）
SELECT n.node_no, n.name, n.node_type, n.status, s.name AS server,
       n.last_heartbeat_at,
       floor(extract(epoch FROM now() - n.last_heartbeat_at))::bigint AS silent_s,
       n.runtime_status, n.runtime_reason, n.agent_version
  FROM nodes n
  LEFT JOIN servers s ON s.tenant_id = n.tenant_id AND s.id = n.server_id
 WHERE n.status <> 'destroyed' AND n.serving_status = 'active'
   AND (n.last_heartbeat_at IS NULL OR n.last_heartbeat_at < now() - interval '90 seconds')
 ORDER BY n.last_heartbeat_at ASC NULLS FIRST
 LIMIT 50;

\echo == 4 运行状态 degraded（pdnd 经心跳上报；runtime_reason 如 port_in_use:443/tcp:...、config_apply_failed）
SELECT n.node_no, n.name, n.node_type, n.serving_status, n.runtime_status, n.runtime_reason,
       n.runtime_state_at, n.last_heartbeat_at
  FROM nodes n
 WHERE n.status <> 'destroyed' AND n.runtime_status = 'degraded'
 ORDER BY n.runtime_state_at DESC NULLS LAST
 LIMIT 50;

ROLLBACK;
