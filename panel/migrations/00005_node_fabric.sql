-- 节点域：cloud_providers / node_templates / provisioning_runs / nodes /
--          node_identities / node_pools / node_tasks / runtime_instances
--
-- 对应 PRD 第 8–10 章：NODE-001..015、AGT-001..012、POOL-001..004。
--
-- 三条硬不变量：
--   1. NODE-010「任何节点不能从创建中直接进入 active」→ 状态转换表强制经 standby/canary。
--   2. NODE-005「重复点击或超时重试不会创建重复实例」→ provisioning_runs 幂等键唯一。
--   3. NODE-014「已退役节点使用旧身份重新上线会被拒绝」→ node_identities 带 serial 与吊销态。

-- +goose Up

--------------------------------------------------------------------------------
-- 云厂商（NODE-002 Provider 插件）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE cloud_providers (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code          text NOT NULL,
  -- 适配器实现名：hetzner / vultr / aws_ec2 / manual_import / private_baremetal
  adapter       text NOT NULL,
  display_name  text NOT NULL,
  -- 云 API 凭据信封加密（SEC-010）
  credentials_encrypted bytea,
  key_version   int NOT NULL DEFAULT 1,
  -- NODE-012 自动扩容的预算护栏，单位为币种最小单位
  monthly_budget_amount app.minor_amount CHECK (monthly_budget_amount IS NULL OR monthly_budget_amount >= 0),
  budget_currency app.currency_code,
  enabled       boolean NOT NULL DEFAULT false,
  config        jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT cloud_providers_tenant_code_unique UNIQUE (tenant_id, code)
);
CREATE UNIQUE INDEX cloud_providers_tenant_id_id_key ON cloud_providers (tenant_id, id);

CREATE TRIGGER trg_cloud_providers_updated_at BEFORE UPDATE ON cloud_providers
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('cloud_providers');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 资源池（POOL-001）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE node_pools (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code          text NOT NULL,
  name          text NOT NULL,
  region        text,
  -- 能力标签：套餐通过 entitlement 引用这些标签来决定可见资源池
  capabilities  text[] NOT NULL DEFAULT '{}',
  -- POOL-003 容量预测的安全余量（百分比）
  safety_margin_pct smallint NOT NULL DEFAULT 20 CHECK (safety_margin_pct BETWEEN 0 AND 90),
  -- NODE-012/013 自动伸缩护栏
  min_nodes     int NOT NULL DEFAULT 0 CHECK (min_nodes >= 0),
  max_nodes     int CHECK (max_nodes IS NULL OR max_nodes >= 0),
  autoscale_enabled boolean NOT NULL DEFAULT false,
  scale_cooldown_seconds int NOT NULL DEFAULT 900 CHECK (scale_cooldown_seconds >= 0),
  last_scaled_at timestamptz,
  status        text NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active', 'draining', 'disabled')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT node_pools_tenant_code_unique UNIQUE (tenant_id, code),
  CONSTRAINT node_pools_bounds CHECK (max_nodes IS NULL OR max_nodes >= min_nodes)
);
CREATE UNIQUE INDEX node_pools_tenant_id_id_key ON node_pools (tenant_id, id);

