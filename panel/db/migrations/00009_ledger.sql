-- +goose Up
-- ledger 模块：复式记账（科目、交易、分录）与订单余额冻结。
-- 钱的底线放在库里：分录金额 > 0；每笔交易提交时借贷相等、只有一个币种（DEFERRABLE 约束触发器）；
-- 用户类科目的余额缓存不为负；交易与分录只追加。余额缓存由 Go 在同一事务里更新（先按科目 ID 升序锁行），
-- 不再用触发器维护。账务记录不设保留期，不建清理函数。
-- 金额列都是 numeric(20,2)：库里多余小数会被静默舍入，所以不写「= round(x, 2)」这种永远成立的 CHECK；
-- 多余小数由写入前的 money.Check 拦。

CREATE TABLE ledger_accounts (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  account_type  text NOT NULL CHECK (account_type IN (
                  'user_balance', 'user_balance_hold', 'user_commission_pending', 'user_commission_available',
                  'channel_cash', 'platform_revenue', 'platform_fee_expense', 'platform_promo_expense',
                  'suspense', 'late_payment_suspense')),
  currency      text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  owner_user_id uuid REFERENCES users(id) ON DELETE RESTRICT,
  owner_ref     text NOT NULL DEFAULT '' CHECK (char_length(owner_ref) <= 64),
  balance       numeric(20,2) NOT NULL DEFAULT 0,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  -- 用户类科目挂用户、不带 owner_ref；平台类科目不挂用户、必须有 owner_ref
  CONSTRAINT ledger_accounts_owner_shape CHECK (
    (account_type IN ('user_balance', 'user_balance_hold', 'user_commission_pending', 'user_commission_available'))
      = (owner_user_id IS NOT NULL)
    AND (owner_user_id IS NULL) = (owner_ref <> '')
  ),
  -- 余额不透支：用户类科目的余额缓存不得为负
  CONSTRAINT ledger_accounts_user_nonnegative CHECK (owner_user_id IS NULL OR balance >= 0),
  CONSTRAINT ledger_accounts_owner_key UNIQUE NULLS NOT DISTINCT (account_type, currency, owner_user_id, owner_ref),
  -- 给分录的 (account_id, currency) 复合外键用：分录币种必须等于科目币种
  CONSTRAINT ledger_accounts_id_currency_key UNIQUE (id, currency)
);
CREATE INDEX ledger_accounts_owner_idx ON ledger_accounts (owner_user_id) WHERE owner_user_id IS NOT NULL;

CREATE TABLE ledger_transactions (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  kind        text NOT NULL CHECK (kind IN (
                'balance_topup', 'balance_adjusted', 'balance_hold', 'balance_release', 'order_paid',
                'plan_change_refund', 'late_payment_suspense', 'late_payment_applied', 'gift_card_balance',
                'commission_accrued', 'commission_settled', 'commission_paid', 'commission_to_balance')),
  currency    text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  -- 来源：哪个业务对象产生了这笔账（order、payment、user、gift_card_code……）
  source_type text CHECK (source_type ~ '^[a-z_]{1,32}$'),
  source_id   uuid,
  memo        text NOT NULL DEFAULT '' CHECK (char_length(memo) <= 500),
  actor_kind  text NOT NULL CHECK (actor_kind IN ('system', 'user', 'admin')),
  actor_id    uuid,
  occurred_at timestamptz NOT NULL DEFAULT now(),
  -- 建这笔交易的顶层事务号：分录只能在同一个事务里追加，提交后的交易不能再挂分录（见 ledger_entries_same_xact）
  created_xact xid8 NOT NULL DEFAULT pg_current_xact_id(),
  CONSTRAINT ledger_transactions_source_pair CHECK ((source_type IS NULL) = (source_id IS NULL)),
  CONSTRAINT ledger_transactions_actor CHECK ((actor_kind = 'system') = (actor_id IS NULL)),
  -- 人工调账必须写原因
  CONSTRAINT ledger_transactions_adjust_memo CHECK (kind <> 'balance_adjusted' OR char_length(btrim(memo)) >= 5),
  CONSTRAINT ledger_transactions_id_currency_key UNIQUE (id, currency)
);
CREATE INDEX ledger_transactions_source_idx ON ledger_transactions (source_type, source_id) WHERE source_id IS NOT NULL;
CREATE INDEX ledger_transactions_time_idx ON ledger_transactions (occurred_at DESC, id DESC);
-- 每个来源至多一笔的交易：一张订单的充值、冻结、释放、结算各一笔；一笔支付的挂账一笔；
-- 一个来源的换套餐退回一笔；一条佣金记录的解冻一笔；一笔提现的打款一笔；一个礼品卡码的入余额一笔。
-- Go 里写之前先查（返回 ErrDuplicatePosting，不让调用方事务作废），本索引是并发时的兜底。
-- 种类清单与 Go 的 onePerSourceKinds 一致，改一边要同步另一边。
CREATE UNIQUE INDEX ledger_transactions_source_once_key ON ledger_transactions (kind, source_type, source_id)
  WHERE source_id IS NOT NULL AND kind IN (
    'balance_topup', 'balance_hold', 'balance_release', 'order_paid', 'late_payment_suspense',
    'plan_change_refund', 'commission_settled', 'commission_paid', 'gift_card_balance');

