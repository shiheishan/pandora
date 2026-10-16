-- +goose Up
-- catalog：套餐只分两类（用户 2026-10-16 定，见 .claude/rules/redesign-2026-10-16.md「套餐」）。属 catalog 模块。
--   1. plans.kind：subscription 订阅套餐（按时间算，月 / 季 / 半年 / 年四档价，每周期一份流量）与
--      one_time 一次性套餐（按流量算，一个价格 + 一份总流量，有效天数可选）。建好后不改类型：
--      价格经复合外键 (plan_id, plan_kind) 指向 plans (id, kind)，有价格就改不了。一次性套餐不能续费（CHECK）。
--      存量按价格回填：只有一次性价（在售的；没有在售价时看全部）的记 one_time，其余记 subscription。
--   2. 流量重置挂在套餐上：plan_reset_prices 并进 plans.reset_price（空 = 不卖重置），新增 reset_limit
--      次数上限（空 = 不限；订阅按每周期计，一次性按有效期内计，计数在 Go 里按 traffic_reset_logs 算）。
--   3. 可见范围去掉 invite_only（设计稿只有所有人 / 登录用户 / 指定用户组 / 隐藏）：存量改成 hidden（往收紧的方向）。
--   4. 删 plans 上设计稿没有的列：卖点 highlights、上下架时间窗 visible_from / visible_until、排序 sort_order、
--      每人限购 purchase_limit_per_user、库存 stock_total / stock_reserved / stock_sold。这些限制随功能一起取消。
--   5. 删 plan_versions 上的 quota_reset_strategy / quota_reset_day（清零方式由套餐类型决定：订阅按周期、一次性不清零）、
--      max_concurrent（只有 catalog 读写，死字段）、device_release_hours、renewal_extends_period / renewal_resets_quota /
--      renewal_keeps_addons、notes。
--   6. 价格：
--      - 加 plan_kind（随套餐）与 validity_days（一次性套餐的有效天数，空 = 永久；只影响之后的购买）。
--      - 在售价的形状：订阅只许 (month,1) (month,3) (month,6) (year,1) 四档，一次性只许 (one_time,1)。
--      - 删用户组专属价 user_group_id 与价格时间窗 valid_from / valid_until。
--      - 放不下的在售价先归档（组专属价、有时间窗的、周期不在四档里的、与套餐类型不符的）：价格只追加，
--        归档是唯一合法的改动；本事务里临时停掉 prices_guard 做回填与归档，做完恢复（DISABLE TRIGGER 是事务性的）。
--      - prices_one_active_offer 去掉 user_group_id 重建；catalog_guard_price 退回 00008 的写法（删组放行那段随列删除）。
--   7. 线路（节点池）从按版本绑定改为按套餐绑定，改动立即生效，去掉冻结守卫：
--      存量取每个套餐当前版本（没有当前版本时取版本号最大的那个）的绑定。被套餐引用的池仍删不掉（RESTRICT）。
--   8. 删流量包商品表 traffic_packs（订单行对它的引用已由 00052 删掉）。
-- 还没有生产部署（用户 10-09 确认），不留兼容、不分两步删列。本迁移基于 00051（已去掉币种）之后的结构。
--
-- 锁与耗时（一个事务，锁持到提交）：要 plans、plan_versions、prices、plan_reset_prices、plan_node_pools、traffic_packs
--   的 ACCESS EXCLUSIVE，挡住下单与目录读写。目录表都很小（几十到几百行），删列只改目录，回填与建索引瞬间完成。
--   部署顺序：先停 serve / worker，再 migrate，再起新进程。
-- 预检（可选，看会变什么）：
--   SELECT id, code, visibility FROM plans WHERE visibility = 'invite_only';
--   SELECT count(*) FROM prices WHERE status = 'active' AND (user_group_id IS NOT NULL OR valid_from IS NOT NULL
--     OR valid_until IS NOT NULL OR (billing_interval, interval_count) NOT IN
--       (('month', 1), ('month', 3), ('month', 6), ('year', 1), ('one_time', 1)));   -- 这些会被归档
--   被归档的价格若正被套餐卡模板引用，兑换时会被拒（giftcard 提示价格已下架，管理员重选价格后可兑）：
--   SELECT t.id, t.name FROM gift_card_templates t JOIN prices p ON p.id = t.price_id
--    WHERE t.type = 'plan' AND p.status = 'active' AND (p.user_group_id IS NOT NULL OR p.valid_from IS NOT NULL
--      OR p.valid_until IS NOT NULL OR (p.billing_interval, p.interval_count) NOT IN
--        (('month', 1), ('month', 3), ('month', 6), ('year', 1), ('one_time', 1)));
-- 失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库停在上一版，排查后重试。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- 1. 套餐类型
ALTER TABLE plans ADD COLUMN kind text NOT NULL DEFAULT 'subscription' CHECK (kind IN ('subscription', 'one_time'));
UPDATE plans p SET kind = 'one_time'
 WHERE (EXISTS (SELECT 1 FROM prices r WHERE r.plan_id = p.id AND r.status = 'active' AND r.billing_interval = 'one_time')
        AND NOT EXISTS (SELECT 1 FROM prices r WHERE r.plan_id = p.id AND r.status = 'active'
                                                AND r.billing_interval <> 'one_time'))
    OR (NOT EXISTS (SELECT 1 FROM prices r WHERE r.plan_id = p.id AND r.status = 'active')
        AND EXISTS (SELECT 1 FROM prices r WHERE r.plan_id = p.id AND r.billing_interval = 'one_time')
        AND NOT EXISTS (SELECT 1 FROM prices r WHERE r.plan_id = p.id AND r.billing_interval <> 'one_time'));
