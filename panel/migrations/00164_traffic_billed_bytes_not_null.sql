-- 计费字节列收成非空（兼容清理 C3 第 21 项，M-e；项目没有存量部署）。
--
-- 00133 加列时留了空值：当时已有的小时桶没有这个数，按天汇总遇上空桶就把整天记成未知。
-- 现在没有那样的桶。两张小时表上「非负」的 NOT VALID 检查在这里验证；三张表的
-- billed_bytes 改成非空。按天汇总（nodefabric.trafficDailyRollupSQL）直接 sum。
--
-- 非空的做法：先加 NOT VALID 的 IS NOT NULL 检查并 VALIDATE，再 SET NOT NULL，然后丢掉
-- 这道临时检查。空库上 VALIDATE 只读目录附近的页，不重写表。
--
-- 列默认值 0：00099 的回填语句在测试里会原样重放，它的列清单写在加列之前，插行时不带
-- billed_bytes。省略列得到 0；显式 NULL 仍被拒绝。产品写入（traffic_rollup.go）总是带这列。
--
-- 锁与耗时：三张都是大表。VALIDATE 在本事务已经持有的锁下做一次顺序扫描；全新安装时表是空的。
-- lock_timeout 5s 拿不到就失败重来。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE public.node_traffic_hourly
  VALIDATE CONSTRAINT node_traffic_hourly_billed_bytes_check;
ALTER TABLE public.node_user_traffic_hourly
  VALIDATE CONSTRAINT node_user_traffic_hourly_billed_bytes_check;

ALTER TABLE public.node_traffic_hourly
  ADD CONSTRAINT node_traffic_hourly_billed_bytes_not_null
  CHECK (billed_bytes IS NOT NULL) NOT VALID;
ALTER TABLE public.node_traffic_hourly
  VALIDATE CONSTRAINT node_traffic_hourly_billed_bytes_not_null;
ALTER TABLE public.node_traffic_hourly
  ALTER COLUMN billed_bytes SET NOT NULL;
ALTER TABLE public.node_traffic_hourly
  DROP CONSTRAINT node_traffic_hourly_billed_bytes_not_null;

ALTER TABLE public.node_user_traffic_hourly
  ADD CONSTRAINT node_user_traffic_hourly_billed_bytes_not_null
  CHECK (billed_bytes IS NOT NULL) NOT VALID;
ALTER TABLE public.node_user_traffic_hourly
  VALIDATE CONSTRAINT node_user_traffic_hourly_billed_bytes_not_null;
ALTER TABLE public.node_user_traffic_hourly
  ALTER COLUMN billed_bytes SET NOT NULL;
ALTER TABLE public.node_user_traffic_hourly
  DROP CONSTRAINT node_user_traffic_hourly_billed_bytes_not_null;

ALTER TABLE public.node_user_traffic_daily
  ADD CONSTRAINT node_user_traffic_daily_billed_bytes_not_null
  CHECK (billed_bytes IS NOT NULL) NOT VALID;
ALTER TABLE public.node_user_traffic_daily
  VALIDATE CONSTRAINT node_user_traffic_daily_billed_bytes_not_null;
ALTER TABLE public.node_user_traffic_daily
  ALTER COLUMN billed_bytes SET NOT NULL;
ALTER TABLE public.node_user_traffic_daily
  DROP CONSTRAINT node_user_traffic_daily_billed_bytes_not_null;

ALTER TABLE public.node_traffic_hourly
  ALTER COLUMN billed_bytes SET DEFAULT 0;
ALTER TABLE public.node_user_traffic_hourly
  ALTER COLUMN billed_bytes SET DEFAULT 0;
ALTER TABLE public.node_user_traffic_daily
  ALTER COLUMN billed_bytes SET DEFAULT 0;

COMMENT ON COLUMN public.node_traffic_hourly.billed_bytes IS
  '这一小时乘过节点倍率的计费字节（非重复上报、放行名单内的合规项）。';
COMMENT ON COLUMN public.node_user_traffic_hourly.billed_bytes IS
  '这一小时这个 uid 乘过节点倍率的计费字节。';
COMMENT ON COLUMN public.node_user_traffic_daily.billed_bytes IS
  '这一天各小时乘过倍率的计费字节之和。';

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 注释、默认值、非空与「非负」检查的验证状态都退回 00133 结束时的样子。
COMMENT ON COLUMN public.node_traffic_hourly.billed_bytes IS
  '这一小时乘过节点倍率的计费字节（非重复上报、放行名单内的合规项）；NULL 表示这个桶跨过了 00133 之前，未知。';
COMMENT ON COLUMN public.node_user_traffic_hourly.billed_bytes IS
  '这一小时这个 uid 乘过节点倍率的计费字节；NULL 表示这个桶跨过了 00133 之前，未知。';
COMMENT ON COLUMN public.node_user_traffic_daily.billed_bytes IS NULL;

ALTER TABLE public.node_user_traffic_daily ALTER COLUMN billed_bytes DROP DEFAULT;
ALTER TABLE public.node_user_traffic_hourly ALTER COLUMN billed_bytes DROP DEFAULT;
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
