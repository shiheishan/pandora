-- +goose Up
-- catalog 模块：套餐、套餐可见的用户组、套餐版本、权益、配额定义、价格、流量包。
--
-- 与旧表相比：去掉租户列与 RLS；不再有与套餐一一对应的商品表，价格直接挂在套餐上；
-- 金额改为 NUMERIC(20,2)（元），不加「x = round(x, 2)」的 CHECK：该类型先舍入再查约束，那条永远成立，
-- 多余小数只靠 repo 写入前的 money.Check 拦下；套餐可见的用户组从 uuid 数组改为关联表，删组时由外键挡住；
-- 卖点从 text[] 改为 jsonb 数组；版本状态只留有写入方的 draft、published；超额策略只有「停止服务」一种，不再建列。
-- 状态转换的规则在 Go（service 的转换表）；库里只兜「已发布快照不可改、价格只能归档」与行级约束。

-- 套餐。库存三列：stock_total 为空表示不限量；预留与已售由 catalog service 的单条 UPDATE 维护。
CREATE TABLE plans (
  id                      uuid PRIMARY KEY DEFAULT uuidv7(),
  code                    text NOT NULL CHECK (code ~ '^[a-z0-9][a-z0-9_-]{1,63}$'),
  name                    text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 120),
  description             text CHECK (char_length(description) <= 2000),
  status                  text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'archived')),
  -- 当前对外销售的版本；外键（同套餐）在 plan_versions 建好后补
  current_version_id      uuid,
  visibility              text NOT NULL DEFAULT 'public'
                            CHECK (visibility IN ('public', 'authenticated', 'group', 'invite_only', 'hidden')),
  visible_from            timestamptz,
  visible_until           timestamptz,
  allow_new_purchase      boolean NOT NULL DEFAULT true,
  allow_renewal           boolean NOT NULL DEFAULT true,
  allow_upgrade           boolean NOT NULL DEFAULT true,
  -- 空 = 不限购；否则为单个用户累计可新购的次数（计数在 order 模块）
  purchase_limit_per_user int CHECK (purchase_limit_per_user IS NULL OR purchase_limit_per_user > 0),
  stock_total             int CHECK (stock_total IS NULL OR stock_total >= 0),
  stock_reserved          int NOT NULL DEFAULT 0 CHECK (stock_reserved >= 0),
  stock_sold              int NOT NULL DEFAULT 0 CHECK (stock_sold >= 0),
  -- 卖点：字符串数组，最多 5 条；逐条规则（去空白、1–40 字、不重复）在 Go
  highlights              jsonb NOT NULL DEFAULT '[]'::jsonb,
  recommended             boolean NOT NULL DEFAULT false,
  sort_order              int NOT NULL DEFAULT 0,
  -- 后台写入的乐观锁
  row_version             bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_at              timestamptz NOT NULL DEFAULT now(),
  updated_at              timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT plans_code_key UNIQUE (code),
  CONSTRAINT plans_stock_capacity CHECK (stock_total IS NULL OR stock_sold + stock_reserved <= stock_total),
  CONSTRAINT plans_visible_window CHECK (visible_from IS NULL OR visible_until IS NULL OR visible_until > visible_from),
  CONSTRAINT plans_highlights_shape CHECK (
    jsonb_typeof(highlights) = 'array'
    AND jsonb_array_length(highlights) <= 5
    AND NOT jsonb_path_exists(highlights, '$[*] ? (@.type() != "string")')
  ),
  -- 在售的套餐必须有当前版本（版本已发布由 service 保证，外键保证属于本套餐）
  CONSTRAINT plans_active_has_version CHECK (status <> 'active' OR current_version_id IS NOT NULL)
);

CREATE INDEX plans_sort_idx ON plans (sort_order, created_at, id);

-- 分组可见（visibility = 'group'）的套餐对哪些用户组可见。组里还被套餐引用时不许删组（RESTRICT）。
CREATE TABLE plan_visible_groups (
  plan_id       uuid NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
  user_group_id uuid NOT NULL REFERENCES user_groups(id) ON DELETE RESTRICT,
  created_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (plan_id, user_group_id)
);

CREATE INDEX plan_visible_groups_group_idx ON plan_visible_groups (user_group_id);