-- 新建套餐必须明说类型
ALTER TABLE plans ALTER COLUMN kind DROP DEFAULT;
UPDATE plans SET allow_renewal = false WHERE kind = 'one_time' AND allow_renewal;
ALTER TABLE plans
  -- 给价格的 (plan_id, plan_kind) 复合外键用
  ADD CONSTRAINT plans_id_kind_key UNIQUE (id, kind),
  ADD CONSTRAINT plans_one_time_no_renewal CHECK (kind <> 'one_time' OR NOT allow_renewal);

-- 2. 流量重置
ALTER TABLE plans
  -- 不收 0：0 元的重置等于让用户免费清零；要送就由后台手动重置
  ADD COLUMN reset_price numeric(20,2) CHECK (reset_price IS NULL OR reset_price > 0),
  ADD COLUMN reset_limit int CHECK (reset_limit IS NULL OR reset_limit BETWEEN 1 AND 1000),
  ADD CONSTRAINT plans_reset_limit_needs_price CHECK (reset_limit IS NULL OR reset_price IS NOT NULL);
UPDATE plans p SET reset_price = r.unit_amount FROM plan_reset_prices r WHERE r.plan_id = p.id;
DROP TABLE plan_reset_prices;

-- 3. 可见范围
UPDATE plans SET visibility = 'hidden' WHERE visibility = 'invite_only';
ALTER TABLE plans DROP CONSTRAINT plans_visibility_check;
ALTER TABLE plans ADD CONSTRAINT plans_visibility_check
  CHECK (visibility IN ('public', 'authenticated', 'group', 'hidden'));

-- 4. plans 上设计稿没有的列
DROP INDEX plans_sort_idx;
ALTER TABLE plans
  DROP CONSTRAINT plans_stock_capacity,
  DROP CONSTRAINT plans_visible_window,
  DROP CONSTRAINT plans_highlights_shape,
  DROP COLUMN highlights,
  DROP COLUMN visible_from,
  DROP COLUMN visible_until,
  DROP COLUMN sort_order,
  DROP COLUMN purchase_limit_per_user,
  DROP COLUMN stock_total,
  DROP COLUMN stock_reserved,
  DROP COLUMN stock_sold;

-- 5. plan_versions 上的多余列（ALTER TABLE 不触发版本冻结守卫）
ALTER TABLE plan_versions
  DROP CONSTRAINT plan_versions_reset_day_exact,
  DROP COLUMN quota_reset_strategy,
  DROP COLUMN quota_reset_day,
  DROP COLUMN max_concurrent,
  DROP COLUMN device_release_hours,
  DROP COLUMN renewal_extends_period,
  DROP COLUMN renewal_resets_quota,
  DROP COLUMN renewal_keeps_addons,
  DROP COLUMN notes;

-- 6. 价格
ALTER TABLE prices DISABLE TRIGGER prices_guard;

ALTER TABLE prices
  ADD COLUMN plan_kind text,
  ADD COLUMN validity_days int CHECK (validity_days IS NULL OR validity_days BETWEEN 1 AND 36500);
UPDATE prices r SET plan_kind = p.kind FROM plans p WHERE p.id = r.plan_id;
ALTER TABLE prices ALTER COLUMN plan_kind SET NOT NULL;

