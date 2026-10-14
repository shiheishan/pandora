-- +goose Up
-- notify 模块 P6：站内信、通知去重、通知偏好、公告、Telegram 绑定；模板与投递放开站内信、Telegram 渠道，
-- 并种下到期、过期、召回、流量预警、支付成功、工单回复的模板。
-- 依赖 00005_identity.sql 的 users、00007_notify.sql 的模板与投递表。
-- 还没有生产库：00007 的两张表在这里直接换 CHECK（与 00017 换事件类型 CHECK 的做法相同）。

-- 模板：站内信、邮件、Telegram 三个渠道。
ALTER TABLE notification_templates DROP CONSTRAINT notification_templates_channel_check;
ALTER TABLE notification_templates ADD CONSTRAINT notification_templates_channel_check
  CHECK (channel IN ('email', 'inapp', 'telegram'));

-- 投递：出站的邮件与 Telegram。站内信不进投递表（写进 notification_inbox 即送达）。
-- Telegram 投递的 payload_enc 封存 chat_id 与模板变量，同邮件一样进入终态即清空。
ALTER TABLE notification_deliveries DROP CONSTRAINT notification_deliveries_channel_check;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_channel_check
  CHECK (channel IN ('email', 'telegram'));

-- 站内信：写入时按当时的模板渲染好主题与正文（之后改模板不改已发出的消息），用户拉取即可看到。
-- 不复用 notification_deliveries：那是出站队列（内容封存、终态清空、按租约认领），站内信要长期可读、
-- 带已读状态，生命周期与索引都不同。保留 90 天（与投递记录相同），由 retention:notify 清理。
CREATE TABLE notification_inbox (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  template_code text NOT NULL CHECK (char_length(template_code) BETWEEN 1 AND 64),
  subject       text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 200),
  body          text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 20000),
  read_at       timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now()
);

-- 收件箱列表：按用户、时间倒序
CREATE INDEX notification_inbox_user_idx ON notification_inbox (user_id, created_at DESC);
-- 未读数
CREATE INDEX notification_inbox_unread_idx ON notification_inbox (user_id) WHERE read_at IS NULL;
-- 保留期清理
CREATE INDEX notification_inbox_created_idx ON notification_inbox (created_at);

-- 通知去重：一件事（到期提醒的某一档、某一期的流量预警、某一单的支付成功……）只通知一次，与渠道无关。
-- 写站内信与投递之前先插这里，撞键就整件事跳过。键里带业务标识与周期（如订阅 id + 档位 + 周期末），
-- 续费后的下一个周期是新的一件事。保留 400 天（长于任何一档提醒的窗口），由 retention:notify 清理。
CREATE TABLE notification_events (
  dedupe_key    text PRIMARY KEY CHECK (char_length(dedupe_key) BETWEEN 1 AND 200),
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  template_code text NOT NULL CHECK (char_length(template_code) BETWEEN 1 AND 64),
  created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX notification_events_created_idx ON notification_events (created_at);
-- 外键级联
CREATE INDEX notification_events_user_idx ON notification_events (user_id);

-- 通知偏好：只存用户改过的项，没存的按缺省（全开）。交易类不能关。站内信不受偏好控制。
CREATE TABLE notification_preferences (
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  category   text NOT NULL CHECK (category IN ('transactional', 'service', 'marketing')),
  channel    text NOT NULL CHECK (channel IN ('email', 'telegram')),
  enabled    boolean NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, category, channel),
  CONSTRAINT notification_preferences_transactional_always_on CHECK (category <> 'transactional' OR enabled)
);

