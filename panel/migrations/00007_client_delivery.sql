-- 客户端与订阅交付域：devices / subscription_credentials / config_bundles /
--                      client_releases / client_capabilities
--
-- 对应 PRD 第 11 章 CLI-001..CLI-014，以及 XBD-002/003/004/005/008。
--
-- 两条硬不变量：
--   1. CLI-007/008「客户端不保存用户明文密码」→ 设备走密钥对 + 授权码绑定，库里只有公钥。
--   2. XBD-002「不能仅依赖难猜 URL」→ 凭据是可独立吊销的实体，带作用域、限流与有效期。

-- +goose Up

--------------------------------------------------------------------------------
-- 设备（CLI-001 / CLI-007 / XBD-008）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE devices (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id         uuid NOT NULL,
  -- CLI-007：设备本地生成密钥对，平台只见公钥
  public_key      bytea NOT NULL,
  key_algorithm   text NOT NULL DEFAULT 'ed25519',
  -- CLI-001 能力登记。刻意不设硬件序列号/精确位置字段 ——
  -- 「不默认收集非必要数据」在 schema 层就没有存放处。
  platform        text NOT NULL CHECK (platform IN ('windows', 'macos', 'linux', 'android', 'ios', 'web')),
  arch            text,
  os_version      text,
  app_version     text,
  -- 客户端支持的配置格式与版本，适配决策的输入（CLI-002）
  config_schemas  text[] NOT NULL DEFAULT '{}',
  update_channel  text NOT NULL DEFAULT 'stable'
                    CHECK (update_channel IN ('stable', 'beta', 'internal')),
  friendly_name   text,
  status          text NOT NULL DEFAULT 'active'
                    CHECK (status IN ('pending', 'active', 'revoked', 'released')),
  -- XBD-008 设备宽容策略：解绑后占位保留到该时刻，避免正常重装永久占位
  released_at     timestamptz,
  revoked_at      timestamptz,
  revoked_reason  text,
  last_seen_at    timestamptz,
  first_seen_at   timestamptz NOT NULL DEFAULT now(),
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT devices_public_key_unique UNIQUE (public_key),
  CONSTRAINT devices_user_fk
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX devices_tenant_id_id_key ON devices (tenant_id, id);

-- 活跃设备计数的驱动索引（XBD-008 按套餐限制活跃设备数）
CREATE INDEX idx_devices_active ON devices (tenant_id, user_id)
  WHERE status = 'active';

CREATE TRIGGER trg_devices_updated_at BEFORE UPDATE ON devices
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('devices');
-- +goose StatementEnd

-- +goose StatementBegin
-- CLI-007 设备授权：浏览器出授权码/二维码，客户端凭码换取设备身份。
CREATE TABLE device_authorizations (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- 用户在浏览器里看到并输入的短码，库中只存哈希
  user_code_hash  bytea NOT NULL,
  device_code_hash bytea NOT NULL,
  device_public_key bytea NOT NULL,
  platform        text NOT NULL,
  -- 授权成功后回填
  user_id         uuid,
  device_id       uuid REFERENCES devices(id) ON DELETE SET NULL,
  status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'approved', 'denied', 'expired', 'consumed')),
  -- 轮询限流：客户端过快轮询会被要求退避
  poll_interval_seconds smallint NOT NULL DEFAULT 5,
  last_polled_at  timestamptz,
  expires_at      timestamptz NOT NULL,
  approved_at     timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT device_authorizations_user_code_unique UNIQUE (user_code_hash),
  CONSTRAINT device_authorizations_device_code_unique UNIQUE (device_code_hash)
);

CREATE INDEX idx_device_authorizations_expiry ON device_authorizations (expires_at)
  WHERE status = 'pending';

SELECT app.enable_tenant_rls('device_authorizations');
-- +goose StatementEnd