-- 新结构放不下的在售价归档（与 prices_guard 允许的唯一改动同形：active → archived，row_version 加一）
UPDATE prices SET status = 'archived', row_version = row_version + 1
 WHERE status = 'active'
   AND (user_group_id IS NOT NULL OR valid_from IS NOT NULL OR valid_until IS NOT NULL
        OR NOT ((plan_kind = 'subscription'
                 AND (billing_interval, interval_count) IN (('month', 1), ('month', 3), ('month', 6), ('year', 1)))
             OR (plan_kind = 'one_time' AND billing_interval = 'one_time' AND interval_count = 1)));

DROP INDEX prices_one_active_offer;
ALTER TABLE prices
  DROP CONSTRAINT prices_validity_window,
  DROP COLUMN user_group_id,
  DROP COLUMN valid_from,
  DROP COLUMN valid_until,
  ADD CONSTRAINT prices_plan_kind_fk FOREIGN KEY (plan_id, plan_kind)
    REFERENCES plans (id, kind) ON DELETE RESTRICT,
  -- 有效天数只属于一次性价
  ADD CONSTRAINT prices_validity_one_time CHECK (validity_days IS NULL OR billing_interval = 'one_time'),
  -- 在售价的形状；归档的历史价不受限（价格除状态外不可改，在售时合规，归档后也就合规）
  ADD CONSTRAINT prices_offer_shape CHECK (status = 'archived'
    OR (plan_kind = 'subscription'
        AND (billing_interval, interval_count) IN (('month', 1), ('month', 3), ('month', 6), ('year', 1)))
    OR (plan_kind = 'one_time' AND billing_interval = 'one_time' AND interval_count = 1));

-- 同一套餐、周期只有一档在售价（一次性套餐因此只有一个在售价）
CREATE UNIQUE INDEX prices_one_active_offer ON prices (plan_id, billing_interval, interval_count)
  WHERE status = 'active';

-- 价格：不可删；除状态与 row_version 外任何列都不可改；状态只许 active → archived，且 row_version 恰好加一。
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION catalog_guard_price() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION '价格 % 只能归档，不能删除', OLD.id USING ERRCODE = 'restrict_violation';
  END IF;
  IF (to_jsonb(NEW) - ARRAY['status', 'row_version']) IS DISTINCT FROM (to_jsonb(OLD) - ARRAY['status', 'row_version']) THEN
    RAISE EXCEPTION '价格 % 不可修改，请归档后新建', OLD.id USING ERRCODE = 'restrict_violation';
  END IF;
  IF NOT (OLD.status = 'active' AND NEW.status = 'archived' AND NEW.row_version = OLD.row_version + 1) THEN
    RAISE EXCEPTION '价格 % 只能从在售归档一次', OLD.id USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

ALTER TABLE prices ENABLE TRIGGER prices_guard;

-- 7. 线路按套餐绑定
ALTER TABLE plan_node_pools RENAME TO plan_node_pools_by_version;
ALTER INDEX plan_node_pools_pkey RENAME TO plan_node_pools_by_version_pkey;
ALTER INDEX plan_node_pools_pool_idx RENAME TO plan_node_pools_by_version_pool_idx;

-- 套餐删除时连带删掉绑定（与 plan_visible_groups 一样）；池被套餐引用时删不掉
CREATE TABLE plan_node_pools (
  plan_id    uuid NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
  pool_id    uuid NOT NULL REFERENCES node_pools(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (plan_id, pool_id)
);
CREATE INDEX plan_node_pools_pool_idx ON plan_node_pools (pool_id);

INSERT INTO plan_node_pools (plan_id, pool_id)
SELECT p.id, b.pool_id
  FROM plans p
  JOIN LATERAL (
    SELECT v.id FROM plan_versions v
     WHERE v.plan_id = p.id
     ORDER BY (v.id = p.current_version_id) DESC, v.version DESC
     LIMIT 1) src ON true
  JOIN plan_node_pools_by_version b ON b.plan_version_id = src.id;

DROP TABLE plan_node_pools_by_version;

-- 8. 流量包商品
DROP TABLE traffic_packs;

-- +goose Down
-- 逐项退回上一版（00051 之后）的结构。
-- 放不下、要拒绝的：设了有效天数的一次性价（旧结构里一次性价只有永久一种，退回会把限期的变成永久），
--   设了重置次数上限的套餐（旧结构没有上限，退回会放宽）。有就拒绝回退。
-- 退不回原样、但不放宽的：Up 归档掉的价格保持归档；invite_only 改成的 hidden 保持 hidden；被删的卖点、时间窗、排序、
--   库存、限购、版本上的多余列按缺省值加回（一次性套餐的版本清零方式记 never，其余记 billing_cycle）；
--   一次性套餐被 Up 关掉的 allow_renewal 保持关闭。
-- 线路退回按版本绑定：套餐的绑定复制到它的每个版本（含已发布的，先插后挂冻结守卫）。
-- traffic_packs 与 plan_reset_prices 按 00051 之后的结构重建（没有币种），流量包商品为空表。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM prices WHERE validity_days IS NOT NULL) THEN
    RAISE EXCEPTION '有设了有效天数的一次性价，上一版结构放不下（会变成永久），不能回退';
  END IF;
  IF EXISTS (SELECT 1 FROM plans WHERE reset_limit IS NOT NULL) THEN
    RAISE EXCEPTION '有套餐设了流量重置次数上限，上一版结构放不下（会变成不限），不能回退';
  END IF;
