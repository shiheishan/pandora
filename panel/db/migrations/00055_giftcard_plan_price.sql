-- +goose Up
-- giftcard：套餐卡必须指定价格档（用户 2026-10-16 定：送订阅套餐要选一档周期价；一次性套餐只有一个价，也指向它）。
-- 原来 price_id 可空、空时按一个月开通；套餐两类化后「一个月」对一次性套餐没有意义，改为必填。属 giftcard 模块。
-- 送的流量（通用卡、盲盒奖品）改为当期有效，加到那一份订阅本周期的 bonus_value（00053），表结构不变；
-- 兑换流水的 subscription_id 不再有「流量包记为未分配」的情形（没有可挂的那一份时由 Go 拒绝兑换）。
--
-- 锁与耗时：gift_card_templates 加 CHECK 要扫一遍全表（模板很少），瞬间完成。
-- 预检（下面的 DO 块会再核一遍）：SELECT count(*) FROM gift_card_templates WHERE type = 'plan' AND price_id IS NULL;
--   应为 0；有就拒绝迁移：先在后台给这些套餐卡选好价格档（或归档它们），再迁移。
-- 失败回滚：任一步失败（含 lock_timeout 超时、预检不过）整笔回滚，库停在上一版。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM gift_card_templates WHERE type = 'plan' AND price_id IS NULL) THEN
    RAISE EXCEPTION '有套餐卡模板没选价格档，先给它们选好价格档再迁移';
  END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE gift_card_templates
  ADD CONSTRAINT gift_card_templates_plan_needs_price CHECK (type <> 'plan' OR price_id IS NOT NULL);

-- +goose Down
-- 只去掉这条 CHECK，退回后都放得下。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

ALTER TABLE gift_card_templates DROP CONSTRAINT gift_card_templates_plan_needs_price;