CREATE TABLE ledger_entries (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  transaction_id uuid NOT NULL,
  account_id     uuid NOT NULL,
  currency       text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  direction      text NOT NULL CHECK (direction IN ('debit', 'credit')),
  amount         numeric(20,2) NOT NULL CHECK (amount > 0),
  created_at     timestamptz NOT NULL DEFAULT now(),
  -- 复合外键钉住币种：分录币种 = 交易币种 = 科目币种
  CONSTRAINT ledger_entries_transaction_fk FOREIGN KEY (transaction_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT,
  CONSTRAINT ledger_entries_account_fk FOREIGN KEY (account_id, currency)
    REFERENCES ledger_accounts (id, currency) ON DELETE RESTRICT
);
CREATE INDEX ledger_entries_transaction_idx ON ledger_entries (transaction_id);
CREATE INDEX ledger_entries_account_idx ON ledger_entries (account_id, created_at DESC, id DESC);

-- 只追加：应用不能改、不能删
CREATE TRIGGER ledger_transactions_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON ledger_transactions
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();
CREATE TRIGGER ledger_entries_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON ledger_entries
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

-- 已提交的交易不能再追加分录：分录所属交易必须是当前顶层事务建的
-- （pg_current_xact_id() 返回顶层事务号，保存点里也一样）。交易不存在时交给外键报错。
-- +goose StatementBegin
CREATE FUNCTION assert_ledger_entry_same_xact() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  v_xact xid8;
BEGIN
  SELECT t.created_xact INTO v_xact FROM ledger_transactions t WHERE t.id = NEW.transaction_id;
  IF FOUND AND v_xact <> pg_current_xact_id() THEN
    RAISE EXCEPTION '账务交易 % 已提交，不能再追加分录', NEW.transaction_id USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER ledger_entries_same_xact
  BEFORE INSERT ON ledger_entries
  FOR EACH ROW EXECUTE FUNCTION assert_ledger_entry_same_xact();

-- 账本配平：唯一的提交时检查。每笔交易至少两条分录、借贷相等、只有一个币种；
-- DEFERRABLE INITIALLY DEFERRED，事务中途可以不平，提交那一刻必须平，否则整个事务回滚。
-- 交易与分录各挂一个：插了交易却一条分录都没插，同样在提交时被拦下。
-- +goose StatementBegin
CREATE FUNCTION assert_ledger_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  v_txn        uuid;
  v_entries    int;
  v_currencies int;
  v_debit      numeric;
  v_credit     numeric;
BEGIN
  IF TG_TABLE_NAME = 'ledger_transactions' THEN
    v_txn := NEW.id;
  ELSE
    v_txn := NEW.transaction_id;
  END IF;
  SELECT count(*), count(DISTINCT e.currency),
         coalesce(sum(e.amount) FILTER (WHERE e.direction = 'debit'), 0),
         coalesce(sum(e.amount) FILTER (WHERE e.direction = 'credit'), 0)
    INTO v_entries, v_currencies, v_debit, v_credit
    FROM ledger_entries e
   WHERE e.transaction_id = v_txn;
  IF v_entries < 2 THEN
    RAISE EXCEPTION '账务交易 % 至少要有两条分录', v_txn USING ERRCODE = 'check_violation';
  END IF;
  IF v_currencies <> 1 THEN
    RAISE EXCEPTION '账务交易 % 混用了 % 种币种', v_txn, v_currencies USING ERRCODE = 'check_violation';
  END IF;
  IF v_debit <> v_credit THEN
    RAISE EXCEPTION '账务交易 % 借贷不平：借 % 贷 %', v_txn, v_debit, v_credit USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER ledger_transactions_balanced
  AFTER INSERT ON ledger_transactions
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION assert_ledger_balanced();
CREATE CONSTRAINT TRIGGER ledger_entries_balanced
  AFTER INSERT ON ledger_entries
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION assert_ledger_balanced();

-- 订单余额冻结：每单至多一笔。状态 held → captured（结算扣款）或 released（取消、超时释放），只走一次。
CREATE TABLE balance_holds (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  -- 外键由 00013 补（订单表在 00013_order.sql 才建）
  order_id       uuid NOT NULL,
  user_id        uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  currency       text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  amount         numeric(20,2) NOT NULL CHECK (amount > 0),
  status         text NOT NULL DEFAULT 'held' CHECK (status IN ('held', 'captured', 'released')),
  hold_txn_id    uuid NOT NULL,
  capture_txn_id uuid,
  release_txn_id uuid,
  held_at        timestamptz NOT NULL DEFAULT now(),
  captured_at    timestamptz,
  released_at    timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT balance_holds_order_key UNIQUE (order_id),
  CONSTRAINT balance_holds_hold_txn_fk FOREIGN KEY (hold_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT,
  CONSTRAINT balance_holds_capture_txn_fk FOREIGN KEY (capture_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT,
  CONSTRAINT balance_holds_release_txn_fk FOREIGN KEY (release_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT,
  CONSTRAINT balance_holds_state CHECK (
    (status = 'held' AND capture_txn_id IS NULL AND captured_at IS NULL
      AND release_txn_id IS NULL AND released_at IS NULL)
    OR (status = 'captured' AND capture_txn_id IS NOT NULL AND captured_at IS NOT NULL
      AND release_txn_id IS NULL AND released_at IS NULL)
    OR (status = 'released' AND release_txn_id IS NOT NULL AND released_at IS NOT NULL
      AND capture_txn_id IS NULL AND captured_at IS NULL)
  )
);
CREATE INDEX balance_holds_user_idx ON balance_holds (user_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS balance_holds;
DROP TABLE IF EXISTS ledger_entries;
DROP TABLE IF EXISTS ledger_transactions;
DROP FUNCTION IF EXISTS assert_ledger_balanced();
DROP FUNCTION IF EXISTS assert_ledger_entry_same_xact();
DROP TABLE IF EXISTS ledger_accounts;