-- +goose StatementBegin
-- CLI-009 远程注销：设备令牌与设备身份分开吊销，离线设备下次联网即失效。
CREATE TABLE device_tokens (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  device_id     uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  user_id       uuid NOT NULL,
  token_hash    bytea NOT NULL,
  kind          text NOT NULL DEFAULT 'refresh' CHECK (kind IN ('refresh', 'config')),
  status        text NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active', 'rotated', 'revoked')),
  replaced_by   uuid REFERENCES device_tokens(id) ON DELETE SET NULL,
  expires_at    timestamptz NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT device_tokens_hash_unique UNIQUE (token_hash)
);

CREATE INDEX idx_device_tokens_device ON device_tokens (device_id) WHERE status = 'active';

SELECT app.enable_tenant_rls('device_tokens');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 订阅凭据（XBD-002 / XBD-003 / XBD-005）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE subscription_credentials (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  subscription_id uuid NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  user_id         uuid NOT NULL,
  -- 凭据本身：高熵随机串，库里只有哈希。「难猜 URL」只是附带效果，
  -- 真正的控制点是下面的 scope / 限流 / 吊销三件套（XBD-002 验收）。
  token_hash      bytea NOT NULL,
  token_prefix    text NOT NULL,
  -- 作用域：整个订阅，还是绑定到某台设备
  scope           text NOT NULL DEFAULT 'subscription'
                    CHECK (scope IN ('subscription', 'device')),
  device_id       uuid REFERENCES devices(id) ON DELETE CASCADE,
  -- XBD-005 配置访问控制：频率与风险阈值
  rate_limit_per_hour int NOT NULL DEFAULT 60 CHECK (rate_limit_per_hour > 0),
  fetch_count     bigint NOT NULL DEFAULT 0,
  last_fetched_at timestamptz,
  last_fetch_ip_hash bytea,
  status          text NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'grace', 'revoked', 'expired')),
  -- XBD-003 订阅重置：旧凭据立即或在短宽限期内失效
  revoked_at      timestamptz,
  revoked_reason  text,
  grace_until     timestamptz,
  expires_at      timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT subscription_credentials_hash_unique UNIQUE (token_hash),
  CONSTRAINT subscription_credentials_device_scope
    CHECK ((scope = 'device') = (device_id IS NOT NULL))
);

CREATE INDEX idx_subscription_credentials_sub ON subscription_credentials (subscription_id)
  WHERE status IN ('active', 'grace');

SELECT app.enable_tenant_rls('subscription_credentials');

COMMENT ON TABLE subscription_credentials IS
  'XBD-002 验收「不能仅依赖难猜 URL；凭据可独立失效」：每条凭据是独立实体，可单独吊销、限流、绑设备。';
-- +goose StatementEnd

-- +goose StatementBegin
-- XBD-005「异常高频获取会限速、验证或吊销」的证据表，追加写。
CREATE TABLE credential_access_log (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  credential_id   uuid NOT NULL REFERENCES subscription_credentials(id) ON DELETE CASCADE,
  ip_hash         bytea,
  ip_asn          int,
  user_agent_hash bytea,
  client_kind     text,
  outcome         text NOT NULL
                    CHECK (outcome IN ('served', 'rate_limited', 'challenged', 'denied', 'revoked')),
  config_bundle_id uuid,
  occurred_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_credential_access_cred ON credential_access_log (credential_id, occurred_at DESC);

SELECT app.enable_tenant_rls('credential_access_log');
SELECT app.make_append_only('credential_access_log');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 配置包（CLI-003 Config Adapter / CLI-004 签名 / CLI-005 版本与缓存）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE config_bundles (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  subscription_id uuid NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  credential_id   uuid REFERENCES subscription_credentials(id) ON DELETE SET NULL,
  device_id       uuid REFERENCES devices(id) ON DELETE SET NULL,
  -- 由哪个 Config Adapter 渲染（CLI-003 supports/validate/render/sign/sanitize）
  adapter         text NOT NULL,
  schema_version  text NOT NULL,
  version         int NOT NULL,
  -- 内容与签名（CLI-004：版本、有效期、客户端约束、哈希、签名）
  payload         bytea NOT NULL,
  content_hash    bytea NOT NULL,
  signature       bytea NOT NULL,
  signing_key_id  text NOT NULL,
  -- CLI-005：ETag 即 content_hash 的十六进制，支持条件请求与增量
  etag            text NOT NULL,
  -- 客户端约束：低于此版本的客户端拒绝使用本配置
  min_client_version text,
  -- CLI-002 适配决策的可解释输入快照：套餐、权益、地区、资源健康、灰度、风险
  decision_input  jsonb NOT NULL DEFAULT '{}'::jsonb,
  policy_version  text,
  -- 包含的节点集合，退役节点变更时可精确定位受影响配置
  node_ids        uuid[] NOT NULL DEFAULT '{}',
  status          text NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'superseded', 'revoked')),
  issued_at       timestamptz NOT NULL DEFAULT now(),
  expires_at      timestamptz NOT NULL,

  CONSTRAINT config_bundles_version_unique UNIQUE (subscription_id, adapter, version),
  CONSTRAINT config_bundles_expiry_after_issue CHECK (expires_at > issued_at)
);

