-- 用途：订单、支付与复式账本对平。第 1、2 段是硬指标（应为空），第 3、4 段是候选异常（命中要人工看）。
-- 身份：aegis_app + 租户。
-- 变量：tenant、timeout（对账扫全表，数据量大时调到 60s）。
-- 借贷配平由延迟约束触发器在提交时保证，这里不再复核；缓存余额与分录求和的漂移用 app.verify_ledger_all()。
-- 约定（对着 billing 代码核过）：已付订单结算时写一笔 kind=order_paid（充值单是 balance_topup）、
-- source_type='order'、source_id=订单 id 的交易；order_paid 贷 platform_revenue = 订单总额。
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

\echo == 1 缓存余额与分录求和不一致的账户（verify_ledger_all 只返回有漂移的行，应为空）
SELECT * FROM app.verify_ledger_all();

\echo == 2 余额方向反了的账户（用户科目与保留科目都是贷方科目，signed 为正 = 余额为负，应为空）
SELECT account_type, owner_user_id, currency, -balance_signed AS balance
  FROM ledger_accounts
 WHERE account_type IN ('user_balance', 'user_balance_hold') AND balance_signed > 0;

-- 命中不一定是缺陷：种子/迁移造的订单没有分录，0 元单已排除。先看 kind 与 paid_at。
\echo == 3 候选：已付/履约/退款的订单没有对应的 order_paid 或 balance_topup 分录（最近 50 笔）
SELECT o.order_no, o.kind, o.status, o.currency, o.total_amount, o.paid_amount, o.paid_at
  FROM orders o
 WHERE o.status IN ('paid', 'fulfilled', 'partially_refunded', 'refunded')
   AND o.total_amount > 0
   AND NOT EXISTS (SELECT 1 FROM ledger_transactions t
                    WHERE t.tenant_id = o.tenant_id AND t.source_type = 'order'
                      AND t.source_id = o.id AND t.kind IN ('order_paid', 'balance_topup'))
 ORDER BY o.paid_at DESC NULLS LAST
 LIMIT 50;

\echo == 4 候选：order_paid 贷记 platform_revenue 的合计 不等于 订单总额（排除充值单，最近 50 笔）
SELECT o.order_no, o.kind, o.status, o.currency, o.total_amount, rev.credit_total, rev.txn_count
  FROM orders o
  CROSS JOIN LATERAL (
        SELECT coalesce(sum(e.amount), 0) AS credit_total, count(DISTINCT t.id) AS txn_count
          FROM ledger_transactions t
          JOIN ledger_entries e ON e.tenant_id = t.tenant_id AND e.transaction_id = t.id
          JOIN ledger_accounts a ON a.tenant_id = e.tenant_id AND a.id = e.account_id
         WHERE t.tenant_id = o.tenant_id AND t.source_type = 'order' AND t.source_id = o.id
           AND t.kind = 'order_paid' AND a.account_type = 'platform_revenue'
           AND e.direction = 'credit') rev
 WHERE o.status IN ('paid', 'fulfilled', 'partially_refunded', 'refunded')
   AND o.kind <> 'topup' AND o.total_amount > 0
   AND rev.txn_count > 0 AND rev.credit_total <> o.total_amount
 ORDER BY o.paid_at DESC NULLS LAST
 LIMIT 50;

\echo == 5 候选：渠道实收（payments 成功笔合计）不等于订单应付 payable_amount（已付/履约订单，最近 50 笔）
SELECT o.order_no, o.kind, o.currency, o.payable_amount, p.paid_total, p.n
  FROM orders o
  CROSS JOIN LATERAL (
        SELECT coalesce(sum(x.amount), 0) AS paid_total, count(*) AS n
          FROM payments x
         WHERE x.tenant_id = o.tenant_id AND x.order_id = o.id AND x.status = 'succeeded') p
 WHERE o.status IN ('paid', 'fulfilled') AND o.payable_amount <> p.paid_total
 ORDER BY o.paid_at DESC NULLS LAST
 LIMIT 50;

ROLLBACK;
