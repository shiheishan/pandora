-- +goose Up
-- 去掉「币种」这一维：整个系统只有人民币（用户 2026-10-16 定，见 .claude/rules/redesign-2026-10-16.md「钱」）。
-- 删掉所有表的 currency 列（及 subscriptions.snapshot_currency、gift_card_redemptions.refund_currency、
-- payment_providers.currencies），金额一律按人民币（元）理解。还没有生产部署（用户 10-09 确认），不留兼容、不迁移数据。
--
-- 为什么一个迁移跨多个模块（各表属主不变，只删列）：
--   币种由大量复合外键 (x_id, currency) REFERENCES <表> (id, currency) 钉在一起——账本交易的 (id, currency) 被
--   ledger_entries、balance_holds（ledger）、commission_entries、withdrawals、commission_transfers、
--   commission_forfeitures（commission）、late_payment_cases（payment）引用；coupons 的 (id, currency) 被
--   coupon_redemptions（promo）与 orders（order）引用；orders 的 (id, currency) 被 order_items 引用。
--   任何一个模块单独删列，都会撞上别的模块的外键；只能按依赖顺序在一个事务里拆：
--   先删引用方的复合外键 → 再删只为它们存在的 (id, currency) 唯一键 → 再删列 → 最后按原名把外键重建成只引用 (id)。
--
-- 逐项：
--   - 复合外键按原名重建、只去掉 currency（都改为引用 (id)，ON DELETE RESTRICT 不变）：
--     ledger_entries_transaction_fk、ledger_entries_account_fk、balance_holds_{hold,capture,release}_txn_fk、
--     commission_entries_{accrual,settle,void}_txn_fk、withdrawals_payout_txn_fk、commission_transfers_ledger_txn_fk、
--     commission_forfeitures_ledger_txn_fk、late_payment_cases_{suspense,applied}_txn_fk、
--     coupon_redemptions_coupon_fkey、orders_coupon_fk、order_items_order_fk、revenue_report_adjustments_reversal_fk。
--   - 只为复合外键存在的唯一键直接删：ledger_accounts_id_currency_key、ledger_transactions_id_currency_key、
--     coupons_id_currency_key、orders_id_currency_key、revenue_report_adjustments_id_currency_key（主键已保证 id 唯一）。
--   - 含 currency 的唯一键、索引、CHECK 按原名重建、只去掉 currency：ledger_accounts_owner_key（ledger 的 Go 里
--     ON CONFLICT ON CONSTRAINT 用这个名字）、prices_one_active_offer、revenue_report_adjustments_series_idx、
--     plan_reset_prices_pkey、gift_card_redemptions_refund_shape。
--   - 例外：commission_forfeitures_user_currency_key 的名字里带币种，重建成 commission_forfeitures_user_key（UNIQUE (user_id)：
--     同一个人只注销作废一次）。
--   - 列上的 CHECK (currency IN ('CNY', 'USD')) 随列删除。
--   - 账本配平 assert_ledger_balanced 去掉「只有一个币种」那一项（借贷相等、至少两条分录照旧）。
--   不做套餐改造：套餐、流量包、券周期等由 00052 起的迁移在本迁移之后的结构上做。
--
-- 锁与耗时（整个迁移在一个事务里，锁持到提交）：
--   要下面 23 张表的 ACCESS EXCLUSIVE，挡住全部下单、支付回调、记账、佣金、兑换与报表写入：
--   prices、plan_reset_prices、traffic_packs、coupons、coupon_redemptions、ledger_accounts、ledger_transactions、
--   ledger_entries、balance_holds、subscriptions、payment_providers、payment_intents、payments、late_payment_cases、
--   orders、order_items、commission_entries、withdrawals、commission_transfers、commission_forfeitures、
--   gift_card_templates、gift_card_redemptions、revenue_report_adjustments。
--   DROP COLUMN 只改目录、不重写表；DROP CONSTRAINT / INDEX 瞬间完成；重建外键要整表校验一遍引用方
--   （耗时与账本分录、订单行等的行数成正比），重建唯一键与索引要整表建索引。
--   追加写守卫是语句级 UPDATE / DELETE 触发器，ALTER TABLE 不触发它。
--   部署顺序：旧进程会写 currency 列，新进程不认识它，新旧进程都不能跨过本迁移运行：先停 serve / worker，再 migrate，再起新进程。
-- 预检（下面的 DO 块会再核一遍）：
--   上面各表里不应有 currency <> 'CNY' 的行（还没有生产部署，正常只有开发、测试库可能有）。有就拒绝：删掉列会让美元金额被
--   悄悄当成人民币，而且同一用户同时有两种币种的科目时重建 ledger_accounts_owner_key 也会撞唯一。处理办法是清空那个库重装。
--   行数：SELECT count(*) FROM ledger_entries; SELECT count(*) FROM order_items; SELECT count(*) FROM ledger_transactions;
--   到百万级时把外键的重建拆成 ADD CONSTRAINT … NOT VALID + 另起语句 VALIDATE。目前没有生产库，按一个事务做。
-- 失败回滚：任一步失败（含 lock_timeout 超时、预检不过）整笔回滚，库停在 00050，排查后重试。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM prices WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM plan_reset_prices WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM traffic_packs WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM coupons WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM coupon_redemptions WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM ledger_accounts WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM ledger_transactions WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM ledger_entries WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM balance_holds WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM subscriptions WHERE snapshot_currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM payment_intents WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM payments WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM late_payment_cases WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM orders WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM order_items WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM commission_entries WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM withdrawals WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM commission_transfers WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM commission_forfeitures WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM gift_card_templates WHERE currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM gift_card_redemptions WHERE currency <> 'CNY' OR refund_currency <> 'CNY')
     OR EXISTS (SELECT 1 FROM revenue_report_adjustments WHERE currency <> 'CNY') THEN
    RAISE EXCEPTION '库里有非人民币（USD）的金额，删掉币种会把它们当成人民币；系统只有人民币，先清空这个库再迁移';
  END IF;
