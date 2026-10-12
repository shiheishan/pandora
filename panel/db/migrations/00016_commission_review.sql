-- +goose Up
-- 待复核佣金的后台「通过 / 作废」（用户 2026-10-13 定）。
--
-- 待复核 = status 'pending' 且 review_required。
--   通过：清 review_required、记复核人与复核时间，review_reason 留作历史；之后由 commission:settle 照常解冻。
--   作废：转终态 voided，同一事务里记一笔 commission_voided 冲回计提（借 用户待结佣金 / 贷 平台收入 main），
--         按来源（这条佣金）唯一。
--
-- 锁与耗时（本迁移整体在一个事务里，锁持到提交）：
--   commission_entries 换约束：ACCESS EXCLUSIVE 下整表校验一遍。表按订单数增长，现在很小。
--   ledger_transactions 换 kind 的 CHECK：ACCESS EXCLUSIVE 下整表校验一遍（新清单是旧清单的超集，校验必过）；
--   按来源唯一的部分索引重建（DROP + CREATE，同一事务里原子替换）。这两步挡住记账，耗时与账本行数成正比。
--   预检：上线前 SELECT count(*) FROM ledger_transactions。行数到百万级时改成两个 NO TRANSACTION 迁移：
--     ADD CONSTRAINT … NOT VALID 后另起语句 VALIDATE（不挡读写）；CREATE UNIQUE INDEX CONCURRENTLY 新名 →
--     DROP INDEX CONCURRENTLY 旧名 → 改名。目前还没有生产库，账本很小，按一个事务做。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库保持原样，换低峰重试即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- 账本：新增交易类型 commission_voided，并进按来源唯一的清单（与 Go 的 Kind.OnePerSource 一致）
ALTER TABLE ledger_transactions DROP CONSTRAINT ledger_transactions_kind_check;
ALTER TABLE ledger_transactions ADD CONSTRAINT ledger_transactions_kind_check CHECK (kind IN (
  'balance_topup', 'balance_adjusted', 'balance_hold', 'balance_release', 'order_paid',
  'plan_change_refund', 'late_payment_suspense', 'late_payment_applied', 'gift_card_balance',
  'commission_accrued', 'commission_settled', 'commission_paid', 'commission_to_balance',
  'commission_voided'));

DROP INDEX ledger_transactions_source_once_key;
CREATE UNIQUE INDEX ledger_transactions_source_once_key ON ledger_transactions (kind, source_type, source_id)
  WHERE source_id IS NOT NULL AND kind IN (
    'balance_topup', 'balance_hold', 'balance_release', 'order_paid', 'late_payment_suspense',
    'plan_change_refund', 'commission_settled', 'commission_paid', 'gift_card_balance',
    'commission_voided');

