-- +goose Up
-- commission 模块：三级分销条目分级（level / share_percent）、按用户佣金覆盖表、提现方式与 USDT 实付数量、
-- 佣金转余额关联条目（提现关闭自动转余额时记 entry_id）。属 commission 模块。
--
-- 锁与耗时（本迁移整体在一个事务里，锁持到提交）：
--   commission_entries、withdrawals、commission_transfers 各拿 ACCESS EXCLUSIVE（加列、换约束、建表时校验或改结构），
--   换约束时整表校验一遍；耗时与这三张表的行数成正比。现在没有生产库，表很小。
-- 预检：上线前 SELECT count(*) FROM commission_entries; SELECT count(*) FROM withdrawals;
--   SELECT count(*) FROM commission_transfers;
-- 失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库保持原样，换低峰重试即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- (a) commission_entries：分级与份额，并更新金额公式约束
ALTER TABLE commission_entries
  ADD COLUMN level smallint NOT NULL DEFAULT 1
    CONSTRAINT commission_entries_level_check CHECK (level BETWEEN 1 AND 3),
  ADD COLUMN share_percent smallint NOT NULL DEFAULT 100
    CONSTRAINT commission_entries_share_check CHECK (share_percent BETWEEN 1 AND 100);
-- 第几级邀请人，1 是直接邀请人；这一级分到这一单佣金总额的百分比；没开多级分销时第一级 100

ALTER TABLE commission_entries
  ALTER COLUMN level DROP DEFAULT,
  ALTER COLUMN share_percent DROP DEFAULT;
-- 存量行已回填 1 / 100；新行必须由代码显式给值

ALTER TABLE commission_entries DROP CONSTRAINT commission_entries_formula;
ALTER TABLE commission_entries ADD CONSTRAINT commission_entries_formula CHECK (
  commission_amount = trunc(trunc(base_amount * rate_bp / 10000, 2) * share_percent / 100, 2)
);
-- 先按比例算出这一单的佣金总额（向零截断到分），再按这一级的份额截断到分；share_percent = 100 时与旧公式相同

ALTER TABLE commission_entries
  ADD CONSTRAINT commission_entries_order_level_key UNIQUE (order_id, level);
-- 一单每一级最多一条

ALTER TABLE commission_entries
  ADD COLUMN auto_transfer_due boolean NOT NULL DEFAULT false,
  ADD CONSTRAINT commission_entries_auto_transfer_shape CHECK (NOT auto_transfer_due OR status = 'available');
-- 提现关闭时解冻的站点币种条目记 true，等单独的事务把它转入余额后清掉；转失败留着，下一轮任务重试。
-- 只有已解冻（available）的条目能挂这个标记；available 是终态，不会再被作废。

CREATE INDEX commission_entries_auto_transfer_due_idx
  ON commission_entries (settled_at, id) WHERE auto_transfer_due;
-- 补扫只找挂着标记的少数行

-- (b) 按用户单独设的佣金比例与计佣范围，只对该用户作为直接邀请人的以后订单生效
CREATE TABLE commission_user_overrides (
  user_id      uuid PRIMARY KEY REFERENCES users(id) ON DELETE RESTRICT,
  rate_percent smallint CHECK (rate_percent BETWEEN 0 AND 50),
  scope        text CHECK (scope IN ('every_order', 'first_order')),
  updated_by   uuid REFERENCES users(id) ON DELETE RESTRICT,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT commission_user_overrides_not_empty CHECK (rate_percent IS NOT NULL OR scope IS NOT NULL)
);
-- NULL 表示这一项跟全局设置走；rate_percent 为 0 表示这个人不再拿佣金。是配置表，不挂追加写触发器，改动留在审计里。

-- (c) withdrawals：提现方式与 USDT 实付数量
ALTER TABLE withdrawals
  ADD COLUMN method text NOT NULL DEFAULT 'other'
    CONSTRAINT withdrawals_method_check CHECK (
      method IN (
        'alipay', 'wechat', 'bank', 'paypal',
        'usdt_trc20', 'usdt_erc20', 'usdt_bep20', 'other'
      )
    );
-- 存量行（自由填写的收款信息）迁成 other；other 只用于显示，新申请不能再选（由代码拦）

