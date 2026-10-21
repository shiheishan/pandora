-- +goose Up
-- 删订阅自定义模板表 subscription_templates，并删 traffic_reset_logs 上三个已没有查询在用的索引。两张表都属 subscription 模块
-- （docs/architecture.md 4.1）。
--
-- subscription_templates（00039 建）：后台订阅模板的列、取、存、恢复缺省、预览五个接口 10-21 已删（前端不调，用户同意按对齐核对清理），
--   从此没有任何写入口；拉取时读它覆盖内置模板的那条路径（repo.TemplateContents → render.Request.Template）在同一提交里删掉，
--   内置模板照旧。表里若还有行，删表后这些自定义模板就没有了（新二进制本来也不读）。
-- traffic_reset_logs（00011 建）的索引：10-21 删了流量重置记录的列表、统计、导出接口后，剩下读这张表的只有
--   repo.PaidResetCounts（按 subscription_id 等值连接 + reason = 'order' + created_at 下限），走 traffic_reset_logs_sub_idx，保留；
--   外键 traffic_reset_logs_sub_user_fk 在 subscriptions 删改时的反查也靠它（首列 subscription_id）。
--   另外三个没有 SQL 在用：user_idx (user_id, created_at DESC) 原给「某用户的重置记录」，reason_idx、created_idx 原给统计与全站列表；
--   保留期清理、数据巡检（tests/invariants.sql、integrity_checks）都不读这张表，写入（InsertResetLogs）只多维护索引。三个都删。
-- 一步删、不分「先停读再删」两步：安装器升级时先停服务再迁移（install.sh），没有新旧二进制同时连库的窗口；
--   现在也没有生产部署（10-09 确认）。不涉及授权：configure-app-role.sql 按 ALL TABLES 授权，迁移后重跑即可，没有单独登记这张表。
--
-- 锁与耗时（整个迁移在一个事务里，锁持到提交）：DROP TABLE subscription_templates 与三个 DROP INDEX 都拿 ACCESS EXCLUSIVE
--   （DROP INDEX 锁 traffic_reset_logs 本身），只改系统目录、删文件，与行数无关，毫秒级。期间挡住写重置日志的请求（重置流量、周期滚动）。
--   预检：SELECT format, octet_length(content), updated_at FROM subscription_templates;  有行说明以前在后台存过自定义模板，
--     删表后不可找回（Down 只建回空表），需要留档就先导出。索引无需预检。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库保持原样，换低峰重试。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

DROP TABLE subscription_templates;

DROP INDEX traffic_reset_logs_user_idx;
DROP INDEX traffic_reset_logs_reason_idx;
DROP INDEX traffic_reset_logs_created_idx;

-- +goose Down
-- 按 00039、00011 原样建回（列、类型、主键、CHECK、缺省与索引定义逐项对照；两者在之后的迁移里都没再改过）。
-- 模板内容不可还原：表建回来是空的，拉取回到内置模板（删表前的二进制也是没有行就用内置）。
-- 建索引在事务里普通 CREATE INDEX：拿 SHARE 锁、挡写重置日志，耗时与 traffic_reset_logs 行数成正比（每次重置每个流量行一条，行数有界）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE INDEX traffic_reset_logs_created_idx ON traffic_reset_logs (created_at DESC);
CREATE INDEX traffic_reset_logs_reason_idx ON traffic_reset_logs (reason, created_at DESC);
CREATE INDEX traffic_reset_logs_user_idx ON traffic_reset_logs (user_id, created_at DESC);

CREATE TABLE subscription_templates (
  format     text PRIMARY KEY
             CHECK (format IN ('clash', 'clash-premium', 'stash', 'singbox', 'surfboard', 'loon')),
  content    text NOT NULL CHECK (octet_length(content) BETWEEN 1 AND 262144),
  updated_at timestamptz NOT NULL DEFAULT now()
);
