-- 按日流量（00072 的 subscription_usage_daily）补一条 (tenant_id, day) 索引。
--
-- 原表只有主键 (tenant_id, subscription_id, day)：按「本租户某几天」取行（后台行为趋势的活跃用户、
-- 00107 起趋势按天汇总的重算与回填，以及读路径判断昨天那一行是否还会变）只能把这个租户的全部
-- 按日流量扫一遍。90 天数据的 5k 库副本上，趋势实时部分单这一步就要 26ms，并且随天数线性增长。
--
-- 只索引 tenant_id 与 day，不 INCLUDE bytes / updated_at：入库路径每分钟按 (租户, 订阅, 日) upsert
-- 并改 bytes 与 updated_at，这两列进了索引，更新就不再是 HOT，入库的写放大会变大；
-- tenant_id 与 day 写入后不变，更新照旧 HOT，新增的代价只在每条订阅每天第一次写入时多一条索引项。
--
-- 发布是停写窗口（install.sh 先停三个网关再跑迁移），普通 CREATE INDEX 的写锁不影响线上。

-- +goose Up

SET LOCAL lock_timeout = '5s';

CREATE INDEX IF NOT EXISTS idx_subscription_usage_daily_tenant_day
  ON subscription_usage_daily (tenant_id, day);

-- +goose Down

DROP INDEX IF EXISTS idx_subscription_usage_daily_tenant_day;
