-- 用途：未来 N 天内到期的订阅（含是否自动续费、是否已设期末取消），最早到期的在前。
-- 身份：aegis_app + 租户。
-- 变量：days（缺省 7）、tenant、timeout。输出含邮箱，别贴进仓库。
\if :{?tenant}
\else
  \set tenant '00000000-0000-7000-8000-000000000001'
\endif
\if :{?timeout}
\else
  \set timeout 15s
\endif
\if :{?days}
\else
  \set days 7
\endif
BEGIN READ ONLY;
SET LOCAL ROLE aegis_app;
SET LOCAL search_path TO pg_catalog, public, pg_temp;
SET LOCAL jit = off;
SET LOCAL statement_timeout = :'timeout';
SELECT set_config('app.tenant_id', :'tenant', true) AS tenant_set \gset

\echo == 未来 :days 天内到期的订阅（至多 100 行）
SELECT s.id AS subscription_id, u.email, pl.name AS plan, s.status,
       s.current_period_end, s.grace_end, s.auto_renew, s.cancel_at_period_end
  FROM subscriptions s
  JOIN users u ON u.tenant_id = s.tenant_id AND u.id = s.user_id
  LEFT JOIN plans pl ON pl.tenant_id = s.tenant_id AND pl.id = s.plan_id
 WHERE s.status IN ('active', 'trialing', 'past_due', 'grace')
   AND s.current_period_end > now()
   AND s.current_period_end <= now() + make_interval(days => :days)
 ORDER BY s.current_period_end
 LIMIT 100;

\echo == 按到期日汇总
SELECT (s.current_period_end AT TIME ZONE 'UTC')::date AS end_day_utc, count(*) AS n
  FROM subscriptions s
 WHERE s.status IN ('active', 'trialing', 'past_due', 'grace')
   AND s.current_period_end > now()
   AND s.current_period_end <= now() + make_interval(days => :days)
 GROUP BY 1
 ORDER BY 1;

ROLLBACK;