ALTER TABLE withdrawals ALTER COLUMN method DROP DEFAULT;

ALTER TABLE withdrawals
  ADD COLUMN paid_usdt_amount numeric(24,6)
    CONSTRAINT withdrawals_paid_usdt_amount_check CHECK (paid_usdt_amount > 0);
-- USDT 打款时管理员可选记录的实付数量，仅记录，不参与记账

ALTER TABLE withdrawals
  ADD CONSTRAINT withdrawals_usdt_amount_shape CHECK (
    paid_usdt_amount IS NULL
    OR (status = 'paid' AND method IN ('usdt_trc20', 'usdt_erc20', 'usdt_bep20'))
  );

ALTER TABLE withdrawals
  ADD CONSTRAINT withdrawals_usdt_reference_shape CHECK (
    status <> 'paid'
    OR method NOT IN ('usdt_trc20', 'usdt_erc20', 'usdt_bep20')
    OR (method = 'usdt_trc20' AND payout_reference ~ '^[0-9a-fA-F]{64}$')
    OR (method IN ('usdt_erc20', 'usdt_bep20') AND payout_reference ~ '^0x[0-9a-fA-F]{64}$')
  );
-- USDT 打款的流水号就是链上交易哈希：TRC20 是 64 位十六进制，ERC20 / BEP20 是 0x 加 64 位十六进制。面板不发起任何链上转账。

-- (d) commission_transfers：自动转余额关联佣金条目
ALTER TABLE commission_transfers
  ADD COLUMN entry_id uuid REFERENCES commission_entries(id) ON DELETE RESTRICT,
  ADD CONSTRAINT commission_transfers_entry_key UNIQUE (entry_id);
-- 提现关闭时佣金解冻后自动转入余额，这一行记是哪条佣金转的；手动转余额为 NULL。
-- 唯一约束保证同一条佣金只自动转一次。表挂着 deny_mutation，加可空列不改已有行，不触发它。

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- 自动转余额的 entry_id 不预检：删掉这一列后转余额记录本身仍然有效，只是丢了与条目的关联。
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM commission_entries WHERE level > 1 OR share_percent <> 100) THEN
    RAISE EXCEPTION '已有多级分销的佣金条目（level > 1 或 share_percent <> 100），不能回退本迁移（先人工处理这些行）';
  END IF;
  IF EXISTS (SELECT 1 FROM withdrawals WHERE method <> 'other' OR paid_usdt_amount IS NOT NULL) THEN
    RAISE EXCEPTION '已有非 other 的提现方式或 USDT 实付数量记录，不能回退本迁移（先人工处理这些行）';
  END IF;
  IF EXISTS (SELECT 1 FROM commission_entries WHERE auto_transfer_due) THEN
    RAISE EXCEPTION '还有等待自动转入余额的佣金条目（auto_transfer_due），不能回退本迁移（先让 commission:settle 转完）';
  END IF;
  IF EXISTS (SELECT 1 FROM commission_user_overrides) THEN
    RAISE EXCEPTION 'commission_user_overrides 已有按用户的佣金设置，不能回退本迁移（先人工处理）';
  END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE commission_transfers
  DROP CONSTRAINT commission_transfers_entry_key,
  DROP COLUMN entry_id;

ALTER TABLE withdrawals
  DROP CONSTRAINT withdrawals_usdt_reference_shape,
  DROP CONSTRAINT withdrawals_usdt_amount_shape,
  DROP COLUMN paid_usdt_amount,
  DROP COLUMN method;

DROP TABLE commission_user_overrides;

DROP INDEX commission_entries_auto_transfer_due_idx;

ALTER TABLE commission_entries
  DROP CONSTRAINT commission_entries_auto_transfer_shape,
  DROP COLUMN auto_transfer_due;

ALTER TABLE commission_entries DROP CONSTRAINT commission_entries_order_level_key;

ALTER TABLE commission_entries DROP CONSTRAINT commission_entries_formula;
ALTER TABLE commission_entries ADD CONSTRAINT commission_entries_formula CHECK (
  commission_amount = trunc(base_amount * rate_bp / 10000, 2)
);

ALTER TABLE commission_entries
  DROP COLUMN share_percent,
  DROP COLUMN level;