-- 套餐版本：权益的不可变快照。改额度 = 开新版本并发布，已购用户留在自己的版本上。
-- 每个套餐最多一个草稿；发布即冻结（frozen_at），之后整行与子表都不可改、不可删。
CREATE TABLE plan_versions (
  id                     uuid PRIMARY KEY DEFAULT uuidv7(),
  plan_id                uuid NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
  version                int NOT NULL CHECK (version > 0),
  status                 text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published')),
  frozen_at              timestamptz,
  quota_reset_strategy   text NOT NULL DEFAULT 'billing_cycle'
                           CHECK (quota_reset_strategy IN ('never', 'natural_month', 'billing_cycle', 'fixed_day')),
  -- fixed_day 策略的日期（1–28，避开月末歧义）
  quota_reset_day        smallint CHECK (quota_reset_day IS NULL OR quota_reset_day BETWEEN 1 AND 28),
  grace_period_hours     int NOT NULL DEFAULT 0 CHECK (grace_period_hours >= 0),
  grace_keeps_service    boolean NOT NULL DEFAULT true,
  renewal_extends_period boolean NOT NULL DEFAULT true,
  renewal_resets_quota   boolean NOT NULL DEFAULT true,
  renewal_keeps_addons   boolean NOT NULL DEFAULT true,
  max_devices            int CHECK (max_devices IS NULL OR max_devices > 0),
  max_concurrent         int CHECK (max_concurrent IS NULL OR max_concurrent > 0),
  device_release_hours   int NOT NULL DEFAULT 24 CHECK (device_release_hours >= 0),
  -- 全程生效的按用户限速（kbps），空 = 不限
  throttle_kbps          int CHECK (throttle_kbps IS NULL OR throttle_kbps > 0),
  notes                  text CHECK (char_length(notes) <= 2000),
  row_version            bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
  -- 建这个版本的管理员；账号删除时置空（冻结守卫放行这一列）
  created_by             uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at             timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT plan_versions_plan_version_key UNIQUE (plan_id, version),
  -- 给 plans.current_version_id 的复合外键用：当前版本必须属于本套餐
  CONSTRAINT plan_versions_plan_id_id_key UNIQUE (plan_id, id),
  CONSTRAINT plan_versions_frozen_shape CHECK ((status = 'draft') = (frozen_at IS NULL)),
  CONSTRAINT plan_versions_freeze_after_create CHECK (frozen_at IS NULL OR frozen_at >= created_at),
  CONSTRAINT plan_versions_reset_day_exact CHECK ((quota_reset_strategy = 'fixed_day') = (quota_reset_day IS NOT NULL))
);

CREATE UNIQUE INDEX plan_versions_one_draft ON plan_versions (plan_id) WHERE status = 'draft';

ALTER TABLE plans ADD CONSTRAINT plans_current_version_fk
  FOREIGN KEY (id, current_version_id) REFERENCES plan_versions (plan_id, id) ON DELETE RESTRICT;

-- 权益：能力码 → JSON 值，属于某个版本。
CREATE TABLE entitlements (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  plan_version_id uuid NOT NULL REFERENCES plan_versions(id) ON DELETE CASCADE,
  code            text NOT NULL CHECK (char_length(btrim(code)) BETWEEN 1 AND 64),
  value           jsonb NOT NULL DEFAULT 'true'::jsonb,
  created_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT entitlements_version_code_key UNIQUE (plan_version_id, code)
);

