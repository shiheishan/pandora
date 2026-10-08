-- 用途：订阅按状态分布；找「已过周期末却还在用状态」的漏扫；expired 里还能原地续费的数量。
-- 身份：aegis_app + 租户（复现产品看到的数据）。
-- 变量：tenant（缺省默认租户）、timeout（缺省 15s，与运行角色一致）。
\if :{?tenant}
\else
  \set tenant '00000000-0000-7000-8000-000000000001'
\endif
\if :{?timeout}
\else
  \set timeout 15s
\endif
BEGIN READ ONLY;
SET LOCAL ROLE aegis_app;
SET LOCAL search_path TO pg_catalog, public, pg_temp;
SET LOCAL jit = off;
SET LOCAL statement_timeout = :'timeout';
SELECT set_config('app.tenant_id', :'tenant', true) AS tenant_set \gset

\echo == 1 订阅按状态分布
SELECT status, count(*) AS n,
       count(*) FILTER (WHERE current_period_end <= now()) AS period_ended,
       min(current_period_end) AS min_period_end,
       max(current_period_end) AS max_period_end
  FROM subscriptions
 GROUP BY status
 ORDER BY n DESC;

-- 谓词与 billing.ScanExpiredSubscriptions 的 expireDueSQL 相同：这些行下一轮扫描该翻成 expired。
-- 长期有行、lag 越来越大，说明 aegis-admin 的过期扫描没在跑或被卡住。
\echo == 2 已到期却还没被扫成 expired（正常应为空或 lag 很小）
SELECT status, count(*) AS n,
       min(current_period_end) AS oldest_period_end,
       floor(extract(epoch FROM now() - min(current_period_end)) / 60)::bigint AS oldest_lag_min
  FROM subscriptions
 WHERE status IN ('active', 'trialing', 'past_due', 'grace')
   AND current_period_end <= now()
   AND (status <> 'grace' OR grace_end IS NULL OR grace_end <= now())
 GROUP BY status;

\echo == 3 expired 的原地续费窗口（renewable = 窗口未关，过期满 30 天由扫描写 renewal_closed_at）
SELECT renewal_closed_at IS NULL AS renewable, count(*) AS n
  FROM subscriptions
 WHERE status = 'expired'
 GROUP BY 1;

ROLLBACK;