END
$$;
-- +goose StatementEnd

-- 1. 先删引用方的复合外键
ALTER TABLE ledger_entries
  DROP CONSTRAINT ledger_entries_transaction_fk,
  DROP CONSTRAINT ledger_entries_account_fk;
ALTER TABLE balance_holds
  DROP CONSTRAINT balance_holds_hold_txn_fk,
  DROP CONSTRAINT balance_holds_capture_txn_fk,
  DROP CONSTRAINT balance_holds_release_txn_fk;
ALTER TABLE commission_entries
  DROP CONSTRAINT commission_entries_accrual_txn_fk,
  DROP CONSTRAINT commission_entries_settle_txn_fk,
  DROP CONSTRAINT commission_entries_void_txn_fk;
ALTER TABLE withdrawals DROP CONSTRAINT withdrawals_payout_txn_fk;
ALTER TABLE commission_transfers DROP CONSTRAINT commission_transfers_ledger_txn_fk;
ALTER TABLE commission_forfeitures DROP CONSTRAINT commission_forfeitures_ledger_txn_fk;
ALTER TABLE late_payment_cases
  DROP CONSTRAINT late_payment_cases_suspense_txn_fk,
  DROP CONSTRAINT late_payment_cases_applied_txn_fk;
ALTER TABLE coupon_redemptions DROP CONSTRAINT coupon_redemptions_coupon_fkey;
ALTER TABLE order_items DROP CONSTRAINT order_items_order_fk;
ALTER TABLE orders DROP CONSTRAINT orders_coupon_fk;
ALTER TABLE revenue_report_adjustments DROP CONSTRAINT revenue_report_adjustments_reversal_fk;

-- 2. 再删只为复合外键存在的 (id, currency) 唯一键
ALTER TABLE ledger_accounts DROP CONSTRAINT ledger_accounts_id_currency_key;
ALTER TABLE ledger_transactions DROP CONSTRAINT ledger_transactions_id_currency_key;
ALTER TABLE coupons DROP CONSTRAINT coupons_id_currency_key;
ALTER TABLE orders DROP CONSTRAINT orders_id_currency_key;
ALTER TABLE revenue_report_adjustments DROP CONSTRAINT revenue_report_adjustments_id_currency_key;

-- 3. 含 currency 的唯一键、索引、CHECK（下面第 5 步按原名重建）
ALTER TABLE ledger_accounts DROP CONSTRAINT ledger_accounts_owner_key;
DROP INDEX prices_one_active_offer;
DROP INDEX revenue_report_adjustments_series_idx;
ALTER TABLE plan_reset_prices DROP CONSTRAINT plan_reset_prices_pkey;
ALTER TABLE commission_forfeitures DROP CONSTRAINT commission_forfeitures_user_currency_key;
ALTER TABLE gift_card_redemptions DROP CONSTRAINT gift_card_redemptions_refund_shape;