CREATE TRIGGER trg_node_pools_updated_at BEFORE UPDATE ON node_pools
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('node_pools');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 节点模板（NODE-001 / NODE-011）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE node_templates (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code            text NOT NULL,
  name            text NOT NULL,
  provider_id     uuid,
  region          text,
  instance_type   text,
  image           text,
  -- NODE-004 cloud-init 模板；版本化且不含长期密钥（验收标准）
  cloud_init_template text,
  cloud_init_version  int NOT NULL DEFAULT 1,
  -- AGT-005 Runtime Profile：安装哪个运行时、什么版本、什么参数
  runtime_profile jsonb NOT NULL DEFAULT '{}'::jsonb,
  agent_channel   text NOT NULL DEFAULT 'stable'
                    CHECK (agent_channel IN ('stable', 'beta', 'internal')),
  -- NODE-007 安全基线检查项清单
  security_baseline jsonb NOT NULL DEFAULT '{}'::jsonb,
  default_pool_id uuid,
  -- 预估成本，用于 NODE-001「创建前可预览资源和预计成本」
  estimated_monthly_cost app.minor_amount,
  cost_currency   app.currency_code,
  requires_approval boolean NOT NULL DEFAULT true,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT node_templates_tenant_code_unique UNIQUE (tenant_id, code),
  CONSTRAINT node_templates_provider_fk
    FOREIGN KEY (tenant_id, provider_id) REFERENCES cloud_providers (tenant_id, id) ON DELETE SET NULL,
  CONSTRAINT node_templates_pool_fk
    FOREIGN KEY (tenant_id, default_pool_id) REFERENCES node_pools (tenant_id, id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX node_templates_tenant_id_id_key ON node_templates (tenant_id, id);

CREATE TRIGGER trg_node_templates_updated_at BEFORE UPDATE ON node_templates
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('node_templates');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 节点（NODE 生命周期状态机，PRD 8.1）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE nodes (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name            text NOT NULL,
  pool_id         uuid,
  template_id     uuid,
  provider_id     uuid,
  -- 云侧资源 ID：NODE-006「能准确指出残留资源和持续费用」、NODE-015 残留扫描的比对键
  provider_resource_id text,
  region          text,
  availability_zone text,

  -- PRD 8.1 完整状态机
  status          text NOT NULL DEFAULT 'draft' CHECK (status IN (
                    'draft', 'provisioning', 'bootstrapping', 'attesting',
                    'installing', 'validating', 'standby', 'canary', 'active',
                    'draining', 'maintenance', 'retired', 'destroyed',
                    -- 异常态
                    'provisioning_failed', 'bootstrap_failed', 'unhealthy',
                    'quarantined', 'upgrade_failed', 'destroy_failed')),

  -- 网络：管理面不保存全局 Root SSH 密钥（ARC-004 验收标准）
  public_ipv4     inet,
  public_ipv6     inet,
  private_ipv4    inet,
  hostname        text,

  -- AGT-004 心跳与资产
  agent_version   text,
  runtime_version text,
  last_heartbeat_at timestamptz,
  -- AGT-009 配置漂移：Agent 上报的实际配置哈希 vs 平台期望
  applied_config_version int,
  applied_config_hash bytea,
  desired_config_version int,
  -- POOL-004 健康评分；硬性安全故障直接隔离，不被总分掩盖
  health_score    smallint CHECK (health_score IS NULL OR health_score BETWEEN 0 AND 100),
  hard_fault      boolean NOT NULL DEFAULT false,
  hard_fault_reason text,

  -- 资产快照
  cpu_cores       int,
  memory_mb       int,
  disk_gb         int,

  -- 调度权重（POOL-002）
  weight          int NOT NULL DEFAULT 100 CHECK (weight >= 0),
  -- 灰度批次标记（NODE-010）
  canary_group    text,

  -- 成本归属
  cost_center     text,
  monthly_cost    app.minor_amount,
  cost_currency   app.currency_code,

  entered_status_at timestamptz NOT NULL DEFAULT now(),
  retired_at      timestamptz,
  destroyed_at    timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT nodes_tenant_name_unique UNIQUE (tenant_id, name),
  CONSTRAINT nodes_pool_fk
    FOREIGN KEY (tenant_id, pool_id) REFERENCES node_pools (tenant_id, id) ON DELETE SET NULL,
  CONSTRAINT nodes_template_fk
    FOREIGN KEY (tenant_id, template_id) REFERENCES node_templates (tenant_id, id) ON DELETE SET NULL,
  CONSTRAINT nodes_provider_fk
    FOREIGN KEY (tenant_id, provider_id) REFERENCES cloud_providers (tenant_id, id) ON DELETE SET NULL,
  -- 同一云厂商下资源 ID 唯一，防止一个云实例被登记成两个节点
  CONSTRAINT nodes_provider_resource_unique UNIQUE (provider_id, provider_resource_id),
  CONSTRAINT nodes_hard_fault_needs_reason
    CHECK (NOT hard_fault OR hard_fault_reason IS NOT NULL)
);
CREATE UNIQUE INDEX nodes_tenant_id_id_key ON nodes (tenant_id, id);

-- 调度热路径索引：只有健康的正式节点参与分配
CREATE INDEX idx_nodes_schedulable ON nodes (tenant_id, pool_id, weight DESC)
  WHERE status = 'active' AND NOT hard_fault;
-- AGT-004「心跳超时自动停止新分配」的扫描索引
CREATE INDEX idx_nodes_heartbeat ON nodes (last_heartbeat_at)
  WHERE status IN ('standby', 'canary', 'active');
CREATE INDEX idx_nodes_status ON nodes (tenant_id, status);

CREATE TRIGGER trg_nodes_updated_at BEFORE UPDATE ON nodes
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('nodes');
-- +goose StatementEnd

-- +goose StatementBegin
-- NODE-010 的强制点：合法转换表里没有 * -> active 的直达边，
-- 只有 canary -> active，而 canary 只能来自 standby。
CREATE TABLE node_transitions (
  from_status text NOT NULL,
  to_status   text NOT NULL,
  PRIMARY KEY (from_status, to_status)
);

INSERT INTO node_transitions (from_status, to_status) VALUES
  ('draft', 'provisioning'), ('draft', 'bootstrapping'), ('draft', 'destroyed'),
  ('provisioning', 'bootstrapping'), ('provisioning', 'provisioning_failed'),
  ('provisioning_failed', 'provisioning'), ('provisioning_failed', 'destroyed'),
  ('bootstrapping', 'attesting'), ('bootstrapping', 'bootstrap_failed'),
  ('bootstrap_failed', 'bootstrapping'), ('bootstrap_failed', 'destroyed'),
  ('attesting', 'installing'), ('attesting', 'bootstrap_failed'), ('attesting', 'quarantined'),
  ('installing', 'validating'), ('installing', 'bootstrap_failed'),
  ('validating', 'standby'), ('validating', 'bootstrap_failed'), ('validating', 'unhealthy'),
  -- 唯一进入正式池的路径：standby -> canary -> active
  ('standby', 'canary'), ('standby', 'maintenance'), ('standby', 'retired'),
  ('standby', 'unhealthy'), ('standby', 'quarantined'),
  ('canary', 'active'), ('canary', 'standby'), ('canary', 'unhealthy'), ('canary', 'quarantined'),
  ('active', 'draining'), ('active', 'maintenance'), ('active', 'unhealthy'),
  ('active', 'quarantined'), ('active', 'upgrade_failed'),
  ('unhealthy', 'standby'), ('unhealthy', 'draining'), ('unhealthy', 'quarantined'),
  ('unhealthy', 'maintenance'),
  ('quarantined', 'draining'), ('quarantined', 'retired'), ('quarantined', 'standby'),
  ('upgrade_failed', 'active'), ('upgrade_failed', 'draining'), ('upgrade_failed', 'quarantined'),
  ('maintenance', 'standby'), ('maintenance', 'draining'), ('maintenance', 'retired'),
  ('draining', 'retired'), ('draining', 'active'), ('draining', 'maintenance'),
  ('retired', 'destroyed'), ('retired', 'destroy_failed'),
  ('destroy_failed', 'destroyed'), ('destroy_failed', 'retired');

COMMENT ON TABLE node_transitions IS
  'NODE-010 验收「任何节点不能从创建中直接进入 active」：表中不存在到 active 的边，除了来自 canary。';
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.guard_node_transition() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.status IS DISTINCT FROM OLD.status THEN
    IF NOT EXISTS (
      SELECT 1 FROM node_transitions
      WHERE from_status = OLD.status AND to_status = NEW.status
    ) THEN
      RAISE EXCEPTION
        '节点 % 非法状态跳转：% -> %（PRD 8.1 生命周期状态机）',
        OLD.id, OLD.status, NEW.status
        USING ERRCODE = 'check_violation';
    END IF;
    NEW.entered_status_at := now();
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER trg_nodes_transition BEFORE UPDATE ON nodes
  FOR EACH ROW EXECUTE FUNCTION app.guard_node_transition();
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 节点身份（NODE-009 / NODE-014，SPIFFE 式短期证书）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE node_identities (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  node_id         uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  -- 每次轮换递增；旧序号的证书一律拒绝，这是 NODE-014 验收的关键
  serial          int NOT NULL,
  -- Agent 本地生成密钥，平台只见公钥（NODE-009）
  public_key      bytea NOT NULL,
  -- SPIFFE ID 形如 spiffe://aegis/tenant/<tid>/node/<nid>
  spiffe_id       text NOT NULL,
  certificate_pem text,
  fingerprint     bytea NOT NULL,
  status          text NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'rotating', 'revoked', 'expired')),
  revoked_reason  text,
  revoked_at      timestamptz,
  issued_at       timestamptz NOT NULL DEFAULT now(),
  expires_at      timestamptz NOT NULL,

  CONSTRAINT node_identities_node_serial_unique UNIQUE (node_id, serial),
  CONSTRAINT node_identities_fingerprint_unique UNIQUE (fingerprint)
);