-- 公告。定向用 uuid 数组而不是关联表：用户组删除后关联行会级联消失，公告就变成全员可见；
-- 数组里留着已删的组，公告只是谁也匹配不到。存在性由 service 保存时经 catalog、identity 校验。
-- 状态：draft → scheduled / published；scheduled → published（notify:publish_announcements 到点转）；
-- 任一非撤回状态 → withdrawn（终态，不删行）。版本号做乐观锁。
CREATE TABLE announcements (
  id                    uuid PRIMARY KEY DEFAULT uuidv7(),
  title                 text NOT NULL CHECK (char_length(title) BETWEEN 2 AND 160),
  body                  text NOT NULL CHECK (char_length(body) BETWEEN 2 AND 20000),
  severity              text NOT NULL DEFAULT 'info'
                          CHECK (severity IN ('info', 'notice', 'warning', 'critical')),
  pinned                boolean NOT NULL DEFAULT false,
  -- 为空表示不限；两者都填是「并且」
  target_plan_ids       uuid[] NOT NULL DEFAULT '{}' CHECK (cardinality(target_plan_ids) <= 200),
  target_user_group_ids uuid[] NOT NULL DEFAULT '{}' CHECK (cardinality(target_user_group_ids) <= 200),
  status                text NOT NULL DEFAULT 'draft'
                          CHECK (status IN ('draft', 'scheduled', 'published', 'withdrawn')),
  version               int NOT NULL DEFAULT 1 CHECK (version >= 1),
  publish_at            timestamptz,
  expires_at            timestamptz,
  published_at          timestamptz,
  withdrawn_at          timestamptz,
  withdrawn_by          uuid REFERENCES users(id) ON DELETE SET NULL,
  created_by            uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT announcements_window CHECK (expires_at IS NULL OR publish_at IS NULL OR expires_at > publish_at),
  CONSTRAINT announcements_scheduled_at CHECK (status <> 'scheduled' OR publish_at IS NOT NULL),
  CONSTRAINT announcements_published_at CHECK (status <> 'published' OR published_at IS NOT NULL),
  CONSTRAINT announcements_withdrawn CHECK ((status = 'withdrawn') = (withdrawn_at IS NOT NULL))
);

-- 门户可见公告
CREATE INDEX announcements_live_idx ON announcements (publish_at) WHERE status = 'published';
-- 定时发布任务
CREATE INDEX announcements_scheduled_idx ON announcements (publish_at) WHERE status = 'scheduled';

-- Telegram 绑定：一个用户一个 Telegram、一个 Telegram 一个用户。
-- 绑定必须双向验证：门户发一次性绑定码，用户在 Telegram 里发给 bot，webhook 带着 chat_id 回来核对；
-- 不让用户直接填 chat_id（填别人的就能劫持别人的通知）。
CREATE TABLE telegram_bindings (
  user_id  uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  chat_id  bigint NOT NULL CHECK (chat_id <> 0),
  username text NOT NULL DEFAULT '' CHECK (char_length(username) <= 64),
  bound_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT telegram_bindings_chat_key UNIQUE (chat_id)
);

-- 绑定码：10 分钟、一次性；只存 HMAC（子密钥 pandora/hmac/notify/v1），不存明文。
-- 过期一天后由 retention:notify 删除。
CREATE TABLE telegram_bind_codes (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash  bytea NOT NULL CHECK (octet_length(code_hash) = 32),
  expires_at timestamptz NOT NULL,
  used_at    timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT telegram_bind_codes_hash_key UNIQUE (code_hash),
  CONSTRAINT telegram_bind_codes_ttl CHECK (expires_at > created_at)
);

-- 发新码时作废本人未用的旧码
CREATE INDEX telegram_bind_codes_user_idx ON telegram_bind_codes (user_id) WHERE used_at IS NULL;
-- 清理
CREATE INDEX telegram_bind_codes_expires_idx ON telegram_bind_codes (expires_at);

