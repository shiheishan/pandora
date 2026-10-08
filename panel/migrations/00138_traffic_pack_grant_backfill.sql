-- backfill: batched-by=traffic_pack_grants.id 5000/批（按 id 键集翻页）; rerunnable=只改 WHERE subscription_id IS NULL 的行，挂上的不再被选中
-- 存量流量包挂到一份订阅上（购买模型统一 Q5 与 8.1 第 3 题，按推荐 A；w7buya）。
--
-- 00137 加的 traffic_pack_grants.subscription_id 存量全为空。按用户挂到一份订阅上，次序：
--   ① 生效中（active / trialing / grace / past_due）的订阅里到期最晚的（永不过期的算最晚）；
--   ② 已过期但还在 30 天窗口内（expired 且 renewal_closed_at 为空）的，到期最晚的；
--   ③ 都没有就留空，算「未分配」：以后开通第一份时自动挂上（billing provisionSubscription），
--      或由用户在门户选一份。
-- 已经有多条在用订阅的老用户，原来几条共用一池，回填后全部挂到到期最晚的那份（用户 8.1 第 3 题，
-- 推荐 A）；那份停用后余额可以转走（00137 的规则 2）。用完的余额也一起挂上，历史上看得出属于哪份。
-- 每改挂一笔写一条 actor_kind='migration' 的转移流水（00137 的提交时检查要求同一事务有流水）。
-- 这条流水也是「升级前的旧包」的标记：只有它、没有 user / admin 流水的余额，用户可以从生效中的
-- 那份自己挪一次到另一份（用户 2026-10-07 定，00137 守卫情形 3）；挪过之后按原规则。
--
-- 回填在一个 DO 块里按 id 键集分批：每批一条 UPDATE…FROM 加一条 INSERT…SELECT 流水；每批打
-- NOTICE（行数与毫秒，psql 手跑时看），总计写 LOG。整个迁移仍是一个事务（goose），分批限制的是
-- 单条语句的工作集；中途失败整体回滚，重跑只碰仍为空的行。
-- 耗时（对照机空闲，5k 副本）：实测库没有流量包余额，回填空跑约 20ms；造 1 万笔余额（每份订阅 2 笔）
-- 后约 7.2s：每批 5000 笔约 3s（每笔过 00137 的改挂守卫、流水归属检查与外键），提交时的约束触发器
-- 约 1.2s。按两倍估（预检克隆库一遍、正式库一遍）约 15s；期间这些余额行被锁，停写升级窗口内无影响。
--
-- Down：撤销回填。迁移以超级用户运行，SET LOCAL session_replication_role = replica 绕开追加写触发器
-- 与 00137 的改挂守卫，把 migration 流水指向的余额改回空，再删掉 migration 流水。只要有用户或后台
-- 自己转移过的流水（actor_kind <> 'migration'）就拒绝：那之后余额的去向不再只由回填决定。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- +goose StatementBegin
DO $$
DECLARE
  v_last  uuid := '00000000-0000-0000-0000-000000000000';
  v_next  uuid;
  v_moved bigint;
  v_total bigint := 0;
  v_batches int := 0;
  v_t0    timestamptz := clock_timestamp();
  v_tb    timestamptz;
BEGIN
  LOOP
    v_tb := clock_timestamp();
    SELECT g.id INTO v_next
      FROM (SELECT id FROM public.traffic_pack_grants
             WHERE subscription_id IS NULL AND id > v_last
             ORDER BY id LIMIT 5000) g
     ORDER BY g.id DESC LIMIT 1;
    EXIT WHEN v_next IS NULL;

    WITH batch AS (
      SELECT g.tenant_id, g.id, g.user_id
        FROM public.traffic_pack_grants g
       WHERE g.subscription_id IS NULL AND g.id > v_last AND g.id <= v_next
    ), target AS (
      SELECT b.tenant_id, b.id,
             (SELECT s.id FROM public.subscriptions s
               WHERE s.tenant_id = b.tenant_id AND s.user_id = b.user_id
                 AND (s.status IN ('active','trialing','grace','past_due')
                      OR (s.status = 'expired' AND s.renewal_closed_at IS NULL))
               ORDER BY (s.status <> 'expired') DESC,
                        s.current_period_end DESC NULLS FIRST, s.id DESC
               LIMIT 1) AS sub_id
        FROM batch b
    ), moved AS (
      UPDATE public.traffic_pack_grants g
         SET subscription_id = t.sub_id
        FROM target t
       WHERE g.tenant_id = t.tenant_id AND g.id = t.id
         AND t.sub_id IS NOT NULL AND g.subscription_id IS NULL
      RETURNING g.tenant_id, g.id, g.user_id, g.subscription_id,
                g.granted_bytes - g.consumed_bytes AS remaining
    ), logged AS (
      INSERT INTO public.traffic_pack_transfers
        (tenant_id, grant_id, user_id, from_subscription_id, to_subscription_id,
         remaining_bytes, actor_kind)
      SELECT tenant_id, id, user_id, NULL, subscription_id, remaining, 'migration'
        FROM moved
      RETURNING 1
    )
    SELECT count(*) INTO v_moved FROM logged;

    v_total := v_total + v_moved;
    v_batches := v_batches + 1;
    v_last := v_next;
    RAISE NOTICE '00138 batch %: attached % grants in % ms', v_batches, v_moved,
      round(extract(epoch FROM clock_timestamp() - v_tb) * 1000);
  END LOOP;
  RAISE LOG '00138 traffic pack backfill: attached % grants in % batches, % ms', v_total, v_batches,
    round(extract(epoch FROM clock_timestamp() - v_t0) * 1000);
END
$$;
-- +goose StatementEnd

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM public.traffic_pack_transfers WHERE actor_kind <> 'migration') THEN
    RAISE EXCEPTION '00138 down refused: traffic pack transfers made by users or admins exist; the backfill can no longer be undone on its own';
  END IF;
END
$$;
-- +goose StatementEnd

SET LOCAL session_replication_role = replica;
UPDATE public.traffic_pack_grants g
   SET subscription_id = NULL
  FROM public.traffic_pack_transfers t
 WHERE t.tenant_id = g.tenant_id AND t.grant_id = g.id AND t.actor_kind = 'migration';
DELETE FROM public.traffic_pack_transfers WHERE actor_kind = 'migration';
SET LOCAL session_replication_role = origin;
