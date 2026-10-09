-- 用途：通知队列积压：按渠道与状态的计数与最老一条、巡检的两个阈值、近 6 小时的错误信息前 10、notify.email 降级开关。
-- 身份：超级用户（单租户部署与产品口径一致；看全部租户）。
-- 变量：无。
-- 对应 panel/deploy/RUNBOOK.md 第 7 章；巡检 healthcheck.sh 的阈值是「排队超过 30 分钟 >= 200」「近 6 小时失败 >= 50」。
BEGIN READ ONLY;

\echo == 1 未终结与失败：渠道 x 状态（failed 是 5 次都没发出去的终态，不会自动重发）
SELECT channel, status, count(*) AS n, min(created_at) AS oldest, max(attempts) AS max_attempts
  FROM notification_deliveries
 WHERE status IN ('queued', 'sending', 'failed')
 GROUP BY channel, status
 ORDER BY channel, status;

\echo == 2 巡检口径
SELECT count(*) FILTER (WHERE status = 'queued' AND created_at < now() - interval '30 minutes') AS queued_over_30m,
       count(*) FILTER (WHERE status = 'failed' AND created_at > now() - interval '6 hours') AS failed_6h
  FROM notification_deliveries;

\echo == 3 近 6 小时排队中或失败的错误信息前 10
SELECT channel, status, left(error_message, 120) AS error, count(*) AS n
  FROM notification_deliveries
 WHERE status IN ('queued', 'failed') AND error_message IS NOT NULL
   AND created_at > now() - interval '6 hours'
 GROUP BY 1, 2, 3
 ORDER BY n DESC
 LIMIT 10;

\echo == 4 降级开关 notify.email（关 = 邮件只排队不发；缺行视为开启）
SELECT tenant_id, code, enabled FROM feature_switches WHERE code = 'notify.email';

ROLLBACK;
