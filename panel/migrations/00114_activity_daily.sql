-- 后台行为趋势（GET /v1/stats/timeseries）按天预汇总。
--
-- 原来 adminops.ActivityTimeseries 每次请求都对最近 N 天（最多 90 天）的审计事件、订阅拉取日志与
-- 按日流量现场聚合（5k 库副本约 75ms），代价随天数与这几张只增不删的表线性增长。
--
-- 现在拆成两半：
--   - app.activity_daily_compute(租户, 起日, 止日)：逐天算注册、登录、下单、独立 IP、活跃用户，
--     是原请求 SQL 的原文，只把「最近 N 天」换成 [起日, 止日] 两个参数（止日另加的上界只是让它
--     走时间索引，不改变能连上的行）。读路径、定时任务与下面的回填都只经它，口径只有一个出处。
--   - activity_daily：只存已经结束的日子。aegis-admin 的保留期任务每轮重算最近 2 个已结束日
--     （吸收迟到的写入，比如用户时区晚于会话时区时按日流量还在写前一天），读路径只用「可用」的行，
--     其余（今天，以及缺行或不可用的日子）实时经同一个函数算。
--
-- 切日的时区不变：原 SQL 用 date_trunc('day', …) 与 ::date 按会话时区（数据库 TimeZone 设置，部署
-- 强制 UTC）切日。这里不改成站点时区，而是把算这一行时的会话时区一起存下（tz 列，主键的一部分），
-- 读的时候只取 tz 等于当前会话时区的行；会话时区一变，旧行自动不用、回到实时算，数字不会变。
--
-- 「可用」是下面两种之一（读路径 adminops.activityDailyFinalSQL）：
--   1. 定稿：computed_at 不早于该日结束后整一天（(day + 2) 按会话时区的零点）。那时审计与拉取日志
--      （按写入时刻记时间）早已不会再落进这一天；按日流量的 day 是用户时区的日，与会话时区相差
--      不超过一天，也早已写完。定时任务在 day + 2 那天第一次运行时写出定稿行。
--   2. 昨天的「无迟到写入」行：computed_at 不早于该日结束后 10 分钟（审计与拉取日志都在写入时刻的
--      短事务里落库，10 分钟后不会再有记到这一天的新行），并且这一天的按日流量没有在 computed_at
--      前 5 分钟之后被写过（updated_at；入库每次 upsert 都刷新它，5 分钟覆盖算这一行时仍在途的入库事务）。
--      用户时区晚于会话时区时，昨天的按日流量还会写到第二天中午，这期间这一行不可用、实时算，
--      写停之后的下一轮重算（10 分钟一次）即可用。
-- 可用之后底表的变化（订阅被删导致按日流量级联删除等）不再反映到这一天，属于预期的快照语义。
--
-- 回填：近 90 天（接口上限）的已结束日，按跑迁移时的会话时区；昨天那一行按上面第 2 条判断。
-- 保留 400 天（adminops.ActivityDailyRetentionDays），由保留期任务删除；读路径最远 90 天。
-- 派生读数，不是证据；丢了可以从底表重算。

-- +goose Up

SET LOCAL lock_timeout = '5s';

