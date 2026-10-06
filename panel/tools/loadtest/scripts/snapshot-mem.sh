#!/usr/bin/env bash
# [INPUT]: 依赖同目录 lt-common.sh 的 lt_init / lt_psql / lt_valkey，依赖面板库的 PostgreSQL 超级用户与 .env 的 VALKEY_PASSWORD
# [OUTPUT]: DIR/pg-memory-<标签>-<UTC 时间>.txt（内存参数、共享内存分配、连接按状态与角色、本库的命中与临时文件计数、库大小）与 DIR/valkey-<标签>-<UTC 时间>.txt（INFO memory / stats / clients 与 DBSIZE）
# [POS]: tools/loadtest/scripts 的数据库与缓存内部视角快照，压测前后各打一次做差；进程级 RSS/CPU 由 sample-procs.sh 采
#
# 用法（面板主机，root）：
#   snapshot-mem.sh DIR [标签，缺省 snap]      例如压测前 before、压测后 after
#
# 看什么：
#   PostgreSQL  temp_files / temp_bytes 涨了说明 work_mem 不够、在落盘排序；blks_read 相对 blks_hit
#               涨得多说明 shared_buffers 装不下热数据；连接数逼近 max_connections（Docker 版 60）即排队
#   Valkey      used_memory 逼近 maxmemory（Docker 版 96mb，allkeys-lru）时 evicted_keys 会涨——
#               被淘汰的可能是限流计数与幂等键，压测结论要把这一项带上
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lt-common.sh
. "$SCRIPT_DIR/lt-common.sh"

DIR="${1:-}"
LABEL="${2:-snap}"
[[ -n "$DIR" ]] || lt_die "用法：snapshot-mem.sh DIR [标签]"
[[ "$LABEL" =~ ^[A-Za-z0-9_-]+$ ]] || lt_die "标签只能含字母、数字、下划线与连字符"
lt_require_root
lt_init
mkdir -p -- "$DIR"
STAMP="$(lt_stamp)"
PG_OUT="$DIR/pg-memory-$LABEL-$STAMP.txt"
VK_OUT="$DIR/valkey-$LABEL-$STAMP.txt"

lt_psql -P pager=off <<'SQL' > "$PG_OUT"
\echo '## 内存相关参数'
SELECT name, setting, unit
  FROM pg_settings
 WHERE name IN ('shared_buffers', 'work_mem', 'maintenance_work_mem', 'effective_cache_size',
                'max_connections', 'shared_preload_libraries', 'huge_pages')
 ORDER BY name;
\echo '## 共享内存分配 top 15（pg_shmem_allocations）'
SELECT name, pg_size_pretty(allocated_size) AS allocated
  FROM pg_shmem_allocations
 ORDER BY allocated_size DESC
 LIMIT 15;
SELECT pg_size_pretty(sum(allocated_size)) AS shmem_total FROM pg_shmem_allocations;
\echo '## 连接：按角色与状态'
SELECT coalesce(usename, '(后台进程)') AS role, coalesce(state, backend_type) AS state, count(*)
  FROM pg_stat_activity
 GROUP BY 1, 2
 ORDER BY 3 DESC;
\echo '## 本库累计计数（与另一次快照做差）'
SELECT numbackends, xact_commit, xact_rollback, blks_read, blks_hit,
       round(100.0 * blks_hit / nullif(blks_hit + blks_read, 0), 2) AS hit_pct,
       temp_files, pg_size_pretty(temp_bytes) AS temp_bytes, deadlocks, tup_returned, tup_fetched,
       tup_inserted, tup_updated, tup_deleted
  FROM pg_stat_database
 WHERE datname = current_database();
\echo '## 库大小'
SELECT pg_size_pretty(pg_database_size(current_database())) AS database_size;
SQL

{
  printf '## INFO memory\n'
  lt_valkey INFO memory
  printf '\n## INFO stats\n'
  lt_valkey INFO stats
  printf '\n## INFO clients\n'
  lt_valkey INFO clients
  printf '\n## DBSIZE\n'
  lt_valkey DBSIZE
} | tr -d '\r' > "$VK_OUT"

lt_say "已写 $PG_OUT 与 $VK_OUT"
