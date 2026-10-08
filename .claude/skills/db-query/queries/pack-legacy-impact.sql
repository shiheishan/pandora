-- 用途：升级到 00137–00139 之后，看 00138 回填把升级前的流量包挂到了哪一份、影响了多少老用户：
--       回填改挂笔数；名下不止一份在用订阅、而且升级前有流量包余量的用户；包挂到的那份是不是到期最晚；
--       因为包只挂到一份而停发节点的另一份（额度用完、自己名下没有包）；还能「挪一次」的订阅
--       （与门户 legacy_movable_pack_bytes 同口径）；未分配的余量。
-- 身份：aegis_app + 租户。
-- 变量：tenant（缺省默认租户）、timeout（缺省 15s）。
-- 口径：「升级前的旧包」= 有 actor_kind='migration' 流水的余额；remaining_bytes 取回填那一刻（停服中，等于
--       升级前）的余量。「在用」与 00138 第 ① 档相同；「下发节点」与 nodefabric ListNodeUsers 相同。
-- 对比：升级前跑 pack-legacy-pre.sql，第 3 段的订阅数应等于本文件第 4 段的「停发」数加上包恰好挂上去的数。
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

\echo == 1 00138 回填：改挂笔数、涉及人数、当时的余量；仍未分配（subscription_id 为空）的余额
SELECT (SELECT count(*) FROM traffic_pack_transfers WHERE actor_kind = 'migration') AS migrated_grants,
       (SELECT count(DISTINCT user_id) FROM traffic_pack_transfers WHERE actor_kind = 'migration') AS migrated_users,
       (SELECT coalesce(sum(remaining_bytes), 0) FROM traffic_pack_transfers WHERE actor_kind = 'migration')::bigint
         AS migrated_remaining_bytes,
       (SELECT count(*) FROM traffic_pack_grants WHERE subscription_id IS NULL) AS unattached_grants,
       (SELECT coalesce(sum(granted_bytes - consumed_bytes), 0) FROM traffic_pack_grants
         WHERE subscription_id IS NULL AND consumed_bytes < granted_bytes)::bigint AS unattached_remaining_bytes;

\echo == 2 转移流水按操作方分布（migration 之外有行时 00137/00138 的 Down 会拒绝回滚）
SELECT actor_kind, count(*) AS n, count(DISTINCT grant_id) AS grants, min(created_at) AS first_at, max(created_at) AS last_at
  FROM traffic_pack_transfers GROUP BY actor_kind ORDER BY actor_kind;

\echo == 3 名下不止一份在用订阅、升级前有旧包余量的用户：包挂到的那份是不是到期最晚
WITH legacy AS (
  SELECT t.user_id, t.to_subscription_id AS sub_id, sum(t.remaining_bytes)::bigint AS remaining
    FROM traffic_pack_transfers t
   WHERE t.actor_kind = 'migration' AND t.remaining_bytes > 0
   GROUP BY 1, 2
), live AS (
  SELECT user_id, count(*) AS live_subs,
         (array_agg(id ORDER BY current_period_end DESC NULLS FIRST, id DESC))[1] AS latest_sub
    FROM subscriptions
   WHERE status IN ('active', 'trialing', 'grace', 'past_due')
   GROUP BY user_id
)
SELECT count(DISTINCT l.user_id) AS multi_sub_users,
       count(DISTINCT l.user_id) FILTER (WHERE l.sub_id = v.latest_sub) AS attached_to_latest,
       count(DISTINCT l.user_id) FILTER (WHERE l.sub_id <> v.latest_sub) AS attached_elsewhere,
       coalesce(sum(l.remaining), 0)::bigint AS remaining_bytes
  FROM legacy l JOIN live v ON v.user_id = l.user_id
 WHERE v.live_subs > 1;

\echo == 4 因旧包只挂到一份而停发节点的另一份：本该下发、额度用完、自己名下没有余量，而同一用户另一份上挂着旧包
SELECT count(*) AS stranded_subs, count(DISTINCT s.user_id) AS stranded_users
  FROM subscriptions s
 WHERE s.status IN ('active', 'trialing', 'grace')
   AND (s.current_period_end IS NULL OR s.current_period_end > now())
   AND EXISTS (SELECT 1 FROM quota_balances qb
                WHERE qb.tenant_id = s.tenant_id AND qb.subscription_id = s.id
                  AND qb.metric = 'traffic.bytes'
                  AND qb.remaining IS NOT NULL AND qb.remaining <= 0)
   AND NOT EXISTS (SELECT 1 FROM traffic_pack_grants g
                    WHERE g.tenant_id = s.tenant_id AND g.subscription_id = s.id
                      AND g.consumed_bytes < g.granted_bytes)
   AND EXISTS (SELECT 1 FROM traffic_pack_grants g
                 JOIN traffic_pack_transfers t ON t.tenant_id = g.tenant_id AND t.grant_id = g.id
                                              AND t.actor_kind = 'migration'
                WHERE g.tenant_id = s.tenant_id AND g.user_id = s.user_id
                  AND g.subscription_id <> s.id
                  AND g.consumed_bytes < g.granted_bytes);

\echo == 5 legacy_movable_pack_bytes > 0 的订阅（只有 migration 流水、还有余量，用户能从这份挪一次）
SELECT count(*) AS subs, count(DISTINCT user_id) AS users, coalesce(sum(movable), 0)::bigint AS movable_bytes
  FROM (SELECT g.subscription_id, g.user_id, sum(g.granted_bytes - g.consumed_bytes) AS movable
          FROM traffic_pack_grants g
         WHERE g.subscription_id IS NOT NULL AND g.consumed_bytes < g.granted_bytes
           AND EXISTS (SELECT 1 FROM traffic_pack_transfers t
                        WHERE t.tenant_id = g.tenant_id AND t.grant_id = g.id AND t.actor_kind = 'migration')
           AND NOT EXISTS (SELECT 1 FROM traffic_pack_transfers t
                            WHERE t.tenant_id = g.tenant_id AND t.grant_id = g.id AND t.actor_kind <> 'migration')
         GROUP BY g.subscription_id, g.user_id) m;

ROLLBACK;
