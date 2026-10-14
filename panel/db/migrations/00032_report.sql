-- +goose Up
-- report 模块：营收报表的人工调整（revenue_report_adjustments）与后台行为趋势的按天汇总（activity_daily）。
-- 依赖 00005_identity.sql 的 users。看板的其余数字一律经属主模块 service 的聚合读，不读别人的表。
--
-- revenue_report_adjustments：只影响后台营收报表的展示值，不进账本、不改订单与支付。
--   - 只追加（deny_mutation）。撤销 = 新插一行冲正：reversal_of 指向原行、金额取反、生效日同原行。
--   - 一行至多被冲一次（reversal_of 唯一）；冲正行与原行同币种（(reversal_of, currency) 复合外键）。
--     「不能冲冲正行」「金额取反」由 report service 在咨询锁内判，唯一约束是并发时的兜底。
--   - 金额单位是元，numeric(20,2)，非零，绝对值不超过 100 亿元（与旧接口的 ±1e12 分相同）；
--     多余小数由写入前的 money.Check 拦（库里 numeric 会静默舍入，不写永远成立的 round CHECK）。
--   - 幂等由路由级 Idempotency 中间件负责，表上不再存幂等键。
--   - 不设保留期：它是报表口径的依据，与账本一样长期保留。
--
-- activity_daily：后台行为趋势（注册、登录、下单、独立 IP、活跃用户）的按天汇总，派生读数，丢了可以重算。
--   - 按站点时区切日，算这一行时的时区记在 tz（主键的一部分）；站点时区一改，旧行自动不用、回到实时算。
--   - report:activity_daily 每 10 分钟重算最近两个已结束日并覆盖写；读路径只用定稿的行
--     （computed_at 不早于该日结束后整一天），其余日子实时算。
--   - 保留 400 天，retention:activity_daily 每天普通 DELETE（不是只追加表）。
--   - 旧的库内函数 activity_daily_compute 不再建：逐天口径在 report service 里拼 audit、subscription 的聚合读。

CREATE TABLE revenue_report_adjustments (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  currency     text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  amount       numeric(20,2) NOT NULL CHECK (amount <> 0 AND amount BETWEEN -10000000000 AND 10000000000),
  reason       text NOT NULL CHECK (char_length(btrim(reason)) BETWEEN 5 AND 500),
  -- 计入报表的日子（站点时区的自然日）；不晚于登记当天由 service 按站点时区判
  effective_on date NOT NULL,
  reversal_of  uuid,
  -- 登记的管理员；只追加的表不能 SET NULL，用户也不会被删
  created_by   uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at   timestamptz NOT NULL DEFAULT now(),
  -- 给冲正的复合外键用：冲正行币种 = 原行币种
  CONSTRAINT revenue_report_adjustments_id_currency_key UNIQUE (id, currency),
  CONSTRAINT revenue_report_adjustments_reversal_fk FOREIGN KEY (reversal_of, currency)
    REFERENCES revenue_report_adjustments (id, currency) ON DELETE RESTRICT,
  -- 一行至多被冲一次（NULL 不参与唯一）
  CONSTRAINT revenue_report_adjustments_reversal_key UNIQUE (reversal_of),
  CONSTRAINT revenue_report_adjustments_not_self CHECK (reversal_of IS NULL OR reversal_of <> id)
);

-- 营收按日与概览：按币种、生效日取
CREATE INDEX revenue_report_adjustments_series_idx ON revenue_report_adjustments (currency, effective_on);
-- 调整列表按登记时间倒序
CREATE INDEX revenue_report_adjustments_created_idx ON revenue_report_adjustments (created_at DESC, id DESC);
-- users 外键的 RESTRICT 检查
CREATE INDEX revenue_report_adjustments_created_by_idx ON revenue_report_adjustments (created_by);

CREATE TRIGGER revenue_report_adjustments_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON revenue_report_adjustments
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

CREATE TABLE activity_daily (
  -- 切日用的站点时区（IANA 名）
  tz           text NOT NULL CHECK (char_length(tz) BETWEEN 1 AND 64),
  day          date NOT NULL,
  registered   bigint NOT NULL CHECK (registered >= 0),
  logins       bigint NOT NULL CHECK (logins >= 0),
  orders       bigint NOT NULL CHECK (orders >= 0),
  unique_ips   bigint NOT NULL CHECK (unique_ips >= 0),
  active_users bigint NOT NULL CHECK (active_users >= 0),
  -- 算这一行的时刻；读路径据此判断是否定稿
  computed_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tz, day)
);

-- 保留期清理按日子删
CREATE INDEX activity_daily_day_idx ON activity_daily (day);

-- +goose Down
DROP TABLE IF EXISTS activity_daily;
DROP TABLE IF EXISTS revenue_report_adjustments;
