-- 用途：对账一个用户的购买与余额（购买模型统一之后）：每份订阅（备注名、套餐、到期、链接凭据换过几次）、
--       每笔流量包挂在哪一份及其转移经过、订单的金额拆解（小计、优惠、剩余价值、余额、应付、实付、免掉的零头）、
--       支付记录、余额账户与逐笔余额流水、礼品卡兑换、与这个用户相关的审计记录。走完一条钱的路径后用它逐项核对。
-- 身份：aegis_app + 租户。
-- 变量：uid（用户 id）或 email（二选一）、tenant、timeout、n（订单与流水条数，缺省 20）。输出含邮箱与订单号，别贴进仓库。
-- 口径：
--   - 订单恒等式 total_amount = subtotal_amount - discount_amount - proration_credit_amount（00071）；
--     换套餐免掉的零头记在 discount_amount，审计 digest 里是 small_due_waived。
--   - 余额 = -ledger_accounts.balance_signed（用户科目是贷方科目）；流水的 signed_amount 取反后正数是进账。
--   - 凭据只给 token_prefix 与换发次数，不出明文与哈希。
\if :{?tenant}
\else
  \set tenant '00000000-0000-7000-8000-000000000001'
\endif
\if :{?timeout}
\else
  \set timeout 15s
\endif
\if :{?n}
\else
  \set n 20
\endif
BEGIN READ ONLY;
SET LOCAL ROLE aegis_app;
SET LOCAL search_path TO pg_catalog, public, pg_temp;
SET LOCAL jit = off;
SET LOCAL statement_timeout = :'timeout';
SELECT set_config('app.tenant_id', :'tenant', true) AS tenant_set \gset
\if :{?uid}
\elif :{?email}
  SELECT id AS uid FROM users WHERE email_lower = lower(:'email') \gset
\else
  \echo 需要 -v uid=<用户 id> 或 -v email=<邮箱>
  \quit
\endif

\echo == 1 订阅（每份：备注名、套餐、状态、周期、凭据前缀与换发次数、本期流量、挂在这份上的流量包余量）
SELECT s.id, s.label, pl.code AS plan, s.status, s.current_period_start, s.current_period_end,
       c.token_prefix, c.rotated_count, c.status AS cred_status,
       q.limit_value, q.consumed, q.remaining,
       (SELECT coalesce(sum(g.granted_bytes - g.consumed_bytes), 0) FROM traffic_pack_grants g
         WHERE g.tenant_id = s.tenant_id AND g.subscription_id = s.id AND g.consumed_bytes < g.granted_bytes)::bigint
         AS pack_remaining
  FROM subscriptions s
  LEFT JOIN plans pl ON pl.tenant_id = s.tenant_id AND pl.id = s.plan_id
  LEFT JOIN LATERAL (SELECT token_prefix, rotated_count, status FROM subscription_credentials sc
                      WHERE sc.tenant_id = s.tenant_id AND sc.subscription_id = s.id
                      ORDER BY created_at DESC LIMIT 1) c ON true
  LEFT JOIN LATERAL (SELECT limit_value, consumed, remaining FROM quota_balances qb
                      WHERE qb.tenant_id = s.tenant_id AND qb.subscription_id = s.id AND qb.metric = 'traffic.bytes'
                      ORDER BY period_start DESC LIMIT 1) q ON true
 WHERE s.user_id = :'uid'
 ORDER BY s.created_at;

\echo == 2 流量包（挂在哪一份）与转移流水
SELECT g.id AS grant_id, g.source, g.granted_bytes, g.consumed_bytes, g.subscription_id, g.created_at,
       (SELECT string_agg(t.actor_kind || ':' || coalesce(t.from_subscription_id::text, '-') || '>' ||
                          t.to_subscription_id::text || '@' || t.remaining_bytes, ' | ' ORDER BY t.created_at)
          FROM traffic_pack_transfers t WHERE t.tenant_id = g.tenant_id AND t.grant_id = g.id) AS transfers
  FROM traffic_pack_grants g
 WHERE g.user_id = :'uid'
 ORDER BY g.created_at;

\echo == 3 订单金额拆解（新的在前）
SELECT o.order_no, o.kind, o.status, o.subtotal_amount AS subtotal, o.discount_amount AS discount,
       o.proration_credit_amount AS credit, o.total_amount AS total, o.balance_applied AS bal,
       o.payable_amount AS payable, o.paid_amount AS paid, o.refunded_amount AS refunded,
       o.subscription_id, o.subscription_label AS label, o.created_by, o.expires_at, o.paid_at, o.cancel_reason
  FROM orders o
 WHERE o.user_id = :'uid'
 ORDER BY o.created_at DESC
 LIMIT :n;

\echo == 4 支付记录
SELECT o.order_no, p.amount, p.status, p.method, pp.code AS provider, p.paid_at
  FROM payments p
  JOIN orders o ON o.tenant_id = p.tenant_id AND o.id = p.order_id
  JOIN payment_providers pp ON pp.tenant_id = p.tenant_id AND pp.id = p.provider_id
 WHERE o.user_id = :'uid'
 ORDER BY p.created_at DESC
 LIMIT :n;

\echo == 5 余额账户（余额 = -balance_signed）
SELECT account_type, currency, -balance_signed AS balance
  FROM ledger_accounts WHERE owner_user_id = :'uid' ORDER BY account_type;

\echo == 6 余额流水（正数进账；按时间）
SELECT t.occurred_at, a.account_type, t.kind, t.source_type, -e.signed_amount AS amount, t.memo
  FROM ledger_entries e
  JOIN ledger_accounts a ON a.tenant_id = e.tenant_id AND a.id = e.account_id
  JOIN ledger_transactions t ON t.tenant_id = e.tenant_id AND t.id = e.transaction_id
 WHERE a.owner_user_id = :'uid'
 ORDER BY t.occurred_at, e.created_at
 LIMIT :n;

\echo == 7 礼品卡兑换
SELECT r.redeemed_at, tp.type, tp.name, r.granted
  FROM gift_card_redemptions r
  JOIN gift_card_templates tp ON tp.tenant_id = r.tenant_id AND tp.id = r.template_id
 WHERE r.user_id = :'uid'
 ORDER BY r.redeemed_at;

\echo == 8 审计（这个用户做的，或落在他的订单、订阅上的）
SELECT a.occurred_at, a.actor_kind, a.action, a.resource_type, a.outcome,
       left(a.after_digest::text, 300) AS after_digest
  FROM audit_events a
 WHERE a.actor_id = :'uid'
    OR a.resource_id IN (SELECT id FROM orders WHERE user_id = :'uid'
                         UNION ALL SELECT id FROM subscriptions WHERE user_id = :'uid'
                         UNION ALL SELECT :'uid'::uuid)
 ORDER BY a.occurred_at DESC
 LIMIT :n;

ROLLBACK;