END
$$;
-- +goose StatementEnd

-- 8. 流量包商品
CREATE TABLE traffic_packs (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  name          text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 60),
  traffic_bytes bigint NOT NULL CHECK (traffic_bytes > 0 AND traffic_bytes <= 1125899906842624),
  unit_amount   numeric(20,2) NOT NULL CHECK (unit_amount > 0),
  recommended   boolean NOT NULL DEFAULT false,
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
  sort_order    int NOT NULL DEFAULT 0,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX traffic_packs_catalog_idx ON traffic_packs (status, sort_order, created_at, id);
CREATE TRIGGER traffic_packs_deny_delete
  BEFORE DELETE ON traffic_packs FOR EACH ROW EXECUTE FUNCTION deny_delete();
CREATE TRIGGER traffic_packs_deny_truncate
  BEFORE TRUNCATE ON traffic_packs FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

-- 7. 线路按版本绑定
ALTER TABLE plan_node_pools RENAME TO plan_node_pools_by_plan;
ALTER INDEX plan_node_pools_pkey RENAME TO plan_node_pools_by_plan_pkey;
ALTER INDEX plan_node_pools_pool_idx RENAME TO plan_node_pools_by_plan_pool_idx;

CREATE TABLE plan_node_pools (
  plan_version_id uuid NOT NULL REFERENCES plan_versions(id) ON DELETE CASCADE,
  pool_id         uuid NOT NULL REFERENCES node_pools(id) ON DELETE RESTRICT,
  PRIMARY KEY (plan_version_id, pool_id)
);
CREATE INDEX plan_node_pools_pool_idx ON plan_node_pools (pool_id);

INSERT INTO plan_node_pools (plan_version_id, pool_id)
SELECT v.id, b.pool_id
  FROM plan_node_pools_by_plan b
  JOIN plan_versions v ON v.plan_id = b.plan_id;

CREATE TRIGGER plan_node_pools_guard
  BEFORE INSERT OR UPDATE OR DELETE ON plan_node_pools
  FOR EACH ROW EXECUTE FUNCTION catalog_guard_version_child();

DROP TABLE plan_node_pools_by_plan;

-- 6. 价格（只加列、换约束，不 UPDATE 价格行，不用停守卫）
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION catalog_guard_price() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF OLD.user_group_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM user_groups WHERE id = OLD.user_group_id) THEN
      RETURN OLD;
    END IF;
    RAISE EXCEPTION '价格 % 只能归档，不能删除', OLD.id USING ERRCODE = 'restrict_violation';
  END IF;
  IF (to_jsonb(NEW) - ARRAY['status', 'row_version']) IS DISTINCT FROM (to_jsonb(OLD) - ARRAY['status', 'row_version']) THEN
    RAISE EXCEPTION '价格 % 不可修改，请归档后新建', OLD.id USING ERRCODE = 'restrict_violation';
  END IF;
  IF NOT (OLD.status = 'active' AND NEW.status = 'archived' AND NEW.row_version = OLD.row_version + 1) THEN
    RAISE EXCEPTION '价格 % 只能从在售归档一次', OLD.id USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP INDEX prices_one_active_offer;
ALTER TABLE prices
  DROP CONSTRAINT prices_offer_shape,
  DROP CONSTRAINT prices_validity_one_time,
  DROP CONSTRAINT prices_plan_kind_fk,
  DROP COLUMN validity_days,
  DROP COLUMN plan_kind,
  ADD COLUMN user_group_id uuid,
  ADD COLUMN valid_from timestamptz,
  ADD COLUMN valid_until timestamptz,
  ADD CONSTRAINT prices_user_group_id_fkey FOREIGN KEY (user_group_id)
    REFERENCES user_groups(id) ON DELETE CASCADE,
  ADD CONSTRAINT prices_validity_window CHECK (valid_from IS NULL OR valid_until IS NULL OR valid_until > valid_from);
