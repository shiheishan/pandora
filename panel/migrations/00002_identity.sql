-- 身份域：tenants / users / organizations / identities / sessions / passkeys /
--          roles / permissions / role_bindings
--
-- 对应 PRD 第 5 章 IAM-001..IAM-012。核心不变量：
--   · 库里永不出现明文密码、明文验证码、明文长期令牌（IAM-002/003、SEC-011）
--   · 账号是否存在不可通过任何单表查询侧信道泄露（IAM-006，配合应用层统一响应）
--   · 权限默认拒绝，新接口不声明权限就拿不到任何数据（IAM-009）

-- +goose Up

--------------------------------------------------------------------------------
-- 用户
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE users (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  email             citext NOT NULL,
  email_verified_at timestamptz,
  -- 展示名与真实姓名分开：客服工单里只暴露 display_name（角色矩阵「默认脱敏」）
  display_name      text,
  status            text NOT NULL DEFAULT 'active'
                      CHECK (status IN ('pending', 'active', 'suspended', 'banned',
                                        'deletion_scheduled', 'anonymized')),
  -- XBD-009 用户分组：决定可见套餐、资源池、价格与通知
  user_group_id     uuid,
  locale            text NOT NULL DEFAULT 'zh-CN',
  timezone          text NOT NULL DEFAULT 'UTC',
  -- 风险画像摘要，明细在 risk_events（SEC-015）
  risk_level        text NOT NULL DEFAULT 'normal'
                      CHECK (risk_level IN ('trusted', 'normal', 'elevated', 'high')),
  -- IAM-004：高权账号强制 MFA，此处记录当前满足的最强因子
  mfa_enforced      boolean NOT NULL DEFAULT false,
  -- IAM-012 注销与匿名化工作流
  deletion_requested_at timestamptz,
  anonymized_at         timestamptz,
  last_login_at     timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),

  -- 租户内邮箱唯一。复合唯一同时充当 DATA-002 要求的租户复合约束。
  CONSTRAINT users_tenant_email_unique UNIQUE (tenant_id, email)
);

-- 外键都带上 tenant_id，让「跨租户挂接子记录」在数据库层就不可能发生
CREATE UNIQUE INDEX users_tenant_id_id_key ON users (tenant_id, id);

CREATE INDEX idx_users_tenant_status ON users (tenant_id, status)
  WHERE status IN ('active', 'pending');
CREATE INDEX idx_users_deletion_due ON users (deletion_requested_at)
  WHERE status = 'deletion_scheduled';

CREATE TRIGGER trg_users_updated_at BEFORE UPDATE ON users
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('users');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 用户组（XBD-009）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE user_groups (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code        text NOT NULL,
  name        text NOT NULL,
  description text,
  -- 组级覆盖：可见套餐、资源池白名单、价格系数、通知策略
  policy      jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT user_groups_tenant_code_unique UNIQUE (tenant_id, code)
);
CREATE UNIQUE INDEX user_groups_tenant_id_id_key ON user_groups (tenant_id, id);

CREATE TRIGGER trg_user_groups_updated_at BEFORE UPDATE ON user_groups
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('user_groups');