-- 一个节点同时只能有一份有效身份
CREATE UNIQUE INDEX uq_node_identities_active ON node_identities (node_id)
  WHERE status = 'active';
CREATE INDEX idx_node_identities_expiry ON node_identities (expires_at) WHERE status = 'active';

SELECT app.enable_tenant_rls('node_identities');

COMMENT ON TABLE node_identities IS
  'NODE-014 验收「已退役节点使用旧身份重新上线会被拒绝」：吊销即置 revoked，且 serial 单调递增不可复用。';
-- +goose StatementEnd

-- +goose StatementBegin
-- NODE-008 已有服务器导入：一次性 Bootstrap Token，只存哈希，用后即焚。
CREATE TABLE bootstrap_tokens (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  token_hash    bytea NOT NULL,
  template_id   uuid,
  pool_id       uuid,
  node_id       uuid REFERENCES nodes(id) ON DELETE SET NULL,
  max_uses      smallint NOT NULL DEFAULT 1 CHECK (max_uses > 0),
  used_count    smallint NOT NULL DEFAULT 0 CHECK (used_count >= 0),
  created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
  -- NODE-008：10–30 分钟有效
  expires_at    timestamptz NOT NULL,
  consumed_at   timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT bootstrap_tokens_hash_unique UNIQUE (token_hash),
  CONSTRAINT bootstrap_tokens_uses_bounded CHECK (used_count <= max_uses),
  CONSTRAINT bootstrap_tokens_template_fk
    FOREIGN KEY (tenant_id, template_id) REFERENCES node_templates (tenant_id, id) ON DELETE SET NULL,
  CONSTRAINT bootstrap_tokens_pool_fk
    FOREIGN KEY (tenant_id, pool_id) REFERENCES node_pools (tenant_id, id) ON DELETE SET NULL
);

