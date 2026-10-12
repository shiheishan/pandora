-- +goose Up
-- commission 模块：分销佣金条目、提现申请、佣金转余额记录。
-- 钱的流向全在账本里（计提 commission_accrued、解冻 commission_settled、打款 commission_paid、
-- 转余额 commission_to_balance）；这里的表记的是「为什么有这笔钱」，并带上对应交易的 id。
-- 金额列都是 numeric(20,2)，多余小数由写入前的 money.Check 拦（库里 CHECK 写不出来）。
-- 状态机在 Go 里（转换表 + CanTransition，更新带 WHERE status = 旧状态），库里只留枚举与各状态的形状。

-- 佣金条目：订单结算时给推荐人计提一条（冻结），冻结期满由 commission:settle 转为可提现。
-- 佣金 = trunc(基数 × rate_bp / 10000)，基数是订单总额（含余额抵扣）；Go 用 money.ApplyBP 算，库里再核一遍。
CREATE TABLE commission_entries (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  referrer_user_id  uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  referee_user_id   uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  order_id          uuid NOT NULL REFERENCES orders(id) ON DELETE RESTRICT,
  -- 订单号快照，门户佣金明细直接显示，不回查订单
  order_no          text NOT NULL CHECK (char_length(order_no) BETWEEN 1 AND 64),
  payment_id        uuid REFERENCES payments(id) ON DELETE RESTRICT,
  currency          text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  base_amount       numeric(20,2) NOT NULL CHECK (base_amount > 0),
  rate_bp           int NOT NULL CHECK (rate_bp > 0 AND rate_bp <= 5000),
  commission_amount numeric(20,2) NOT NULL CHECK (commission_amount > 0),
  status            text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'available')),
  frozen_until      timestamptz NOT NULL,
  -- 需要人工复核的条目不自动解冻
  review_required   boolean NOT NULL DEFAULT false,
  review_reason     text CHECK (char_length(review_reason) BETWEEN 1 AND 200),
  accrual_txn_id    uuid NOT NULL REFERENCES ledger_transactions(id) ON DELETE RESTRICT,
  settle_txn_id     uuid REFERENCES ledger_transactions(id) ON DELETE RESTRICT,
  settled_at        timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  -- 一个订单对同一推荐人只产生一条佣金
  CONSTRAINT commission_entries_order_referrer_key UNIQUE (order_id, referrer_user_id),
  CONSTRAINT commission_entries_accrual_txn_key UNIQUE (accrual_txn_id),
  CONSTRAINT commission_entries_settle_txn_key UNIQUE (settle_txn_id),
  CONSTRAINT commission_entries_no_self CHECK (referrer_user_id <> referee_user_id),
  CONSTRAINT commission_entries_formula
    CHECK (commission_amount = trunc(base_amount * rate_bp / 10000, 2)),
  CONSTRAINT commission_entries_review_shape CHECK (review_required = (review_reason IS NOT NULL)),
  CONSTRAINT commission_entries_state_shape CHECK (
    (status = 'pending' AND settle_txn_id IS NULL AND settled_at IS NULL)
    OR (status = 'available' AND settle_txn_id IS NOT NULL AND settled_at IS NOT NULL AND NOT review_required)
  )
);

-- 门户明细与汇总
CREATE INDEX commission_entries_referrer_idx ON commission_entries (referrer_user_id, created_at DESC);
-- 「首单」口径按被推荐人查；也是 users 外键的 RESTRICT 检查
CREATE INDEX commission_entries_referee_idx ON commission_entries (referee_user_id);
-- 解冻任务按到期时间扫
CREATE INDEX commission_entries_due_idx ON commission_entries (frozen_until)
  WHERE status = 'pending' AND NOT review_required;

CREATE TRIGGER commission_entries_no_delete
  BEFORE DELETE OR TRUNCATE ON commission_entries
  FOR EACH STATEMENT EXECUTE FUNCTION deny_delete();

-- 提现申请：requested → approved / rejected，approved → paid。审批不动账，打款那一步记 commission_paid。
-- 收款信息（支付宝账号、银行卡号）经 Sealer 封存，AAD 绑定申请人。
CREATE TABLE withdrawals (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id          uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  currency         text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  amount           numeric(20,2) NOT NULL CHECK (amount > 0),
  payout_sealed    bytea NOT NULL CHECK (octet_length(payout_sealed) > 0),
  status           text NOT NULL DEFAULT 'requested'
                     CHECK (status IN ('requested', 'approved', 'rejected', 'paid')),
  reject_reason    text CHECK (char_length(btrim(reject_reason)) BETWEEN 1 AND 500),
  -- 转账流水号，日后对账用
  payout_reference text CHECK (char_length(btrim(payout_reference)) BETWEEN 1 AND 128),
  payout_txn_id    uuid REFERENCES ledger_transactions(id) ON DELETE RESTRICT,
  reviewed_by      uuid REFERENCES users(id) ON DELETE RESTRICT,
  reviewed_at      timestamptz,
  paid_by          uuid REFERENCES users(id) ON DELETE RESTRICT,
  requested_at     timestamptz NOT NULL DEFAULT now(),
  completed_at     timestamptz,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT withdrawals_payout_txn_key UNIQUE (payout_txn_id),
  CONSTRAINT withdrawals_state_shape CHECK (
    (status = 'requested' AND reviewed_at IS NULL AND reject_reason IS NULL
       AND payout_reference IS NULL AND payout_txn_id IS NULL AND completed_at IS NULL)
    OR (status = 'approved' AND reviewed_at IS NOT NULL AND reject_reason IS NULL
       AND payout_reference IS NULL AND payout_txn_id IS NULL AND completed_at IS NULL)
    OR (status = 'rejected' AND reviewed_at IS NOT NULL AND reject_reason IS NOT NULL
       AND payout_reference IS NULL AND payout_txn_id IS NULL AND completed_at IS NOT NULL)
    OR (status = 'paid' AND reviewed_at IS NOT NULL AND reject_reason IS NULL
       AND payout_reference IS NOT NULL AND payout_txn_id IS NOT NULL AND completed_at IS NOT NULL)
  )
);

-- 一个用户同一时间只有一笔在途提现（申请中或已批准待打款）
CREATE UNIQUE INDEX withdrawals_one_in_flight_key ON withdrawals (user_id)
  WHERE status IN ('requested', 'approved');
CREATE INDEX withdrawals_user_idx ON withdrawals (user_id, requested_at DESC);
CREATE INDEX withdrawals_status_idx ON withdrawals (status, requested_at DESC);
CREATE INDEX withdrawals_requested_idx ON withdrawals (requested_at DESC);

CREATE TRIGGER withdrawals_no_delete
  BEFORE DELETE OR TRUNCATE ON withdrawals
  FOR EACH STATEMENT EXECUTE FUNCTION deny_delete();

-- 佣金转余额：每一笔记一行，与账本里的 commission_to_balance 交易一一对应（id 即交易的来源 id）。
CREATE TABLE commission_transfers (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  currency      text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  amount        numeric(20,2) NOT NULL CHECK (amount > 0),
  ledger_txn_id uuid NOT NULL REFERENCES ledger_transactions(id) ON DELETE RESTRICT,
  created_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT commission_transfers_ledger_txn_key UNIQUE (ledger_txn_id)
);
CREATE INDEX commission_transfers_user_idx ON commission_transfers (user_id, created_at DESC);

CREATE TRIGGER commission_transfers_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON commission_transfers
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

-- +goose Down
DROP TABLE IF EXISTS commission_transfers;
DROP TABLE IF EXISTS withdrawals;
DROP TABLE IF EXISTS commission_entries;
