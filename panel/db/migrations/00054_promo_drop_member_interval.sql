-- +goose Up
-- promo：删会员折扣 member_discounts 与券的限定周期 coupon_intervals（00046 建；用户 2026-10-16 定，
-- 设计稿没有这两样，见 .claude/rules/redesign-2026-10-16.md「套餐」）。属 promo 模块。
-- 注意：限定了周期的券删表后对所有周期可用（限制随功能一起取消，是用户定的产品口径）；
-- 后台设过的会员折扣随表丢弃，之后下单不再打折。还没有生产部署（用户 10-09 确认）。
--
-- 锁与耗时：两张小表直接 DROP，要它们的 ACCESS EXCLUSIVE，瞬间完成。
--   部署顺序：先停 serve / worker，再 migrate，再起新进程（旧进程下单会读这两张表）。
-- 预检（可选，看会丢什么）：SELECT count(*) FROM member_discounts; SELECT count(DISTINCT coupon_id) FROM coupon_intervals;
-- 失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库停在上一版，重试即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

DROP TABLE coupon_intervals;
DROP TABLE member_discounts;

-- +goose Down
-- 按 00046 原文重建两张表（空表：会员折扣与券的周期限定无从恢复，退回后没有折扣、券不限周期，
-- 与 Up 之后的行为一致，不会放宽）。不需要数据守卫。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

CREATE TABLE member_discounts (
  user_id    uuid PRIMARY KEY REFERENCES users(id) ON DELETE RESTRICT,
  -- 减免百分比：10 = 打九折，100 = 免费（与 Xboard 同语义）
  percent    smallint NOT NULL CHECK (percent BETWEEN 1 AND 100),
  updated_by uuid REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX member_discounts_updated_by_idx ON member_discounts (updated_by) WHERE updated_by IS NOT NULL;

CREATE TABLE coupon_intervals (
  coupon_id        uuid NOT NULL REFERENCES coupons(id) ON DELETE CASCADE,
  billing_interval text NOT NULL
                     CHECK (billing_interval IN ('day', 'week', 'month', 'quarter', 'year', 'one_time')),
  PRIMARY KEY (coupon_id, billing_interval)
);