-- 4. 删列（列上的 CHECK 随列删除）
ALTER TABLE prices DROP COLUMN currency;
ALTER TABLE plan_reset_prices DROP COLUMN currency;
ALTER TABLE traffic_packs DROP COLUMN currency;
ALTER TABLE coupons DROP COLUMN currency;
ALTER TABLE coupon_redemptions DROP COLUMN currency;
ALTER TABLE ledger_accounts DROP COLUMN currency;
ALTER TABLE ledger_transactions DROP COLUMN currency;
ALTER TABLE ledger_entries DROP COLUMN currency;
ALTER TABLE balance_holds DROP COLUMN currency;
ALTER TABLE subscriptions DROP COLUMN snapshot_currency;
ALTER TABLE payment_providers DROP COLUMN currencies;
ALTER TABLE payment_intents DROP COLUMN currency;
ALTER TABLE payments DROP COLUMN currency;
ALTER TABLE late_payment_cases DROP COLUMN currency;
ALTER TABLE orders DROP COLUMN currency;
ALTER TABLE order_items DROP COLUMN currency;
ALTER TABLE commission_entries DROP COLUMN currency;
ALTER TABLE withdrawals DROP COLUMN currency;
ALTER TABLE commission_transfers DROP COLUMN currency;
ALTER TABLE commission_forfeitures DROP COLUMN currency;
ALTER TABLE gift_card_templates DROP COLUMN currency;
ALTER TABLE gift_card_redemptions DROP COLUMN currency, DROP COLUMN refund_currency;
ALTER TABLE revenue_report_adjustments DROP COLUMN currency;

-- 5. 按原名重建，只去掉 currency
ALTER TABLE ledger_accounts
  ADD CONSTRAINT ledger_accounts_owner_key UNIQUE NULLS NOT DISTINCT (account_type, owner_user_id, owner_ref);

-- 同一套餐、周期、用户组（公开价的组为空，按相同处理）只有一档在售价
CREATE UNIQUE INDEX prices_one_active_offer ON prices (plan_id, billing_interval, interval_count, user_group_id)
  NULLS NOT DISTINCT WHERE status = 'active';

-- 营收按日与概览：按生效日取
CREATE INDEX revenue_report_adjustments_series_idx ON revenue_report_adjustments (effective_on);

-- 每个套餐至多一档重置价
ALTER TABLE plan_reset_prices ADD CONSTRAINT plan_reset_prices_pkey PRIMARY KEY (plan_id);

-- 注销只有一次：同一个人最多作废一次（重放、并发的第二次撞这里）
ALTER TABLE commission_forfeitures ADD CONSTRAINT commission_forfeitures_user_key UNIQUE (user_id);

ALTER TABLE gift_card_redemptions
  ADD CONSTRAINT gift_card_redemptions_refund_shape CHECK (refund_amount IS NULL OR plan_mode = 'changed');

ALTER TABLE ledger_entries
  ADD CONSTRAINT ledger_entries_transaction_fk FOREIGN KEY (transaction_id)
    REFERENCES ledger_transactions (id) ON DELETE RESTRICT,
  ADD CONSTRAINT ledger_entries_account_fk FOREIGN KEY (account_id)
    REFERENCES ledger_accounts (id) ON DELETE RESTRICT;
ALTER TABLE balance_holds
  ADD CONSTRAINT balance_holds_hold_txn_fk FOREIGN KEY (hold_txn_id)
    REFERENCES ledger_transactions (id) ON DELETE RESTRICT,
  ADD CONSTRAINT balance_holds_capture_txn_fk FOREIGN KEY (capture_txn_id)
    REFERENCES ledger_transactions (id) ON DELETE RESTRICT,
  ADD CONSTRAINT balance_holds_release_txn_fk FOREIGN KEY (release_txn_id)
    REFERENCES ledger_transactions (id) ON DELETE RESTRICT;
ALTER TABLE commission_entries
  ADD CONSTRAINT commission_entries_accrual_txn_fk FOREIGN KEY (accrual_txn_id)
    REFERENCES ledger_transactions (id) ON DELETE RESTRICT,
  ADD CONSTRAINT commission_entries_settle_txn_fk FOREIGN KEY (settle_txn_id)
    REFERENCES ledger_transactions (id) ON DELETE RESTRICT,
  ADD CONSTRAINT commission_entries_void_txn_fk FOREIGN KEY (void_txn_id)
    REFERENCES ledger_transactions (id) ON DELETE RESTRICT;
