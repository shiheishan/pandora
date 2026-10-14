-- +goose Up
-- 去掉 payment_providers.adapter 里从来不会写入的 'demo_hmac'（死代码清理）：只给测试用的 HMAC 支付适配器已删，
-- 后台也只建得出 epay。只收窄 CHECK，不动数据；表归 payment 模块。
--
-- 锁与耗时：ACCESS EXCLUSIVE 下整表校验一遍新 CHECK，表只有几行，瞬间完成。
-- 预检：SELECT count(*) FROM payment_providers WHERE adapter = 'demo_hmac';  -- 应为 0
-- 失败回滚：任一步失败（含 lock_timeout 超时、预检不为 0 让校验失败）整笔回滚，库保持原样。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE payment_providers DROP CONSTRAINT payment_providers_adapter_check;
ALTER TABLE payment_providers ADD CONSTRAINT payment_providers_adapter_check
  CHECK (adapter IN ('epay', 'offline'));

-- +goose Down
-- 退回 00012 的原样：取值放宽，现有行一定满足。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE payment_providers DROP CONSTRAINT payment_providers_adapter_check;
ALTER TABLE payment_providers ADD CONSTRAINT payment_providers_adapter_check
  CHECK (adapter IN ('epay', 'demo_hmac', 'offline'));
