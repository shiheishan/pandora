-- +goose Up
-- 后台可编辑的订阅模板，每种格式一行，属 subscription 模块；库里没有某格式的行就用代码内置模板（恢复缺省 = 删行）；
-- 同时把 subscription_fetch_log.format 的 CHECK 放宽到新格式（stash、shadowrocket、loon、surfboard、sip008）。
--
-- 锁与耗时：建表瞬间完成；fetch log 换 CHECK 时 DROP CONSTRAINT 与 ADD CONSTRAINT ... NOT VALID 拿 ACCESS EXCLUSIVE 但不扫表；
--   VALIDATE CONSTRAINT 要扫一遍表（表有保留期清理，行数有界），整个迁移在一个事务里，所以 ACCESS EXCLUSIVE 一直持有到 VALIDATE 结束，
--   这段时间订阅拉取写日志会等锁；lock_timeout 5s 拿不到锁就失败、不排队堵住拉取。
-- 预检：无需（新 CHECK 是旧 CHECK 的超集，旧行一定满足）。
-- 失败回滚：整个迁移一个事务，失败即全部回滚，重跑即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE TABLE subscription_templates (
  format     text PRIMARY KEY
             CHECK (format IN ('clash', 'clash-premium', 'stash', 'singbox', 'surfboard', 'loon')),
  content    text NOT NULL CHECK (octet_length(content) BETWEEN 1 AND 262144),
  updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE subscription_fetch_log DROP CONSTRAINT subscription_fetch_log_format_check;
ALTER TABLE subscription_fetch_log ADD CONSTRAINT subscription_fetch_log_format_check
  CHECK (format IS NULL OR format IN ('clash', 'clash-premium', 'stash', 'singbox', 'uri', 'shadowrocket', 'loon', 'surfboard', 'sip008')) NOT VALID;
ALTER TABLE subscription_fetch_log VALIDATE CONSTRAINT subscription_fetch_log_format_check;

-- +goose Down
-- 还原旧 CHECK；fetch log 是只追加表（触发器挡 UPDATE/DELETE），已写入的新格式行改不掉也删不掉，
-- 所以旧 CHECK 以 NOT VALID 加回（只约束之后的新行），不 VALIDATE；模板表直接删除（模板内容丢失，回滚后本来也用不到）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE subscription_fetch_log DROP CONSTRAINT subscription_fetch_log_format_check;
ALTER TABLE subscription_fetch_log ADD CONSTRAINT subscription_fetch_log_format_check
  CHECK (format IS NULL OR format IN ('clash', 'clash-premium', 'singbox', 'uri')) NOT VALID;
DROP TABLE subscription_templates;