-- 内置模板种子，与 notify service registry.go 的默认内容逐字一致（PG 测试对照）。
INSERT INTO notification_templates (code, channel, subject, body) VALUES
('subscription.expiring', 'inapp', '套餐即将到期',
'你的「{{plan}}」将在 {{days}} 天后（{{expires_at}}）到期。到期后节点会停止服务，记得及时续费。'),
('subscription.expiring', 'email', '【{{site}}】你的套餐 {{days}} 天后到期',
'你好，

你的「{{plan}}」将在 {{days}} 天后到期（{{expires_at}}）。
到期后节点将停止服务，请及时续费以免影响使用。

{{site}}'),
('subscription.expiring', 'telegram', '套餐即将到期',
'你的「{{plan}}」将在 {{days}} 天后（{{expires_at}}）到期，记得续费。'),
('subscription.expired', 'inapp', '套餐已到期',
'你的「{{plan}}」已于 {{expired_at}} 到期，节点已停止服务。续费后在客户端里更新订阅即可恢复，订阅链接不变。'),
('subscription.expired', 'email', '【{{site}}】你的套餐已到期',
'你好，

你的「{{plan}}」已于 {{expired_at}} 到期，节点已停止服务。
续费后在客户端里更新一次订阅即可恢复，订阅链接不变，无需重新导入。
过期满 30 天后将不能再原地续费，只能重新购买并更换订阅链接。

{{site}}'),
('subscription.expired', 'telegram', '套餐已到期',
'你的「{{plan}}」已于 {{expired_at}} 到期，续费后更新订阅即可恢复，链接不变。'),
('subscription.recall', 'inapp', '套餐已过期 {{days}} 天',
'你的「{{plan}}」已于 {{expired_at}} 到期。现在续费，原订阅链接自动恢复，无需重新导入；过期满 30 天后只能重新购买。'),
('subscription.recall', 'email', '【{{site}}】你的套餐已过期 {{days}} 天',
'你好，

你的「{{plan}}」已于 {{expired_at}} 到期，至今已 {{days}} 天。
现在续费，原订阅链接自动恢复，无需重新导入；过期满 30 天后只能重新购买，并需要更换订阅链接。

{{site}}'),
('subscription.recall', 'telegram', '套餐已过期 {{days}} 天',
'你的「{{plan}}」已于 {{expired_at}} 到期，现在续费原链接自动恢复。'),
('quota.warning', 'inapp', '流量即将用尽',
'你的「{{plan}}」已使用 {{percent}}% 流量（剩余 {{remaining}}）。用尽后将无法连接节点。'),
('quota.warning', 'email', '【{{site}}】流量已使用 {{percent}}%',
'你好，

你的「{{plan}}」已使用 {{percent}}% 流量，剩余 {{remaining}}。
流量用尽后将无法连接节点，可在面板购买流量包或升级套餐。

{{site}}'),
('quota.warning', 'telegram', '流量预警',
'你的「{{plan}}」已使用 {{percent}}% 流量，剩余 {{remaining}}。'),
('order.paid', 'inapp', '支付成功',
'订单 {{order_no}} 已支付成功，「{{plan}}」已开通，有效期至 {{expires_at}}。'),
('order.paid', 'telegram', '支付成功',
'订单 {{order_no}} 已支付，「{{plan}}」已开通，有效期至 {{expires_at}}。'),
('ticket.replied', 'inapp', '工单有新回复',
'你的工单「{{subject}}」有新回复，点击查看。'),
('ticket.replied', 'telegram', '工单有新回复',
'你的工单「{{subject}}」有新回复。');

-- +goose Down
DROP TABLE IF EXISTS telegram_bind_codes;
DROP TABLE IF EXISTS telegram_bindings;
DROP TABLE IF EXISTS announcements;
DROP TABLE IF EXISTS notification_preferences;
DROP TABLE IF EXISTS notification_events;
DROP TABLE IF EXISTS notification_inbox;

-- 回到 00007：只有邮件渠道、只有两个验证码模板。本迁移种下的模板与 Telegram 投递一并删掉
-- （00007 的派发不认这些模板，留着只会停在 queued）。
DELETE FROM notification_deliveries
 WHERE channel <> 'email' OR template_code NOT IN ('auth.email_verify', 'auth.password_reset');
DELETE FROM notification_templates
 WHERE channel <> 'email' OR code NOT IN ('auth.email_verify', 'auth.password_reset');

ALTER TABLE notification_deliveries DROP CONSTRAINT notification_deliveries_channel_check;
ALTER TABLE notification_deliveries ADD CONSTRAINT notification_deliveries_channel_check
  CHECK (channel IN ('email'));
ALTER TABLE notification_templates DROP CONSTRAINT notification_templates_channel_check;
ALTER TABLE notification_templates ADD CONSTRAINT notification_templates_channel_check
  CHECK (channel IN ('email'));