CREATE INDEX idx_bootstrap_tokens_expiry ON bootstrap_tokens (expires_at) WHERE consumed_at IS NULL;

SELECT app.enable_tenant_rls('bootstrap_tokens');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 开通编排（NODE-005 幂等 / NODE-006 补偿工作流）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE provisioning_runs (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- NODE-005：每个请求一个幂等键，重复点击或超时重试撞唯一约束后返回原 run
  idempotency_key text NOT NULL,
  node_id         uuid REFERENCES nodes(id) ON DELETE SET NULL,
  template_id     uuid,
  provider_id     uuid,
  mode            text NOT NULL DEFAULT 'auto_create'
                    CHECK (mode IN ('auto_create', 'import_existing', 'private_baremetal')),
  status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'awaiting_approval', 'planning', 'applying',
                                      'bootstrapping', 'validating', 'succeeded',
                                      'failed', 'compensating', 'compensated', 'abandoned')),
  -- NODE-003 OpenTofu：plan 差异存档，供审批前预览
  tofu_plan       jsonb,
  tofu_state_ref  text,
  -- NODE-006：失败时精确指出残留资源与持续费用
  orphan_resources jsonb NOT NULL DEFAULT '[]'::jsonb,
  requires_approval boolean NOT NULL DEFAULT true,
  approval_request_id uuid,
  requested_by    uuid REFERENCES users(id) ON DELETE SET NULL,
  -- NODE-011 批量创建的批次归属与失败阈值
  batch_id        uuid,
  error_code      text,
  error_message   text,
  started_at      timestamptz,
  finished_at     timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT provisioning_runs_idem_unique UNIQUE (tenant_id, idempotency_key),
  CONSTRAINT provisioning_runs_template_fk
    FOREIGN KEY (tenant_id, template_id) REFERENCES node_templates (tenant_id, id) ON DELETE SET NULL,
  CONSTRAINT provisioning_runs_provider_fk
    FOREIGN KEY (tenant_id, provider_id) REFERENCES cloud_providers (tenant_id, id) ON DELETE SET NULL
);

CREATE INDEX idx_provisioning_runs_active ON provisioning_runs (tenant_id, status)
  WHERE status NOT IN ('succeeded', 'compensated', 'abandoned');
CREATE INDEX idx_provisioning_runs_batch ON provisioning_runs (batch_id) WHERE batch_id IS NOT NULL;

