-- R100（D-E-3 方案 a）：套餐加「卖点」列表与「推荐」标记，后台编辑、门户套餐卡显示。
--
-- highlights 是有序的短句列表：最多 5 条，每条去首尾空白后 1–40 字、不许空串
-- 与重复，这些逐条规则由 adminops 校验并给出 highlights.{i} 字段错误；数据库
-- 只兜住两条结构性的：条数上限与不含 NULL 元素。recommended 与流量包同名，
-- 多个套餐可以同时推荐，不做互斥。
--
-- 两列都带默认值，存量套餐即「没有卖点、不推荐」，不需要回填。

-- +goose Up

-- +goose StatementBegin
ALTER TABLE plans
  ADD COLUMN highlights text[] NOT NULL DEFAULT '{}',
  ADD COLUMN recommended boolean NOT NULL DEFAULT false,
  ADD CONSTRAINT plans_highlights_bounded CHECK (
    cardinality(highlights) <= 5 AND array_position(highlights, NULL) IS NULL
  );
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
ALTER TABLE plans
  DROP CONSTRAINT IF EXISTS plans_highlights_bounded,
  DROP COLUMN IF EXISTS recommended,
  DROP COLUMN IF EXISTS highlights;
-- +goose StatementEnd
