-- +goose Up

-- 线下收款渠道。
--
-- 「标记已支付」（XBD-015）需要一个渠道来承载那笔收款记录：管理员确认用户
-- 银行转账到了，这笔钱要进 payments 表、进账本，而不能凭空让订单变成已支付。
-- 复用既有的回调结算链路是最省心的做法 —— 订阅开通、优惠券核销、余额解冻、
-- 佣金计提全都跟着走，不会出现「线下付的单少了个环节」。
--
-- accepting_new = false：用户在收银台选不到它。
-- credentials_encrypted 给空 bytea：这个渠道没有外部密钥，不需要信封加密，
-- 但列上有非空约束。

-- +goose StatementBegin
INSERT INTO payment_providers
  (tenant_id, code, adapter, display_name, credentials_encrypted,
   supported_currencies, config, enabled, accepting_new)
SELECT t.id, 'offline', 'offline', '线下收款', ''::bytea,
       ARRAY['CNY','USD']::text[], '{}'::jsonb, true, false
  FROM tenants t
ON CONFLICT (tenant_id, code) DO NOTHING;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DELETE FROM payment_providers WHERE code = 'offline';
-- +goose StatementEnd