ALTER TABLE withdrawals
  ADD CONSTRAINT withdrawals_payout_txn_fk FOREIGN KEY (payout_txn_id)
    REFERENCES ledger_transactions (id) ON DELETE RESTRICT;
ALTER TABLE commission_transfers
  ADD CONSTRAINT commission_transfers_ledger_txn_fk FOREIGN KEY (ledger_txn_id)
    REFERENCES ledger_transactions (id) ON DELETE RESTRICT;
ALTER TABLE commission_forfeitures
  ADD CONSTRAINT commission_forfeitures_ledger_txn_fk FOREIGN KEY (ledger_txn_id)
    REFERENCES ledger_transactions (id) ON DELETE RESTRICT;
ALTER TABLE late_payment_cases
  ADD CONSTRAINT late_payment_cases_suspense_txn_fk FOREIGN KEY (suspense_txn_id)
    REFERENCES ledger_transactions (id) ON DELETE RESTRICT,
  ADD CONSTRAINT late_payment_cases_applied_txn_fk FOREIGN KEY (applied_txn_id)
    REFERENCES ledger_transactions (id) ON DELETE RESTRICT;
ALTER TABLE coupon_redemptions
  ADD CONSTRAINT coupon_redemptions_coupon_fkey FOREIGN KEY (coupon_id)
    REFERENCES coupons (id) ON DELETE RESTRICT;
ALTER TABLE orders
  ADD CONSTRAINT orders_coupon_fk FOREIGN KEY (coupon_id)
    REFERENCES coupons (id) ON DELETE RESTRICT;
ALTER TABLE order_items
  ADD CONSTRAINT order_items_order_fk FOREIGN KEY (order_id)
    REFERENCES orders (id) ON DELETE RESTRICT;
ALTER TABLE revenue_report_adjustments
  ADD CONSTRAINT revenue_report_adjustments_reversal_fk FOREIGN KEY (reversal_of)
    REFERENCES revenue_report_adjustments (id) ON DELETE RESTRICT;

-- 6. 账本配平：每笔交易至少两条分录、借贷相等（不再有币种）。
-- DEFERRABLE INITIALLY DEFERRED 的约束触发器绑定的是函数名，CREATE OR REPLACE 后不用重建触发器。
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION assert_ledger_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  v_txn        uuid;
  v_entries    int;
  v_debit      numeric;
  v_credit     numeric;
BEGIN
  IF TG_TABLE_NAME = 'ledger_transactions' THEN
    v_txn := NEW.id;
  ELSE
    v_txn := NEW.transaction_id;
  END IF;
  SELECT count(*),
         coalesce(sum(e.amount) FILTER (WHERE e.direction = 'debit'), 0),
         coalesce(sum(e.amount) FILTER (WHERE e.direction = 'credit'), 0)
    INTO v_entries, v_debit, v_credit
    FROM ledger_entries e
   WHERE e.transaction_id = v_txn;
  IF v_entries < 2 THEN
    RAISE EXCEPTION '账务交易 % 至少要有两条分录', v_txn USING ERRCODE = 'check_violation';
  END IF;
  IF v_debit <> v_credit THEN
    RAISE EXCEPTION '账务交易 % 借贷不平：借 % 贷 %', v_txn, v_debit, v_credit USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- 逐项退回 00050 的结构：列加回（已有行一律记 CNY，列上的原 CHECK 一并加回，原来没有缺省值的再去掉缺省值）、
-- (id, currency) 唯一键与复合外键、含 currency 的唯一键与索引、账本配平函数都按 00009～00046 的原文重建。
-- 退回后所有行都是人民币，旧结构都放得下，不需要数据守卫。
-- 锁与耗时同 Up：重建复合外键、唯一键要整表校验、建索引。
-- 只追加的 gift_card_redemptions 要给换套餐退款的行补 refund_currency，在本事务里临时停掉它的追加写守卫，补完立刻恢复
-- （ALTER TABLE … DISABLE TRIGGER 是事务性的，失败回滚时守卫随之恢复）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION assert_ledger_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
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

-- 先删 Up 重建的外键与键
ALTER TABLE revenue_report_adjustments DROP CONSTRAINT revenue_report_adjustments_reversal_fk;
ALTER TABLE order_items DROP CONSTRAINT order_items_order_fk;
ALTER TABLE orders DROP CONSTRAINT orders_coupon_fk;
ALTER TABLE coupon_redemptions DROP CONSTRAINT coupon_redemptions_coupon_fkey;
ALTER TABLE late_payment_cases
  DROP CONSTRAINT late_payment_cases_suspense_txn_fk,
  DROP CONSTRAINT late_payment_cases_applied_txn_fk;
