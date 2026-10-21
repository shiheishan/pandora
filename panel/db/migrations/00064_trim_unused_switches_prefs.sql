-- +goose Up
-- 清掉两类没有消费方的配置（用户 2026-10-21 同意按后台 / 门户对齐核对的推荐清理；还没有生产部署，直接删）：
-- 1. settings：降级开关 auth.login、subscription.renewal、client.config_sync 是 00002 种的核心项（essential），
--    没有任何代码读它们。删掉之后不再有 essential 的行，essential 列与 CHECK feature_switches_essential_stays_on
--    一并删除（不留没人用的机制）；剩下的开关与 settings/service 的 SeedSwitches 由 TestSeedSwitchesMatchMigration 对照。
-- 2. notify：通知偏好「marketing × telegram」没有消费方（marketing 类只有 admin.broadcast 的 email 模板，
--    模板由 registry.go 固定、后台不能新建）。删掉存量行，并加 CHECK 让营销类只剩 email，与 service 的偏好目录一致。
-- 两张表分属 settings 与 notify 模块；这一轮只分到 00064 一个号，按派工说明合放在一个迁移里。
--
-- 锁与耗时（整个迁移在一个事务里，锁持到提交）：
--   feature_switches 只有 8 行：DELETE 加 DROP COLUMN 拿 ACCESS EXCLUSIVE，瞬间完成（DROP COLUMN 只改目录，不重写表）。
--   notification_preferences 只存用户改过的项（每人至多 6 行）：ADD CONSTRAINT CHECK 拿 ACCESS EXCLUSIVE 并整表扫一遍，
--   与行数成正比，十万行级在 1 秒内；期间改偏好的请求等锁。
--   预检：SELECT count(*) FROM notification_preferences; 到百万行级时把 CHECK 改成 NOT VALID 再另起迁移 VALIDATE。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库停在 00063 原样，换低峰重试即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

DELETE FROM feature_switches WHERE code IN ('auth.login', 'subscription.renewal', 'client.config_sync');
ALTER TABLE feature_switches DROP CONSTRAINT feature_switches_essential_stays_on;
ALTER TABLE feature_switches DROP COLUMN essential;

DELETE FROM notification_preferences WHERE category = 'marketing' AND channel = 'telegram';
ALTER TABLE notification_preferences
  ADD CONSTRAINT notification_preferences_marketing_email_only CHECK (category <> 'marketing' OR channel = 'email');

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 删掉的 marketing × telegram 偏好行不还原：这一格本来就没有模板会读，退回后按缺省（开）显示，行为不变。
ALTER TABLE notification_preferences DROP CONSTRAINT IF EXISTS notification_preferences_marketing_email_only;

-- 结构与种子逐项退回 00002：essential 列（缺省 false）、CHECK、三个核心开关（开启、essential）。
ALTER TABLE feature_switches ADD COLUMN essential boolean NOT NULL DEFAULT false;
ALTER TABLE feature_switches
  ADD CONSTRAINT feature_switches_essential_stays_on CHECK (NOT essential OR enabled);
INSERT INTO feature_switches (code, enabled, essential) VALUES
  ('auth.login', true, true),
  ('subscription.renewal', true, true),
  ('client.config_sync', true, true);
