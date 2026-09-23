-- 安全域与扩展域：risk_events / approval_requests / audit_events / security_alerts /
--                  webhook_deliveries / plugins / system_settings
--
-- 对应 PRD 第 13、14 章：SEC-012/013/015/016，EXT-004..008，XBD-023。
--
-- 三条硬不变量：
--   1. SEC-012「普通管理员无删除或覆盖审计记录权限」→ audit_events 追加写 + 哈希链。
--   2. SEC-013「申请人不能审批自己」→ 触发器跨表比对，数据库层拒绝。
--   3. EXT-004「重放事件被拒绝」→ webhook 投递带 nonce 唯一约束。

-- +goose Up

--------------------------------------------------------------------------------
-- 审批（SEC-013 / PAY-010）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE approval_requests (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- 动作类型：refund / balance_adjustment / sensitive_export / permission_change /
  --          bulk_node_operation / node_provision / withdrawal / plan_migration
  action_type     text NOT NULL,
  resource_type   text,
  resource_id     uuid,
  -- 申请内容与影响预览（SUB-009「迁移前展示受影响用户、权益和金额」）
  request_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  impact_preview  jsonb NOT NULL DEFAULT '{}'::jsonb,
  reason          text NOT NULL,
  requested_by    uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  -- 需要几个人批准；双人审批即 2
  required_approvals smallint NOT NULL DEFAULT 1 CHECK (required_approvals >= 1),
  approvals_count smallint NOT NULL DEFAULT 0 CHECK (approvals_count >= 0),
  status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'approved', 'rejected', 'expired', 'cancelled', 'executed')),
  -- SEC-013「超时可升级」
  escalate_after  timestamptz,
  escalated_at    timestamptz,
  expires_at      timestamptz,
  decided_at      timestamptz,
  executed_at     timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT approval_requests_reason_meaningful CHECK (length(reason) >= 5),
  CONSTRAINT approval_requests_approvals_bounded CHECK (approvals_count <= required_approvals)
);

CREATE INDEX idx_approval_requests_pending ON approval_requests (tenant_id, status, created_at)
  WHERE status = 'pending';
CREATE INDEX idx_approval_requests_escalate ON approval_requests (escalate_after)
  WHERE status = 'pending' AND escalate_after IS NOT NULL;

CREATE TRIGGER trg_approval_requests_updated_at BEFORE UPDATE ON approval_requests
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('approval_requests');
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE approval_decisions (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  request_id    uuid NOT NULL REFERENCES approval_requests(id) ON DELETE CASCADE,
  decided_by    uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  decision      text NOT NULL CHECK (decision IN ('approve', 'reject')),
  comment       text,
  decided_at    timestamptz NOT NULL DEFAULT now(),

  -- 同一个人对同一申请只能表态一次
  CONSTRAINT approval_decisions_one_per_person UNIQUE (request_id, decided_by)
);

SELECT app.enable_tenant_rls('approval_decisions');
SELECT app.make_append_only('approval_decisions');
-- +goose StatementEnd

-- +goose StatementBegin
-- SEC-013 验收「申请人不能审批自己」：跨表校验，写在数据库里，
-- 任何绕过应用层的直连操作也逃不掉。
CREATE OR REPLACE FUNCTION app.guard_self_approval() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  v_requester uuid;
BEGIN
  SELECT requested_by INTO v_requester
  FROM approval_requests WHERE id = NEW.request_id;

  IF v_requester = NEW.decided_by THEN
    RAISE EXCEPTION
      '用户 % 不能审批自己发起的申请 %（SEC-013）',
      NEW.decided_by, NEW.request_id
      USING ERRCODE = 'insufficient_privilege';
  END IF;

  RETURN NEW;
END $$;

