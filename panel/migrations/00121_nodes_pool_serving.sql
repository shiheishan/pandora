-- 门户节点预览与订阅下发的资格查询按池取节点（w3core）。
--
-- 资格查询（subscription.listEligibleNodesTx）从套餐版本绑的池出发，再取池里的节点。
-- nodes 上带 pool_id 的现有索引只有 idx_nodes_schedulable（00005），它的谓词是
-- status = 'active' AND NOT hard_fault，资格查询的条件推不出这个谓词（逻辑节点按
-- serving_status 管，控制节点才看 status），用不上；没有它就只能整表扫 nodes，
-- 心跳留下的死元组越多越慢（5k-r3：300 节点时这条语句均值 61.8ms，每行读 12 个缓冲块）。
--
-- 部分索引的谓词 serving_status = 'active' 正是资格查询里的一条（DeliverableNodeSQL），
-- 规划器认得出它可用。pool_id、serving_status 心跳都不写，不影响心跳更新走 HOT。

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE INDEX idx_nodes_pool_serving
  ON public.nodes (tenant_id, pool_id)
  WHERE serving_status = 'active';

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP INDEX IF EXISTS public.idx_nodes_pool_serving;
