-- 计量域：usage_sources / usage_batches / usage_events / usage_aggregates /
--          quota_balances / quota_adjustments
--
-- 对应 PRD 第 10 章 USE-001..USE-007。
--
-- 去重设计说明（USE-002 是本域最难的一条）：
--   原始事件表按月 RANGE 分区，而分区表的唯一约束必须包含分区键，
--   这会让「同一 event_id 跨月重复」逃过约束。
--   因此去重不靠明细表，而靠两道非分区的窄表：
--     · usage_batches (node_id, batch_sequence) 唯一 —— 挡住整批重传
--     · usage_event_keys (tenant_id, event_id)  唯一 —— 挡住单条重复
--   明细表只负责存证与聚合溯源，不承担幂等职责。

-- +goose Up

--------------------------------------------------------------------------------
-- 用量来源（USE-001 签名事件的签发主体）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE usage_sources (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  kind          text NOT NULL DEFAULT 'node' CHECK (kind IN ('node', 'gateway', 'manual', 'import')),
  node_id       uuid REFERENCES nodes(id) ON DELETE CASCADE,
  -- 用于验签的公钥（与 node_identities 同源，冗余一份便于计量服务独立校验）
  public_key    bytea,
  -- 该来源已确认接收的最大批次序号，用于识别缺口与乱序
  last_batch_sequence bigint NOT NULL DEFAULT 0,
  last_seen_at  timestamptz,
  status        text NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active', 'suspended', 'revoked')),
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT usage_sources_node_unique UNIQUE (node_id)
);

SELECT app.enable_tenant_rls('usage_sources');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 批次（USE-002 / USE-003 离线补传）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE usage_batches (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  source_id       uuid NOT NULL REFERENCES usage_sources(id) ON DELETE CASCADE,
  node_id         uuid REFERENCES nodes(id) ON DELETE SET NULL,
  -- 节点本地单调递增序号：USE-002「node_id+sequence 唯一约束」的落点
  batch_sequence  bigint NOT NULL CHECK (batch_sequence > 0),
  event_count     int NOT NULL DEFAULT 0 CHECK (event_count >= 0),
  -- USE-001：签名覆盖节点、周期、序号与全部事件摘要
  content_hash    bytea NOT NULL,
  signature       bytea NOT NULL,
  signature_valid boolean NOT NULL DEFAULT false,
  config_version  int,
  -- Agent 侧的采集窗口；与 received_at 的差值即迟到时长（USE-004 迟到修正依据）
  window_start    timestamptz NOT NULL,
  window_end      timestamptz NOT NULL,
  received_at     timestamptz NOT NULL DEFAULT now(),
  status          text NOT NULL DEFAULT 'received'
                    CHECK (status IN ('received', 'accepted', 'rejected', 'applied', 'superseded')),
  reject_reason   text,
  applied_at      timestamptz,

  -- 这条唯一约束是整个计量域幂等性的地基
  CONSTRAINT usage_batches_source_sequence_unique UNIQUE (source_id, batch_sequence),
  CONSTRAINT usage_batches_window_order CHECK (window_end >= window_start)
);

CREATE INDEX idx_usage_batches_pending ON usage_batches (tenant_id, received_at)
  WHERE status IN ('received', 'accepted');
CREATE INDEX idx_usage_batches_node ON usage_batches (node_id, batch_sequence DESC);

SELECT app.enable_tenant_rls('usage_batches');

COMMENT ON TABLE usage_batches IS
  'USE-002 验收「重复、乱序和重传不会重复扣减」：整批重传撞 (source_id, batch_sequence) 唯一约束后直接返回已接收。';
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 单条事件去重键（窄表，非分区，可按期清理）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE usage_event_keys (
  tenant_id   uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  event_id    uuid NOT NULL,
  batch_id    uuid NOT NULL REFERENCES usage_batches(id) ON DELETE CASCADE,
  occurred_at timestamptz NOT NULL,
  -- 保留期到点后清理；早于保留期的重复事件已无法再影响任何未结账周期
  expires_at  timestamptz NOT NULL,

  PRIMARY KEY (tenant_id, event_id)
);

CREATE INDEX idx_usage_event_keys_expiry ON usage_event_keys (expires_at);

SELECT app.enable_tenant_rls('usage_event_keys');

