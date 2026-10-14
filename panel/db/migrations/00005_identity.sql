-- +goose Up
-- identity 模块：用户、口令、用户组、注册会话、验证码、邀请码、邀请关系。
--
-- 与旧表相比去掉了租户列与 RLS；邮箱用 citext，UNIQUE(email) 直接按不区分大小写判重。
-- 口令、验证码、注册令牌只存哈希：口令是 Argon2id PHC 串，注册令牌是 sha256，
-- 验证码与收件目标是 HMAC（子密钥 pandora/hmac/identity/v1）。

-- 用户组：套餐可见、专属价格、优惠券、节点池按它限定（这些引用在各自模块的迁移里建外键）。
CREATE TABLE user_groups (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  -- 建组后不可改：别的模块按 id 引用，code 只做人读标识
  code        text NOT NULL CHECK (code ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
  name        text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 64),
  description text CHECK (char_length(description) <= 500),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT user_groups_code_key UNIQUE (code)
);

-- 用户。status 只留有写入方的三种：后台停用（suspended）与封禁（banned）都吊销全部会话。
CREATE TABLE users (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  email             citext NOT NULL CHECK (char_length(email) BETWEEN 3 AND 254),
  email_verified_at timestamptz,
  display_name      text CHECK (char_length(display_name) <= 64),
  status            text NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active', 'suspended', 'banned')),
  -- 组里还有用户时不许删组（RESTRICT）：静默移出会改变用户能看到的套餐与价格
  user_group_id     uuid REFERENCES user_groups(id) ON DELETE RESTRICT,
  risk_level        text NOT NULL DEFAULT 'normal'
                      CHECK (risk_level IN ('trusted', 'normal', 'elevated', 'high')),
  last_login_at     timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT users_email_key UNIQUE (email)
);

-- 后台用户列表按 (created_at DESC, id DESC) 翻页
CREATE INDEX users_created_idx ON users (created_at DESC, id DESC);
CREATE INDEX users_group_idx ON users (user_group_id) WHERE user_group_id IS NOT NULL;

-- 口令单独成表：通用的用户读取不会顺手带出哈希；没设过口令的账号没有这一行。
CREATE TABLE user_passwords (
  user_id    uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  phc        text NOT NULL CHECK (phc LIKE '$argon2id$%'),
  rotated_at timestamptz NOT NULL DEFAULT now(),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- 邀请码：用户的个人码在门户「邀请」页懒生成，每人同时只有一个 active 的码。
CREATE TABLE invite_codes (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  code          citext NOT NULL CHECK (char_length(code) BETWEEN 4 AND 32),
  owner_user_id uuid REFERENCES users(id) ON DELETE CASCADE,
  max_uses      int CHECK (max_uses IS NULL OR max_uses > 0),
  used_count    int NOT NULL DEFAULT 0 CHECK (used_count >= 0),
  channel       text CHECK (char_length(channel) <= 64),
  campaign      text CHECK (char_length(campaign) <= 64),
  expires_at    timestamptz,
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'exhausted', 'disabled')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT invite_codes_code_key UNIQUE (code),
  CONSTRAINT invite_codes_not_oversold CHECK (max_uses IS NULL OR used_count <= max_uses)
);

CREATE UNIQUE INDEX invite_codes_one_active_per_owner ON invite_codes (owner_user_id)
  WHERE status = 'active' AND owner_user_id IS NOT NULL;

-- 注册会话：第 1 步签发，第 2 步凭令牌建号。邀请码在第 1 步解析并绑定，第 2 步只认这一个。
CREATE TABLE registration_sessions (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  token_hash     bytea NOT NULL CHECK (octet_length(token_hash) = 32),
  email          citext NOT NULL CHECK (char_length(email) BETWEEN 3 AND 254),
  invite_code_id uuid REFERENCES invite_codes(id) ON DELETE CASCADE,
  -- 来源 IP 的 HMAC（pandora/hmac/ip/v1）与 UA，供风控关联，不存明文 IP
  ip_hash        bytea,
  user_agent     text CHECK (char_length(user_agent) <= 512),
  status         text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'consumed')),
  -- 验证码输错次数，满 5 次这个注册会话作废
  attempts       smallint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  expires_at     timestamptz NOT NULL,
  consumed_at    timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT registration_sessions_token_key UNIQUE (token_hash),
  CONSTRAINT registration_sessions_consumed_shape CHECK ((status = 'consumed') = (consumed_at IS NOT NULL))
);

-- 保留期清理按 created_at 删（7 天）
CREATE INDEX registration_sessions_created_idx ON registration_sessions (created_at);

-- 一次性验证码：注册验邮箱（email_verify）与找回密码（password_reset）。
CREATE TABLE verification_codes (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  purpose      text NOT NULL CHECK (purpose IN ('email_verify', 'password_reset')),
  target_hash  bytea NOT NULL CHECK (octet_length(target_hash) = 32),
  code_hash    bytea NOT NULL CHECK (octet_length(code_hash) = 32),
  user_id      uuid REFERENCES users(id) ON DELETE CASCADE,
  attempts     smallint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  max_attempts smallint NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
  consumed_at  timestamptz,
  expires_at   timestamptz NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  -- 找回密码的码必须绑定到具体账号
  CONSTRAINT verification_codes_reset_has_user CHECK (purpose <> 'password_reset' OR user_id IS NOT NULL)
);

CREATE INDEX verification_codes_lookup_idx ON verification_codes (purpose, target_hash, created_at DESC)
  WHERE consumed_at IS NULL;
CREATE INDEX verification_codes_created_idx ON verification_codes (created_at);

-- 邀请关系：被邀请人为主键，一个用户只有一个上级；首次绑定后不可改（只追加）。
-- 外键一律 RESTRICT：只追加守卫会挡住级联的 UPDATE / DELETE，写明比隐式失败清楚。
CREATE TABLE referrals (
  referee_user_id  uuid PRIMARY KEY REFERENCES users(id) ON DELETE RESTRICT,
  referrer_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  invite_code_id   uuid REFERENCES invite_codes(id) ON DELETE RESTRICT,
  channel          text CHECK (char_length(channel) <= 64),
  campaign         text CHECK (char_length(campaign) <= 64),
  risk_flag        text NOT NULL DEFAULT 'none'
                     CHECK (risk_flag IN ('none', 'suspicious', 'confirmed_fraud')),
  bound_at         timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT referrals_no_self CHECK (referee_user_id <> referrer_user_id)
);

CREATE INDEX referrals_referrer_idx ON referrals (referrer_user_id, bound_at DESC);

CREATE TRIGGER referrals_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON referrals
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

-- +goose Down
DROP TABLE IF EXISTS referrals;
DROP TABLE IF EXISTS verification_codes;
DROP TABLE IF EXISTS registration_sessions;
DROP TABLE IF EXISTS invite_codes;
DROP TABLE IF EXISTS user_passwords;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS user_groups;
