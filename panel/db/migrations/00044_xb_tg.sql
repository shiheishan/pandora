-- +goose Up
-- Xboard 补齐「xb-tg」块（条目 #20 #21 #36，见 .claude/runs/xboard-parity.md 第 2 节；共用约定一块只许一个迁移，
-- 所以 notify 与 support 两个模块的改动写在同一个文件里）：
--   1. notify：Telegram 工单提醒消息与工单的映射。管理员在管理员群组里回复（reply_to）这条提醒，就按映射找到工单。
--   2. support：关闭原因加 auto_closed（定时任务 support:auto_close 按后台设置自动关闭「等待用户」太久的工单）。
--   3. support：自动关闭扫描用的部分索引。
--   4. notify：三个发到管理员群组的 Telegram 模板种子，与 notify service registry.go 的默认内容逐字一致（PG 测试对照）。
-- 锁与耗时：tickets 换 CHECK 要扫一遍全表校验（ACCESS EXCLUSIVE），tickets 是小表（工单量级），在事务里直接做；
-- 部分索引同理普通建。还没有生产部署（用户 10-09 确认），不需要 NOT VALID 两步。
-- 预检：无（新表、只放宽取值）。失败即整个事务回滚，库停在 00042。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- 工单提醒消息映射：属主 notify。只存定位用的三个字段：哪个群（chat_id）、哪条消息（message_id）、对应哪张工单。
-- 派发把提醒发出去、Telegram 回了 message_id 之后写一行；保留 90 天（与投递记录相同），由 retention:notify 清理。
CREATE TABLE telegram_ticket_messages (
  chat_id    bigint NOT NULL CHECK (chat_id <> 0),
  message_id bigint NOT NULL CHECK (message_id > 0),
  ticket_id  uuid NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (chat_id, message_id)
);
-- 外键与按工单查
CREATE INDEX telegram_ticket_messages_ticket_idx ON telegram_ticket_messages (ticket_id);
-- 保留期清理
CREATE INDEX telegram_ticket_messages_created_idx ON telegram_ticket_messages (created_at);

-- 关闭原因：用户关闭 / 撤回 / 客服关闭 / 自动关闭
ALTER TABLE tickets DROP CONSTRAINT tickets_closed_reason_check;
ALTER TABLE tickets ADD CONSTRAINT tickets_closed_reason_check
  CHECK (closed_reason IN ('user_closed', 'withdrawn', 'agent_closed', 'auto_closed'));

-- 自动关闭扫描：等待用户的工单按最后更新时间
CREATE INDEX tickets_auto_close_idx ON tickets (updated_at, id) WHERE status = 'pending_user';

-- 管理员群组提醒模板（registry.go 的 adminTicketBody 与这里两条工单模板的正文相同）
INSERT INTO notification_templates (code, channel, subject, body) VALUES
('admin.order_paid', 'telegram', '收款通知',
'订单号：{{order_no}}
金额：{{amount}}
套餐：{{plan}}
用户：{{email}}'),
('admin.ticket_opened', 'telegram', '新工单 {{ticket_no}}',
'用户：{{email}}
套餐：{{plan}}
剩余流量：{{traffic}}
到期：{{expires_at}}
余额：{{balance}}

主题：{{subject}}
{{body}}

直接回复这条消息即可回复工单。'),
('admin.ticket_replied', 'telegram', '工单 {{ticket_no}} 有新回复',
'用户：{{email}}
套餐：{{plan}}
剩余流量：{{traffic}}
到期：{{expires_at}}
余额：{{balance}}

主题：{{subject}}
{{body}}

直接回复这条消息即可回复工单。');

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

DELETE FROM notification_deliveries
 WHERE template_code IN ('admin.order_paid', 'admin.ticket_opened', 'admin.ticket_replied');
DELETE FROM notification_templates
 WHERE code IN ('admin.order_paid', 'admin.ticket_opened', 'admin.ticket_replied');

DROP INDEX IF EXISTS tickets_auto_close_idx;

-- 不可完全还原：退回后没有 auto_closed，自动关闭的工单并入「客服关闭」（仍是已关闭，只是原因合并）
UPDATE tickets SET closed_reason = 'agent_closed' WHERE closed_reason = 'auto_closed';
ALTER TABLE tickets DROP CONSTRAINT tickets_closed_reason_check;
ALTER TABLE tickets ADD CONSTRAINT tickets_closed_reason_check
  CHECK (closed_reason IN ('user_closed', 'withdrawn', 'agent_closed'));

DROP TABLE IF EXISTS telegram_ticket_messages;