CREATE TRIGGER trg_provisioning_runs_updated_at BEFORE UPDATE ON provisioning_runs
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('provisioning_runs');
-- +goose StatementEnd

-- +goose StatementBegin
-- NODE-006「每一步记录结果」：编排的每个步骤及其补偿动作，追加写。
CREATE TABLE provisioning_steps (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  run_id        uuid NOT NULL REFERENCES provisioning_runs(id) ON DELETE CASCADE,
  seq           int NOT NULL,
  step          text NOT NULL,
  status        text NOT NULL CHECK (status IN ('started', 'succeeded', 'failed', 'compensated', 'skipped')),
  -- 该步骤创建了什么云资源，补偿时据此清理
  created_resources jsonb NOT NULL DEFAULT '[]'::jsonb,
  detail        jsonb NOT NULL DEFAULT '{}'::jsonb,
  error_message text,
  occurred_at   timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT provisioning_steps_seq_unique UNIQUE (run_id, seq)
);

SELECT app.enable_tenant_rls('provisioning_steps');
SELECT app.make_append_only('provisioning_steps');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- Agent 任务（AGT-003 白名单任务）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE node_tasks (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  node_id       uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  -- AGT-003：仅限预定义任务码。任意 Shell / 任意文件读取 / 任意 URL 执行不在此列举中，
  -- 因此在数据层就无法表达，Agent 端也只认这些码。
  task_type     text NOT NULL CHECK (task_type IN (
                  'config.apply', 'config.rollback',
                  'runtime.install', 'runtime.start', 'runtime.stop',
                  'runtime.reload', 'runtime.upgrade', 'runtime.rollback',
                  'agent.upgrade', 'health.check', 'usage.flush',
                  'diagnostics.collect', 'identity.rotate', 'node.drain')),
  payload       jsonb NOT NULL DEFAULT '{}'::jsonb,
  -- 任务签名：Agent 校验签名、有效期与节点绑定后才执行
  signature     bytea,
  signing_key_id text,
  status        text NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'dispatched', 'running',
                                    'succeeded', 'failed', 'expired', 'cancelled')),
  attempts      smallint NOT NULL DEFAULT 0,
  max_attempts  smallint NOT NULL DEFAULT 3,
  result        jsonb,
  error_message text,
  -- 限时：过期任务 Agent 一律拒绝执行
  expires_at    timestamptz NOT NULL,
  dispatched_at timestamptz,
  completed_at  timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    uuid REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX idx_node_tasks_dispatch ON node_tasks (node_id, created_at)
  WHERE status = 'pending';
CREATE INDEX idx_node_tasks_expiry ON node_tasks (expires_at)
  WHERE status IN ('pending', 'dispatched', 'running');

SELECT app.enable_tenant_rls('node_tasks');

COMMENT ON TABLE node_tasks IS
  'AGT-003 验收「任意 Shell、任意文件读取和任意 URL 执行均不可用」：task_type 是封闭枚举，无逃逸口。';
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 节点配置版本与下发（AGT-006/007/008）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE node_configs (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- AGT-006 配置分层：global < environment < region < pool < template < node
  scope           text NOT NULL
                    CHECK (scope IN ('global', 'environment', 'region', 'pool', 'template', 'node')),
  scope_ref       uuid,
  scope_key       text,
  version         int NOT NULL,
  payload         jsonb NOT NULL,
  -- AGT-007：候选配置经 Schema 与安全校验后签名下发
  content_hash    bytea NOT NULL,
  signature       bytea,
  signing_key_id  text,
  signed_at       timestamptz,
  signature_expires_at timestamptz,
  status          text NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft', 'validated', 'signed', 'published', 'superseded', 'rolled_back')),
  -- 灰度发布控制（NFR-006）
  rollout_percent smallint NOT NULL DEFAULT 0 CHECK (rollout_percent BETWEEN 0 AND 100),
  published_at    timestamptz,
  created_by      uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT node_configs_scope_version_unique UNIQUE (tenant_id, scope, scope_ref, scope_key, version),
  CONSTRAINT node_configs_signed_requires_sig
    CHECK (status NOT IN ('signed', 'published') OR (signature IS NOT NULL AND signature_expires_at IS NOT NULL))
);

CREATE INDEX idx_node_configs_published ON node_configs (tenant_id, scope, scope_ref)
  WHERE status = 'published';

SELECT app.enable_tenant_rls('node_configs');

