-- 地基迁移：扩展、app 辅助 schema、租户表、以及支撑 DATA-001/002/003 的通用机制。
--
-- 对应 PRD：
--   DATA-001 统一标识    → 全部主键 uuidv7()（PG 18 内置，时间有序且不可枚举）
--   DATA-002 租户字段    → app.current_tenant_id() + 行级安全策略，应用漏写 tenant_id 由数据库兜底
--   DATA-003 不可变记录  → app.deny_mutation() 触发器 + 权限回收双保险
--   ARC-006  租户隔离    → FORCE ROW LEVEL SECURITY，连表属主自己也受策略约束

-- +goose Up

-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS citext;      -- 邮箱等大小写不敏感标识
CREATE EXTENSION IF NOT EXISTS pgcrypto;    -- gen_random_bytes / digest
CREATE EXTENSION IF NOT EXISTS btree_gist;  -- 时间范围排他约束（订阅周期防重叠）
-- +goose StatementEnd

-- +goose StatementBegin
CREATE SCHEMA IF NOT EXISTS app;
COMMENT ON SCHEMA app IS 'AegisPanel 平台级辅助函数与策略，不存放业务表';
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 租户上下文：由应用在每个连接/事务开始时 SET LOCAL app.tenant_id = '...'
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.current_tenant_id() RETURNS uuid
LANGUAGE sql STABLE PARALLEL SAFE AS $$
  SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid
$$;
COMMENT ON FUNCTION app.current_tenant_id() IS
  'DATA-002：读取当前会话租户。未设置时返回 NULL，此时所有受 RLS 保护的表均查不到任何行（默认拒绝）。';
-- +goose StatementEnd

-- +goose StatementBegin
-- 当前操作者（用于审计归因与追加写表的 actor 默认值）。
CREATE OR REPLACE FUNCTION app.current_actor_id() RETURNS uuid
LANGUAGE sql STABLE PARALLEL SAFE AS $$
  SELECT NULLIF(current_setting('app.actor_id', true), '')::uuid
$$;
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 通用触发器
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
-- DATA-003：账本、审计、原始用量、支付事件一律追加写。
-- 触发器负责挡住应用误操作，权限回收负责挡住直连数据库的人。
CREATE OR REPLACE FUNCTION app.deny_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION
    '表 %.% 为追加写（DATA-003），不允许 % 操作',
    TG_TABLE_SCHEMA, TG_TABLE_NAME, TG_OP
    USING ERRCODE = 'insufficient_privilege';
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
-- 把一张表标记为追加写：装触发器 + 回收写权限。
CREATE OR REPLACE FUNCTION app.make_append_only(p_table regclass) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
  v_name text := replace(p_table::text, '.', '_');
BEGIN
  EXECUTE format(
    'CREATE TRIGGER trg_%s_append_only BEFORE UPDATE OR DELETE ON %s
       FOR EACH STATEMENT EXECUTE FUNCTION app.deny_mutation()',
    v_name, p_table);
  EXECUTE format('REVOKE UPDATE, DELETE, TRUNCATE ON %s FROM PUBLIC', p_table);
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
-- 把一张表纳入租户隔离：开启 RLS 并安装读写双向策略。
-- 用 FORCE 是关键 —— 否则表属主（也就是应用连接用的角色）会自动绕过策略，
-- ARC-006「自动化测试无法跨租户读取或修改数据」就形同虚设。
CREATE OR REPLACE FUNCTION app.enable_tenant_rls(p_table regclass) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
  EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', p_table);
  EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', p_table);
  EXECUTE format(
    'CREATE POLICY tenant_isolation ON %s
       USING (tenant_id = app.current_tenant_id())
       WITH CHECK (tenant_id = app.current_tenant_id())',
    p_table);
END $$;
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 金额约定（SUB-010 多币种）
--   一律用 BIGINT 存币种最小单位（分/聪），绝不使用 float。
--   currency 为 ISO-4217 三字母大写码，exponent 记录最小单位与主单位的换算位数。
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE DOMAIN app.currency_code AS char(3)
  CHECK (VALUE ~ '^[A-Z]{3}$');

CREATE DOMAIN app.minor_amount AS bigint;

COMMENT ON DOMAIN app.minor_amount IS
  'SUB-010：币种最小单位整数金额。禁止浮点。负值仅在账本借贷分录中有意义。';
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 租户
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE tenants (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  slug            citext NOT NULL UNIQUE,
  display_name    text NOT NULL,
  status          text NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'suspended', 'archived')),
  -- 账期、到期、配额重置全部按该时区计算（SUB-010 / XBD-006）
  timezone        text NOT NULL DEFAULT 'UTC',
  default_currency app.currency_code NOT NULL DEFAULT 'USD',
  -- 品牌与站点参数（XBD-021 / XBD-023），Schema 校验在应用层
  settings        jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_tenants_updated_at BEFORE UPDATE ON tenants
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

COMMENT ON TABLE tenants IS
  '附录A：首个商用版本单租户运行，但全表预留 tenant_id，后续开多租户无需改表结构。';
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 幂等键（PAY-003 / NODE-005 共用）
--   任何“重复提交必须只产生一次业务结果”的入口都先在这里抢占唯一键。
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE idempotency_keys (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- scope 区分用途：payment_webhook / order_create / provisioning_run ...
  scope           text NOT NULL,
  idempotency_key text NOT NULL,
  -- 请求体指纹：同一个 key 配不同请求体属于冲突，必须报错而不是返回旧结果
  request_hash    bytea NOT NULL,
  status          text NOT NULL DEFAULT 'in_flight'
                    CHECK (status IN ('in_flight', 'succeeded', 'failed')),
  response_code   int,
  response_body   jsonb,
  -- 指向本次操作真正产生的业务实体，便于对账时回溯
  resource_type   text,
  resource_id     uuid,
  locked_until    timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  completed_at    timestamptz,
  expires_at      timestamptz NOT NULL DEFAULT now() + interval '30 days',

  CONSTRAINT idempotency_keys_unique UNIQUE (tenant_id, scope, idempotency_key)
);

CREATE INDEX idx_idempotency_keys_expiry ON idempotency_keys (expires_at)
  WHERE status <> 'in_flight';

SELECT app.enable_tenant_rls('idempotency_keys');

COMMENT ON TABLE idempotency_keys IS
  'PAY-003：同一回调重复 100 次只产生一次业务结果。唯一约束由数据库保证，不依赖应用层判重。';
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS tenants;
DROP DOMAIN IF EXISTS app.minor_amount;
DROP DOMAIN IF EXISTS app.currency_code;
DROP FUNCTION IF EXISTS app.enable_tenant_rls(regclass);
DROP FUNCTION IF EXISTS app.make_append_only(regclass);
DROP FUNCTION IF EXISTS app.deny_mutation();
DROP FUNCTION IF EXISTS app.set_updated_at();
DROP FUNCTION IF EXISTS app.current_actor_id();
DROP FUNCTION IF EXISTS app.current_tenant_id();
DROP SCHEMA IF EXISTS app;
-- +goose StatementEnd