ALTER TABLE commission_forfeitures DROP CONSTRAINT commission_forfeitures_ledger_txn_fk;
ALTER TABLE commission_transfers DROP CONSTRAINT commission_transfers_ledger_txn_fk;
ALTER TABLE withdrawals DROP CONSTRAINT withdrawals_payout_txn_fk;
ALTER TABLE commission_entries
  DROP CONSTRAINT commission_entries_accrual_txn_fk,
  DROP CONSTRAINT commission_entries_settle_txn_fk,
  DROP CONSTRAINT commission_entries_void_txn_fk;
ALTER TABLE balance_holds
  DROP CONSTRAINT balance_holds_hold_txn_fk,
  DROP CONSTRAINT balance_holds_capture_txn_fk,
  DROP CONSTRAINT balance_holds_release_txn_fk;
ALTER TABLE ledger_entries
  DROP CONSTRAINT ledger_entries_transaction_fk,
  DROP CONSTRAINT ledger_entries_account_fk;

ALTER TABLE gift_card_redemptions DROP CONSTRAINT gift_card_redemptions_refund_shape;
ALTER TABLE commission_forfeitures DROP CONSTRAINT commission_forfeitures_user_key;
ALTER TABLE plan_reset_prices DROP CONSTRAINT plan_reset_prices_pkey;
DROP INDEX revenue_report_adjustments_series_idx;
DROP INDEX prices_one_active_offer;
ALTER TABLE ledger_accounts DROP CONSTRAINT ledger_accounts_owner_key;

-- 加回列：已有行记 CNY；原来没有缺省值的列再去掉缺省值（traffic_packs.currency 原本就是 DEFAULT 'CNY'）
ALTER TABLE prices ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE prices ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE plan_reset_prices ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE plan_reset_prices ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE traffic_packs ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE coupons ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE coupons ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE coupon_redemptions ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE coupon_redemptions ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE ledger_accounts ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE ledger_accounts ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE ledger_transactions ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE ledger_transactions ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE ledger_entries ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE ledger_entries ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE balance_holds ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE balance_holds ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE subscriptions
  ADD COLUMN snapshot_currency text NOT NULL DEFAULT 'CNY' CHECK (snapshot_currency IN ('CNY', 'USD'));
ALTER TABLE subscriptions ALTER COLUMN snapshot_currency DROP DEFAULT;
ALTER TABLE payment_intents ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE payment_intents ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE payments ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE payments ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE late_payment_cases ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE late_payment_cases ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE orders ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE orders ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE order_items ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE order_items ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE commission_entries ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE commission_entries ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE withdrawals ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE withdrawals ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE commission_transfers ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE commission_transfers ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE commission_forfeitures ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE commission_forfeitures ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE gift_card_templates ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE gift_card_templates ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE gift_card_redemptions ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE gift_card_redemptions ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE gift_card_redemptions ADD COLUMN refund_currency text CHECK (refund_currency IN ('CNY', 'USD'));
ALTER TABLE revenue_report_adjustments
  ADD COLUMN currency text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD'));
ALTER TABLE revenue_report_adjustments ALTER COLUMN currency DROP DEFAULT;

-- 支付渠道支持的币种：线下收款回到种子里的两种，其余渠道记人民币
ALTER TABLE payment_providers
  ADD COLUMN currencies jsonb NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(currencies) = 'array' AND currencies <@ '["CNY", "USD"]'::jsonb);
UPDATE payment_providers SET currencies = '["CNY", "USD"]'::jsonb WHERE adapter = 'offline';
UPDATE payment_providers SET currencies = '["CNY"]'::jsonb WHERE adapter <> 'offline';

-- 换套餐退回余额的兑换行补上退款币种（只追加表，临时停守卫）
ALTER TABLE gift_card_redemptions DISABLE TRIGGER gift_card_redemptions_append_only;
UPDATE gift_card_redemptions SET refund_currency = 'CNY' WHERE refund_amount IS NOT NULL;
ALTER TABLE gift_card_redemptions ENABLE TRIGGER gift_card_redemptions_append_only;
ALTER TABLE gift_card_redemptions ADD CONSTRAINT gift_card_redemptions_refund_shape CHECK (
  (refund_amount IS NULL) = (refund_currency IS NULL) AND (refund_amount IS NULL OR plan_mode = 'changed')
);