COMMENT ON TABLE usage_event_keys IS
  'USE-002：单条事件幂等键。故意做成窄表且不分区，保证唯一约束是全局的。';
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 原始事件（USE-004「保存有限期原始事件」，按月分区）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE usage_events (
  id            uuid NOT NULL DEFAULT uuidv7(),
  tenant_id     uuid NOT NULL,
  batch_id      uuid NOT NULL,
  event_id      uuid NOT NULL,
  node_id       uuid,
  -- 归属：用户订阅或权益
  user_id       uuid,
  subscription_id uuid,
  -- 指标与量值
  metric        text NOT NULL,
  quantity      bigint NOT NULL CHECK (quantity >= 0),
  unit          text NOT NULL DEFAULT 'bytes',
  -- 事件序号（节点内单调），用于顺序校验
  sequence      bigint,
  config_version int,
  occurred_at   timestamptz NOT NULL,
  received_at   timestamptz NOT NULL DEFAULT now(),
  -- 迟到标记：超出当前结算窗口的事件按窗口修正并保留证据（USE-004）
  late          boolean NOT NULL DEFAULT false,

  PRIMARY KEY (tenant_id, occurred_at, id)
) PARTITION BY RANGE (occurred_at);

CREATE INDEX idx_usage_events_subscription ON usage_events (tenant_id, subscription_id, occurred_at DESC);
CREATE INDEX idx_usage_events_batch ON usage_events (batch_id);
CREATE INDEX idx_usage_events_node ON usage_events (node_id, occurred_at DESC);
-- +goose StatementEnd

-- +goose StatementBegin
-- 按月创建分区的辅助函数；由 XBD-024 幂等定时任务提前若干月调用。
-- 幂等：已存在则直接返回，重复运行安全。
CREATE OR REPLACE FUNCTION app.ensure_usage_partition(p_month date) RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
  v_start date := date_trunc('month', p_month)::date;
  v_end   date := (date_trunc('month', p_month) + interval '1 month')::date;
  v_name  text := format('usage_events_%s', to_char(v_start, 'YYYYMM'));
