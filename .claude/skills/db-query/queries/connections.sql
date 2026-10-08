-- 用途：连接数与会话：按用户和状态汇总、离 max_connections 还有多少、长事务、被锁阻塞的会话。
-- 身份：超级用户（pg_stat_activity 只有超级用户或 pg_read_all_stats 才看得到别人会话的全部列）。
-- 预算（deploy/.env.example 的算式，compose 里 max_connections=60）：3 条超级用户保留 + 11 条维护余量
-- + public 常驻 1 条 LISTEN + 三个网关各 15 条上限（缺省 public 16、admin 15、node 15）。
-- client_addr 为空 = 走 unix socket（新版 install.sh 装出来的网关经 unix socket 连库；本脚本自己的会话也是）。
BEGIN READ ONLY;

\echo == 1 总量
SELECT current_setting('max_connections')::int AS max_connections,
       current_setting('superuser_reserved_connections')::int AS superuser_reserved,
       count(*) AS client_backends,
       count(*) FILTER (WHERE state = 'active') AS active,
       count(*) FILTER (WHERE state = 'idle in transaction') AS idle_in_tx
  FROM pg_stat_activity
 WHERE backend_type = 'client backend';

\echo == 2 按用户 x 状态 x 是否 unix socket
SELECT usename, state, client_addr IS NULL AS unix_socket, count(*) AS n,
       max(now() - state_change) AS longest_in_state
  FROM pg_stat_activity
 WHERE backend_type = 'client backend' AND datname = current_database()
 GROUP BY usename, state, client_addr IS NULL
 ORDER BY usename, n DESC;

\echo == 3 事务开了超过 30 秒的会话（最老的在前，至多 15 行）
SELECT pid, usename, state, now() - xact_start AS xact_age, now() - state_change AS in_state,
       wait_event_type, wait_event, left(regexp_replace(query, '\s+', ' ', 'g'), 120) AS query
  FROM pg_stat_activity
 WHERE backend_type = 'client backend' AND datname = current_database()
   AND xact_start IS NOT NULL AND now() - xact_start > interval '30 seconds'
   AND pid <> pg_backend_pid()
 ORDER BY xact_start
 LIMIT 15;

\echo == 4 正在等锁的会话和挡住它们的会话
SELECT w.pid AS waiting_pid, w.usename, now() - w.state_change AS waited,
       pg_blocking_pids(w.pid) AS blocked_by,
       left(regexp_replace(w.query, '\s+', ' ', 'g'), 120) AS waiting_query
  FROM pg_stat_activity w
 WHERE w.wait_event_type = 'Lock' AND w.datname = current_database();

ROLLBACK;