-- +goose StatementBegin
-- 不是 STRICT、不带 SET、STABLE 的单条 SELECT：规划器会把它内联进调用方的查询，参数按常量估算。
-- 按会话时区切日（与原请求 SQL 一致）；调用方在 RLS 下以 aegis_app 调，租户条件同时显式写在里面。
CREATE FUNCTION app.activity_daily_compute(p_tenant uuid, p_from date, p_to date)
RETURNS TABLE (day date, registered bigint, logins bigint, orders bigint, unique_ips bigint, active_users int)
LANGUAGE sql STABLE AS $$
WITH d AS (
  SELECT generate_series(p_from::timestamptz, p_to::timestamptz, '1 day')::date AS day
), active AS (
  -- 两路来源去重：成功的订阅拉取（按会话时区切日），与按日流量（00072，按用户 / 站点时区记的日）
  SELECT day, count(DISTINCT user_id)::int AS n FROM (
    SELECT date_trunc('day', f.fetched_at)::date AS day, s.user_id
      FROM subscription_fetch_log f
      JOIN subscriptions s ON s.tenant_id = f.tenant_id AND s.id = f.subscription_id
     WHERE f.tenant_id = p_tenant AND f.result = 'ok'
       AND f.fetched_at >= p_from::timestamptz
       AND f.fetched_at < (p_to + 1)::timestamptz
    UNION
    SELECT u.day, s.user_id
      FROM subscription_usage_daily u
      JOIN subscriptions s ON s.tenant_id = u.tenant_id AND s.id = u.subscription_id
     WHERE u.tenant_id = p_tenant AND u.bytes > 0
       AND u.day >= p_from AND u.day <= p_to
  ) x GROUP BY day
)
SELECT d.day,
  count(*) FILTER (WHERE a.action = 'user.registered'),
  count(*) FILTER (WHERE a.action = 'user.login' AND a.outcome = 'success'),
  count(*) FILTER (WHERE a.action = 'order.created'),
  count(DISTINCT a.source_ip_hash),
  coalesce(max(ac.n), 0)
  FROM d
  LEFT JOIN active ac ON ac.day = d.day
  LEFT JOIN audit_events a
    ON a.tenant_id = p_tenant AND date_trunc('day', a.occurred_at)::date = d.day
   AND a.occurred_at >= p_from::timestamptz
   AND a.occurred_at < (p_to + 1)::timestamptz
 GROUP BY d.day
$$;

COMMENT ON FUNCTION app.activity_daily_compute(uuid, date, date) IS
  '后台行为趋势的逐天口径（按会话时区切日），读路径、定时汇总与回填共用的唯一出处。';
GRANT EXECUTE ON FUNCTION app.activity_daily_compute(uuid, date, date) TO aegis_app;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE activity_daily (
  tenant_id    uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  -- 算这一行时的会话时区（current_setting('TimeZone')），切日用的就是它
  tz           text NOT NULL CHECK (tz <> ''),
  day          date NOT NULL,
  registered   bigint NOT NULL CHECK (registered >= 0),
  logins       bigint NOT NULL CHECK (logins >= 0),
  orders       bigint NOT NULL CHECK (orders >= 0),
  unique_ips   bigint NOT NULL CHECK (unique_ips >= 0),
  active_users int NOT NULL CHECK (active_users >= 0),
  -- 算这一行的事务时刻；读路径据此判断这一行是否可用（见文件头）
  computed_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, tz, day)
);

SELECT app.enable_tenant_rls('activity_daily');

-- 应用重算（upsert）、读，并由保留期任务删 400 天以前的行；不清表
GRANT SELECT, INSERT, UPDATE, DELETE ON activity_daily TO aegis_app;
REVOKE TRUNCATE ON activity_daily FROM aegis_app;

COMMENT ON TABLE activity_daily IS
  '后台行为趋势按天汇总（只存已结束日，按会话时区切日并记下时区）。定时任务重算最近 2 个已结束日，读路径只用可用（定稿，或昨天且无迟到写入）的行。派生读数。';
-- +goose StatementEnd

-- 回填（PG18 测试按下面两行标记截取这一段，在回滚的事务里重跑，与原请求 SQL 比对）。
-- activity-backfill:begin
-- +goose StatementBegin
INSERT INTO activity_daily AS a
  (tenant_id, tz, day, registered, logins, orders, unique_ips, active_users, computed_at)
SELECT t.id, current_setting('TimeZone'), f.day, f.registered, f.logins, f.orders,
       f.unique_ips, f.active_users, now()
  FROM tenants t
  CROSS JOIN LATERAL app.activity_daily_compute(t.id, current_date - 90, current_date - 1) f
ON CONFLICT (tenant_id, tz, day) DO UPDATE SET
  registered   = EXCLUDED.registered,
  logins       = EXCLUDED.logins,
  orders       = EXCLUDED.orders,
  unique_ips   = EXCLUDED.unique_ips,
  active_users = EXCLUDED.active_users,
  computed_at  = EXCLUDED.computed_at;
-- +goose StatementEnd
-- activity-backfill:end

-- +goose Down

-- +goose StatementBegin
-- 派生表，删掉不丢证据；本迁移之前的代码不读也不写它
DROP TABLE IF EXISTS activity_daily;
DROP FUNCTION IF EXISTS app.activity_daily_compute(uuid, date, date);
-- +goose StatementEnd
