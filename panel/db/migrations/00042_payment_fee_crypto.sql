-- +goose Up
-- payment 模块（表都归 payment）：Xboard 补齐 #15 向用户收支付手续费、#57 加密货币渠道（BTCPay Server、CoinPayments）。
-- 产品 2026-10-14 定：手续费照 Xboard 做成每个渠道「固定 + 比例」，缺省 0（关）；向用户收、加在这次支付意图的金额上，
-- 订单应付与总额不变；用余额付不收；记账时单独贷记平台收入的 payment_surcharge 科目，不计入佣金基数。
-- #43 单独回调域名是 settings 里的一个键（payment.notify_base_url），不动表。
--
--   payment_providers：adapter 加 btcpay、coinpayments；加 fee_fixed（元）与 fee_percent（百分比，两位小数）。
--   payment_intents：加 surcharge_amount，是 amount 里向用户加收的手续费（amount = 订单应付 + surcharge_amount）。
--   payments：加 surcharge_amount，从意图快照抄过来；结算与挂账只把 amount − surcharge_amount 算作订单的钱。
--
-- 锁与耗时（整个迁移一个事务，锁持到提交）：
--   payment_providers 换 adapter 的 CHECK、加两列：表只有几行，瞬间完成。
--   payment_intents、payments 加 NOT NULL DEFAULT 0 的列：PG 11 起是只改目录的快速默认值，不重写表；
--   新 CHECK 要在 ACCESS EXCLUSIVE 下整表校验一遍（新列全是 0，校验必过），耗时与行数成正比，挡住建意图与回调入账。
--   payments 的追加写守卫是语句级 UPDATE / DELETE 触发器，ALTER TABLE 不触发它。
--   预检：上线前 SELECT count(*) FROM payments; SELECT count(*) FROM payment_intents;
--     到百万级时把两条 CHECK 改成 ADD CONSTRAINT … NOT VALID，另起一个迁移 VALIDATE（不挡读写）。目前还没有生产库，按一个事务做。
-- 失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库保持原样，换低峰重试即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE payment_providers DROP CONSTRAINT payment_providers_adapter_check;
ALTER TABLE payment_providers ADD CONSTRAINT payment_providers_adapter_check
  CHECK (adapter IN ('epay', 'btcpay', 'coinpayments', 'offline'));

-- 手续费 = 订单应付 × fee_percent / 100 + fee_fixed，向上取整到分（Go 里算，见 payment/service/surcharge.go）。
-- 上限挡手滑：固定部分最多 1000 元，比例最多 100%。线下收款不收手续费。
ALTER TABLE payment_providers
  ADD COLUMN fee_fixed numeric(20,2) NOT NULL DEFAULT 0 CHECK (fee_fixed >= 0 AND fee_fixed <= 1000),
  ADD COLUMN fee_percent numeric(5,2) NOT NULL DEFAULT 0 CHECK (fee_percent >= 0 AND fee_percent <= 100),
  ADD CONSTRAINT payment_providers_offline_no_fee CHECK (adapter <> 'offline' OR (fee_fixed = 0 AND fee_percent = 0));

-- 订单应付 > 0，所以手续费一定小于意图金额
ALTER TABLE payment_intents
  ADD COLUMN surcharge_amount numeric(20,2) NOT NULL DEFAULT 0,
  ADD CONSTRAINT payment_intents_surcharge CHECK (surcharge_amount >= 0 AND surcharge_amount < amount);

ALTER TABLE payments
  ADD COLUMN surcharge_amount numeric(20,2) NOT NULL DEFAULT 0,
  ADD CONSTRAINT payments_surcharge CHECK (surcharge_amount >= 0 AND surcharge_amount < amount);

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- 旧结构放不下的数据：加密货币渠道（旧 CHECK 不收），以及收过手续费的意图与收款（去掉列后金额里哪部分是手续费就丢了，
-- 旧代码会把整笔当成订单应付去核对）。有就拒绝回退，先人工处理；没有就照常退。
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM payment_providers WHERE adapter IN ('btcpay', 'coinpayments')) THEN
    RAISE EXCEPTION '还有 BTCPay / CoinPayments 渠道，旧结构放不下，不能回退 00042';
  END IF;
  IF EXISTS (SELECT 1 FROM payments WHERE surcharge_amount > 0)
     OR EXISTS (SELECT 1 FROM payment_intents WHERE surcharge_amount > 0) THEN
    RAISE EXCEPTION '已有收过手续费的支付，去掉 surcharge_amount 会丢失手续费拆分，不能回退 00042';
  END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE payments DROP CONSTRAINT payments_surcharge, DROP COLUMN surcharge_amount;
ALTER TABLE payment_intents DROP CONSTRAINT payment_intents_surcharge, DROP COLUMN surcharge_amount;

ALTER TABLE payment_providers
  DROP CONSTRAINT payment_providers_offline_no_fee,
  DROP COLUMN fee_percent,
  DROP COLUMN fee_fixed;

ALTER TABLE payment_providers DROP CONSTRAINT payment_providers_adapter_check;
ALTER TABLE payment_providers ADD CONSTRAINT payment_providers_adapter_check
  CHECK (adapter IN ('epay', 'offline'));
