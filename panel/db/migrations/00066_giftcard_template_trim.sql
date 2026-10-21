-- +goose Up
-- 礼品卡模板删掉后台建不出、改不了的字段（用户 2026-10-21 同意，后台与用户端对齐核对 3.5）。属 giftcard 模块。
--   1. 说明 description、主题色 theme_color：10-16 设计稿还原后后台新建恒发空串、没有编辑入口；门户卡面小字改为只按卡型给固定文案。
--   2. 冷却小时 cooldown_hours：后台建不出非 0 的卡，兑换时的冷却校验一并删掉（每人限兑次数照旧）。
--   3. 限定套餐 gift_card_template_plans：后台建不出带限定套餐的卡，兑换时的「套餐不在适用范围」校验一并删掉，整表删。
--   4. 需邀请 require_invite：后台建不出勾了它的卡，兑换时的「仅受邀用户」校验一并删掉（领取条件只剩仅新用户 / 仅付费用户）。
-- 只有 giftcard 的 repo 读写这些列和表。删列一步做、不分「先停用再删」两步：安装器升级时先停服务再迁移（install.sh），
-- 没有新旧二进制同时连库的窗口；现在也没有生产部署（10-09 确认）。
--
-- 锁与耗时（整个迁移在一个事务里，锁持到提交）：gift_card_templates 拿 ACCESS EXCLUSIVE，删列只改系统目录、不重写表；
--   删 gift_card_template_plans 拿它自己的 ACCESS EXCLUSIVE，并为去掉外键短暂锁 gift_card_templates 与 plans。
--   模板是后台手工建的几十行，毫秒级；期间挡住的只有礼品卡模板读写、兑换与套餐表的写。
--   预检：SELECT count(*) FROM gift_card_templates WHERE description <> '' OR theme_color <> '' OR cooldown_hours <> 0 OR require_invite;
--         SELECT count(*) FROM gift_card_template_plans;
--     非零是要随本迁移丢掉的值（Down 还原不了，见下），先告诉管理员；按现有界面这两个数都应为 0。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库保持原样，换低峰重试。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 列上的 CHECK 随列一起删
ALTER TABLE gift_card_templates
  DROP COLUMN description,
  DROP COLUMN require_invite,
  DROP COLUMN cooldown_hours,
  DROP COLUMN theme_color;

-- 外键与索引 gift_card_template_plans_plan_idx 随表删
DROP TABLE gift_card_template_plans;

-- +goose Down
-- 结构退回本迁移之前的样子（列的类型、缺省、CHECK 与约束名，限定套餐表的外键与索引逐项照 00015 原样；
-- 列加回到表尾，列序与 00015 不同，不影响按列名读写）。删掉的值还原不了：说明、主题色回填空串，需邀请回填 false，冷却回填 0，限定套餐表建回空表，
-- 正是现有后台建出来的模板的值。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE gift_card_templates
  ADD COLUMN description text NOT NULL DEFAULT ''
    CONSTRAINT gift_card_templates_description_check CHECK (char_length(description) <= 2000),
  ADD COLUMN require_invite boolean NOT NULL DEFAULT false,
  ADD COLUMN cooldown_hours int NOT NULL DEFAULT 0
    CONSTRAINT gift_card_templates_cooldown_hours_check CHECK (cooldown_hours BETWEEN 0 AND 87600),
  ADD COLUMN theme_color text NOT NULL DEFAULT ''
    CONSTRAINT gift_card_templates_theme_color_check CHECK (char_length(theme_color) <= 32);

-- 「仅持有这些套餐的用户」：没有行即不限。套餐只归档不删，外键用 RESTRICT。
CREATE TABLE gift_card_template_plans (
  template_id uuid NOT NULL REFERENCES gift_card_templates(id) ON DELETE CASCADE,
  plan_id     uuid NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
  PRIMARY KEY (template_id, plan_id)
);
CREATE INDEX gift_card_template_plans_plan_idx ON gift_card_template_plans (plan_id);
