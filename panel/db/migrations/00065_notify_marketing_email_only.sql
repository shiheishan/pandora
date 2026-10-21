-- +goose Up
-- 通知偏好「marketing × telegram」没有消费方（用户 2026-10-21 同意按后台 / 门户对齐核对的推荐清理）。属 notify 模块。
-- marketing 类只有 admin.broadcast 的 email 模板，模板由 registry.go 固定、后台不能新建。删掉存量行，
-- 并加 CHECK 让营销类只剩 email，与 service 的偏好目录（inbox.go 的 preferenceCatalog）一致。
--
-- 锁与耗时（整个迁移在一个事务里，锁持到提交）：notification_preferences 只存用户改过的项（迁移前每人至多 6 行，之后 5 行）：
--   ADD CONSTRAINT CHECK 拿 ACCESS EXCLUSIVE 并整表扫一遍，与行数成正比，十万行级在 1 秒内；期间改偏好的请求等锁。
--   预检：SELECT count(*) FROM notification_preferences; 到百万行级时把 CHECK 改成 NOT VALID 再另起迁移 VALIDATE。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库停在 00064 原样，换低峰重试即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

DELETE FROM notification_preferences WHERE category = 'marketing' AND channel = 'telegram';
ALTER TABLE notification_preferences
  ADD CONSTRAINT notification_preferences_marketing_email_only CHECK (category <> 'marketing' OR channel = 'email');

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 删掉的 marketing × telegram 偏好行不还原：这一格本来就没有模板会读，退回后按缺省（开）显示，行为不变。
ALTER TABLE notification_preferences DROP CONSTRAINT notification_preferences_marketing_email_only;
