-- +goose Up
-- 脏数据定时巡检与告警（用户 10-19 的问题整理：脏数据没有定时巡检和告警；检查项与表由主 Agent 10-20 定）。
-- 一块只许一个迁移，所以 report 与 notify 两个模块的改动写在同一个文件里（照 00044_xb_tg.sql），各表属主不变：
--   1. report：integrity_checks 存每项检查的最新结果。只有 report 的 repo 读写；检查本身是各属主 service 的只读方法，
--      由定时任务 report:integrity（每 30 分钟）逐项跑完写这里，后台总览待办读这里。
--   2. notify：notification_events.user_id 放开为可空。数据巡检告警发到管理员群组、不关某个用户，
--      去重键 integrity:<检查>:<这一段违例的起点>:<违例数> 照样写这张表防重发。
--   3. notify：管理员群组模板 admin.integrity_alert 的种子，与 notify service registry.go 的默认内容逐字一致（PG 测试对照）。
-- 锁与耗时（整个迁移在一个事务里，锁持到提交）：新建表瞬间完成；notification_events 去掉 NOT NULL 只改目录
-- （ACCESS EXCLUSIVE，瞬间，不扫表）；插一行模板。
--   预检：无（新表、只放宽约束、插新模板码）。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库停在 00058，换低峰重试。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- 每项检查一行（code 是检查代号，代码里的常量；没跑过的检查没有行）。
--   count：最近一次检查的违例行数；sample：最多 5 个样例 id（文本，新的在前），没有违例时为空数组。
--   failing_since：这一段违例从哪次检查开始（从 0 变成非 0 时写下，回到 0 清空），告警去重键带它，区分前后两段违例。
--   alerted_count：这一段里上次发告警时的违例数；违例数超过它才再发，回到 0 清零。没配管理员群组时不发也不记。
CREATE TABLE integrity_checks (
  code          text PRIMARY KEY CHECK (code ~ '^[a-z][a-z0-9_]{0,63}$'),
  count         bigint NOT NULL CHECK (count >= 0),
  sample        jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(sample) = 'array'),
  checked_at    timestamptz NOT NULL,
  failing_since timestamptz,
  alerted_count bigint NOT NULL DEFAULT 0 CHECK (alerted_count >= 0),
  CONSTRAINT integrity_checks_failing_shape CHECK ((count = 0) = (failing_since IS NULL)),
  CONSTRAINT integrity_checks_clean_shape CHECK (count > 0 OR (alerted_count = 0 AND sample = '[]'::jsonb)),
  -- 样例最多 5 个、不多于违例数；CASE 保证先判是数组再取长度（非数组由 sample 列的 CHECK 拦下，不在这里报函数错）
  CONSTRAINT integrity_checks_sample_size CHECK (
    CASE WHEN jsonb_typeof(sample) = 'array' THEN jsonb_array_length(sample) <= LEAST(count, 5) ELSE true END)
);

-- 去重键可以不关某个用户（管理员群组的巡检告警）
ALTER TABLE notification_events ALTER COLUMN user_id DROP NOT NULL;

-- 管理员群组的数据巡检告警模板（registry.go 的 TemplateAdminIntegrityAlert）
INSERT INTO notification_templates (code, channel, subject, body) VALUES
('admin.integrity_alert', 'telegram', '数据巡检告警',
'数据巡检发现异常
检查：{{check}}（{{code}}）
违例：{{count}} 条

处理办法见运维手册「数据巡检告警」一章，修完下一轮巡检回到 0。');

-- 授权不写在迁移里：安装与升级在 migrate up 之后跑 configure-app-role.sql，给 pandora_app 全部表的 DML。

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- 巡检告警的投递、去重键与模板一并删掉（去重键里只有巡检告警不关用户，退回 NOT NULL 前必须清掉）
DELETE FROM notification_deliveries WHERE template_code = 'admin.integrity_alert';
DELETE FROM notification_templates WHERE code = 'admin.integrity_alert';
DELETE FROM notification_events WHERE user_id IS NULL;
-- 恢复 NOT NULL 要扫一遍 notification_events（保留 400 天的去重键），持 ACCESS EXCLUSIVE 到提交
ALTER TABLE notification_events ALTER COLUMN user_id SET NOT NULL;

DROP TABLE IF EXISTS integrity_checks;