ALTER TABLE users ADD CONSTRAINT users_user_group_fk
  FOREIGN KEY (tenant_id, user_group_id) REFERENCES user_groups (tenant_id, id)
  ON DELETE SET NULL;
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 凭据：密码（IAM-003 Argon2id）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE user_passwords (
  user_id       uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- 完整 PHC 串：$argon2id$v=19$m=...,t=...,p=...$salt$hash
  -- 参数内嵌，将来调强参数可随登录逐步重算，无需一次性迁移
  phc           text NOT NULL CHECK (phc LIKE '$argon2id$%'),
  -- SEC-005：命中已泄露口令库时置位，下次登录强制改密
  breached      boolean NOT NULL DEFAULT false,
  must_rotate   boolean NOT NULL DEFAULT false,
  rotated_at    timestamptz NOT NULL DEFAULT now(),
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_user_passwords_updated_at BEFORE UPDATE ON user_passwords
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('user_passwords');

COMMENT ON TABLE user_passwords IS
  'IAM-003：仅存 Argon2id PHC 串。管理员无任何途径读取明文密码。';
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 凭据：Passkey / WebAuthn（IAM-003，优先于密码）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE passkeys (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id           uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_id     bytea NOT NULL,
  public_key        bytea NOT NULL,
  -- 防克隆：认证器每次签名递增，回退即视为凭据被复制
  sign_count        bigint NOT NULL DEFAULT 0,
  aaguid            uuid,
  transports        text[] NOT NULL DEFAULT '{}',
  backup_eligible   boolean NOT NULL DEFAULT false,
  backup_state      boolean NOT NULL DEFAULT false,
  friendly_name     text,
  last_used_at      timestamptz,
  -- IAM-003：可独立撤销
  revoked_at        timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT passkeys_credential_id_unique UNIQUE (credential_id)
);

CREATE INDEX idx_passkeys_user ON passkeys (tenant_id, user_id) WHERE revoked_at IS NULL;

SELECT app.enable_tenant_rls('passkeys');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 凭据：TOTP + 恢复码（IAM-004）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE totp_secrets (
  user_id        uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- 信封加密后的密钥（SEC-010），明文只在校验瞬间存在于内存
  secret_encrypted bytea NOT NULL,
  key_version    int NOT NULL DEFAULT 1,
  algorithm      text NOT NULL DEFAULT 'SHA1',
  digits         smallint NOT NULL DEFAULT 6,
  period_seconds smallint NOT NULL DEFAULT 30,
  confirmed_at   timestamptz,
  -- 防重放：同一时间步的验证码只允许用一次
  last_used_step bigint,
  created_at     timestamptz NOT NULL DEFAULT now()
);

SELECT app.enable_tenant_rls('totp_secrets');

CREATE TABLE recovery_codes (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  -- 只存哈希，且一次性
  code_hash   bytea NOT NULL,
  used_at     timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT recovery_codes_hash_unique UNIQUE (tenant_id, code_hash)
);

CREATE INDEX idx_recovery_codes_user ON recovery_codes (tenant_id, user_id) WHERE used_at IS NULL;

SELECT app.enable_tenant_rls('recovery_codes');

COMMENT ON TABLE recovery_codes IS 'IAM-004：恢复码一次性使用，库中只有哈希。';
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 注册事务与邮箱验证（IAM-001 / IAM-002）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE registration_sessions (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- 下发给前端的是这个 token 的明文，库里只有哈希
  token_hash      bytea NOT NULL,
  email           citext,
  -- 一次性 Nonce：重复提交同一个 nonce 直接拒绝
  nonce           bytea NOT NULL,
  -- 绑定注册时的风险上下文：IP 段、ASN、设备指纹摘要、挑战结果
  risk_context    jsonb NOT NULL DEFAULT '{}'::jsonb,
  invite_code_id  uuid,
  status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'verified', 'consumed', 'expired', 'rejected')),
  attempts        smallint NOT NULL DEFAULT 0,
  expires_at      timestamptz NOT NULL,
  consumed_at     timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT registration_sessions_token_unique UNIQUE (token_hash),
  CONSTRAINT registration_sessions_nonce_unique UNIQUE (tenant_id, nonce)
);

CREATE INDEX idx_registration_sessions_expiry ON registration_sessions (expires_at)
  WHERE status = 'pending';

SELECT app.enable_tenant_rls('registration_sessions');

