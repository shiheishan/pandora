-- 用途：看清「我现在是谁、能看到什么」，判断「查不到」是数据真没有还是 RLS 挡住了。
-- 身份：超级用户（psql.sh 默认身份）。文件里临时切到 aegis_app 做对照，事务末尾回滚。
-- 变量：tenant（缺省默认租户）。
-- 看法：第 2 段（超级用户，RLS 对它不生效）有行、第 3 段是 0、第 4 段有行，
--       说明数据在，只是会话没设租户。第 4 段仍为 0 而第 2 段里该租户有行，查 tenant 值是否写错。
\if :{?tenant}
\else
  \set tenant '00000000-0000-7000-8000-000000000001'
\endif
BEGIN READ ONLY;

\echo == 1 当前身份（is_super / bypass_rls 为 t 说明不受 RLS 约束）
SELECT current_user, session_user,
       r.rolsuper AS is_super, r.rolbypassrls AS bypass_rls,
       current_setting('app.tenant_id', true) AS tenant_setting
  FROM pg_roles r WHERE r.rolname = current_user;

\echo == 2 超级用户视角：各租户的用户数
SELECT tenant_id, count(*) AS users FROM users GROUP BY tenant_id ORDER BY users DESC;

SET LOCAL ROLE aegis_app;
\echo == 3 aegis_app，未设租户（默认拒绝，应为 0）
SELECT current_user, count(*) AS users_visible FROM users;

SELECT set_config('app.tenant_id', :'tenant', true) AS tenant_set \gset
\echo == 4 aegis_app，设了租户
SELECT current_user, app.current_tenant_id() AS tenant, count(*) AS users_visible FROM users;

ROLLBACK;