CREATE INDEX prices_group_idx ON prices (user_group_id) WHERE user_group_id IS NOT NULL;
CREATE UNIQUE INDEX prices_one_active_offer ON prices (plan_id, billing_interval, interval_count, user_group_id)
  NULLS NOT DISTINCT WHERE status = 'active';

-- 5. plan_versions 的列（ALTER TABLE 不触发冻结守卫；给一次性套餐的版本改清零方式要临时停守卫）
ALTER TABLE plan_versions
  ADD COLUMN quota_reset_strategy text NOT NULL DEFAULT 'billing_cycle'
    CHECK (quota_reset_strategy IN ('never', 'natural_month', 'billing_cycle', 'fixed_day')),
  ADD COLUMN quota_reset_day smallint CHECK (quota_reset_day IS NULL OR quota_reset_day BETWEEN 1 AND 28),
  ADD COLUMN renewal_extends_period boolean NOT NULL DEFAULT true,
  ADD COLUMN renewal_resets_quota boolean NOT NULL DEFAULT true,
  ADD COLUMN renewal_keeps_addons boolean NOT NULL DEFAULT true,
  ADD COLUMN max_concurrent int CHECK (max_concurrent IS NULL OR max_concurrent > 0),
  ADD COLUMN device_release_hours int NOT NULL DEFAULT 24 CHECK (device_release_hours >= 0),
  ADD COLUMN notes text CHECK (char_length(notes) <= 2000),
  ADD CONSTRAINT plan_versions_reset_day_exact CHECK ((quota_reset_strategy = 'fixed_day') = (quota_reset_day IS NOT NULL));
ALTER TABLE plan_versions DISABLE TRIGGER plan_versions_guard;
UPDATE plan_versions v SET quota_reset_strategy = 'never'
  FROM plans p WHERE p.id = v.plan_id AND p.kind = 'one_time';
ALTER TABLE plan_versions ENABLE TRIGGER plan_versions_guard;

-- 4. plans 上的列
ALTER TABLE plans
  ADD COLUMN visible_from timestamptz,
  ADD COLUMN visible_until timestamptz,
  ADD COLUMN purchase_limit_per_user int CHECK (purchase_limit_per_user IS NULL OR purchase_limit_per_user > 0),
  ADD COLUMN stock_total int CHECK (stock_total IS NULL OR stock_total >= 0),
  ADD COLUMN stock_reserved int NOT NULL DEFAULT 0 CHECK (stock_reserved >= 0),
  ADD COLUMN stock_sold int NOT NULL DEFAULT 0 CHECK (stock_sold >= 0),
  ADD COLUMN highlights jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN sort_order int NOT NULL DEFAULT 0,
  ADD CONSTRAINT plans_stock_capacity CHECK (stock_total IS NULL OR stock_sold + stock_reserved <= stock_total),
  ADD CONSTRAINT plans_visible_window CHECK (visible_from IS NULL OR visible_until IS NULL OR visible_until > visible_from),
  ADD CONSTRAINT plans_highlights_shape CHECK (
    jsonb_typeof(highlights) = 'array'
    AND jsonb_array_length(highlights) <= 5
    AND NOT jsonb_path_exists(highlights, '$[*] ? (@.type() != "string")')
  );
CREATE INDEX plans_sort_idx ON plans (sort_order, created_at, id);

-- 3. 可见范围
ALTER TABLE plans DROP CONSTRAINT plans_visibility_check;
ALTER TABLE plans ADD CONSTRAINT plans_visibility_check
  CHECK (visibility IN ('public', 'authenticated', 'group', 'invite_only', 'hidden'));

-- 2. 流量重置价格表
CREATE TABLE plan_reset_prices (
  plan_id     uuid NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
  unit_amount numeric(20,2) NOT NULL CHECK (unit_amount > 0),
  created_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT plan_reset_prices_pkey PRIMARY KEY (plan_id)
);
INSERT INTO plan_reset_prices (plan_id, unit_amount)
SELECT id, reset_price FROM plans WHERE reset_price IS NOT NULL;
ALTER TABLE plans
  DROP CONSTRAINT plans_reset_limit_needs_price,
  DROP COLUMN reset_limit,
  DROP COLUMN reset_price;

-- 1. 套餐类型（价格的复合外键已在上面删掉）
ALTER TABLE plans
  DROP CONSTRAINT plans_one_time_no_renewal,
  DROP CONSTRAINT plans_id_kind_key,
  DROP COLUMN kind;
