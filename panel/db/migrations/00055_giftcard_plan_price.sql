-- +goose Up
-- giftcard：套餐卡必须指定价格档（用户 2026-10-16 定：送订阅套餐要选一档周期价；一次性套餐只有一个价，也指向它）。
-- 原来 price_id 可空、空时按一个月开通；套餐两类化后「一个月」对一次性套餐没有意义，改为必填。属 giftcard 模块。
-- 送的流量（通用卡、盲盒奖品）改为当期有效，加到那一份订阅本周期的 bonus_value（00053），表结构不变；
-- 兑换流水的 subscription_id 不再有「流量包记为未分配」的情形（没有可挂的那一份时由 Go 拒绝兑换）。
-- 兑换流水的 plan_mode 加 'stacked'：套餐卡叠加到同款一次性套餐（10-16 落点模型），不借用 renewed。
--
-- 锁与耗时：gift_card_templates 加 CHECK 要扫一遍全表（模板很少），瞬间完成。
-- 预检（下面的 DO 块会再核一遍）：SELECT count(*) FROM gift_card_templates WHERE type = 'plan' AND price_id IS NULL;
--   应为 0；有就拒绝迁移：先在后台给这些套餐卡选好价格档（或归档它们），再迁移。
--   gift_card_redemptions 换 plan_mode 的 CHECK 也要扫一遍全表（兑换流水，量级同兑换次数），持 ACCESS EXCLUSIVE 到提交；
--   大库上按兑换量估时，超过 lock_timeout 就整笔回滚、低峰重试。
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

ALTER TABLE gift_card_redemptions
  DROP CONSTRAINT gift_card_redemptions_plan_mode_check,
  ADD CONSTRAINT gift_card_redemptions_plan_mode_check CHECK (plan_mode IN ('new', 'renewed', 'changed', 'stacked'));

-- +goose Down
-- 去掉套餐卡必选价的 CHECK；plan_mode 退回三种。已有 stacked 流水的放不下，守卫拒绝（叠加是新语义，改记 renewed 会丢信息）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM gift_card_redemptions WHERE plan_mode = 'stacked') THEN
    RAISE EXCEPTION '有礼品卡叠加兑换的流水（plan_mode = stacked），退回前的结构放不下';
  END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE gift_card_redemptions
  DROP CONSTRAINT gift_card_redemptions_plan_mode_check,
  ADD CONSTRAINT gift_card_redemptions_plan_mode_check CHECK (plan_mode IN ('new', 'renewed', 'changed'));

ALTER TABLE gift_card_templates DROP CONSTRAINT gift_card_templates_plan_needs_price;
