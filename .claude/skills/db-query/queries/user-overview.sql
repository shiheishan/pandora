-- 用途：一个用户名下的全部状态：账号、订阅、配额、流量包、余额与保留余额、最近订单、在线设备。
-- 身份：aegis_app + 租户。
-- 变量：uid（用户 id）或 email（二选一）、tenant、timeout。输出含邮箱，别贴进仓库。
-- 找不到用户时 psql 报 "no rows returned for \gset"：先跑 identity-check.sql 确认租户对不对。
-- 用 email_lower 查而不是 email：运行角色受 RLS，citext 的等号不是 LEAKPROOF，走不了索引
-- （00119）；复现产品行为时写法要一致，数据多时才不会全租户扫描。
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

\if :{?uid}
\elif :{?email}
  SELECT id AS uid FROM users WHERE email_lower = lower(:'email') \gset
\else
  \echo 需要 -v uid=<用户 id> 或 -v email=<邮箱>
  \quit
\endif

\echo == 1 账号
SELECT id, email, status, risk_level, user_group_id, created_at, last_login_at
  FROM users WHERE id = :'uid';

\echo == 2 订阅（新的在前）
SELECT s.id, pl.name AS plan, s.status, s.current_period_start, s.current_period_end,
       s.grace_end, s.auto_renew, s.cancel_at_period_end, s.device_limit, s.renewal_closed_at
  FROM subscriptions s
  LEFT JOIN plans pl ON pl.tenant_id = s.tenant_id AND pl.id = s.plan_id
 WHERE s.user_id = :'uid'
 ORDER BY s.created_at DESC;

\echo == 3 配额（limit_value 为空 = 不限量；remaining 是生成列 = limit_value + granted_addon + adjusted - consumed）
SELECT q.subscription_id, q.metric, q.period, q.period_start, q.period_end,
       q.limit_value, q.adjusted, q.consumed, q.remaining, q.overage_action
  FROM quota_balances q
  JOIN subscriptions s ON s.tenant_id = q.tenant_id AND s.id = q.subscription_id
 WHERE s.user_id = :'uid'
 ORDER BY q.subscription_id, q.metric, q.period_start DESC;

\echo == 4 流量包（00137 起挂在某一份订阅上，subscription_id 为空 = 未分配；永不过期；剩余 = granted_bytes - consumed_bytes；转移经过见 purchase-user-ledger.sql）
SELECT id, subscription_id, source, source_id, granted_bytes, consumed_bytes,
       granted_bytes - consumed_bytes AS remaining_bytes, created_at
  FROM traffic_pack_grants
 WHERE user_id = :'uid'
 ORDER BY subscription_id NULLS LAST, created_at;

\echo == 5 余额账户（用户科目是贷方科目，余额 = -balance_signed；user_balance_hold 是下单时冻结的部分）
SELECT account_type, currency, -balance_signed AS balance, status
  FROM ledger_accounts
 WHERE owner_user_id = :'uid'
 ORDER BY account_type, currency;

\echo == 6 最近 10 笔订单
SELECT order_no, kind, status, currency, total_amount, balance_applied, payable_amount,
       paid_amount, refunded_amount, created_at, paid_at, fulfilled_at
  FROM orders
 WHERE user_id = :'uid'
 ORDER BY created_at DESC
 LIMIT 10;

\echo == 7 在线设备（按订阅；窗口取租户设置 device_limit.window_minutes，缺省 5 分钟；窗口内没记录的订阅不出现）
SELECT d.subscription_id, d.device_count, d.node_count, d.last_seen_at
  FROM subscription_online_devices d
  JOIN subscriptions s ON s.tenant_id = d.tenant_id AND s.id = d.subscription_id
 WHERE s.user_id = :'uid';

ROLLBACK;