CREATE INDEX idx_config_bundles_active ON config_bundles (subscription_id, adapter)
  WHERE status = 'active';
CREATE INDEX idx_config_bundles_etag ON config_bundles (tenant_id, etag);

SELECT app.enable_tenant_rls('config_bundles');

COMMENT ON TABLE config_bundles IS
  'CLI-002 验收「相同输入和策略版本得到可解释结果」：decision_input + policy_version 完整记录了这次适配的依据。';
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 客户端发布（CLI-006 下载中心 / CLI-010 安全更新）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE client_releases (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  platform        text NOT NULL CHECK (platform IN ('windows', 'macos', 'linux', 'android', 'ios')),
  arch            text NOT NULL DEFAULT 'x86_64',
  channel         text NOT NULL CHECK (channel IN ('stable', 'beta', 'internal')),
  version         text NOT NULL,
  -- CLI-010 版本单调：同平台同通道内序号必须递增，杜绝降级攻击
  version_ordinal bigint NOT NULL,
  -- 制品与校验
  artifact_url    text NOT NULL,
  artifact_size   bigint NOT NULL CHECK (artifact_size > 0),
  sha256          bytea NOT NULL,
  signature       bytea NOT NULL,
  signing_key_id  text NOT NULL,
  release_notes   text,
  min_os_version  text,
  -- 灰度与禁用（CLI-010「灰度和禁用列表」）
  rollout_percent smallint NOT NULL DEFAULT 0 CHECK (rollout_percent BETWEEN 0 AND 100),
  blocked         boolean NOT NULL DEFAULT false,
  blocked_reason  text,
  -- NFR-009：低于此版本的客户端收到强制升级提示
  is_min_supported boolean NOT NULL DEFAULT false,
  published_at    timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT client_releases_unique UNIQUE (tenant_id, platform, arch, channel, version),
  CONSTRAINT client_releases_ordinal_unique UNIQUE (tenant_id, platform, arch, channel, version_ordinal),
  CONSTRAINT client_releases_blocked_needs_reason
    CHECK (NOT blocked OR blocked_reason IS NOT NULL)
);

CREATE INDEX idx_client_releases_lookup
  ON client_releases (tenant_id, platform, arch, channel, version_ordinal DESC)
  WHERE published_at IS NOT NULL AND NOT blocked;

SELECT app.enable_tenant_rls('client_releases');

COMMENT ON TABLE client_releases IS
  'CLI-010 验收「篡改更新包或回滚到禁用版本失败」：sha256+signature 挡篡改，version_ordinal 单调 + blocked 挡降级。';
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS client_releases;
DROP TABLE IF EXISTS config_bundles;
DROP TABLE IF EXISTS credential_access_log;
DROP TABLE IF EXISTS subscription_credentials;
DROP TABLE IF EXISTS device_tokens;
DROP TABLE IF EXISTS device_authorizations;
DROP TABLE IF EXISTS devices;
-- +goose StatementEnd