COMMENT ON TABLE registration_sessions IS
  'IAM-001：注册前先签发短期事务。过期或重复提交（nonce 唯一约束）均被拒绝。';
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE verification_codes (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- 用途：email_verify / password_reset / email_change / device_authorize ...
  purpose       text NOT NULL,
  -- 收件目标的哈希而非明文，日志与库都拿不到「谁在收验证码」的完整线索
  target_hash   bytea NOT NULL,
  code_hash     bytea NOT NULL,
  user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
  attempts      smallint NOT NULL DEFAULT 0,
  max_attempts  smallint NOT NULL DEFAULT 5,
  consumed_at   timestamptz,
  -- IAM-002：5–10 分钟失效
  expires_at    timestamptz NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_verification_codes_lookup
  ON verification_codes (tenant_id, purpose, target_hash, created_at DESC)
  WHERE consumed_at IS NULL;
CREATE INDEX idx_verification_codes_expiry ON verification_codes (expires_at);

SELECT app.enable_tenant_rls('verification_codes');

COMMENT ON TABLE verification_codes IS
  'IAM-002：验证码只存哈希，日志与数据库中均无明文（验收标准硬要求）。';
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 会话与令牌（IAM-005 / IAM-011）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE sessions (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  -- EXT-001：会话归属哪一个 API 域，令牌不可跨域使用
  audience        text NOT NULL CHECK (audience IN ('public', 'admin', 'client')),
  device_id       uuid,
  -- 只存摘要信息，不采集硬件序列号与精确位置（CLI-001 验收标准）
  user_agent      text,
  ip_hash         bytea,
  ip_asn          int,
  ip_country      char(2),
  -- 本次会话已满足的认证强度：password / passkey / mfa_totp / sso
  auth_methods    text[] NOT NULL DEFAULT '{}',
  -- SEC-009：高风险动作要求近期重认证
  last_reauth_at  timestamptz,
  risk_score      smallint NOT NULL DEFAULT 0,
  revoked_at      timestamptz,
  revoked_reason  text,
  expires_at      timestamptz NOT NULL,
  last_seen_at    timestamptz NOT NULL DEFAULT now(),
  created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_sessions_user ON sessions (tenant_id, user_id, created_at DESC)
  WHERE revoked_at IS NULL;
CREATE INDEX idx_sessions_expiry ON sessions (expires_at) WHERE revoked_at IS NULL;

SELECT app.enable_tenant_rls('sessions');
-- +goose StatementEnd

-- +goose StatementBegin
-- Refresh Token 旋转（IAM-005）。
-- 每次刷新签发新 token 并把旧的置为 rotated，同时记录 replaced_by 形成链条。
-- 一旦发现已 rotated 的 token 被再次使用，说明令牌被窃取 —— 整条链立即失效。
CREATE TABLE refresh_tokens (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  session_id    uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash    bytea NOT NULL,
  status        text NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active', 'rotated', 'revoked', 'compromised')),
  replaced_by   uuid REFERENCES refresh_tokens(id) ON DELETE SET NULL,
  expires_at    timestamptz NOT NULL,
  used_at       timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT refresh_tokens_hash_unique UNIQUE (token_hash)
);

CREATE INDEX idx_refresh_tokens_session ON refresh_tokens (session_id)
  WHERE status = 'active';

SELECT app.enable_tenant_rls('refresh_tokens');
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE api_tokens (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
  name          text NOT NULL,
  -- 明文只在创建响应里出现一次（IAM-011 验收标准）
  token_hash    bytea NOT NULL,
  -- 供用户识别的前缀，如 aeg_pub_3f9a…，本身不足以还原令牌
  token_prefix  text NOT NULL,
  audience      text NOT NULL CHECK (audience IN ('public', 'admin', 'client', 'node')),
  scopes        text[] NOT NULL DEFAULT '{}',
  -- 来源限制：CIDR 白名单
  allowed_cidrs inet[],
  last_used_at  timestamptz,
  expires_at    timestamptz,
  revoked_at    timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT api_tokens_hash_unique UNIQUE (token_hash)
);

CREATE INDEX idx_api_tokens_owner ON api_tokens (tenant_id, user_id) WHERE revoked_at IS NULL;

SELECT app.enable_tenant_rls('api_tokens');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 外部身份 / 企业 SSO（IAM-008）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE identity_providers (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code          text NOT NULL,
  kind          text NOT NULL CHECK (kind IN ('oidc', 'saml', 'oauth2')),
  display_name  text NOT NULL,
  issuer        text,
  client_id     text,
  -- 信封加密（SEC-010）
  client_secret_encrypted bytea,
  key_version   int NOT NULL DEFAULT 1,
  -- 域名验证通过后才允许该 IdP 自动接管对应邮箱域
  verified_domains text[] NOT NULL DEFAULT '{}',
  config        jsonb NOT NULL DEFAULT '{}'::jsonb,
  enabled       boolean NOT NULL DEFAULT false,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT identity_providers_tenant_code_unique UNIQUE (tenant_id, code)
);
CREATE UNIQUE INDEX identity_providers_tenant_id_id_key ON identity_providers (tenant_id, id);

CREATE TRIGGER trg_identity_providers_updated_at BEFORE UPDATE ON identity_providers
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('identity_providers');

CREATE TABLE user_identities (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider_id   uuid NOT NULL REFERENCES identity_providers(id) ON DELETE CASCADE,
  subject       text NOT NULL,
  email_at_idp  citext,
  linked_at     timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT user_identities_provider_subject_unique UNIQUE (provider_id, subject)
);

SELECT app.enable_tenant_rls('user_identities');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 组织（IAM-007）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE organizations (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  slug           citext NOT NULL,
  display_name   text NOT NULL,
  owner_user_id  uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  billing_email  citext,
  status         text NOT NULL DEFAULT 'active'
                   CHECK (status IN ('active', 'suspended', 'archived')),
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT organizations_tenant_slug_unique UNIQUE (tenant_id, slug)
);
CREATE UNIQUE INDEX organizations_tenant_id_id_key ON organizations (tenant_id, id);

CREATE TRIGGER trg_organizations_updated_at BEFORE UPDATE ON organizations
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('organizations');

CREATE TABLE organization_members (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role            text NOT NULL DEFAULT 'member'
                    CHECK (role IN ('owner', 'admin', 'billing', 'member')),
  -- IAM-007 验收：成员离开组织后立即失去组织数据访问 —— 由删除本行 + 会话重算权限保证
  joined_at       timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT organization_members_unique UNIQUE (organization_id, user_id)
);

CREATE INDEX idx_organization_members_user ON organization_members (tenant_id, user_id);

SELECT app.enable_tenant_rls('organization_members');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- RBAC + 数据范围（IAM-009 / IAM-010）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE permissions (
  -- 权限用稳定字符串主键，便于代码里以常量引用并做编译期检查
  code        text PRIMARY KEY,
  domain      text NOT NULL,
  description text NOT NULL,
  -- 该权限是否属于高风险动作，命中则要求重认证（SEC-009）
  high_risk   boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE permissions IS
  'IAM-009：新增接口必须先在此登记权限码，网关对未登记权限的路由默认拒绝。';

CREATE TABLE roles (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code         text NOT NULL,
  name         text NOT NULL,
  description  text,
  -- 系统内置角色不可被租户管理员删改
  is_system    boolean NOT NULL DEFAULT false,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT roles_tenant_code_unique UNIQUE (tenant_id, code)
);
CREATE UNIQUE INDEX roles_tenant_id_id_key ON roles (tenant_id, id);

CREATE TRIGGER trg_roles_updated_at BEFORE UPDATE ON roles
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('roles');

CREATE TABLE role_permissions (
  role_id         uuid NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  permission_code text NOT NULL REFERENCES permissions(code) ON DELETE CASCADE,
  PRIMARY KEY (role_id, permission_code)
);

-- 角色绑定 = 角色 + 数据范围（IAM-009「角色权限叠加本人、组织、租户、资源组范围」）
CREATE TABLE role_bindings (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id      uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role_id        uuid NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  scope_type     text NOT NULL DEFAULT 'self'
                   CHECK (scope_type IN ('self', 'organization', 'tenant', 'resource_group')),
  scope_id       uuid,
  -- IAM-010 临时提权：到期后无需人工操作即可回收
  granted_by     uuid REFERENCES users(id) ON DELETE SET NULL,
  grant_reason   text,
  approval_request_id uuid,
  expires_at     timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT role_bindings_unique UNIQUE (user_id, role_id, scope_type, scope_id)
);

-- expires_at 进索引键而非索引谓词：now() 是 STABLE 不是 IMMUTABLE，
-- 放在 WHERE 里会被 PostgreSQL 拒绝。查询时用
-- `WHERE tenant_id=$1 AND user_id=$2 AND (expires_at IS NULL OR expires_at > now())`
-- 依然能走这条索引。
CREATE INDEX idx_role_bindings_user ON role_bindings (tenant_id, user_id, expires_at);
CREATE INDEX idx_role_bindings_expiring ON role_bindings (expires_at)
  WHERE expires_at IS NOT NULL;

SELECT app.enable_tenant_rls('role_bindings');
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS role_bindings;
DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS permissions;
DROP TABLE IF EXISTS organization_members;
DROP TABLE IF EXISTS organizations;
DROP TABLE IF EXISTS user_identities;
DROP TABLE IF EXISTS identity_providers;
DROP TABLE IF EXISTS api_tokens;
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS verification_codes;
DROP TABLE IF EXISTS registration_sessions;
DROP TABLE IF EXISTS recovery_codes;
DROP TABLE IF EXISTS totp_secrets;
DROP TABLE IF EXISTS passkeys;
DROP TABLE IF EXISTS user_passwords;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_user_group_fk;
DROP TABLE IF EXISTS user_groups;
DROP TABLE IF EXISTS users;
-- +goose StatementEnd