-- 含 currency 的唯一键与索引（00008、00009、00032、00040、00046 原文）
ALTER TABLE ledger_accounts
  ADD CONSTRAINT ledger_accounts_owner_key UNIQUE NULLS NOT DISTINCT (account_type, currency, owner_user_id, owner_ref);
CREATE UNIQUE INDEX prices_one_active_offer ON prices (plan_id, currency, billing_interval, interval_count, user_group_id)
  NULLS NOT DISTINCT WHERE status = 'active';
CREATE INDEX revenue_report_adjustments_series_idx ON revenue_report_adjustments (currency, effective_on);
ALTER TABLE plan_reset_prices ADD CONSTRAINT plan_reset_prices_pkey PRIMARY KEY (plan_id, currency);
ALTER TABLE commission_forfeitures
  ADD CONSTRAINT commission_forfeitures_user_currency_key UNIQUE (user_id, currency);

-- 复合外键用的 (id, currency) 唯一键
ALTER TABLE ledger_accounts ADD CONSTRAINT ledger_accounts_id_currency_key UNIQUE (id, currency);
ALTER TABLE ledger_transactions ADD CONSTRAINT ledger_transactions_id_currency_key UNIQUE (id, currency);
ALTER TABLE coupons ADD CONSTRAINT coupons_id_currency_key UNIQUE (id, currency);
ALTER TABLE orders ADD CONSTRAINT orders_id_currency_key UNIQUE (id, currency);
ALTER TABLE revenue_report_adjustments
  ADD CONSTRAINT revenue_report_adjustments_id_currency_key UNIQUE (id, currency);

-- 复合外键（原文）
ALTER TABLE ledger_entries
  ADD CONSTRAINT ledger_entries_transaction_fk FOREIGN KEY (transaction_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT,
  ADD CONSTRAINT ledger_entries_account_fk FOREIGN KEY (account_id, currency)
    REFERENCES ledger_accounts (id, currency) ON DELETE RESTRICT;
ALTER TABLE balance_holds
  ADD CONSTRAINT balance_holds_hold_txn_fk FOREIGN KEY (hold_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT,
  ADD CONSTRAINT balance_holds_capture_txn_fk FOREIGN KEY (capture_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT,
  ADD CONSTRAINT balance_holds_release_txn_fk FOREIGN KEY (release_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT;
ALTER TABLE commission_entries
  ADD CONSTRAINT commission_entries_accrual_txn_fk FOREIGN KEY (accrual_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT,
  ADD CONSTRAINT commission_entries_settle_txn_fk FOREIGN KEY (settle_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT,
  ADD CONSTRAINT commission_entries_void_txn_fk FOREIGN KEY (void_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT;
ALTER TABLE withdrawals
  ADD CONSTRAINT withdrawals_payout_txn_fk FOREIGN KEY (payout_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT;
ALTER TABLE commission_transfers
  ADD CONSTRAINT commission_transfers_ledger_txn_fk FOREIGN KEY (ledger_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT;
ALTER TABLE commission_forfeitures
  ADD CONSTRAINT commission_forfeitures_ledger_txn_fk FOREIGN KEY (ledger_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT;
ALTER TABLE late_payment_cases
  ADD CONSTRAINT late_payment_cases_suspense_txn_fk FOREIGN KEY (suspense_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT,
  ADD CONSTRAINT late_payment_cases_applied_txn_fk FOREIGN KEY (applied_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT;
ALTER TABLE coupon_redemptions
  ADD CONSTRAINT coupon_redemptions_coupon_fkey FOREIGN KEY (coupon_id, currency)
    REFERENCES coupons (id, currency) ON DELETE RESTRICT;
ALTER TABLE orders
  ADD CONSTRAINT orders_coupon_fk FOREIGN KEY (coupon_id, currency)
    REFERENCES coupons (id, currency) ON DELETE RESTRICT;
ALTER TABLE order_items
  ADD CONSTRAINT order_items_order_fk FOREIGN KEY (order_id, currency)
    REFERENCES orders (id, currency) ON DELETE RESTRICT;
ALTER TABLE revenue_report_adjustments
  ADD CONSTRAINT revenue_report_adjustments_reversal_fk FOREIGN KEY (reversal_of, currency)
    REFERENCES revenue_report_adjustments (id, currency) ON DELETE RESTRICT;