-- 佣金条目：复核人、复核时间（通过与作废都记）、作废原因与冲回交易
ALTER TABLE commission_entries
  ADD COLUMN reviewed_by uuid REFERENCES users(id) ON DELETE RESTRICT,
  ADD COLUMN reviewed_at timestamptz,
  ADD COLUMN void_reason text CHECK (char_length(btrim(void_reason)) BETWEEN 5 AND 500),
  ADD COLUMN void_txn_id uuid,
  ADD CONSTRAINT commission_entries_void_txn_key UNIQUE (void_txn_id),
  -- 冲回交易外键带上币种（同计提、解冻）
  ADD CONSTRAINT commission_entries_void_txn_fk FOREIGN KEY (void_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT;

ALTER TABLE commission_entries DROP CONSTRAINT commission_entries_status_check;
ALTER TABLE commission_entries ADD CONSTRAINT commission_entries_status_check
  CHECK (status IN ('pending', 'available', 'voided'));

-- 复核形状：待复核必须有原因；复核人与复核时间成对；有原因而不再待复核的，必须是复核过的（通过后原因留作历史）
ALTER TABLE commission_entries DROP CONSTRAINT commission_entries_review_shape;
ALTER TABLE commission_entries ADD CONSTRAINT commission_entries_review_shape CHECK (
  (NOT review_required OR review_reason IS NOT NULL)
  AND ((reviewed_by IS NULL) = (reviewed_at IS NULL))
  AND (review_reason IS NULL OR review_required OR reviewed_at IS NOT NULL)
);

ALTER TABLE commission_entries DROP CONSTRAINT commission_entries_state_shape;
ALTER TABLE commission_entries ADD CONSTRAINT commission_entries_state_shape CHECK (
  (status = 'pending' AND settle_txn_id IS NULL AND settled_at IS NULL
     AND void_txn_id IS NULL AND void_reason IS NULL)
  OR (status = 'available' AND settle_txn_id IS NOT NULL AND settled_at IS NOT NULL AND NOT review_required
     AND void_txn_id IS NULL AND void_reason IS NULL)
  OR (status = 'voided' AND settle_txn_id IS NULL AND settled_at IS NULL AND NOT review_required
     AND void_txn_id IS NOT NULL AND void_reason IS NOT NULL AND reviewed_at IS NOT NULL)
);

-- 后台「待复核」列表按计提时间翻页
CREATE INDEX commission_entries_review_idx ON commission_entries (created_at DESC, id DESC)
  WHERE status = 'pending' AND review_required;

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- 复核过的条目（通过或作废）与冲回交易在旧结构里放不下：有就拒绝回退，避免悄悄丢掉复核记录或冲回分录
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM commission_entries WHERE reviewed_at IS NOT NULL OR status = 'voided') THEN
    RAISE EXCEPTION '已有复核过的佣金条目，不能回退到 00015（先人工处理这些条目）';
  END IF;
  IF EXISTS (SELECT 1 FROM ledger_transactions WHERE kind = 'commission_voided') THEN
    RAISE EXCEPTION '账本里已有 commission_voided 交易，不能回退到 00015';
  END IF;
END
$$;
-- +goose StatementEnd

DROP INDEX commission_entries_review_idx;

ALTER TABLE commission_entries DROP CONSTRAINT commission_entries_state_shape;
ALTER TABLE commission_entries ADD CONSTRAINT commission_entries_state_shape CHECK (
  (status = 'pending' AND settle_txn_id IS NULL AND settled_at IS NULL)
  OR (status = 'available' AND settle_txn_id IS NOT NULL AND settled_at IS NOT NULL AND NOT review_required)
);

ALTER TABLE commission_entries DROP CONSTRAINT commission_entries_review_shape;
ALTER TABLE commission_entries ADD CONSTRAINT commission_entries_review_shape
  CHECK (review_required = (review_reason IS NOT NULL));

ALTER TABLE commission_entries DROP CONSTRAINT commission_entries_status_check;
ALTER TABLE commission_entries ADD CONSTRAINT commission_entries_status_check
  CHECK (status IN ('pending', 'available'));

ALTER TABLE commission_entries
  DROP CONSTRAINT commission_entries_void_txn_fk,
  DROP CONSTRAINT commission_entries_void_txn_key,
  DROP COLUMN void_txn_id,
  DROP COLUMN void_reason,
  DROP COLUMN reviewed_at,
  DROP COLUMN reviewed_by;

DROP INDEX ledger_transactions_source_once_key;
CREATE UNIQUE INDEX ledger_transactions_source_once_key ON ledger_transactions (kind, source_type, source_id)
  WHERE source_id IS NOT NULL AND kind IN (
    'balance_topup', 'balance_hold', 'balance_release', 'order_paid', 'late_payment_suspense',
    'plan_change_refund', 'commission_settled', 'commission_paid', 'gift_card_balance');

ALTER TABLE ledger_transactions DROP CONSTRAINT ledger_transactions_kind_check;
ALTER TABLE ledger_transactions ADD CONSTRAINT ledger_transactions_kind_check CHECK (kind IN (
  'balance_topup', 'balance_adjusted', 'balance_hold', 'balance_release', 'order_paid',
  'plan_change_refund', 'late_payment_suspense', 'late_payment_applied', 'gift_card_balance',
  'commission_accrued', 'commission_settled', 'commission_paid', 'commission_to_balance'));