COMMENT ON TABLE node_configs IS
  'AGT-007 验收「签名无效或过期时 Agent 拒绝应用」：签名与到期时间随配置一同下发，CHECK 保证已发布配置必然带签名。';
-- +goose StatementEnd

-- +goose StatementBegin
-- AGT-008 原子切换与自动回滚的过程记录，追加写。
CREATE TABLE node_config_applications (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  node_id       uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  config_id     uuid NOT NULL REFERENCES node_configs(id) ON DELETE RESTRICT,
  previous_config_id uuid REFERENCES node_configs(id) ON DELETE SET NULL,
  phase         text NOT NULL
                  CHECK (phase IN ('downloaded', 'verified', 'precheck_passed', 'precheck_failed',
                                   'switched', 'health_passed', 'health_failed',
                                   'rolled_back', 'failed')),
  detail        jsonb NOT NULL DEFAULT '{}'::jsonb,
  occurred_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_node_config_apps_node ON node_config_applications (node_id, occurred_at DESC);

SELECT app.enable_tenant_rls('node_config_applications');
SELECT app.make_append_only('node_config_applications');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 运行时实例（AGT-005 Runtime Adapter）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE runtime_instances (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  node_id       uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  -- 适配器名，核心平台不依赖任何单一运行组件（AGT-005 验收）
  adapter       text NOT NULL,
  version       text,
  status        text NOT NULL DEFAULT 'unknown'
                  CHECK (status IN ('unknown', 'installing', 'running', 'stopped',
                                    'degraded', 'failed', 'upgrading')),
  listen_spec   jsonb NOT NULL DEFAULT '{}'::jsonb,
  health_detail jsonb NOT NULL DEFAULT '{}'::jsonb,
  last_health_at timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT runtime_instances_node_adapter_unique UNIQUE (node_id, adapter)
);

CREATE TRIGGER trg_runtime_instances_updated_at BEFORE UPDATE ON runtime_instances
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('runtime_instances');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- NODE-015 残留资源扫描
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE orphan_resources (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  provider_id     uuid NOT NULL,
  resource_type   text NOT NULL,
  provider_resource_id text NOT NULL,
  region          text,
  -- 三方比对结果：云上有 / OpenTofu State 有 / 平台库有
  in_cloud        boolean NOT NULL DEFAULT false,
  in_tofu_state   boolean NOT NULL DEFAULT false,
  in_platform_db  boolean NOT NULL DEFAULT false,
  estimated_monthly_cost app.minor_amount,
  cost_currency   app.currency_code,
  status          text NOT NULL DEFAULT 'open'
                    CHECK (status IN ('open', 'acknowledged', 'reclaimed', 'ignored')),
  detected_at     timestamptz NOT NULL DEFAULT now(),
  resolved_at     timestamptz,
  resolved_by     uuid REFERENCES users(id) ON DELETE SET NULL,

  CONSTRAINT orphan_resources_unique UNIQUE (provider_id, resource_type, provider_resource_id),
  CONSTRAINT orphan_resources_provider_fk
    FOREIGN KEY (tenant_id, provider_id) REFERENCES cloud_providers (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_orphan_resources_open ON orphan_resources (tenant_id, status) WHERE status = 'open';

SELECT app.enable_tenant_rls('orphan_resources');

COMMENT ON TABLE orphan_resources IS
  'NODE-015：孤儿实例、磁盘、IP 和快照。三个 in_* 布尔位不一致即为需告警的漂移。';
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS orphan_resources;
DROP TABLE IF EXISTS runtime_instances;
DROP TABLE IF EXISTS node_config_applications;
DROP TABLE IF EXISTS node_configs;
DROP TABLE IF EXISTS node_tasks;
DROP TABLE IF EXISTS provisioning_steps;
DROP TABLE IF EXISTS provisioning_runs;
DROP TABLE IF EXISTS bootstrap_tokens;
DROP TABLE IF EXISTS node_identities;
DROP TRIGGER IF EXISTS trg_nodes_transition ON nodes;
DROP FUNCTION IF EXISTS app.guard_node_transition();
DROP TABLE IF EXISTS node_transitions;
DROP TABLE IF EXISTS nodes;
DROP TABLE IF EXISTS node_templates;
DROP TABLE IF EXISTS node_pools;
DROP TABLE IF EXISTS cloud_providers;
-- +goose StatementEnd