BEGIN
  IF EXISTS (SELECT 1 FROM pg_class WHERE relname = v_name) THEN
    RETURN v_name || ' (exists)';
  END IF;
  EXECUTE format(
    'CREATE TABLE %I PARTITION OF usage_events FOR VALUES FROM (%L) TO (%L)',
    v_name, v_start, v_end);
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', v_name);
  EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', v_name);
  EXECUTE format(
    'CREATE POLICY tenant_isolation ON %I
       USING (tenant_id = app.current_tenant_id())
       WITH CHECK (tenant_id = app.current_tenant_id())', v_name);
  -- DATA-003：原始用量事件追加写
  EXECUTE format(
    'CREATE TRIGGER trg_%s_append_only BEFORE UPDATE OR DELETE ON %I
       FOR EACH STATEMENT EXECUTE FUNCTION app.deny_mutation()', v_name, v_name);
  RETURN v_name || ' (created)';
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
-- 预建：上月、本月、下月三个分区，保证首次部署即可写入。
-- now() 在迁移执行时求值一次，不影响可重复性（分区按自然月对齐）。
DO $$
BEGIN
  PERFORM app.ensure_usage_partition((current_date - interval '1 month')::date);
  PERFORM app.ensure_usage_partition(current_date);
  PERFORM app.ensure_usage_partition((current_date + interval '1 month')::date);
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
-- 兜底分区：接住时钟异常或极端迟到的事件，避免写入直接失败丢数据。
CREATE TABLE usage_events_overflow PARTITION OF usage_events DEFAULT;
ALTER TABLE usage_events_overflow ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_events_overflow FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON usage_events_overflow
  USING (tenant_id = app.current_tenant_id())
  WITH CHECK (tenant_id = app.current_tenant_id());
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 分级聚合（USE-004）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE usage_aggregates (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  grain           text NOT NULL CHECK (grain IN ('hour', 'day', 'month')),
  bucket_start    timestamptz NOT NULL,
  subscription_id uuid REFERENCES subscriptions(id) ON DELETE CASCADE,
  user_id         uuid,
  node_id         uuid REFERENCES nodes(id) ON DELETE SET NULL,
  metric          text NOT NULL,
  quantity        bigint NOT NULL DEFAULT 0 CHECK (quantity >= 0),
  event_count     int NOT NULL DEFAULT 0,
  -- USE-004 验收「聚合结果可追溯到原始批次」
  source_batch_ids uuid[] NOT NULL DEFAULT '{}',
  -- 迟到事件导致的修正次数与最后修正时间
  revision        int NOT NULL DEFAULT 0,
  last_revised_at timestamptz,
  computed_at     timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT usage_aggregates_unique
    UNIQUE (tenant_id, grain, bucket_start, subscription_id, node_id, metric)
);

CREATE INDEX idx_usage_aggregates_sub ON usage_aggregates (tenant_id, subscription_id, grain, bucket_start DESC);

SELECT app.enable_tenant_rls('usage_aggregates');
-- +goose StatementEnd

--------------------------------------------------------------------------------
-- 配额余额（USE-005）
--------------------------------------------------------------------------------

-- +goose StatementBegin
CREATE TABLE quota_balances (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  subscription_id uuid NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  metric          text NOT NULL,
  period          text NOT NULL CHECK (period IN ('total', 'cycle', 'day', 'month')),
  -- 当前周期边界；周期重置即开新行或推进边界并归零 consumed
  period_start    timestamptz NOT NULL,
  period_end      timestamptz,
  -- 套餐给的基础额度
  granted         bigint NOT NULL DEFAULT 0 CHECK (granted >= 0),
  -- XBD-007 附加包/重置包追加的额度，与基础额度分列，退款时可精确回收
  granted_addon   bigint NOT NULL DEFAULT 0 CHECK (granted_addon >= 0),
  consumed        bigint NOT NULL DEFAULT 0 CHECK (consumed >= 0),
  -- 人工调整净额，可正可负，每一笔都在 quota_adjustments 里有据可查
  adjusted        bigint NOT NULL DEFAULT 0,
  -- 便于查询的派生列：NULL 表示不限量
  limit_value     bigint,
  remaining       bigint GENERATED ALWAYS AS
                    (CASE WHEN limit_value IS NULL THEN NULL
                          ELSE limit_value + granted_addon + adjusted - consumed END) STORED,
  -- USE-006 阈值通知去重：记录本周期已通知过的阈值
  notified_thresholds smallint[] NOT NULL DEFAULT '{}',
  -- USE-007 超额处置状态；并发触发只执行一次
  overage_action  text NOT NULL DEFAULT 'none'
                    CHECK (overage_action IN ('none', 'suspended', 'throttled', 'metered')),
  overage_applied_at timestamptz,
  updated_at      timestamptz NOT NULL DEFAULT now(),
  created_at      timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT quota_balances_unique UNIQUE (subscription_id, metric, period, period_start)
);

CREATE INDEX idx_quota_balances_sub ON quota_balances (tenant_id, subscription_id);
-- 周期重置任务的驱动索引
CREATE INDEX idx_quota_balances_period_end ON quota_balances (period_end)
  WHERE period_end IS NOT NULL;

CREATE TRIGGER trg_quota_balances_updated_at BEFORE UPDATE ON quota_balances
  FOR EACH ROW EXECUTE FUNCTION app.set_updated_at();

SELECT app.enable_tenant_rls('quota_balances');

COMMENT ON COLUMN quota_balances.notified_thresholds IS
  'USE-006 验收「同一阈值在一个周期内不重复轰炸」：已通知阈值入数组，周期重置时清空。';
-- +goose StatementEnd

-- +goose StatementBegin
-- USE-005「周期重置和人工调整均有事件记录」，追加写。
CREATE TABLE quota_adjustments (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id       uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  quota_balance_id uuid NOT NULL REFERENCES quota_balances(id) ON DELETE CASCADE,
  subscription_id uuid NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
  metric          text NOT NULL,
  -- period_reset / addon_granted / addon_revoked / manual / refund_reclaim / migration
  reason          text NOT NULL,
  delta           bigint NOT NULL,
  balance_before  bigint NOT NULL,
  balance_after   bigint NOT NULL,
  -- 人工调整必须有说明与操作人（可解释配额扣减 —— 4.2 补强项）
  memo            text,
  actor_kind      text NOT NULL DEFAULT 'system'
                    CHECK (actor_kind IN ('system', 'admin', 'scheduler', 'order', 'refund')),
  actor_id        uuid,
  source_type     text,
  source_id       uuid,
  occurred_at     timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT quota_adjustments_manual_needs_memo
    CHECK (reason <> 'manual' OR (memo IS NOT NULL AND length(memo) >= 5))
);

CREATE INDEX idx_quota_adjustments_balance ON quota_adjustments (quota_balance_id, occurred_at DESC);

SELECT app.enable_tenant_rls('quota_adjustments');
SELECT app.make_append_only('quota_adjustments');
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TABLE IF EXISTS quota_adjustments;
DROP TABLE IF EXISTS quota_balances;
DROP TABLE IF EXISTS usage_aggregates;
DROP TABLE IF EXISTS usage_events;
DROP FUNCTION IF EXISTS app.ensure_usage_partition(date);
DROP TABLE IF EXISTS usage_event_keys;
DROP TABLE IF EXISTS usage_batches;
DROP TABLE IF EXISTS usage_sources;
-- +goose StatementEnd
