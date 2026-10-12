-- 用途：磁盘满或库变慢时看库体积与最大的表（含索引与 TOAST），以及保留期清理管的几张遥测表现在有多少行。
-- 身份：超级用户。
-- 变量：n（行数，缺省 15）。
-- 对应 panel/deploy/RUNBOOK.md 第 8、9 章。遥测表由 aegis-admin 每 10 分钟分批清理，积压时 admin.log 有「…清理失败」。
\if :{?n}
\else
  \set n 15
\endif
BEGIN READ ONLY;

\echo == 1 库体积
SELECT current_database() AS db, pg_size_pretty(pg_database_size(current_database())) AS size;

\echo == 2 最大的 :n 张表
SELECT relname, pg_size_pretty(pg_total_relation_size(relid)) AS total,
       pg_size_pretty(pg_relation_size(relid)) AS heap, n_live_tup, n_dead_tup, last_autovacuum
  FROM pg_catalog.pg_stat_user_tables
 ORDER BY pg_total_relation_size(relid) DESC
 LIMIT :n;

\echo == 3 保留期清理管的两张遥测表：行数与最老一行（正常时在线记录不早于约 70 分钟前、探针点不早于约 48 小时前）
SELECT 'node_alive_ips' AS t, count(*) AS n, min(last_seen_at) AS oldest FROM node_alive_ips
UNION ALL
SELECT 'node_metrics', count(*), min(recorded_at) FROM node_metrics;

ROLLBACK;