CREATE TRIGGER trg_approval_decisions_no_self BEFORE INSERT ON approval_decisions
  FOR EACH ROW EXECUTE FUNCTION app.guard_self_approval();
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 审计（SEC-012）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE audit_events (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- SEC-012 要求的完整要素：主体、动作、对象、前后摘要、请求、审批、来源、结果
  actor_kind    text NOT NULL
                  CHECK (actor_kind IN ('user', 'admin', 'system', 'agent', 'plugin', 'anonymous')),
  actor_id      uuid,
  actor_label   text,
  action        text NOT NULL,
  resource_type text,
  resource_id   uuid,
  -- 变更前后的摘要而非全量，避免审计表成为二次泄漏源（SEC-011）
  before_digest jsonb,
  after_digest  jsonb,
  request_id    text,
  api_domain    text CHECK (api_domain IS NULL OR api_domain IN ('public', 'admin', 'client', 'node')),
  source_ip_hash bytea,
  source_asn    int,
  user_agent    text,
  approval_request_id uuid REFERENCES approval_requests(id) ON DELETE SET NULL,
  outcome       text NOT NULL DEFAULT 'success'
                  CHECK (outcome IN ('success', 'failure', 'denied', 'partial')),
  error_code    text,
  -- 哈希链：每条记录摘要含上一条的哈希，篡改中间任意一条都会断链。
  -- 这让「不可删」从权限约束升级为可数学验证的性质。
  prev_hash     bytea,
  entry_hash    bytea,
  occurred_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_events_actor ON audit_events (tenant_id, actor_id, occurred_at DESC);
CREATE INDEX idx_audit_events_resource ON audit_events (tenant_id, resource_type, resource_id, occurred_at DESC);
CREATE INDEX idx_audit_events_action ON audit_events (tenant_id, action, occurred_at DESC);
CREATE INDEX idx_audit_events_time ON audit_events (occurred_at DESC);

SELECT app.enable_tenant_rls('audit_events');
SELECT app.make_append_only('audit_events');

COMMENT ON TABLE audit_events IS
  'SEC-012 验收「普通管理员无删除或覆盖审计记录权限」：追加写触发器 + 哈希链双重保证。';
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 风险与安全事件（SEC-015 / SEC-016）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE risk_events (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  subject_kind  text NOT NULL
                  CHECK (subject_kind IN ('user', 'device', 'ip', 'order', 'payment',
                                          'referral', 'node', 'credential', 'session')),
  subject_id    uuid,
  subject_key   text,
  -- 触发的规则与得分
  rule_code     text NOT NULL,
  score         smallint NOT NULL DEFAULT 0,
  -- SEC-015 验收「每次限制或冻结均能说明命中原因」
  reasons       jsonb NOT NULL DEFAULT '[]'::jsonb,
  signals       jsonb NOT NULL DEFAULT '{}'::jsonb,
  -- 处置动作
  action        text NOT NULL DEFAULT 'observe'
                  CHECK (action IN ('observe', 'challenge', 'rate_limit', 'require_mfa',
                                    'hold_for_review', 'suspend', 'block')),
  -- 复核工作流
  review_status text NOT NULL DEFAULT 'none'
                  CHECK (review_status IN ('none', 'pending', 'confirmed', 'false_positive')),
  reviewed_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  reviewed_at   timestamptz,
  occurred_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_risk_events_subject ON risk_events (tenant_id, subject_kind, subject_id, occurred_at DESC);
CREATE INDEX idx_risk_events_review ON risk_events (tenant_id, review_status)
  WHERE review_status = 'pending';

SELECT app.enable_tenant_rls('risk_events');
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE security_alerts (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  severity      text NOT NULL CHECK (severity IN ('low', 'medium', 'high', 'critical')),
  category      text NOT NULL,
  title         text NOT NULL,
  detail        jsonb NOT NULL DEFAULT '{}'::jsonb,
  -- SEC-016 响应流程状态
  status        text NOT NULL DEFAULT 'open'
                  CHECK (status IN ('open', 'investigating', 'contained', 'eradicated',
                                    'recovered', 'closed', 'false_positive')),
  -- 处置动作记录：隔离、身份吊销、密钥轮换、证据保全
  actions_taken jsonb NOT NULL DEFAULT '[]'::jsonb,
  affected_resources jsonb NOT NULL DEFAULT '[]'::jsonb,
  assigned_to   uuid REFERENCES users(id) ON DELETE SET NULL,
  postmortem_url text,
  detected_at   timestamptz NOT NULL DEFAULT now(),
  contained_at  timestamptz,
  closed_at     timestamptz
);

CREATE INDEX idx_security_alerts_open ON security_alerts (tenant_id, severity, detected_at DESC)
  WHERE status NOT IN ('closed', 'false_positive');

SELECT app.enable_tenant_rls('security_alerts');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- Webhook（EXT-004）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE webhook_endpoints (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  url           text NOT NULL,
  -- HMAC 签名密钥，信封加密
  secret_encrypted bytea NOT NULL,
  key_version   int NOT NULL DEFAULT 1,
  -- 订阅的版本化事件（EXT-003：subscription.activated.v1）
  event_types   text[] NOT NULL DEFAULT '{}',
  enabled       boolean NOT NULL DEFAULT true,
  -- EXT-007「故障插件可熔断且不拖垮核心服务」
  circuit_state text NOT NULL DEFAULT 'closed'
                  CHECK (circuit_state IN ('closed', 'open', 'half_open')),
  consecutive_failures int NOT NULL DEFAULT 0,
  circuit_opened_at timestamptz,
  description   text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_webhook_endpoints_updated_at BEFORE UPDATE ON webhook_endpoints
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('webhook_endpoints');
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE webhook_deliveries (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  endpoint_id   uuid NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
  event_type    text NOT NULL,
  event_id      uuid NOT NULL,
  payload       jsonb NOT NULL,
  -- EXT-004 防重放三件套：签名、时间戳、Nonce
  signature     bytea NOT NULL,
  timestamp_sent timestamptz NOT NULL,
  nonce         bytea NOT NULL,
  status        text NOT NULL DEFAULT 'queued'
                  CHECK (status IN ('queued', 'sending', 'delivered', 'failed', 'dead_letter')),
  attempts      smallint NOT NULL DEFAULT 0,
  max_attempts  smallint NOT NULL DEFAULT 8,
  next_retry_at timestamptz,
  response_code int,
  response_body text,
  error_message text,
  delivered_at  timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),

  -- 同一事件对同一端点只投一次（幂等）
  CONSTRAINT webhook_deliveries_event_endpoint_unique UNIQUE (endpoint_id, event_id),
  CONSTRAINT webhook_deliveries_nonce_unique UNIQUE (endpoint_id, nonce)
);

CREATE INDEX idx_webhook_deliveries_queue ON webhook_deliveries (next_retry_at)
  WHERE status IN ('queued', 'failed');
CREATE INDEX idx_webhook_deliveries_dlq ON webhook_deliveries (tenant_id, created_at DESC)
  WHERE status = 'dead_letter';

SELECT app.enable_tenant_rls('webhook_deliveries');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 插件（EXT-005 / EXT-006 / EXT-007）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE plugins (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- EXT-005 插件清单声明的内容
  plugin_key    text NOT NULL,
  version       text NOT NULL,
  kind          text NOT NULL
                  CHECK (kind IN ('payment', 'notification', 'identity', 'cloud_provider',
                                  'runtime', 'config', 'tax', 'risk', 'report')),
  display_name  text NOT NULL,
  -- 兼容的平台 API 版本范围
  api_min_version text NOT NULL,
  api_max_version text,
  -- 权限声明：安装前明确展示（EXT-005 验收）
  declared_permissions text[] NOT NULL DEFAULT '{}',
  -- SEC-007 出站白名单：插件只能访问这些域名
  declared_egress_domains text[] NOT NULL DEFAULT '{}',
  subscribed_events text[] NOT NULL DEFAULT '{}',
  config_schema jsonb NOT NULL DEFAULT '{}'::jsonb,
  -- SEC-014 供应链：未签名制品不能发布
  artifact_url  text,
  artifact_sha256 bytea,
  signature     bytea,
  signing_key_id text,
  signature_verified boolean NOT NULL DEFAULT false,
  -- EXT-006 隔离形态
  isolation     text NOT NULL DEFAULT 'container'
                  CHECK (isolation IN ('container', 'wasm', 'in_process_trusted')),
  status        text NOT NULL DEFAULT 'registered'
                  CHECK (status IN ('registered', 'installed', 'enabled', 'disabled',
                                    'upgrading', 'failed', 'uninstalled')),
  installed_at  timestamptz,
  enabled_at    timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT plugins_key_version_unique UNIQUE (tenant_id, plugin_key, version),
  -- EXT-006「插件没有主数据库超级权限」的形式化前提：
  -- 只有明确标记为可信的内置插件才允许进程内运行
  CONSTRAINT plugins_third_party_must_isolate
    CHECK (isolation <> 'in_process_trusted' OR signature_verified),
  -- SEC-014：启用前必须验签
  CONSTRAINT plugins_enabled_requires_signature
    CHECK (status <> 'enabled' OR signature_verified)
);

CREATE INDEX idx_plugins_enabled ON plugins (tenant_id, kind) WHERE status = 'enabled';

CREATE TRIGGER trg_plugins_updated_at BEFORE UPDATE ON plugins
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('plugins');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 系统参数（XBD-023）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE system_settings (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  key           text NOT NULL,
  -- 值按 Schema 校验后写入；无效配置不能发布（XBD-023 验收）
  value         jsonb NOT NULL,
  value_schema  jsonb,
  -- 敏感值只显示掩码（XBD-023 验收）；真值走信封加密
  is_secret     boolean NOT NULL DEFAULT false,
  secret_encrypted bytea,
  key_version   int,
  version       int NOT NULL DEFAULT 1,
  -- NFR-006：配置变更需审批与灰度
  approval_request_id uuid REFERENCES approval_requests(id) ON DELETE SET NULL,
  updated_by    uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT system_settings_key_unique UNIQUE (tenant_id, key),
  -- 密文与明文互斥：标记为 secret 的项，value 里只能放掩码
  CONSTRAINT system_settings_secret_storage
    CHECK (NOT is_secret OR secret_encrypted IS NOT NULL)
);

CREATE TRIGGER trg_system_settings_updated_at BEFORE UPDATE ON system_settings
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('system_settings');
-- +goose StatementEnd

-- +goose StatementBegin
-- 系统参数历史版本，追加写，支持 NFR-006 的差异与回滚。
CREATE TABLE system_setting_revisions (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  setting_id    uuid NOT NULL REFERENCES system_settings(id) ON DELETE CASCADE,
  key           text NOT NULL,
  version       int NOT NULL,
  value_digest  jsonb NOT NULL,
  changed_by    uuid REFERENCES users(id) ON DELETE SET NULL,
  change_reason text,
  changed_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT system_setting_revisions_unique UNIQUE (setting_id, version)
);

SELECT app.enable_tenant_rls('system_setting_revisions');
SELECT app.make_append_only('system_setting_revisions');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- NFR-008 业务降级开关
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE feature_switches (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code          text NOT NULL,
  enabled       boolean NOT NULL DEFAULT true,
  -- NFR-008：攻击或故障时暂停注册、大导出和报表，但登录/续费/配置同步必须保留。
  -- essential 项不允许被关闭，由 CHECK 保证误操作也关不掉。
  essential     boolean NOT NULL DEFAULT false,
  reason        text,
  changed_by    uuid REFERENCES users(id) ON DELETE SET NULL,
  auto_restore_at timestamptz,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT feature_switches_code_unique UNIQUE (tenant_id, code),
  CONSTRAINT feature_switches_essential_stays_on CHECK (NOT essential OR enabled),
  CONSTRAINT feature_switches_disable_needs_reason CHECK (enabled OR reason IS NOT NULL)
);

CREATE TRIGGER trg_feature_switches_updated_at BEFORE UPDATE ON feature_switches
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('feature_switches');

COMMENT ON TABLE feature_switches IS
  'NFR-008 验收「降级开关有审计且可快速恢复」：essential 位保证登录/续费/配置同步永不被降级关掉。';
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS feature_switches;
DROP TABLE IF EXISTS system_setting_revisions;
DROP TABLE IF EXISTS system_settings;
DROP TABLE IF EXISTS plugins;
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhook_endpoints;
DROP TABLE IF EXISTS security_alerts;
DROP TABLE IF EXISTS risk_events;
DROP TABLE IF EXISTS audit_events;
DROP TRIGGER IF EXISTS trg_approval_decisions_no_self ON approval_decisions;
DROP FUNCTION IF EXISTS app.guard_self_approval();
DROP TABLE IF EXISTS approval_decisions;
DROP TABLE IF EXISTS approval_requests;
-- +goose StatementEnd
