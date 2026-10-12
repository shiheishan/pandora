-- +goose Up
-- notify 模块（P2 只有邮件）：通知模板与投递记录。
-- 依赖 00005_identity.sql 的 users 表（投递记录的 user_id 外键）。

-- 通知模板：每个 (code, channel) 一行，后台只能改主题与正文，每改一次 version 加 1。
-- 模板代码、类别、变量白名单、说明和默认内容定义在 notify service 的 registry.go，
-- 下面的种子与那里的默认内容逐字一致（PG 测试对照）；不在 registry 里的 code 不会被发送。
CREATE TABLE notification_templates (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  code       text NOT NULL,
  channel    text NOT NULL CHECK (channel IN ('email', 'inapp', 'telegram')),
  version    int NOT NULL DEFAULT 1 CHECK (version >= 1),
  subject    text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 200),
  body       text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 20000),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT notification_templates_code_format
    CHECK (code ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$' AND char_length(code) <= 64),
  CONSTRAINT notification_templates_unique UNIQUE (code, channel)
);

-- 投递记录：业务事务里写一行 queued，提交后由 notify:dispatch 认领发送。
-- 收件地址与模板变量（含验证码明文）用 Sealer 封存在 payload_enc（AAD 含本行 id），
-- 进入终态（sent / failed / suppressed）时清空；按地址关联只用 recipient_hash
-- （HMAC，子密钥 pandora/hmac/notify/v1）。保留 90 天，由 retention:notify 清理。
CREATE TABLE notification_deliveries (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  -- 按地址投递（注册验证码、找回密码）可以没有用户
  user_id        uuid REFERENCES users(id) ON DELETE CASCADE,
  template_code  text NOT NULL CHECK (char_length(template_code) BETWEEN 1 AND 64),
  channel        text NOT NULL CHECK (channel IN ('email', 'inapp', 'telegram')),
  recipient_hash bytea NOT NULL CHECK (octet_length(recipient_hash) = 32),
  payload_enc    bytea,
  status         text NOT NULL DEFAULT 'queued'
                   CHECK (status IN ('queued', 'sent', 'failed', 'suppressed')),
  attempts       smallint NOT NULL DEFAULT 0,
  max_attempts   smallint NOT NULL DEFAULT 5 CHECK (max_attempts BETWEEN 1 AND 20),
  -- queued 行下次可以认领的时间：新行即刻；认领时推到租约末；失败后按退避推后
  next_retry_at  timestamptz NOT NULL DEFAULT now(),
  error_message  text CHECK (char_length(error_message) <= 500),
  sent_at        timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT notification_deliveries_attempts_range CHECK (attempts BETWEEN 0 AND max_attempts),
  CONSTRAINT notification_deliveries_sent_at CHECK ((status = 'sent') = (sent_at IS NOT NULL)),
  -- queued 必须带着要发的内容；终态必须已清空
  CONSTRAINT notification_deliveries_payload_state
    CHECK ((status = 'queued') = (payload_enc IS NOT NULL))
);

-- 派发认领：到期的 queued 行
CREATE INDEX notification_deliveries_due_idx ON notification_deliveries (next_retry_at)
  WHERE status = 'queued';
-- 保留期清理与按时间查
CREATE INDEX notification_deliveries_created_idx ON notification_deliveries (created_at);
-- 外键级联与按用户查
CREATE INDEX notification_deliveries_user_idx ON notification_deliveries (user_id, created_at DESC)
  WHERE user_id IS NOT NULL;

-- 内置模板种子，与 registry.go 的默认内容逐字一致。
INSERT INTO notification_templates (code, channel, subject, body) VALUES
('auth.email_verify', 'email', '【{{site}}】注册验证码 {{code}}',
'你好，

你正在注册 {{site}}，验证码是：

{{code}}

验证码 {{minutes}} 分钟内有效。如果这不是你本人的操作，忽略这封邮件即可。

{{site}}'),
('auth.password_reset', 'email', '【{{site}}】重置密码验证码 {{code}}',
'你好，

你正在重置 {{site}} 的登录密码，验证码是：

{{code}}

验证码 {{minutes}} 分钟内有效。重置成功后，这个账号在所有设备上的登录都会失效，需要用新密码重新登录。

如果这不是你本人的操作，忽略这封邮件即可，你的密码不会改变。

{{site}}');

-- +goose Down
DROP TABLE IF EXISTS notification_deliveries;
DROP TABLE IF EXISTS notification_templates;
