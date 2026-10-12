-- 用途：订单按状态与类型的分布，及几类「卡住」的订单：未付款过期没清、处理中久不动、预留过期没释放。
-- 身份：aegis_app + 租户。
-- 变量：days（统计窗口，缺省 7）、tenant、timeout。
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

\echo == 1 近 :days 天创建的订单：状态 x 类型
SELECT status, kind, currency, count(*) AS n, sum(total_amount) AS total_minor
  FROM orders
 WHERE created_at >= now() - make_interval(days => :days)
 GROUP BY status, kind, currency
 ORDER BY status, kind, currency;

-- 未付款订单到点由预留过期流程（billing.ExpireDueReservations）释放并置 expired
\echo == 2 未付款已过 expires_at 却还没置 expired（正常应为空或很少）
SELECT status, count(*) AS n, min(expires_at) AS oldest_expires_at,
       floor(extract(epoch FROM now() - min(expires_at)) / 60)::bigint AS oldest_lag_min
  FROM orders
 WHERE status IN ('draft', 'pending_payment') AND expires_at < now()
 GROUP BY status;

\echo == 3 processing 超过 10 分钟没动（支付回调结算进行中或被卡住），至多 20 行
SELECT order_no, kind, status, currency, total_amount, created_at, updated_at
  FROM orders
 WHERE status = 'processing' AND updated_at < now() - interval '10 minutes'
 ORDER BY updated_at
 LIMIT 20;

-- 谓词与 billing.ExpireDueReservations 的候选查询相同：这些订单下一轮该被释放
\echo == 4 预留已到期（held 且 expires_at 已过）而订单仍未收尾，按预留到期时间分段
SELECT o.status AS order_status, count(*) AS n, min(r.expires_at) AS oldest_expires_at,
       floor(extract(epoch FROM now() - min(r.expires_at)) / 60)::bigint AS oldest_lag_min
  FROM order_reservations r
  JOIN orders o ON o.tenant_id = r.tenant_id AND o.id = r.order_id
 WHERE r.state = 'held' AND r.expires_at <= now()
   AND o.status IN ('draft', 'pending_payment', 'processing')
 GROUP BY o.status;

ROLLBACK;
