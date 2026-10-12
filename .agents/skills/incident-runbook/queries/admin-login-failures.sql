-- 用途：后台登录失败的真实原因。对外一律「邮箱或密码不正确」，审计里记了 reason；同一来源与账号短时间内重复失败只记一条。
-- 身份：超级用户。
-- 变量：n（行数，缺省 20）。
-- reason：invalid_credentials（账号不存在或口令错）、account_inactive（停用）、no_admin_role（没有后台角色）。
-- 对应 panel/deploy/RUNBOOK.md 第 2 章。输出里有来源 IP，不贴进仓库和报告。
\if :{?n}
\else
  \set n 20
\endif
BEGIN READ ONLY;

\echo == 1 近 7 天按原因
SELECT after_digest->>'reason' AS reason, count(*) AS n, max(occurred_at) AS last_at
  FROM audit_events
 WHERE action = 'user.login_failed' AND api_domain = 'admin'
   AND occurred_at > now() - interval '7 days'
 GROUP BY 1
 ORDER BY n DESC;

\echo == 2 最近 :n 条
SELECT occurred_at, after_digest->>'reason' AS reason, resource_id AS user_id
  FROM audit_events
 WHERE action = 'user.login_failed' AND api_domain = 'admin'
 ORDER BY occurred_at DESC
 LIMIT :n;

\echo == 3 最近的成功登录与 adminctl 改密（判断「是不是已经有人进去了」）
SELECT occurred_at, action, outcome
  FROM audit_events
 WHERE (action = 'user.login' AND api_domain = 'admin') OR action = 'adminctl.password_reset'
 ORDER BY occurred_at DESC
 LIMIT 10;

ROLLBACK;
