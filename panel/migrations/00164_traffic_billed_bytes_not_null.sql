-- 计费字节列收成非空（兼容清理 C3 第 21 项，M-e；项目没有存量部署）。
--
-- 00133 加列时留了空值：当时已有的小时桶没有这个数，按天汇总遇上空桶就把整天记成未知。
-- 现在没有那样的桶。两张小时表上「非负」的 NOT VALID 检查在这里验证；三张表的
-- billed_bytes 改成非空。按天汇总（nodefabric.trafficDailyRollupSQL）直接 sum。
--
-- 非空直接 SET NOT NULL：本迁移整体在一个事务里，先加 NOT VALID 检查再 VALIDATE 拿不到
-- 锁上的好处（第一条 ALTER 起表就持着 ACCESS EXCLUSIVE 到提交），还会和 PG18 给非空约束
-- 自动起的名字撞上。
--
-- 不给默认值：00133 文件头定的「不能当成 0 去对账」仍成立。写入点（traffic_rollup.go 的
-- 节点行与 uid 行、按天汇总）都显式写这一列；以后哪条写法漏了它，NOT NULL 当场报错，
-- 不会静默记成计费 0 字节。
--
-- 锁与耗时：三张都是大表。每条 VALIDATE 与 SET NOT NULL 在本事务已经持有的锁下各做一次
-- 顺序扫描；全新安装时表是空的。lock_timeout 5s 拿不到就失败重来。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE public.node_traffic_hourly
  VALIDATE CONSTRAINT node_traffic_hourly_billed_bytes_check;
ALTER TABLE public.node_user_traffic_hourly
  VALIDATE CONSTRAINT node_user_traffic_hourly_billed_bytes_check;

ALTER TABLE public.node_traffic_hourly
  ALTER COLUMN billed_bytes SET NOT NULL;
-- 变异（#2 单表回退）：节点小时表加回默认值 0
ALTER TABLE public.node_traffic_hourly
  ALTER COLUMN billed_bytes SET DEFAULT 0;
ALTER TABLE public.node_user_traffic_hourly
  ALTER COLUMN billed_bytes SET NOT NULL;
-- 变异（#6 单表回退）：按天表走 NOT VALID → VALIDATE → SET NOT NULL → 丢临时检查
ALTER TABLE public.node_user_traffic_daily
  ADD CONSTRAINT node_user_traffic_daily_billed_bytes_not_null
  CHECK (billed_bytes IS NOT NULL) NOT VALID;
ALTER TABLE public.node_user_traffic_daily
  VALIDATE CONSTRAINT node_user_traffic_daily_billed_bytes_not_null;
ALTER TABLE public.node_user_traffic_daily
  ALTER COLUMN billed_bytes SET NOT NULL;
ALTER TABLE public.node_user_traffic_daily
  DROP CONSTRAINT node_user_traffic_daily_billed_bytes_not_null;

COMMENT ON COLUMN public.node_traffic_hourly.billed_bytes IS
  '这一小时乘过节点倍率的计费字节（非重复上报、放行名单内的合规项）。';
COMMENT ON COLUMN public.node_user_traffic_hourly.billed_bytes IS
  '这一小时这个 uid 乘过节点倍率的计费字节。';
COMMENT ON COLUMN public.node_user_traffic_daily.billed_bytes IS
  '这一天各小时乘过倍率的计费字节之和。';

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 注释、非空与「非负」检查的验证状态都退回 00133 结束时的样子。
COMMENT ON COLUMN public.node_traffic_hourly.billed_bytes IS
  '这一小时乘过节点倍率的计费字节（非重复上报、放行名单内的合规项）；NULL 表示这个桶跨过了 00133 之前，未知。';
COMMENT ON COLUMN public.node_user_traffic_hourly.billed_bytes IS
  '这一小时这个 uid 乘过节点倍率的计费字节；NULL 表示这个桶跨过了 00133 之前，未知。';
COMMENT ON COLUMN public.node_user_traffic_daily.billed_bytes IS NULL;

ALTER TABLE public.node_traffic_hourly ALTER COLUMN billed_bytes DROP DEFAULT;
ALTER TABLE public.node_user_traffic_daily ALTER COLUMN billed_bytes DROP NOT NULL;
ALTER TABLE public.node_user_traffic_hourly ALTER COLUMN billed_bytes DROP NOT NULL;
ALTER TABLE public.node_traffic_hourly ALTER COLUMN billed_bytes DROP NOT NULL;

ALTER TABLE public.node_traffic_hourly
  DROP CONSTRAINT node_traffic_hourly_billed_bytes_check;
ALTER TABLE public.node_traffic_hourly
  ADD CONSTRAINT node_traffic_hourly_billed_bytes_check CHECK (billed_bytes >= 0) NOT VALID;
ALTER TABLE public.node_user_traffic_hourly
  DROP CONSTRAINT node_user_traffic_hourly_billed_bytes_check;
ALTER TABLE public.node_user_traffic_hourly
  ADD CONSTRAINT node_user_traffic_hourly_billed_bytes_check CHECK (billed_bytes >= 0) NOT VALID;
