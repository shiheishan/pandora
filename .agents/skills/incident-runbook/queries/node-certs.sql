-- 用途：面板集中签发的节点证书：每张证书的状态、退避与最近错误；DNS 凭据的校验结论；近 7 天失败的签发订单按错误码；近 7 天按 CA 的签发数（本地限额对账的口径）。
-- 身份：超级用户（只读列，不碰 secret_sealed）。
-- 变量：无。
-- 对应 panel/deploy/RUNBOOK.md 第 5 章；错误码含义见那一章的表，设计见 docs/node-certificates.md 第 3.6 节。
BEGIN READ ONLY;

\echo == 1 证书：状态、连续失败、退避到何时、最近错误、到期
SELECT name, status, paused_reason, consecutive_failures, next_attempt_at, renew_after,
       last_error_code, left(last_error, 160) AS last_error, not_after
  FROM certificates
 ORDER BY (status = 'active'), updated_at DESC;

\echo == 2 DNS 凭据：校验结论（error 时用它的证书停在 blocked_credential）
SELECT name, provider, zone, verify_status, verified_at, visible_zones, left(verify_error, 160) AS verify_error
  FROM dns_credentials
 ORDER BY name;

\echo == 3 近 7 天失败或取消的签发订单：错误码 x CA
SELECT state, error_code, ca, count(*) AS n, max(finished_at) AS last_at
  FROM certificate_orders
 WHERE state IN ('failed', 'cancelled') AND created_at > now() - interval '7 days'
 GROUP BY 1, 2, 3
 ORDER BY n DESC;

\echo == 4 进行中的订单（同一张证书至多一张；running 的租约过期会被接手）
SELECT o.state, c.name, o.reason, o.attempt, o.lease_until, o.created_at
  FROM certificate_orders o
  JOIN certificates c ON c.tenant_id = o.tenant_id AND c.id = o.certificate_id
 WHERE o.state IN ('queued', 'running')
 ORDER BY o.created_at;

\echo == 5 近 7 天按 CA 的签发数（Let's Encrypt：同一注册域名每 7 天 50 张新证书，续期不算）
SELECT ca, is_renewal, ari_replaces, count(*) AS n
  FROM certificate_issuances
 WHERE created_at > now() - interval '7 days'
 GROUP BY 1, 2, 3
 ORDER BY 1, 2, 3;

ROLLBACK;
