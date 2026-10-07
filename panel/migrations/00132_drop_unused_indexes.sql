-- 删掉没有查询依赖的索引（审计 ledger 第三节第 3 条）。
--
-- node_traffic_reports 每份上报都要维护全部 btree：原来 5 条（主键、(node_id, received_at)、
-- 去重、00041 的两条部分索引里的一条）外加 00123 的编号唯一部分索引。删掉下面三条后，
-- 每次写入维护 主键 + (node_id, received_at) + 编号唯一索引（老节点不带编号时只有前两条）。
--   - idx_node_traffic_reports_dashboard_window / _dashboard_duplicate_window（00041）：
--     旧看板按 (tenant_id, received_at) 扫留档用的。00099 起看板与节点列表只读小时汇总，
--     源码契约禁止看板读留档；Go 里读这张表的只有入库（按 id 回读、按编号判重）、10 秒内容
--     哈希去重、挪节点前按 node_id 计数，都不以 tenant_id 开头。31 天清理按节点走
--     (node_id, received_at)（00131），也不用它们。
--   - idx_node_traffic_reports_dedup (node_id, content_hash, received_at)：w3node 把 10 秒去重
--     并进了 INSERT 的子查询 `node_id = $2 AND content_hash = $8 AND received_at > now() - 10s`。
--     (node_id, received_at) 先定位到这个节点最近 10 秒（每分钟一报，通常 0–1 行），再按哈希
--     过滤，代价与专用索引同一量级。
-- audit_events：idx_audit_events_actor（00009）与 audit_events_actor_time_idx（00025）的定义完全
-- 相同（tenant_id, actor_id, occurred_at DESC），每条审计写两遍同一棵树。留 00025 那条。
--
-- PG18 用例（nodefabric retention_pg18_test 的 droppedIndexScenario）对每条读这些表的查询
-- 在 enable_seqscan=off 下 EXPLAIN，确认都有剩下的索引可走，且计划里不再出现被删的索引名。
--
-- 耗时：DROP INDEX 只删目录项与文件，不扫表，毫秒级；要拿 ACCESS EXCLUSIVE 锁，发布是停写
-- 窗口，不与线上写入相争。
-- Down 原样重建：CREATE INDEX 要扫表并在建索引期间挡住写入（31 天留档约 900 万行时为分钟
-- 量级），只在停写窗口里回滚时跑。

-- +goose Up
SET LOCAL lock_timeout = '5s';

DROP INDEX IF EXISTS public.idx_node_traffic_reports_dashboard_window;
DROP INDEX IF EXISTS public.idx_node_traffic_reports_dashboard_duplicate_window;
DROP INDEX IF EXISTS public.idx_node_traffic_reports_dedup;
DROP INDEX IF EXISTS public.idx_audit_events_actor;

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30min';

-- 00009 原文
CREATE INDEX IF NOT EXISTS idx_audit_events_actor
  ON public.audit_events (tenant_id, actor_id, occurred_at DESC);

-- 00013 原文
CREATE INDEX IF NOT EXISTS idx_node_traffic_reports_dedup
  ON public.node_traffic_reports (node_id, content_hash, received_at DESC);

-- 00041 原文
CREATE INDEX IF NOT EXISTS idx_node_traffic_reports_dashboard_window
  ON public.node_traffic_reports (tenant_id, received_at DESC, id)
  INCLUDE (node_id)
  WHERE duplicate_of IS NULL;

CREATE INDEX IF NOT EXISTS idx_node_traffic_reports_dashboard_duplicate_window
  ON public.node_traffic_reports (tenant_id, received_at DESC)
  WHERE duplicate_of IS NOT NULL;
