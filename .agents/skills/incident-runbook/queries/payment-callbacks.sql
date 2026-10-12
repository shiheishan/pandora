-- 用途：支付回调与订单：卡住的回调、近 24 小时按渠道与结果的回调、最近的处理失败原因、挂账按状态；给了订单号时再看这一单的订单、每次支付尝试与收到的回调。
-- 身份：超级用户。
-- 变量：no（订单号，可选）、hours（回调统计窗口，缺省 24）。
-- 对应 panel/deploy/RUNBOOK.md 第 6 章。验签失败的回调不入库，只在 public.log 里有「支付回调验签失败」。
-- 第 4 段按易支付报文的 out_trade_no 找回调（第二次及以后发起支付时是「订单号-序号」）；别的渠道看 raw_payload。
\if :{?hours}
\else
  \set hours 24
\endif
BEGIN READ ONLY;

\echo == 1 卡住的回调：收到超过 1 分钟仍 pending / failed（与后台系统状态卡片同口径）
SELECT processing_status, count(*) AS n, min(received_at) AS oldest
  FROM payment_events
 WHERE processing_status IN ('pending', 'failed') AND received_at < now() - interval '1 minute'
 GROUP BY processing_status;

\echo == 2 近 :hours 小时的回调：渠道 x 事件 x 处理结果（一条都没有 = 回调没进来）
SELECT p.code AS provider, e.event_type, e.processing_status, count(*) AS n, max(e.received_at) AS last_at
  FROM payment_events e
  JOIN payment_providers p ON p.tenant_id = e.tenant_id AND p.id = e.provider_id
 WHERE e.received_at > now() - make_interval(hours => :hours)
 GROUP BY 1, 2, 3
 ORDER BY 1, 2, 3;

\echo == 3 最近 10 条处理失败的回调
SELECT received_at, event_type, left(processing_error, 160) AS error
  FROM payment_events
 WHERE processing_status = 'failed'
 ORDER BY received_at DESC
 LIMIT 10;

\echo == 4 挂账（取消后才到账、超额扣款）按类型与状态
SELECT case_kind, status, currency, count(*) AS n, sum(amount) AS amount_minor, min(received_at) AS oldest
  FROM late_payment_cases
 GROUP BY 1, 2, 3
 ORDER BY 1, 2, 3;

\if :{?no}
\echo == 5 订单 :no
SELECT order_no, kind, status, currency, total_amount, created_at, expires_at, paid_at
  FROM orders WHERE order_no = :'no';

\echo == 6 这一单的每次支付尝试（provider_ref 就是对外单号）
SELECT i.provider_ref, i.status, i.amount, i.currency, i.failure_code, i.created_at, i.expires_at
  FROM payment_intents i
  JOIN orders o ON o.tenant_id = i.tenant_id AND o.id = i.order_id
 WHERE o.order_no = :'no'
 ORDER BY i.created_at;

\echo == 7 这一单收到的回调（易支付 out_trade_no）
SELECT received_at, event_type, signature_verified, processing_status, left(processing_error, 160) AS error
  FROM payment_events
 WHERE raw_payload->>'out_trade_no' LIKE :'no' || '%'
 ORDER BY received_at;
\endif

ROLLBACK;
