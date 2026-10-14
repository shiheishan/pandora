-- pg_stat_statements 压测辅助 SQL，用 action 选一段：
--   sudo /opt/pandora/deploy/psql.sh -v action=setup -f pg_stat_statements.sql   建扩展（只需一次）
--   sudo /opt/pandora/deploy/psql.sh -v action=reset -f pg_stat_statements.sql   清零（每个场景开始前）
--   sudo /opt/pandora/deploy/psql.sh -v action=top   -f pg_stat_statements.sql   按总耗时前 20
--   sudo /opt/pandora/deploy/psql.sh -v action=calls -f pg_stat_statements.sql   按调用次数前 20
--   sudo /opt/pandora/deploy/psql.sh -v action=scans -f pg_stat_statements.sql   全表扫描嫌疑（按 seq_tup_read）
-- 存档时加 -P pager=off，输出重定向到 ops-local 下的结果目录。
--
-- 前置（不是 SQL）：在测试机建 /etc/postgresql/18/main/conf.d/zz-perf.conf，内容：
--   shared_preload_libraries = 'pg_stat_statements'
--   pg_stat_statements.track = top
--   pg_stat_statements.max = 5000
--   track_io_timing = on
-- 然后 sudo systemctl restart postgresql@18-main。测完删掉 zz-perf.conf 再重启。
-- 不改 conf.d/pandora.conf（那份随安装覆盖）。
--
-- 看执行计划：把查到的 SQL 里的 $1… 换成真实值，
--   EXPLAIN (ANALYZE, BUFFERS, SETTINGS) <SQL>;
-- 写语句放在事务里回滚：BEGIN; EXPLAIN (ANALYZE, BUFFERS) <写语句>; ROLLBACK;
\set ON_ERROR_STOP on
\if :{?action}
\else
  \echo '用法: psql -v action=setup|reset|top|calls|scans -f pg_stat_statements.sql'
  \quit
\endif

SELECT :'action' = 'setup' AS act_setup,
       :'action' = 'reset' AS act_reset,
       :'action' = 'top'   AS act_top,
       :'action' = 'calls' AS act_calls,
       :'action' = 'scans' AS act_scans \gset

\if :act_setup
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
\elif :act_reset
SELECT pg_stat_statements_reset();
\elif :act_top
SELECT
  calls,
  round(total_exec_time::numeric, 1) AS total_ms,
  round(mean_exec_time::numeric, 3) AS mean_ms,
  round(max_exec_time::numeric, 1) AS max_ms,
  rows,
  shared_blks_hit,
  shared_blks_read,
  round(100.0 * shared_blks_hit / nullif(shared_blks_hit + shared_blks_read, 0), 1) AS hit_pct,
  left(regexp_replace(query, '\s+', ' ', 'g'), 200) AS query
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY total_exec_time DESC
LIMIT 20;
\elif :act_calls
SELECT
  calls,
  round(total_exec_time::numeric, 1) AS total_ms,
  round(mean_exec_time::numeric, 3) AS mean_ms,
  round(max_exec_time::numeric, 1) AS max_ms,
  rows,
  shared_blks_hit,
  shared_blks_read,
  round(100.0 * shared_blks_hit / nullif(shared_blks_hit + shared_blks_read, 0), 1) AS hit_pct,
  left(regexp_replace(query, '\s+', ' ', 'g'), 200) AS query
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY calls DESC
LIMIT 20;
\elif :act_scans
SELECT
  schemaname,
  relname,
  seq_scan,
  seq_tup_read,
  idx_scan,
  n_live_tup
FROM pg_stat_user_tables
ORDER BY seq_tup_read DESC
LIMIT 20;
\else
  \echo 'action 只能是 setup、reset、top、calls、scans'
\endif
