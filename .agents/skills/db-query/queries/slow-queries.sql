-- 用途：慢查询排行（pg_stat_statements）：按总耗时、平均耗时、调用次数各取前 N。
-- 身份：超级用户（pg_stat_statements 要 pg_read_all_stats 才能看到别人的语句文本；运行角色 aegis_app 看不全）。
-- 变量：n（每榜条数，缺省 20）。
-- 前提：扩展已开。没开会在第 1 段之后报 relation "pg_stat_statements" does not exist：
--   开关要改 shared_preload_libraries 并重启 PostgreSQL（网关断几秒），生产/测试机上先问用户，
--   再用 panel/tools/loadtest/scripts/pgstat.sh enable --yes（面板机、root）；不要自己 ALTER SYSTEM。
-- 计数从上次 reset 起累计：对照改前改后时，先在面板机上 pgstat.sh reset 再跑负载，查询本身不清零。
\if :{?n}
\else
  \set n 20
\endif
BEGIN READ ONLY;

\echo == 1 扩展与统计起点
SELECT e.extname, e.extversion, i.stats_reset, i.dealloc
  FROM pg_extension e, pg_stat_statements_info i
 WHERE e.extname = 'pg_stat_statements';

-- 只看本库、顶层语句（toplevel）；文本压成一行并截到 160 字
\echo == 2 总耗时前 :n
SELECT s.queryid, s.userid::regrole AS role, s.calls,
       round(s.total_exec_time::numeric, 1) AS total_ms,
       round(s.mean_exec_time::numeric, 3) AS mean_ms,
       round(s.max_exec_time::numeric, 1) AS max_ms,
       s.rows, left(regexp_replace(s.query, '\s+', ' ', 'g'), 160) AS query
  FROM pg_stat_statements s
 WHERE s.dbid = (SELECT oid FROM pg_database WHERE datname = current_database()) AND s.toplevel
 ORDER BY s.total_exec_time DESC LIMIT :n;

\echo == 3 平均耗时前 :n（调用 5 次以上）
SELECT s.queryid, s.userid::regrole AS role, s.calls,
       round(s.mean_exec_time::numeric, 3) AS mean_ms,
       round(s.stddev_exec_time::numeric, 3) AS stddev_ms,
       round(s.max_exec_time::numeric, 1) AS max_ms,
       s.temp_blks_written, left(regexp_replace(s.query, '\s+', ' ', 'g'), 160) AS query
  FROM pg_stat_statements s
 WHERE s.dbid = (SELECT oid FROM pg_database WHERE datname = current_database()) AND s.toplevel
   AND s.calls >= 5
 ORDER BY s.mean_exec_time DESC LIMIT :n;

\echo == 4 调用次数前 :n
SELECT s.queryid, s.userid::regrole AS role, s.calls,
       round(s.total_exec_time::numeric, 1) AS total_ms,
       round(s.mean_exec_time::numeric, 3) AS mean_ms,
       s.rows, left(regexp_replace(s.query, '\s+', ' ', 'g'), 160) AS query
  FROM pg_stat_statements s
 WHERE s.dbid = (SELECT oid FROM pg_database WHERE datname = current_database()) AND s.toplevel
 ORDER BY s.calls DESC LIMIT :n;

ROLLBACK;
