-- +goose Up
-- 删掉只存不用的三列（用户 10-14 同意）：
--   prices.trial_days：价格的试用天数，下单与开通从不读它（注册试用是另一套，不受影响）；
--   plan_versions.grace_period_hours、grace_keeps_service：版本宽限期，订阅只有 active / expired 两态（10-12），
--   到期判定、订阅状态与节点名单都不读它们。
-- 冻结守卫（catalog_guard_plan_version / catalog_guard_price）按 to_jsonb 整行比较，不点名列，删列后照常生效；
-- DROP COLUMN 不触发行级触发器，已发布版本与价格的冻结不受影响。
--
-- 部署顺序：先停旧版本进程再迁移（旧进程的 GORM 结构体还带这三列，删列后它读写价格与版本会报错）。
-- 锁与耗时：DROP COLUMN 只改目录（不重写表），要两张表的 ACCESS EXCLUSIVE，拿到即完成；
--   没有生产部署，表很小。拿不到锁时 lock_timeout 5 秒报错。
-- 预检（可选，看会丢什么）：SELECT count(*) FROM prices WHERE trial_days <> 0;
--   SELECT count(*) FROM plan_versions WHERE grace_period_hours <> 0 OR NOT grace_keeps_service;
-- 失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库保持原样，停掉旧版本进程后重试。
-- 数据不可恢复：Down 按 00008 原样加回三列（类型、默认值、CHECK 一致，列排到表尾），值一律回到默认值。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE prices DROP COLUMN trial_days;
ALTER TABLE plan_versions DROP COLUMN grace_period_hours, DROP COLUMN grace_keeps_service;

-- +goose Down
-- 按 00008_catalog.sql 加回三列（数据不恢复，见文件头）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE plan_versions
  ADD COLUMN grace_period_hours int NOT NULL DEFAULT 0 CHECK (grace_period_hours >= 0),
  ADD COLUMN grace_keeps_service boolean NOT NULL DEFAULT true;
ALTER TABLE prices ADD COLUMN trial_days smallint NOT NULL DEFAULT 0 CHECK (trial_days >= 0);
