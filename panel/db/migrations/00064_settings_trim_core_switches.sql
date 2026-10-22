-- +goose Up
-- 删掉没有消费方的三个核心降级开关（用户 2026-10-21 同意按后台 / 门户对齐核对的推荐清理；还没有生产部署，直接删）。属 settings 模块。
-- auth.login、subscription.renewal、client.config_sync 是 00002 种的核心项（essential），没有任何代码读它们。
-- 删掉之后不再有 essential 的行，essential 列与 CHECK feature_switches_essential_stays_on 一并删除（不留没人用的机制）；
-- 剩下的开关与 settings/service 的 SeedSwitches 由 TestSeedSwitchesMatchMigration 对照。
--
-- 锁与耗时（整个迁移在一个事务里，锁持到提交）：feature_switches 只有 8 行，DELETE 加 DROP COLUMN 拿 ACCESS EXCLUSIVE，
--   瞬间完成（DROP COLUMN 只改目录，不重写表）；期间挡住的只有降级开关的读写。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库停在 00063 原样，换低峰重试即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

DELETE FROM feature_switches WHERE code IN ('auth.login', 'subscription.renewal', 'client.config_sync');
ALTER TABLE feature_switches DROP CONSTRAINT feature_switches_essential_stays_on;
ALTER TABLE feature_switches DROP COLUMN essential;

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 结构与种子逐项退回 00002：essential 列（缺省 false）、CHECK、三个核心开关（开启、essential）。
ALTER TABLE feature_switches ADD COLUMN essential boolean NOT NULL DEFAULT false;
ALTER TABLE feature_switches
  ADD CONSTRAINT feature_switches_essential_stays_on CHECK (NOT essential OR enabled);
INSERT INTO feature_switches (code, enabled, essential) VALUES
  ('auth.login', true, true),
  ('subscription.renewal', true, true),
  ('client.config_sync', true, true);
