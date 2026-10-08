-- 用途：升级到 00137 之前（流量包还挂在用户上、按人共用一池）先数一遍：有流量包余量的人、其中名下
--       不止一份在用订阅的人、额度已用完全靠共用流量包才下发节点的订阅。升级后用 pack-legacy-impact.sql
--       对比 00138 把包挂到了哪一份。只适用于 00137 之前的库（traffic_pack_grants 还没有 subscription_id）。
-- 身份：aegis_app + 租户。
-- 变量：tenant（缺省默认租户）、timeout（缺省 15s）。
-- 口径：「在用」与 00138 回填的第 ① 档相同：status IN (active, trialing, grace, past_due)；「下发节点」与
--       nodefabric ListNodeUsers 相同：status IN (active, trialing, grace) 且周期没结束，额度用完时要有流量包余量。
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

\echo == 1 流量包余额总览（笔数、有余量的笔数、涉及人数、剩余字节）
SELECT count(*) AS grants,
       count(*) FILTER (WHERE consumed_bytes < granted_bytes) AS open_grants,
       count(DISTINCT user_id) AS users,
       count(DISTINCT user_id) FILTER (WHERE consumed_bytes < granted_bytes) AS users_with_remaining,
       coalesce(sum(granted_bytes - consumed_bytes), 0)::bigint AS remaining_bytes
  FROM traffic_pack_grants;

\echo == 2 每人在用订阅份数分布（只算有流量包余量的人）
WITH pack AS (
  SELECT user_id, sum(granted_bytes - consumed_bytes)::bigint AS remaining
    FROM traffic_pack_grants
   WHERE consumed_bytes < granted_bytes
   GROUP BY user_id
), live AS (
  SELECT user_id, count(*) AS live_subs
    FROM subscriptions
   WHERE status IN ('active', 'trialing', 'grace', 'past_due')
   GROUP BY user_id
)
SELECT coalesce(l.live_subs, 0) AS live_subs, count(*) AS users, sum(p.remaining)::bigint AS remaining_bytes
  FROM pack p LEFT JOIN live l ON l.user_id = p.user_id
 GROUP BY 1 ORDER BY 1;

\echo == 3 额度已用完、靠共用流量包才下发节点的订阅（升级后包没挂到这份就会停发）
SELECT count(*) AS subs, count(DISTINCT s.user_id) AS users
  FROM subscriptions s
 WHERE s.status IN ('active', 'trialing', 'grace')
   AND (s.current_period_end IS NULL OR s.current_period_end > now())
   AND EXISTS (SELECT 1 FROM quota_balances qb
                WHERE qb.tenant_id = s.tenant_id AND qb.subscription_id = s.id
                  AND qb.metric = 'traffic.bytes'
                  AND qb.remaining IS NOT NULL AND qb.remaining <= 0)
   AND EXISTS (SELECT 1 FROM traffic_pack_grants g
                WHERE g.tenant_id = s.tenant_id AND g.user_id = s.user_id
                  AND g.consumed_bytes < g.granted_bytes);

ROLLBACK;
