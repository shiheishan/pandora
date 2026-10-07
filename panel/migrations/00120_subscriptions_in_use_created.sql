-- 后台在线设备概览（/v1/devices）的补齐分支用的部分索引（w3core）。
--
-- 概览改成先按订阅聚合窗口内的在线记录、取前 200 条，不足 200 条时用「没有在线记录的
-- 在用订阅」按创建时间从新到旧补齐（nodefabric.onlineDevicesSQL）。补齐那一支是
-- 「租户 + 在用状态 + created_at DESC + LIMIT 200」：有这条部分索引就能按序取、凑够即停，
-- 没有它要把租户全部订阅取出来排序。
--
-- 谓词与查询里的状态列表逐字相同（active、trialing、grace），规划器才认得出它可用。
-- 只收在用订阅，索引比整表小；created_at 建后不变，status 也很少改，几乎不增加写放大。

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE INDEX idx_subscriptions_in_use_created
  ON public.subscriptions (tenant_id, created_at DESC)
  WHERE status IN ('active', 'trialing', 'grace');

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP INDEX IF EXISTS public.idx_subscriptions_in_use_created;
