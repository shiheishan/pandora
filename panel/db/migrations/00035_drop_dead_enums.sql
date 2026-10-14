-- +goose Up
-- 去掉两个从来不会写入的枚举值（死代码清理）：
--   payment_providers.adapter 的 'demo_hmac'：只给测试用的 HMAC 支付适配器已删，后台也只建得出 epay；
--   order_reservation_events.event_kind 的 'release'：预留释放只走超时（expire）与取消（cancel）两条路，
--     Go 里从没写过 release（连同 order_reservation_events_shape 里的同一个值一起去掉）。
-- 两处都在 CHECK 上收窄取值，不动数据；payment_providers 归 payment 模块，order_reservation_events 归 order 模块。
--
-- 锁与耗时（整个迁移在一个事务里，锁持到提交）：
--   两张表各在 ACCESS EXCLUSIVE 下整表校验一遍新 CHECK。payment_providers 只有几行；
--   order_reservation_events 按订单数增长（每单两行），校验耗时与行数成正比，挡住这段时间的下单与结算。
--   预检：上线前跑
--     SELECT count(*) FROM payment_providers WHERE adapter = 'demo_hmac';            -- 应为 0
--     SELECT count(*) FROM order_reservation_events WHERE event_kind = 'release';     -- 应为 0
--     SELECT count(*) FROM order_reservation_events;                                  -- 看校验要扫多少行
--   行数到百万级时改成 NO TRANSACTION 迁移：ADD CONSTRAINT … NOT VALID 后另起语句 VALIDATE（不挡读写）。
--   目前还没有生产库，按一个事务做。
--   失败回滚：任一步失败（含 lock_timeout 超时、预检里不为 0 的行让新 CHECK 校验失败）整笔回滚，库保持原样；
--   校验失败先查清那几行从哪来，再重试。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE payment_providers DROP CONSTRAINT payment_providers_adapter_check;
ALTER TABLE payment_providers ADD CONSTRAINT payment_providers_adapter_check
  CHECK (adapter IN ('epay', 'offline'));

ALTER TABLE order_reservation_events DROP CONSTRAINT order_reservation_events_event_kind_check;
ALTER TABLE order_reservation_events ADD CONSTRAINT order_reservation_events_event_kind_check
  CHECK (event_kind IN ('reserve', 'capture', 'expire', 'cancel'));

ALTER TABLE order_reservation_events DROP CONSTRAINT order_reservation_events_shape;
ALTER TABLE order_reservation_events ADD CONSTRAINT order_reservation_events_shape CHECK (
  (event_kind = 'reserve' AND from_state IS NULL AND to_state = 'held')
  OR (event_kind = 'capture' AND from_state = 'held' AND to_state = 'captured')
  OR (event_kind IN ('expire', 'cancel') AND from_state = 'held' AND to_state = 'released'));

-- +goose Down
-- 退回 00012 / 00013 的原样：取值放宽，现有行一定满足，无需数据守卫。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE order_reservation_events DROP CONSTRAINT order_reservation_events_shape;
ALTER TABLE order_reservation_events ADD CONSTRAINT order_reservation_events_shape CHECK (
  (event_kind = 'reserve' AND from_state IS NULL AND to_state = 'held')
  OR (event_kind = 'capture' AND from_state = 'held' AND to_state = 'captured')
  OR (event_kind IN ('release', 'expire', 'cancel') AND from_state = 'held' AND to_state = 'released'));

ALTER TABLE order_reservation_events DROP CONSTRAINT order_reservation_events_event_kind_check;
ALTER TABLE order_reservation_events ADD CONSTRAINT order_reservation_events_event_kind_check
  CHECK (event_kind IN ('reserve', 'capture', 'release', 'expire', 'cancel'));

ALTER TABLE payment_providers DROP CONSTRAINT payment_providers_adapter_check;
ALTER TABLE payment_providers ADD CONSTRAINT payment_providers_adapter_check
  CHECK (adapter IN ('epay', 'demo_hmac', 'offline'));
