-- +goose Up

-- 快捷登录令牌（对标 Xboard getQuickLoginUrl）。
--
-- 用途：用户在面板里点一下，拿到一条「打开即登录」的链接，
-- 用来在另一台设备上免密码进入，或者贴进客户端的内置浏览器。
--
-- 这是一条能直接换到身份的凭证，所以设计上处处从紧：
--   · 只存哈希。库被读走也不能反推出可用的链接。
--   · 60 秒有效。够用户复制粘贴，不够别人截获后慢慢用。
--   · 一次性。used_at 一旦落下，同一条链接再打开就无效。
--   · 绑定签发时的会话。原会话被踢或退出登录后，这条链接跟着失效 ——
--     否则「退出登录」就成了假的：手里还攥着链接的人照样能进来。

CREATE TABLE quick_login_tokens (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  -- 签发它的会话。会话失效则令牌失效
  session_id   uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  token_hash   bytea NOT NULL,
  used_at      timestamptz(6),
  expires_at   timestamptz(6) NOT NULL,
  created_at   timestamptz(6) NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, token_hash)
);

-- 消费时按哈希查，且只关心还没用过、还没过期的
CREATE INDEX idx_quick_login_live
  ON quick_login_tokens (tenant_id, token_hash)
  WHERE used_at IS NULL;

ALTER TABLE quick_login_tokens ENABLE ROW LEVEL SECURITY;
CREATE POLICY quick_login_tokens_tenant ON quick_login_tokens
  USING (tenant_id = app.current_tenant_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON quick_login_tokens TO aegis_app;

-- +goose Down

DROP TABLE IF EXISTS quick_login_tokens;
