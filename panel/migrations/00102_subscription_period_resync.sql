-- irreversible: 修数据。修复前的凭据到期与本周期配额期末没有留存，分不清哪些行是这条迁移拉齐的；恢复旧值等于让这些订阅重新 404。
-- forward-fix: 拉齐后的值对 00101 的代码同样合法，回到 00101 不需要处理；个别订阅有误，在后台对该订阅加时长或换发链接即可。要整体回到 00102 之前，走升级前备份恢复（panel/deploy/MIGRATION-RUNBOOK.md 第 3 节）。
-- 存量修复：把礼品卡延期留下的「订阅周期末与凭据到期 / 本周期配额分叉」拉齐。
--
-- 背景（第 2 波规划第 3 条）：礼品卡延期（billing.GiftGranter.ExtendExpiry）原先只把
-- subscriptions.current_period_end 往后推，另外两处停在旧到期日：
--   - active 凭据的 expires_at：订阅拉取（subscription.checkCredential）与门户链接列表只看它，
--     过了旧到期日订阅拉取 404、门户里链接消失；
--   - 本周期 cycle 配额行的 period_end：上报流量只记到 period_end > now() 的行上，过了旧到期日
--     套餐流量既不计也不扣。
-- 代码已改为经 extendSubscriptionTx 三处一起改；这里修已经分叉的存量。
--
-- 哪些行可以安全拉齐（只延长、从不缩短，也不碰已用量与周期起点）：
--   一、凭据：status = 'active'、expires_at 非空且早于订阅的 current_period_end。
--      - 订阅限于订阅拉取放行的状态（active / trialing / grace）且有周期末；终态订阅的凭据
--        本来就拉不到，不动。
--      - 续费、变更套餐、开通、换发链接写凭据时都取订阅周期末，只有礼品卡延期会让凭据落后；
--        凭据晚于周期末或为空（永不过期）的不缩短。
--      - 已吊销 / 已过期 / 宽限中的凭据不动：换发后的旧链接不能因此复活。
--   二、cycle 配额行：period = 'cycle'、period_end 非空且早于订阅的 current_period_end，
--      且是该订阅该指标最新的一条 cycle 行（没有 period_start 更晚的同指标 cycle 行）。
--      - 续费与变更套餐会把 cycle 行的 period_end 写成新周期末，开通时也一致，所以 cycle 行
--        落后于订阅周期末只可能来自礼品卡延期；把它推到订阅周期末，就是延期当时该做的事。
--      - 只改 period_end：consumed、period_start 不动（延期是给时间，不是开新周期发新流量）。
--      - day / month 行有自己的滚动边界，total 行没有边界，都不碰。
--
-- 改 period_end 不会让配额行跨过「用尽」边界（remaining 不变），节点下发纪元不必推进；
-- 凭据不进节点名单。订阅行不改。
--
-- Down 拒绝：修复前的旧值没有留存，也不该恢复 —— 恢复等于让这些用户重新 404。

-- +goose Up

SET LOCAL lock_timeout = '5s';

-- +goose StatementBegin
DO $$
DECLARE
  credentials bigint;
  quota_rows  bigint;
BEGIN
  UPDATE subscription_credentials c
     SET expires_at = s.current_period_end
    FROM subscriptions s
   WHERE s.tenant_id = c.tenant_id AND s.id = c.subscription_id
     AND s.status IN ('active', 'trialing', 'grace')
     AND s.current_period_end IS NOT NULL
     AND c.status = 'active'
     AND c.expires_at IS NOT NULL
     AND c.expires_at < s.current_period_end;
  GET DIAGNOSTICS credentials = ROW_COUNT;

  UPDATE quota_balances q
     SET period_end = s.current_period_end, updated_at = now()
    FROM subscriptions s
   WHERE s.tenant_id = q.tenant_id AND s.id = q.subscription_id
     AND s.status IN ('active', 'trialing', 'grace')
     AND s.current_period_end IS NOT NULL
     AND q.period = 'cycle'
     AND q.period_end IS NOT NULL
     AND q.period_end < s.current_period_end
     AND q.period_start < s.current_period_end
     AND NOT EXISTS (
           SELECT 1 FROM quota_balances newer
            WHERE newer.subscription_id = q.subscription_id
              AND newer.metric = q.metric
              AND newer.period = 'cycle'
              AND newer.period_start > q.period_start);
  GET DIAGNOSTICS quota_rows = ROW_COUNT;

  RAISE NOTICE '00102 subscription period resync: % credential(s), % cycle quota row(s) aligned',
    credentials, quota_rows;
END
$$;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DO $$
BEGIN
  RAISE EXCEPTION
    'rollback refused (00102 subscription period resync): the pre-repair credential expiry and cycle quota period ends were not kept, and restoring them would make these subscriptions 404 again';
END
$$;
-- +goose StatementEnd