-- 配额定义：可计量的额度上限（traffic.bytes、devices.active 等），属于某个版本；limit_value 空 = 不限。
CREATE TABLE quota_definitions (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  plan_version_id uuid NOT NULL REFERENCES plan_versions(id) ON DELETE CASCADE,
  metric          text NOT NULL CHECK (char_length(btrim(metric)) BETWEEN 1 AND 64),
  limit_value     bigint CHECK (limit_value IS NULL OR limit_value >= 0),
  unit            text NOT NULL DEFAULT 'bytes' CHECK (char_length(btrim(unit)) BETWEEN 1 AND 32),
  period          text NOT NULL DEFAULT 'cycle' CHECK (period IN ('total', 'cycle', 'day', 'month')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT quota_definitions_version_metric_key UNIQUE (plan_version_id, metric, period)
);

-- 价格：只追加的报价记录。改价 = 归档旧的、建新的，历史订单仍指向旧的那一行。
CREATE TABLE prices (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  plan_id          uuid NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
  currency         text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  unit_amount      numeric(20,2) NOT NULL CHECK (unit_amount >= 0),
  billing_interval text NOT NULL DEFAULT 'month'
                     CHECK (billing_interval IN ('day', 'week', 'month', 'quarter', 'year', 'one_time')),
  interval_count   smallint NOT NULL DEFAULT 1 CHECK (interval_count > 0),
  -- 只存不用：试用没有实现，这里保留管理员填的值
  trial_days       smallint NOT NULL DEFAULT 0 CHECK (trial_days >= 0),
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
  -- 只对这个用户组开放；组里还被价格引用时不许删组（RESTRICT）
  user_group_id    uuid REFERENCES user_groups(id) ON DELETE RESTRICT,
  valid_from       timestamptz,
  valid_until      timestamptz,
  row_version      bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
  created_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT prices_validity_window CHECK (valid_from IS NULL OR valid_until IS NULL OR valid_until > valid_from)
);

-- 同一套餐、币种、周期、用户组（公开价的组为空，按相同处理）只有一档在售价
CREATE UNIQUE INDEX prices_one_active_offer ON prices (plan_id, currency, billing_interval, interval_count, user_group_id)
  NULLS NOT DISTINCT WHERE status = 'active';
CREATE INDEX prices_plan_idx ON prices (plan_id, created_at DESC);
CREATE INDEX prices_group_idx ON prices (user_group_id) WHERE user_group_id IS NOT NULL;

-- 流量包：在售商品。已售出的订单项快照了名称、容量与金额，所以容量与价格可以直接改；
-- 乐观锁是 updated_at（每次 UPDATE 置为 now()）。不允许删除，只能下架。
CREATE TABLE traffic_packs (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  name          text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 60),
  -- 上限 1 PiB：用户余额按笔累加，求和不会逼近 bigint
  traffic_bytes bigint NOT NULL CHECK (traffic_bytes > 0 AND traffic_bytes <= 1125899906842624),
  currency      text NOT NULL DEFAULT 'CNY' CHECK (currency IN ('CNY', 'USD')),
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

-- 目录冻结（docs/architecture.md 第 5.3 节）：Go 里同样拒绝，库里兜底。
-- 违例一律报 restrict_violation（23001），repo 映射成「已发布目录快照不可修改」。

-- 版本：新行只能是草稿；已发布的行不可改（只放行 created_by 被外键置空）、不可删；
-- 草稿只许发布成 published，不许改所属套餐与版本号。
-- +goose StatementBegin
CREATE FUNCTION catalog_guard_plan_version() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'draft' OR NEW.frozen_at IS NOT NULL THEN
      RAISE EXCEPTION '新建的套餐版本必须是草稿' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
  END IF;

  IF TG_OP = 'DELETE' THEN
    IF OLD.status <> 'draft' THEN
      RAISE EXCEPTION '已发布的套餐版本 % 不可删除', OLD.id USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN OLD;
  END IF;

  IF OLD.status <> 'draft' THEN
    IF (to_jsonb(NEW) - 'created_by') IS DISTINCT FROM (to_jsonb(OLD) - 'created_by') THEN
      RAISE EXCEPTION '已发布的套餐版本 % 不可修改，请发布新版本', OLD.id USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
  END IF;

  IF NEW.plan_id IS DISTINCT FROM OLD.plan_id OR NEW.version IS DISTINCT FROM OLD.version THEN
    RAISE EXCEPTION '套餐版本 % 的所属套餐与版本号不可修改', OLD.id USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER plan_versions_guard
  BEFORE INSERT OR UPDATE OR DELETE ON plan_versions
  FOR EACH ROW EXECUTE FUNCTION catalog_guard_plan_version();

-- 权益与配额定义是版本快照的一部分：所属版本已发布时不许增、改、删。
-- 读父行时加 FOR SHARE，与并发的发布（FOR UPDATE 锁版本行）串行。
-- 草稿被删时子行随外键级联删除，那时父行已不存在，放行。
-- +goose StatementBegin
CREATE FUNCTION catalog_guard_version_child() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  v_status text;
BEGIN
  IF TG_OP IN ('UPDATE', 'DELETE') THEN
    SELECT status INTO v_status FROM plan_versions WHERE id = OLD.plan_version_id FOR SHARE;
    IF FOUND AND v_status <> 'draft' THEN
      RAISE EXCEPTION '套餐版本 % 已发布，它的权益与配额不可修改', OLD.plan_version_id
        USING ERRCODE = 'restrict_violation';
    END IF;
  END IF;
  IF TG_OP IN ('INSERT', 'UPDATE') THEN
    SELECT status INTO v_status FROM plan_versions WHERE id = NEW.plan_version_id FOR SHARE;
    IF FOUND AND v_status <> 'draft' THEN
      RAISE EXCEPTION '套餐版本 % 已发布，它的权益与配额不可修改', NEW.plan_version_id
        USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
  END IF;
  RETURN OLD;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER entitlements_guard
  BEFORE INSERT OR UPDATE OR DELETE ON entitlements
  FOR EACH ROW EXECUTE FUNCTION catalog_guard_version_child();

CREATE TRIGGER quota_definitions_guard
  BEFORE INSERT OR UPDATE OR DELETE ON quota_definitions
  FOR EACH ROW EXECUTE FUNCTION catalog_guard_version_child();

-- 价格：不可删；除状态与 row_version 外任何列都不可改；状态只许 active → archived，且 row_version 恰好加一。
-- +goose StatementBegin
CREATE FUNCTION catalog_guard_price() RETURNS trigger
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

CREATE TRIGGER prices_guard
  BEFORE UPDATE OR DELETE ON prices
  FOR EACH ROW EXECUTE FUNCTION catalog_guard_price();

-- +goose Down
DROP TABLE IF EXISTS traffic_packs;
DROP TABLE IF EXISTS prices;
DROP TABLE IF EXISTS quota_definitions;
DROP TABLE IF EXISTS entitlements;
ALTER TABLE IF EXISTS plans DROP CONSTRAINT IF EXISTS plans_current_version_fk;
DROP TABLE IF EXISTS plan_versions;
DROP TABLE IF EXISTS plan_visible_groups;
DROP TABLE IF EXISTS plans;
DROP FUNCTION IF EXISTS catalog_guard_price();
DROP FUNCTION IF EXISTS catalog_guard_version_child();
DROP FUNCTION IF EXISTS catalog_guard_plan_version();
